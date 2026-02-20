package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dsandor/feedbackloop/internal/logger"
	"github.com/dsandor/feedbackloop/internal/proxy"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	transport = flag.String("transport", "stdio", "Transport type: stdio or http")
	httpHost  = flag.String("http-host", "localhost", "HTTP server host (http mode only)")
	httpPort  = flag.Int("http-port", 3000, "HTTP server port (http mode only)")
)

func main() {
	flag.Parse()

	// Exit code to return
	exitCode := 0
	defer func() {
		// Recover from panics and log them
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "PANIC: %v\n", r)
			exitCode = 2
		}
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

	// Validate transport flag
	if *transport != "stdio" && *transport != "http" {
		fmt.Fprintf(os.Stderr, "ERROR: Invalid transport '%s'. Must be 'stdio' or 'http'\n", *transport)
		os.Exit(1)
	}

	// Initialize logger
	log := logger.New()

	log.LogEvent("feedbackloop_starting", "main", map[string]interface{}{
		"version": "0.1.0",
	})

	// Log transport configuration
	log.LogEvent("transport_configured", "main", map[string]interface{}{
		"transport": *transport,
		"http_host": *httpHost,
		"http_port": *httpPort,
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
	proxyServer, err := proxy.NewProxyServer(upstream, log)
	if err != nil {
		log.LogErrorEvent("server_creation_failed", "main", map[string]interface{}{
			"error": err.Error(),
		})
		exitCode = 1
		return
	}

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
