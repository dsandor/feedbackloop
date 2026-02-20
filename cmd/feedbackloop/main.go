package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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

	// Log selected transport mode
	log.LogEvent("transport_mode_selected", "main", map[string]interface{}{
		"transport": *transport,
	})

	// Route to appropriate transport
	switch *transport {
	case "stdio":
		if err := runStdioMode(ctx, upstream, log); err != nil {
			log.LogErrorEvent("stdio_mode_error", "main", map[string]interface{}{
				"error": err.Error(),
			})
			exitCode = 1
		}
	case "http":
		if err := runHTTPMode(ctx, upstream, log, *httpHost, *httpPort); err != nil {
			log.LogErrorEvent("http_mode_error", "main", map[string]interface{}{
				"error": err.Error(),
			})
			exitCode = 1
		}
	}

	log.LogEvent("shutdown_complete", "main", map[string]interface{}{
		"transport": *transport,
	})
}

func runStdioMode(ctx context.Context, upstream *proxy.UpstreamManager, log *logger.Logger) error {
	// Create proxy server
	proxyServer, err := proxy.NewProxyServer(upstream, log)
	if err != nil {
		return fmt.Errorf("proxy server creation failed: %w", err)
	}

	// Create stdio transport
	stdioTransport := &mcp.StdioTransport{}

	log.LogEvent("ready_for_client", "stdio_mode", map[string]interface{}{
		"transport":      "stdio",
		"transport_type": "stdio",
	})

	// Run server
	return proxyServer.Run(ctx, stdioTransport)
}

func runHTTPMode(ctx context.Context, upstream *proxy.UpstreamManager, log *logger.Logger, host string, port int) error {
	// Create SSE handler with getServer function
	sseHandler := mcp.NewSSEHandler(func(req *http.Request) *mcp.Server {
		// For now, create a new proxy server per request
		// (Alternative: reuse singleton ProxyServer if thread-safe)
		proxyServer, err := proxy.NewProxyServer(upstream, log)
		if err != nil {
			log.LogErrorEvent("proxy_server_creation_failed", "http_server", map[string]interface{}{
				"error": err.Error(),
			})
			return nil
		}
		return proxyServer.GetServer()
	}, &mcp.SSEOptions{})

	// Create HTTP server
	addr := fmt.Sprintf("%s:%d", host, port)
	httpServer := &http.Server{
		Addr:    addr,
		Handler: sseHandler,
	}

	// Start server in goroutine
	serverErr := make(chan error, 1)
	go func() {
		log.LogEvent("http_server_starting", "http_server", map[string]interface{}{
			"address":        addr,
			"transport_type": "http",
		})
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	// Wait for shutdown signal or server error
	select {
	case <-ctx.Done():
		// Graceful shutdown
		log.LogEvent("http_server_shutting_down", "http_server", map[string]interface{}{
			"reason": "signal",
		})
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.LogErrorEvent("http_server_shutdown_error", "http_server", map[string]interface{}{
				"error": err.Error(),
			})
			return err
		}
		log.LogEvent("http_server_stopped", "http_server", map[string]interface{}{
			"reason": "graceful_shutdown",
		})
		return nil
	case err := <-serverErr:
		log.LogErrorEvent("http_server_error", "http_server", map[string]interface{}{
			"error": err.Error(),
		})
		return err
	}
}
