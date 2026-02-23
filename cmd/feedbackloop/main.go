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

	"github.com/dsandor/feedbackloop/internal/analysis"
	"github.com/dsandor/feedbackloop/internal/logger"
	"github.com/dsandor/feedbackloop/internal/proxy"
	"github.com/dsandor/feedbackloop/internal/ui"
	"github.com/dsandor/feedbackloop/internal/webui"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	transport  = flag.String("transport", "stdio", "Transport type: stdio or http")
	httpHost   = flag.String("http-host", "localhost", "HTTP server host (http mode only)")
	httpPort   = flag.Int("http-port", 3000, "HTTP server port (http mode only)")
	uiPort     = flag.Int("ui-port", 0, "UI server port (0 = auto-select from 3070-3099)")
	apiKey     = flag.String("api-key", "", "Anthropic API key (overrides ANTHROPIC_API_KEY env var)")
	configFile = flag.String("config", "config.json", "Path to feedbackloop config file (JSON, Claude Desktop mcpServers format)")
	serverName = flag.String("server", "", "Name of the mcpServers entry to proxy (defaults to first entry)")
)

func main() {
	flag.Parse()

	// Track which flags were explicitly set on the command line so config file
	// values only fill in the gaps (priority: CLI > config > env var > default).
	explicitFlags := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { explicitFlags[f.Name] = true })

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

	// Load config early so Settings can fill in any values not set via CLI flags.
	// We will re-use this config later when setting up the upstream.
	earlyConfig, _ := LoadConfig(*configFile)

	// Apply config file settings for any option not explicitly set on the CLI.
	if earlyConfig != nil && earlyConfig.Settings != nil {
		s := earlyConfig.Settings
		if !explicitFlags["transport"] && s.Transport != "" {
			*transport = s.Transport
		}
		if !explicitFlags["http-host"] && s.HTTPHost != "" {
			*httpHost = s.HTTPHost
		}
		if !explicitFlags["http-port"] && s.HTTPPort != 0 {
			*httpPort = s.HTTPPort
		}
		if !explicitFlags["ui-port"] && s.UIPort != 0 {
			*uiPort = s.UIPort
		}
		if !explicitFlags["api-key"] && s.APIKey != "" {
			*apiKey = s.APIKey
		}
		if !explicitFlags["server"] && s.Server != "" {
			*serverName = s.Server
		}
	}

	// Validate transport flag
	if *transport != "stdio" && *transport != "http" {
		fmt.Fprintf(os.Stderr, "ERROR: Invalid transport '%s'. Must be 'stdio' or 'http'\n", *transport)
		os.Exit(1)
	}

	// Initialize logger
	log := logger.New()

	// Resolve API key: CLI flag / config file > ANTHROPIC_API_KEY env var
	resolvedAPIKey := *apiKey
	if resolvedAPIKey == "" {
		resolvedAPIKey = os.Getenv("ANTHROPIC_API_KEY")
	}

	// Initialize AI analyzer — model priority: config file > FEEDBACKLOOP_LLM_MODEL env var > default
	llmModel := os.Getenv("FEEDBACKLOOP_LLM_MODEL")
	if llmModel == "" {
		llmModel = "claude-sonnet-4-6"
	}
	if earlyConfig != nil && earlyConfig.Settings != nil && earlyConfig.Settings.Model != "" {
		llmModel = earlyConfig.Settings.Model
	}
	analyzer := analysis.New(log, resolvedAPIKey, llmModel)
	analyzer.Start(ctx)

	if resolvedAPIKey != "" {
		log.LogEvent("ai_analysis_enabled", "main", map[string]interface{}{
			"model": llmModel,
		})
	} else {
		log.LogEvent("ai_analysis_disabled", "main", map[string]interface{}{
			"reason": "no API key provided (use --api-key, config settings.apiKey, or ANTHROPIC_API_KEY)",
		})
	}

	// Resolve UI port: CLI flag / config file > FEEDBACKLOOP_UI_PORT env var > auto-select
	resolvedUIPort := *uiPort
	if resolvedUIPort == 0 {
		if envPort := os.Getenv("FEEDBACKLOOP_UI_PORT"); envPort != "" {
			fmt.Sscanf(envPort, "%d", &resolvedUIPort)
		}
	}

	// Start web UI server
	uiServer, uiErr := ui.New(log, webui.WebFS, analyzer, resolvedUIPort, *configFile, llmModel, resolvedAPIKey)
	if uiErr != nil {
		fmt.Fprintf(os.Stderr, "WARNING: could not start UI server: %v\n", uiErr)
	} else {
		if startErr := uiServer.Start(ctx); startErr != nil {
			fmt.Fprintf(os.Stderr, "WARNING: UI server failed to start: %v\n", startErr)
		} else {
			uiURL := fmt.Sprintf("http://localhost:%d", uiServer.Port())
			log.LogEvent("ui_server_started", "main", map[string]interface{}{
				"port": uiServer.Port(),
				"url":  uiURL,
			})
			fmt.Fprintf(os.Stderr, "\nFeedbackLoop UI: %s\n\n", uiURL)
		}
	}

	log.LogEvent("feedbackloop_starting", "main", map[string]interface{}{
		"version": "0.1.0",
	})

	// Log transport configuration
	log.LogEvent("transport_configured", "main", map[string]interface{}{
		"transport": *transport,
		"http_host": *httpHost,
		"http_port": *httpPort,
	})

	// Load config and resolve upstream server command (reuse earlyConfig if already valid).
	cfg := earlyConfig
	if cfg == nil {
		var cfgErr error
		cfg, cfgErr = LoadConfig(*configFile)
		if cfgErr != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", cfgErr)
			os.Exit(1)
		}
	}

	upstreamCmd, upstreamArgs, resolveErr := cfg.ResolveServer(*serverName)
	if resolveErr != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", resolveErr)
		os.Exit(1)
	}

	log.LogEvent("upstream_configured", "main", map[string]interface{}{
		"command": upstreamCmd,
		"args":    upstreamArgs,
		"server":  *serverName,
		"config":  *configFile,
	})

	// Create upstream manager
	upstream := proxy.NewUpstreamManager(log, upstreamCmd, upstreamArgs)

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
	clientInfo := &proxy.ClientInfo{
		ClientID:  logger.GenerateCorrelationID(),
		Transport: "stdio",
	}

	// Create proxy server
	proxyServer, err := proxy.NewProxyServer(upstream, log, clientInfo)
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
		// Extract client metadata from HTTP request
		headers := make(map[string]string)
		for _, h := range []string{"User-Agent", "Accept", "Origin", "X-Forwarded-For", "X-Client-Id", "X-Session-Id", "X-Request-Id"} {
			if v := req.Header.Get(h); v != "" {
				headers[h] = v
			}
		}
		clientInfo := &proxy.ClientInfo{
			ClientID:   logger.GenerateCorrelationID(),
			Transport:  "http",
			RemoteAddr: req.RemoteAddr,
			UserAgent:  req.Header.Get("User-Agent"),
			Headers:    headers,
		}

		// For now, create a new proxy server per request
		// (Alternative: reuse singleton ProxyServer if thread-safe)
		proxyServer, err := proxy.NewProxyServer(upstream, log, clientInfo)
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
