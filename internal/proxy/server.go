package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/dsandor/feedbackloop/internal/logger"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ProxyServer is the client-facing MCP server that proxies to upstream.
type ProxyServer struct {
	server     *mcp.Server
	pool       *UpstreamPool
	logger     *logger.Logger
	toolCache  *ToolCache
	clientInfo *ClientInfo
}

// NewProxyServer creates a new proxy server with tool discovery across all
// servers in the pool.
//
// sharedCache is optional. When non-nil the provided ToolCache is used instead
// of creating a fresh one; this allows description overrides to survive across
// ProxyServer instances (important in HTTP/SSE mode where a new ProxyServer is
// created per client connection). Pass nil to use a throwaway cache (stdio mode
// or tests).
func NewProxyServer(pool *UpstreamPool, log *logger.Logger, clientInfo *ClientInfo, sharedCache *ToolCache) (*ProxyServer, error) {
	correlationID := logger.GenerateCorrelationID()

	impl := &mcp.Implementation{
		Name:    "feedbackloop",
		Version: "0.1.0",
	}

	serverOptions := &mcp.ServerOptions{}
	server := mcp.NewServer(impl, serverOptions)

	// Use the provided shared cache, or create a fresh one.
	toolCache := sharedCache
	if toolCache == nil {
		toolCache = NewToolCache(log)
	}

	// Discover tools from all upstream servers in the pool.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := toolCache.DiscoverAllTools(ctx, pool); err != nil {
		return nil, fmt.Errorf("tool discovery failed: %w", err)
	}

	// Register all tools with the proxy server.
	// CreateProxyHandler uses the routes map to resolve the correct session and
	// original tool name — the fallbackSession is nil here because the routes
	// map is always populated by DiscoverAllTools.
	for _, tool := range toolCache.GetTools() {
		server.AddTool(tool, toolCache.CreateProxyHandler(tool.Name, nil, clientInfo))

		toolCorrelationID := logger.GenerateCorrelationID()
		log.LogEvent("tool_registered", toolCorrelationID, map[string]interface{}{
			"tool": tool.Name,
		})
	}

	log.LogEvent("proxy_server_created", correlationID, map[string]interface{}{
		"tool_count":   len(toolCache.GetTools()),
		"multi_server": pool.IsMultiServer(),
		"client_info":  clientInfo,
	})

	return &ProxyServer{
		server:     server,
		pool:       pool,
		logger:     log,
		toolCache:  toolCache,
		clientInfo: clientInfo,
	}, nil
}

// Run starts the proxy server on the given transport (blocks until client disconnects).
func (ps *ProxyServer) Run(ctx context.Context, transport mcp.Transport) error {
	correlationID := fmt.Sprintf("server-run-%d", time.Now().UnixNano())

	ps.logger.LogEvent("server_starting", correlationID, map[string]interface{}{
		"transport": "stdio",
	})

	// Run the server (this blocks until the client disconnects or context is cancelled)
	err := ps.server.Run(ctx, transport)

	if err != nil {
		// Distinguish between different error types
		if errors.Is(err, io.EOF) {
			ps.logger.LogEvent("server_stopped", correlationID, map[string]interface{}{
				"reason": "client_disconnected",
			})
			return nil
		}

		if errors.Is(err, context.Canceled) {
			ps.logger.LogEvent("server_stopped", correlationID, map[string]interface{}{
				"reason": "context_cancelled",
			})
			return nil
		}

		ps.logger.LogErrorEvent("server_stopped_with_error", correlationID, map[string]interface{}{
			"error": err.Error(),
		})
		return fmt.Errorf("server run failed: %w", err)
	}

	ps.logger.LogEvent("server_stopped", correlationID, map[string]interface{}{
		"reason": "clean_shutdown",
	})

	return nil
}

// GetServer returns the underlying MCP server for transport integration.
// This is used by HTTP/SSE transport handlers to connect to the server.
func (ps *ProxyServer) GetServer() *mcp.Server {
	return ps.server
}
