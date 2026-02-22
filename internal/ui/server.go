package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dsandor/feedbackloop/internal/analysis"
	"github.com/dsandor/feedbackloop/internal/logger"
)

// Server serves the web UI and streams log entries via SSE.
type Server struct {
	log         *logger.Logger
	webFS       fs.FS
	port        int
	analyzer    *analysis.Analyzer
	toolMu      sync.RWMutex
	toolCatalog json.RawMessage // cached payload from the most recent tool_catalog log event
	catalogPath string          // path to persist tool catalog JSON on disk
}

// New creates a UI server. If port is 0, an available port in 3070-3099 is chosen automatically.
// The tool catalog is persisted to ~/.feedbackloop/tool_catalog.json so it survives reconnects.
func New(log *logger.Logger, webFS fs.FS, analyzer *analysis.Analyzer, port int) (*Server, error) {
	if port == 0 {
		var err error
		port, err = findAvailablePort()
		if err != nil {
			return nil, err
		}
	}

	catalogPath := ""
	if home, err := os.UserHomeDir(); err == nil {
		dir := filepath.Join(home, ".feedbackloop")
		if mkErr := os.MkdirAll(dir, 0755); mkErr == nil {
			catalogPath = filepath.Join(dir, "tool_catalog.json")
		}
	}

	return &Server{log: log, webFS: webFS, port: port, analyzer: analyzer, catalogPath: catalogPath}, nil
}

// Port returns the port the server will listen on.
func (s *Server) Port() int { return s.port }

// Start launches the HTTP server in the background.
// It shuts down gracefully when ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	// Load persisted catalog so the UI can show tools immediately on open,
	// even before the MCP server has completed discovery for this session.
	s.loadPersistedCatalog()

	webRoot, err := fs.Sub(s.webFS, "web")
	if err != nil {
		return fmt.Errorf("ui web fs: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(webRoot)))
	mux.HandleFunc("/events", s.handleSSE)
	mux.HandleFunc("/api/tools", s.handleTools)
	mux.HandleFunc("/api/analysis", s.handleGetAnalysis)
	mux.HandleFunc("/api/analyze", s.handleRunAnalysis)

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx) //nolint:errcheck
	}()

	// Subscribe SYNCHRONOUSLY before launching the goroutine so we cannot
	// miss the tool_catalog event if it fires before the goroutine is scheduled.
	catalogSubID := logger.GenerateCorrelationID()
	catalogCh := s.log.Subscribe(catalogSubID)
	go s.watchToolCatalog(ctx, catalogCh, catalogSubID)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.log.LogErrorEvent("ui_server_error", "ui", map[string]interface{}{
				"error": err.Error(),
			})
		}
	}()

	return nil
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	subID := logger.GenerateCorrelationID()
	ch := s.log.Subscribe(subID)
	defer s.log.Unsubscribe(subID)

	fmt.Fprintf(w, "event: connected\ndata: {}\n\n")
	flusher.Flush()

	// Send current tool catalog to new subscriber if available
	s.toolMu.RLock()
	catalog := s.toolCatalog
	s.toolMu.RUnlock()
	if catalog != nil {
		// Wrap it in a synthetic LogEntry shape that the frontend already understands
		syntheticEvent := map[string]interface{}{
			"event_type": "tool_catalog",
			"message":    json.RawMessage(catalog),
		}
		if data, err := json.Marshal(syntheticEvent); err == nil {
			fmt.Fprintf(w, "event: log\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}

	for {
		select {
		case entry, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(entry)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: log\ndata: %s\n\n", data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// watchToolCatalog listens on a pre-subscribed channel and caches the tool
// catalog whenever a "tool_catalog" event is received.  The channel and subID
// must be created by the caller (synchronously in Start) so that no events
// are missed due to goroutine scheduling delays.
func (s *Server) watchToolCatalog(ctx context.Context, ch chan logger.LogEntry, subID string) {
	defer s.log.Unsubscribe(subID)

	for {
		select {
		case entry, ok := <-ch:
			if !ok {
				return
			}
			if entry.EventType == "tool_catalog" {
				if data, err := json.Marshal(entry.Message); err == nil {
					s.toolMu.Lock()
					s.toolCatalog = data
					s.toolMu.Unlock()
					s.savePersistedCatalog(data)
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// loadPersistedCatalog reads the previously saved tool catalog from disk.
// It is a no-op if the file does not exist or cannot be parsed.
// It also injects the catalog into the analyzer so Run Analysis works
// immediately without waiting for a live tool_catalog log event.
func (s *Server) loadPersistedCatalog() {
	if s.catalogPath == "" {
		return
	}
	data, err := os.ReadFile(s.catalogPath)
	if err != nil {
		return
	}
	// Parse into a typed struct so we can pass the tools slice to the analyzer.
	var parsed struct {
		Tools []map[string]interface{} `json:"tools"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return
	}
	s.toolMu.Lock()
	s.toolCatalog = data
	s.toolMu.Unlock()

	if s.analyzer != nil && len(parsed.Tools) > 0 {
		s.analyzer.SetToolCatalog(parsed.Tools)
	}
}

// savePersistedCatalog writes the current tool catalog to disk.
func (s *Server) savePersistedCatalog(data []byte) {
	if s.catalogPath == "" {
		return
	}
	if err := os.WriteFile(s.catalogPath, data, 0644); err != nil {
		s.log.LogErrorEvent("catalog_persist_error", "ui", map[string]interface{}{
			"error": err.Error(),
			"path":  s.catalogPath,
		})
	}
}

func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	s.toolMu.RLock()
	catalog := s.toolCatalog
	s.toolMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if catalog == nil {
		w.Write([]byte(`{"tools":[],"total":0}`)) //nolint:errcheck
		return
	}
	w.Write(catalog) //nolint:errcheck
}

func (s *Server) handleGetAnalysis(w http.ResponseWriter, r *http.Request) {
	if s.analyzer == nil {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Write([]byte(`{"status":"unconfigured","message":"Analyzer not initialized.","recommendations":[]}`)) //nolint:errcheck
		return
	}
	report := s.analyzer.GetReport()
	data, err := json.Marshal(report)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(data) //nolint:errcheck
}

func (s *Server) handleRunAnalysis(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if s.analyzer == nil || !s.analyzer.IsConfigured() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"ANTHROPIC_API_KEY not set"}`)) //nolint:errcheck
		return
	}

	if err := s.analyzer.RunAnalysis(r.Context()); err != nil {
		if err.Error() == "analysis already in progress" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"error":"analysis already in progress"}`)) //nolint:errcheck
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"` + err.Error() + `"}`)) //nolint:errcheck
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(`{"status":"running"}`)) //nolint:errcheck
}

func findAvailablePort() (int, error) {
	for port := 3070; port <= 3099; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err == nil {
			ln.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("all ports 3070-3099 are in use")
}
