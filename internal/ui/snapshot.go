package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dsandor/feedbackloop/internal/analysis"
	"github.com/dsandor/feedbackloop/internal/logger"
)

// Snapshot captures a point-in-time view of log entries, tool catalog, and analysis report.
type Snapshot struct {
	Version     string                   `json:"version"`
	Name        string                   `json:"name"`
	CreatedAt   time.Time                `json:"created_at"`
	MaxLogLines int                      `json:"max_log_lines"`
	LogEntries  []logger.LogEntry        `json:"log_entries"`
	ToolCatalog json.RawMessage          `json:"tool_catalog,omitempty"`
	Analysis    *analysis.AnalysisReport `json:"analysis,omitempty"`
}

// SnapshotMeta is the lightweight metadata returned by the list endpoint.
type SnapshotMeta struct {
	Filename  string    `json:"filename"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	SizeBytes int64     `json:"size_bytes"`
	LogCount  int       `json:"log_count"`
}

func (s *Server) saveSnapshot(name string, maxLogLines int) (*SnapshotMeta, error) {
	if s.snapshotsDir == "" {
		return nil, fmt.Errorf("snapshots directory not configured")
	}

	// Collect log entries from in-memory buffer.
	s.logBufMu.RLock()
	buf := make([]logger.LogEntry, len(s.logBuffer))
	copy(buf, s.logBuffer)
	s.logBufMu.RUnlock()

	entries := buf
	if maxLogLines > 0 && len(entries) > maxLogLines {
		entries = entries[len(entries)-maxLogLines:]
	}

	// Collect tool catalog.
	s.toolMu.RLock()
	catalog := s.toolCatalog
	s.toolMu.RUnlock()

	// Collect analysis report if complete.
	var report *analysis.AnalysisReport
	if s.analyzer != nil {
		r := s.analyzer.GetReport()
		if r != nil && r.Status == "complete" {
			report = r
		}
	}

	now := time.Now().UTC()
	snap := Snapshot{
		Version:     "1",
		Name:        name,
		CreatedAt:   now,
		MaxLogLines: maxLogLines,
		LogEntries:  entries,
		ToolCatalog: catalog,
		Analysis:    report,
	}

	safeName := sanitizeSnapshotName(name)
	filename := fmt.Sprintf("snapshot_%s_%s.json", now.Format("20060102_150405"), safeName)
	path := filepath.Join(s.snapshotsDir, filename)

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal snapshot: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return nil, fmt.Errorf("write snapshot: %w", err)
	}

	var sz int64
	if fi, err := os.Stat(path); err == nil {
		sz = fi.Size()
	}

	return &SnapshotMeta{
		Filename:  filename,
		Name:      name,
		CreatedAt: now,
		SizeBytes: sz,
		LogCount:  len(entries),
	}, nil
}

func (s *Server) listSnapshots() ([]SnapshotMeta, error) {
	if s.snapshotsDir == "" {
		return []SnapshotMeta{}, nil
	}

	dirEntries, err := os.ReadDir(s.snapshotsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []SnapshotMeta{}, nil
		}
		return nil, err
	}

	var metas []SnapshotMeta
	for _, de := range dirEntries {
		if de.IsDir() || !strings.HasPrefix(de.Name(), "snapshot_") || !strings.HasSuffix(de.Name(), ".json") {
			continue
		}

		fi, err := de.Info()
		if err != nil {
			continue
		}

		path := filepath.Join(s.snapshotsDir, de.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		// Decode only the header fields to build metadata without loading all log entries.
		var header struct {
			Name       string            `json:"name"`
			CreatedAt  time.Time         `json:"created_at"`
			LogEntries []json.RawMessage `json:"log_entries"`
		}
		if err := json.Unmarshal(data, &header); err != nil {
			continue
		}

		metas = append(metas, SnapshotMeta{
			Filename:  de.Name(),
			Name:      header.Name,
			CreatedAt: header.CreatedAt,
			SizeBytes: fi.Size(),
			LogCount:  len(header.LogEntries),
		})
	}

	// Newest first.
	sort.Slice(metas, func(i, j int) bool {
		return metas[i].CreatedAt.After(metas[j].CreatedAt)
	})

	return metas, nil
}

func (s *Server) loadSnapshot(filename string) (*Snapshot, error) {
	if s.snapshotsDir == "" {
		return nil, fmt.Errorf("snapshots directory not configured")
	}

	// Guard against path traversal.
	if strings.ContainsAny(filename, "/\\") || strings.Contains(filename, "..") {
		return nil, fmt.Errorf("invalid filename")
	}

	path := filepath.Join(s.snapshotsDir, filename)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("unmarshal snapshot: %w", err)
	}

	return &snap, nil
}

func (s *Server) deleteSnapshot(filename string) error {
	if s.snapshotsDir == "" {
		return fmt.Errorf("snapshots directory not configured")
	}

	if strings.ContainsAny(filename, "/\\") || strings.Contains(filename, "..") {
		return fmt.Errorf("invalid filename")
	}

	return os.Remove(filepath.Join(s.snapshotsDir, filename))
}

func sanitizeSnapshotName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	s := b.String()
	if len(s) > 40 {
		s = s[:40]
	}
	if s == "" {
		s = "snapshot"
	}
	return s
}
