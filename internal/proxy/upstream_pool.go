package proxy

import (
	"context"
	"fmt"
	"sync"

	"github.com/dsandor/feedbackloop/internal/logger"
)

// ServerEntry holds config and runtime state for one upstream server.
type ServerEntry struct {
	Key     string
	Command string
	Args    []string
	Manager *UpstreamManager
	Status  string // "connecting", "connected", "failed"
	Error   string
}

// UpstreamPool manages connections to multiple upstream MCP servers.
type UpstreamPool struct {
	servers []*ServerEntry // ordered slice (preserves Add order)
	mu      sync.RWMutex
	logger  *logger.Logger
}

// NewUpstreamPool creates a new pool.
func NewUpstreamPool(log *logger.Logger) *UpstreamPool {
	return &UpstreamPool{
		logger: log,
	}
}

// Add registers a server entry. Must be called before ConnectAll.
func (p *UpstreamPool) Add(key, command string, args []string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	mgr := NewUpstreamManager(p.logger, command, args)
	p.servers = append(p.servers, &ServerEntry{
		Key:     key,
		Command: command,
		Args:    args,
		Manager: mgr,
		Status:  "connecting",
	})
}

// ConnectAll connects to all registered servers concurrently.
// It returns an error only when NO server connected successfully.
func (p *UpstreamPool) ConnectAll(ctx context.Context) error {
	p.mu.RLock()
	entries := make([]*ServerEntry, len(p.servers))
	copy(entries, p.servers)
	p.mu.RUnlock()

	type result struct {
		entry *ServerEntry
		err   error
	}

	ch := make(chan result, len(entries))

	var wg sync.WaitGroup
	for _, entry := range entries {
		wg.Add(1)
		go func(e *ServerEntry) {
			defer wg.Done()
			err := e.Manager.Connect(ctx)
			ch <- result{entry: e, err: err}
		}(entry)
	}

	// Close the channel once all goroutines finish.
	go func() {
		wg.Wait()
		close(ch)
	}()

	successCount := 0
	for res := range ch {
		p.mu.Lock()
		if res.err != nil {
			res.entry.Status = "failed"
			res.entry.Error = res.err.Error()
			p.logger.LogErrorEvent("upstream_pool_connect_failed", "upstream_pool", map[string]interface{}{
				"server": res.entry.Key,
				"error":  res.err.Error(),
			})
		} else {
			res.entry.Status = "connected"
			successCount++
			p.logger.LogEvent("upstream_pool_connect_success", "upstream_pool", map[string]interface{}{
				"server": res.entry.Key,
			})
		}
		p.mu.Unlock()
	}

	if successCount == 0 {
		return fmt.Errorf("all upstream servers failed to connect")
	}

	return nil
}

// IsMultiServer returns true when more than one server is registered.
func (p *UpstreamPool) IsMultiServer() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.servers) > 1
}

// Entries returns a snapshot of all server entries (thread-safe).
func (p *UpstreamPool) Entries() []*ServerEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()

	snapshot := make([]*ServerEntry, len(p.servers))
	copy(snapshot, p.servers)
	return snapshot
}

// Get returns the manager for the named server key.
func (p *UpstreamPool) Get(key string) (*UpstreamManager, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, e := range p.servers {
		if e.Key == key {
			return e.Manager, true
		}
	}
	return nil, false
}

// CloseAll terminates all upstream connections.
func (p *UpstreamPool) CloseAll() {
	p.mu.RLock()
	entries := make([]*ServerEntry, len(p.servers))
	copy(entries, p.servers)
	p.mu.RUnlock()

	for _, e := range entries {
		if err := e.Manager.Close(); err != nil {
			p.logger.LogErrorEvent("upstream_pool_close_failed", "upstream_pool", map[string]interface{}{
				"server": e.Key,
				"error":  err.Error(),
			})
		}
	}
}
