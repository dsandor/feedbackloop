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

// ProxyServer is the client-facing MCP server that proxies to upstream
type ProxyServer struct {
	server    *mcp.Server
	upstream  *UpstreamManager
	logger    *logger.Logger
	toolCache *ToolCache
}

// NewProxyServer creates a new proxy server with tool discovery
func NewProxyServer(upstream *UpstreamManager, log *logger.Logger) (*ProxyServer, error) {
	impl := &mcp.Implementation{
		Name:    "feedbackloop",
		Version: "0.1.0",
	}

	serverOptions := &mcp.ServerOptions{}
	server := mcp.NewServer(impl, serverOptions)

	// Create tool cache
	toolCache := NewToolCache(log)

	// Discover tools from upstream
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := toolCache.DiscoverTools(ctx, upstream.Session())
	if err != nil {
		return nil, fmt.Errorf("tool discovery failed: %w", err)
	}

	// Register all tools with proxy server
	for _, tool := range toolCache.GetTools() {
		server.AddTool(tool, toolCache.CreateProxyHandler(tool.Name, upstream.Session()))

		correlationID := logger.GenerateCorrelationID()
		log.LogEvent("tool_registered", correlationID, map[string]interface{}{
			"tool": tool.Name,
		})
	}

	return &ProxyServer{
		server:    server,
		upstream:  upstream,
		logger:    log,
		toolCache: toolCache,
	}, nil
}

// Run starts the proxy server on the given transport (blocks until client disconnects)
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
