package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dsandor/feedbackloop/internal/logger"
	"github.com/dsandor/feedbackloop/internal/proxy"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	// Exit code to return
	exitCode := 0
	defer func() {
		os.Exit(exitCode)
	}()

	// Create context with signal handling for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle SIGINT and SIGTERM for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Fprintln(os.Stderr, "\nReceived shutdown signal, cleaning up...")
		cancel()
	}()

	// Initialize logger
	log := logger.New()

	log.LogEvent("feedbackloop_starting", "main", map[string]interface{}{
		"version": "0.1.0",
	})

	// Create upstream manager for chrome-devtools-mcp
	upstream := proxy.NewUpstreamManager(log, "npx", []string{"-y", "chrome-devtools-mcp@latest"})

	// Connect to upstream server
	if err := upstream.Connect(ctx); err != nil {
		log.LogErrorEvent("startup_failed", "main", map[string]interface{}{
			"error":  err.Error(),
			"reason": "upstream_connection_failed",
		})
		exitCode = 1
		return
	}
	defer func() {
		if err := upstream.Close(); err != nil {
			log.LogErrorEvent("cleanup_failed", "main", map[string]interface{}{
				"error": err.Error(),
			})
		}
	}()

	// Create proxy server
	proxyServer := proxy.NewProxyServer(upstream, log)

	// Create stdio transport for client communication
	transport := &mcp.StdioTransport{}

	log.LogEvent("ready_for_client", "main", map[string]interface{}{
		"transport": "stdio",
	})

	// Run the proxy server (blocks until client disconnects or context cancelled)
	if err := proxyServer.Run(ctx, transport); err != nil {
		// Check if error is due to context cancellation (graceful shutdown)
		if ctx.Err() != nil {
			log.LogEvent("shutdown_complete", "main", map[string]interface{}{
				"reason": "signal",
			})
		} else {
			log.LogErrorEvent("runtime_error", "main", map[string]interface{}{
				"error": err.Error(),
			})
			exitCode = 1
		}
		return
	}

	log.LogEvent("shutdown_complete", "main", map[string]interface{}{
		"reason": "client_disconnected",
	})
}
