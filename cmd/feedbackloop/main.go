package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dsandor/feedbackloop/internal/analysis"
	"github.com/dsandor/feedbackloop/internal/auth"
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
	serverName = flag.String("server", "", "Name of the mcpServers entry to proxy (defaults to all entries)")
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

	// Load config and resolve upstream server set (reuse earlyConfig if already valid).
	cfg := earlyConfig
	if cfg == nil {
		var cfgErr error
		cfg, cfgErr = LoadConfig(*configFile)
		if cfgErr != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", cfgErr)
			os.Exit(1)
		}
	}

	servers, resolveErr := cfg.ResolveAllServers(*serverName)
	if resolveErr != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", resolveErr)
		os.Exit(1)
	}

	// Build the pool from the resolved servers map.
	pool := proxy.NewUpstreamPool(log)
	serverKeys := make([]string, 0, len(servers))
	for key, srv := range servers {
		pool.Add(key, srv.Command, srv.Args)
		serverKeys = append(serverKeys, key)
	}

	log.LogEvent("upstream_pool_configured", "main", map[string]interface{}{
		"server_count": len(servers),
		"servers":      serverKeys,
		"multi_server": len(servers) > 1,
	})

	// Connect to all upstream servers concurrently.
	if err := pool.ConnectAll(ctx); err != nil {
		log.LogErrorEvent("startup_failed", "main", map[string]interface{}{
			"error":  err.Error(),
			"reason": "all_upstream_connections_failed",
		})
		exitCode = 1
		return
	}
	defer pool.CloseAll()

	// Create a shared ToolCache that persists across ProxyServer instances.
	// This allows description overrides applied via the UI to survive client
	// reconnects (especially in HTTP/SSE mode where NewProxyServer is called
	// per request).
	sharedToolCache := proxy.NewToolCache(log)

	// Configure override persistence: load any previously saved overrides from
	// disk so they are re-applied after a restart.
	if home, err := os.UserHomeDir(); err == nil {
		overridesDir := filepath.Join(home, ".feedbackloop")
		if mkErr := os.MkdirAll(overridesDir, 0755); mkErr == nil {
			sharedToolCache.SetOverridesPath(filepath.Join(overridesDir, "overrides.json"))
		}
	}

	// Wire the shared cache into the UI server so it can apply recommendations.
	if uiServer != nil {
		uiServer.SetToolCache(sharedToolCache)
	}

	// Log selected transport mode
	log.LogEvent("transport_mode_selected", "main", map[string]interface{}{
		"transport": *transport,
	})

	// Route to appropriate transport
	switch *transport {
	case "stdio":
		if err := runStdioMode(ctx, pool, log, sharedToolCache); err != nil {
			log.LogErrorEvent("stdio_mode_error", "main", map[string]interface{}{
				"error": err.Error(),
			})
			exitCode = 1
		}
	case "http":
		// Build OAuth config from settings (if configured).
		oauthCfg := auth.Config{}
		if cfg.Settings != nil && cfg.Settings.OAuth != nil {
			o := cfg.Settings.OAuth
			oauthCfg = auth.Config{
				Enabled:            o.Enabled,
				Mode:               o.Mode,
				Issuer:             o.Issuer,
				JWKSUri:            o.JWKSUri,
				Audience:           o.Audience,
				RequiredScopes:     o.RequiredScopes,
				IntrospectEndpoint: o.IntrospectEndpoint,
				ClientID:           o.ClientID,
				ClientSecret:       o.ClientSecret,
			}
		}
		if oauthCfg.Enabled {
			log.LogEvent("oauth_enabled", "main", map[string]interface{}{
				"mode":   oauthCfg.Mode,
				"issuer": oauthCfg.Issuer,
			})
		}
		if err := runHTTPMode(ctx, pool, log, *httpHost, *httpPort, sharedToolCache, oauthCfg); err != nil {
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

func runStdioMode(ctx context.Context, pool *proxy.UpstreamPool, log *logger.Logger, sharedCache *proxy.ToolCache) error {
	clientInfo := &proxy.ClientInfo{
		ClientID:  logger.GenerateCorrelationID(),
		Transport: "stdio",
	}

	// Create proxy server using the shared cache so overrides persist.
	proxyServer, err := proxy.NewProxyServer(pool, log, clientInfo, sharedCache)
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

func runHTTPMode(ctx context.Context, pool *proxy.UpstreamPool, log *logger.Logger, host string, port int, sharedCache *proxy.ToolCache, oauthCfg auth.Config) error {
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

		// Create a new proxy server per request, sharing the persistent tool cache
		// so that description overrides are applied on every reconnect.
		proxyServer, err := proxy.NewProxyServer(pool, log, clientInfo, sharedCache)
		if err != nil {
			log.LogErrorEvent("proxy_server_creation_failed", "http_server", map[string]interface{}{
				"error": err.Error(),
			})
			return nil
		}
		return proxyServer.GetServer()
	}, &mcp.SSEOptions{})

	// Wrap with OAuth middleware (no-op when disabled).
	var handler http.Handler = sseHandler
	handler = auth.Middleware(oauthCfg, handler)

	// Create HTTP server
	addr := fmt.Sprintf("%s:%d", host, port)
	httpServer := &http.Server{
		Addr:    addr,
		Handler: handler,
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
