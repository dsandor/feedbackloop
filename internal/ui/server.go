package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	configPath  string          // path to the feedbackloop config.json
	llmModel    string          // currently active LLM model ID
	apiKey      string          // Anthropic API key (used for /api/models proxy)

	// Log buffer for snapshot capture.
	logBufMu  sync.RWMutex
	logBuffer []logger.LogEntry

	// Snapshot persistence.
	snapshotsDir        string // ~/.feedbackloop/snapshots
	maxSnapshotLogLines int    // how many log lines to include in a snapshot (default 500)
}

// New creates a UI server. If port is 0, an available port in 3070-3099 is chosen automatically.
// The tool catalog is persisted to ~/.feedbackloop/tool_catalog.json so it survives reconnects.
// configPath, llmModel, and apiKey are optional but enable the Settings tab functionality.
func New(log *logger.Logger, webFS fs.FS, analyzer *analysis.Analyzer, port int, configPath, llmModel, apiKey string) (*Server, error) {
	if port == 0 {
		var err error
		port, err = findAvailablePort()
		if err != nil {
			return nil, err
		}
	}

	catalogPath := ""
	snapshotsDir := ""
	if home, err := os.UserHomeDir(); err == nil {
		dir := filepath.Join(home, ".feedbackloop")
		if mkErr := os.MkdirAll(dir, 0755); mkErr == nil {
			catalogPath = filepath.Join(dir, "tool_catalog.json")
		}
		snapDir := filepath.Join(home, ".feedbackloop", "snapshots")
		if mkErr := os.MkdirAll(snapDir, 0755); mkErr == nil {
			snapshotsDir = snapDir
		}
	}

	// Read maxSnapshotLogLines from config (default 500).
	maxSnapshotLogLines := 500
	if configPath != "" {
		if data, err := os.ReadFile(configPath); err == nil {
			var raw map[string]interface{}
			if json.Unmarshal(data, &raw) == nil {
				if sr, ok := raw["settings"].(map[string]interface{}); ok {
					if v, ok := sr["maxSnapshotLogLines"].(float64); ok && v > 0 {
						maxSnapshotLogLines = int(v)
					}
				}
			}
		}
	}

	return &Server{
		log:                 log,
		webFS:               webFS,
		port:                port,
		analyzer:            analyzer,
		catalogPath:         catalogPath,
		configPath:          configPath,
		llmModel:            llmModel,
		apiKey:              apiKey,
		snapshotsDir:        snapshotsDir,
		maxSnapshotLogLines: maxSnapshotLogLines,
	}, nil
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
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/models", s.handleModels)
	mux.HandleFunc("/api/snapshots", s.handleListSnapshots)
	mux.HandleFunc("/api/snapshot", s.handleSnapshot)

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

	// Subscribe for log buffer (snapshot capture) — must also be synchronous.
	bufSubID := logger.GenerateCorrelationID()
	bufCh := s.log.Subscribe(bufSubID)
	go s.watchLogBuffer(ctx, bufCh, bufSubID)

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

// watchLogBuffer appends every log entry to the in-memory circular buffer used for snapshots.
// The buffer keeps at most 2000 entries (hard cap); older entries are evicted.
func (s *Server) watchLogBuffer(ctx context.Context, ch chan logger.LogEntry, subID string) {
	const maxBuf = 2000
	defer s.log.Unsubscribe(subID)

	for {
		select {
		case entry, ok := <-ch:
			if !ok {
				return
			}
			s.logBufMu.Lock()
			s.logBuffer = append(s.logBuffer, entry)
			if len(s.logBuffer) > maxBuf {
				s.logBuffer = s.logBuffer[len(s.logBuffer)-maxBuf:]
			}
			s.logBufMu.Unlock()
		case <-ctx.Done():
			return
		}
	}
}

// handleSnapshot routes POST (save), GET (load), and DELETE by query param.
func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	switch r.Method {
	case http.MethodPost:
		s.handleSaveSnapshot(w, r)
	case http.MethodGet:
		s.handleLoadSnapshot(w, r)
	case http.MethodDelete:
		s.handleDeleteSnapshot(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleSaveSnapshot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		MaxLogLines int    `json:"max_log_lines"`
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	defer r.Body.Close()
	if len(body) > 0 {
		json.Unmarshal(body, &req) //nolint:errcheck
	}
	if req.MaxLogLines <= 0 {
		req.MaxLogLines = s.maxSnapshotLogLines
	}
	if req.Name == "" {
		req.Name = "snapshot"
	}

	meta, err := s.saveSnapshot(req.Name, req.MaxLogLines)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"` + err.Error() + `"}`)) //nolint:errcheck
		return
	}

	data, _ := json.Marshal(meta)
	w.WriteHeader(http.StatusCreated)
	w.Write(data) //nolint:errcheck
}

func (s *Server) handleLoadSnapshot(w http.ResponseWriter, r *http.Request) {
	filename := r.URL.Query().Get("file")
	if filename == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"file parameter required"}`)) //nolint:errcheck
		return
	}

	snap, err := s.loadSnapshot(filename)
	if err != nil {
		if os.IsNotExist(err) {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"not found"}`)) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"` + err.Error() + `"}`)) //nolint:errcheck
		return
	}

	data, err := json.Marshal(snap)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Write(data) //nolint:errcheck
}

func (s *Server) handleDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	filename := r.URL.Query().Get("file")
	if filename == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"file parameter required"}`)) //nolint:errcheck
		return
	}

	if err := s.deleteSnapshot(filename); err != nil {
		if os.IsNotExist(err) {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"not found"}`)) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"` + err.Error() + `"}`)) //nolint:errcheck
		return
	}

	w.Write([]byte(`{"status":"deleted"}`)) //nolint:errcheck
}

func (s *Server) handleListSnapshots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	metas, err := s.listSnapshots()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"` + err.Error() + `"}`)) //nolint:errcheck
		return
	}

	out, _ := json.Marshal(map[string]interface{}{"snapshots": metas})
	w.Write(out) //nolint:errcheck
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

// settingsPayload is the shape exchanged by GET and POST /api/settings.
type settingsPayload struct {
	ConfigPath          string                 `json:"config_path"`
	MCPServers          map[string]interface{} `json:"mcpServers"`
	Model               string                 `json:"model"`
	APIKeySet           bool                   `json:"api_key_set"`
	Transport           string                 `json:"transport"`
	HTTPHost            string                 `json:"httpHost"`
	HTTPPort            int                    `json:"httpPort"`
	UIPort              int                    `json:"uiPort"`
	Server              string                 `json:"server"`
	MaxSnapshotLogLines int                    `json:"max_snapshot_log_lines"`
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	switch r.Method {
	case http.MethodGet:
		s.handleGetSettings(w, r)
	case http.MethodPost:
		s.handleSaveSettings(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	payload := settingsPayload{
		ConfigPath:          s.configPath,
		Model:               s.llmModel,
		APIKeySet:           s.apiKey != "",
		MCPServers:          map[string]interface{}{},
		Transport:           "stdio",
		HTTPHost:            "localhost",
		HTTPPort:            3000,
		UIPort:              0,
		MaxSnapshotLogLines: s.maxSnapshotLogLines,
	}

	if s.configPath != "" {
		if data, err := os.ReadFile(s.configPath); err == nil {
			var raw map[string]interface{}
			if json.Unmarshal(data, &raw) == nil {
				if servers, ok := raw["mcpServers"].(map[string]interface{}); ok {
					payload.MCPServers = servers
				}
				if sr, ok := raw["settings"].(map[string]interface{}); ok {
					if v, ok := sr["model"].(string); ok && v != "" {
						payload.Model = v
					}
					if v, ok := sr["transport"].(string); ok && v != "" {
						payload.Transport = v
					}
					if v, ok := sr["httpHost"].(string); ok && v != "" {
						payload.HTTPHost = v
					}
					if v, ok := sr["httpPort"].(float64); ok && v > 0 {
						payload.HTTPPort = int(v)
					}
					if v, ok := sr["uiPort"].(float64); ok {
						payload.UIPort = int(v)
					}
					if v, ok := sr["server"].(string); ok {
						payload.Server = v
					}
					if v, ok := sr["maxSnapshotLogLines"].(float64); ok && v > 0 {
						payload.MaxSnapshotLogLines = int(v)
					}
				}
			}
		}
	}

	data, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Write(data) //nolint:errcheck
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 512*1024))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var req struct {
		MCPServers          map[string]interface{} `json:"mcpServers"`
		Model               string                 `json:"model"`
		Transport           string                 `json:"transport"`
		HTTPHost            string                 `json:"httpHost"`
		HTTPPort            int                    `json:"httpPort"`
		UIPort              int                    `json:"uiPort"`
		APIKey              string                 `json:"apiKey"`
		Server              string                 `json:"server"`
		MaxSnapshotLogLines int                    `json:"maxSnapshotLogLines"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if s.configPath == "" {
		http.Error(w, `{"error":"config path not set"}`, http.StatusServiceUnavailable)
		return
	}

	// Read existing raw config so we preserve any unknown keys.
	rawCfg := map[string]interface{}{}
	if data, err := os.ReadFile(s.configPath); err == nil {
		json.Unmarshal(data, &rawCfg) //nolint:errcheck
	}

	rawCfg["mcpServers"] = req.MCPServers

	settings, _ := rawCfg["settings"].(map[string]interface{})
	if settings == nil {
		settings = map[string]interface{}{}
	}

	// Model
	if req.Model != "" {
		settings["model"] = req.Model
		s.llmModel = req.Model
	} else {
		delete(settings, "model")
	}
	// Transport
	if req.Transport != "" {
		settings["transport"] = req.Transport
	} else {
		delete(settings, "transport")
	}
	// HTTP host/port
	if req.HTTPHost != "" {
		settings["httpHost"] = req.HTTPHost
	} else {
		delete(settings, "httpHost")
	}
	if req.HTTPPort > 0 {
		settings["httpPort"] = req.HTTPPort
	} else {
		delete(settings, "httpPort")
	}
	// UI port (0 is valid = auto-select)
	settings["uiPort"] = req.UIPort
	// API key — only update if a new value was provided; never overwrite with blank
	if req.APIKey != "" {
		settings["apiKey"] = req.APIKey
		s.apiKey = req.APIKey
	}
	// Active server name
	if req.Server != "" {
		settings["server"] = req.Server
	} else {
		delete(settings, "server")
	}
	// Snapshot log lines
	if req.MaxSnapshotLogLines > 0 {
		settings["maxSnapshotLogLines"] = req.MaxSnapshotLogLines
		s.maxSnapshotLogLines = req.MaxSnapshotLogLines
	}

	if len(settings) > 0 {
		rawCfg["settings"] = settings
	} else {
		delete(rawCfg, "settings")
	}

	out, err := json.MarshalIndent(rawCfg, "", "  ")
	if err != nil {
		http.Error(w, "cannot marshal config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(s.configPath, out, 0644); err != nil {
		http.Error(w, "cannot write config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"saved","restart_required":true}`)) //nolint:errcheck
}

// handleModels proxies to the Anthropic models list endpoint.
// If no API key is configured it returns an empty list.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if s.apiKey == "" {
		w.Write([]byte(`{"models":[],"api_key_set":false}`)) //nolint:errcheck
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.anthropic.com/v1/models", nil)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	req.Header.Set("x-api-key", s.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.Write([]byte(`{"models":[],"error":"` + err.Error() + `"}`)) //nolint:errcheck
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}

	// Parse Anthropic response and normalise to {models:[{id,display_name},...]}
	var anthropicResp struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			Type        string `json:"type"`
		} `json:"data"`
	}
	if json.Unmarshal(respBody, &anthropicResp) == nil {
		type modelItem struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		}
		items := make([]modelItem, 0, len(anthropicResp.Data))
		for _, m := range anthropicResp.Data {
			items = append(items, modelItem{ID: m.ID, DisplayName: m.DisplayName})
		}
		out, _ := json.Marshal(map[string]interface{}{"models": items, "api_key_set": true})
		w.Write(out) //nolint:errcheck
		return
	}

	// Fallback: forward raw response
	w.Write(respBody) //nolint:errcheck
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
