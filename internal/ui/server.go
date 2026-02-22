package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/dsandor/feedbackloop/internal/logger"
)

// Server serves the web UI and streams log entries via SSE.
type Server struct {
	log         *logger.Logger
	webFS       fs.FS
	port        int
	toolMu      sync.RWMutex
	toolCatalog json.RawMessage // cached payload from the most recent tool_catalog log event
}

// New creates a UI server, finding an available port in 3070-3099.
func New(log *logger.Logger, webFS fs.FS) (*Server, error) {
	port, err := findAvailablePort()
	if err != nil {
		return nil, err
	}
	return &Server{log: log, webFS: webFS, port: port}, nil
}

// Port returns the port the server will listen on.
func (s *Server) Port() int { return s.port }

// Start launches the HTTP server in the background.
// It shuts down gracefully when ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	webRoot, err := fs.Sub(s.webFS, "web")
	if err != nil {
		return fmt.Errorf("ui web fs: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(webRoot)))
	mux.HandleFunc("/events", s.handleSSE)
	mux.HandleFunc("/api/tools", s.handleTools)

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

	go s.watchToolCatalog(ctx)

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

// watchToolCatalog subscribes to the logger and caches the tool catalog
// whenever a "tool_catalog" event is received.
func (s *Server) watchToolCatalog(ctx context.Context) {
	subID := logger.GenerateCorrelationID()
	ch := s.log.Subscribe(subID)
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
				}
			}
		case <-ctx.Done():
			return
		}
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
