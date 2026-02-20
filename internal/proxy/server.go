package proxy

import (
	"context"
	"fmt"
	"time"

	"github.com/dsandor/feedbackloop/internal/logger"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ProxyServer is the client-facing MCP server that proxies to upstream
type ProxyServer struct {
	server   *mcp.Server
	upstream *UpstreamManager
	logger   *logger.Logger
}

// NewProxyServer creates a new proxy server
func NewProxyServer(upstream *UpstreamManager, logger *logger.Logger) *ProxyServer {
	impl := &mcp.Implementation{
		Name:    "feedbackloop",
		Version: "0.1.0",
	}

	// For now, we'll use minimal server options
	// In Phase 2, we'll add tool discovery and proxying
	serverOptions := &mcp.ServerOptions{}

	server := mcp.NewServer(impl, serverOptions)

	return &ProxyServer{
		server:   server,
		upstream: upstream,
		logger:   logger,
	}
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
		ps.logger.LogErrorEvent("server_stopped_with_error", correlationID, map[string]interface{}{
			"error": err.Error(),
		})
		return fmt.Errorf("server run failed: %w", err)
	}

	ps.logger.LogEvent("server_stopped", correlationID, map[string]interface{}{
		"reason": "client_disconnected",
	})

	return nil
}
