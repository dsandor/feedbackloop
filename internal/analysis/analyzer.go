package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/dsandor/feedbackloop/internal/logger"
)

// Analyzer accumulates tool usage data from the log stream and
// calls an LLM to produce improvement recommendations.
type Analyzer struct {
	log    *logger.Logger
	apiKey string
	model  string

	mu          sync.RWMutex
	toolCatalog []map[string]interface{}
	toolStats   map[string]*toolCallStat
	report      *AnalysisReport
}

// New creates a new Analyzer. apiKey may be empty (analysis will be disabled).
// model should be a Claude model ID like "claude-sonnet-4-6".
func New(log *logger.Logger, apiKey, model string) *Analyzer {
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	return &Analyzer{
		log:       log,
		apiKey:    apiKey,
		model:     model,
		toolStats: make(map[string]*toolCallStat),
		report: &AnalysisReport{
			Status:          "pending",
			Message:         "No analysis has been run yet. Click 'Run Analysis' to start.",
			Recommendations: []Recommendation{},
		},
	}
}

// IsConfigured reports whether an API key is available.
func (a *Analyzer) IsConfigured() bool { return a.apiKey != "" }

// Start launches a background goroutine that subscribes to the logger
// and accumulates tool usage data until ctx is cancelled.
// The subscription is created synchronously before returning so that no
// events are missed due to goroutine scheduling delays.
func (a *Analyzer) Start(ctx context.Context) {
	subID := logger.GenerateCorrelationID()
	ch := a.log.Subscribe(subID)
	go a.watch(ctx, ch, subID)
}

func (a *Analyzer) watch(ctx context.Context, ch chan logger.LogEntry, subID string) {
	defer a.log.Unsubscribe(subID)

	for {
		select {
		case entry, ok := <-ch:
			if !ok {
				return
			}
			a.handleEntry(entry)
		case <-ctx.Done():
			return
		}
	}
}

// SetToolCatalog injects a tool catalog directly (e.g. loaded from disk persistence)
// so the analyzer can run even if it missed the live tool_catalog log event.
func (a *Analyzer) SetToolCatalog(catalog []map[string]interface{}) {
	if len(catalog) == 0 {
		return
	}
	a.mu.Lock()
	a.toolCatalog = catalog
	a.mu.Unlock()
}

func (a *Analyzer) handleEntry(entry logger.LogEntry) {
	msg, ok := entry.Message.(map[string]interface{})
	if !ok {
		return
	}

	switch entry.EventType {
	case "tool_catalog":
		// Capture full tool catalog
		if tools, ok := msg["tools"].([]interface{}); ok {
			catalog := make([]map[string]interface{}, 0, len(tools))
			for _, t := range tools {
				if tm, ok := t.(map[string]interface{}); ok {
					catalog = append(catalog, tm)
				}
			}
			a.mu.Lock()
			a.toolCatalog = catalog
			a.mu.Unlock()
		}

	case "tool_call_request":
		toolName, _ := msg["name"].(string)
		if toolName == "" {
			toolName, _ = msg["tool"].(string)
		}
		if toolName == "" {
			return
		}
		a.mu.Lock()
		stat := a.getOrCreateStat(toolName)
		stat.CallCount++
		// Collect sample arguments (up to 10 unique patterns)
		if args, ok := msg["arguments"].(map[string]interface{}); ok && len(stat.SampleArgs) < 10 {
			stat.SampleArgs = append(stat.SampleArgs, args)
		}
		a.mu.Unlock()

	case "tool_call_response":
		toolName, _ := msg["tool"].(string)
		if toolName == "" {
			return
		}
		durMs, _ := msg["duration_ms"].(float64)
		a.mu.Lock()
		stat := a.getOrCreateStat(toolName)
		stat.TotalDurMs += int64(durMs)
		a.mu.Unlock()

	case "tool_call_error", "tool_call_cancelled", "tool_call_timeout":
		toolName, _ := msg["tool"].(string)
		if toolName == "" {
			return
		}
		a.mu.Lock()
		stat := a.getOrCreateStat(toolName)
		stat.ErrorCount++
		if errMsg, ok := msg["error"].(string); ok && len(stat.RecentErrors) < 20 {
			stat.RecentErrors = append(stat.RecentErrors, errMsg)
		}
		a.mu.Unlock()
	}
}

// getOrCreateStat is NOT thread-safe; caller must hold a.mu.
func (a *Analyzer) getOrCreateStat(name string) *toolCallStat {
	if s, ok := a.toolStats[name]; ok {
		return s
	}
	s := &toolCallStat{Name: name}
	a.toolStats[name] = s
	return s
}

// GetReport returns the current analysis report (never nil).
func (a *Analyzer) GetReport() *AnalysisReport {
	if !a.IsConfigured() {
		return &AnalysisReport{
			Status:          "unconfigured",
			Message:         "Set the ANTHROPIC_API_KEY environment variable to enable AI analysis.",
			Recommendations: []Recommendation{},
		}
	}
	a.mu.RLock()
	r := a.report
	a.mu.RUnlock()
	return r
}

// RunAnalysis starts an async analysis. Returns an error if already running or not configured.
func (a *Analyzer) RunAnalysis(ctx context.Context) error {
	if !a.IsConfigured() {
		return fmt.Errorf("ANTHROPIC_API_KEY not set")
	}

	a.mu.Lock()
	if a.report != nil && a.report.Status == "running" {
		a.mu.Unlock()
		return fmt.Errorf("analysis already in progress")
	}
	a.report = &AnalysisReport{
		Status:          "running",
		Message:         "Analysis in progress...",
		Recommendations: []Recommendation{},
	}
	a.mu.Unlock()

	go func() {
		report, err := a.doAnalysis(context.Background())
		a.mu.Lock()
		if err != nil {
			a.report = &AnalysisReport{
				Status:          "error",
				Message:         err.Error(),
				Recommendations: []Recommendation{},
			}
		} else {
			a.report = report
		}
		a.mu.Unlock()

		if err != nil {
			a.log.LogErrorEvent("analysis_error", "analysis", map[string]interface{}{
				"error": err.Error(),
			})
		} else {
			a.log.LogEvent("analysis_complete", "analysis", map[string]interface{}{
				"recommendation_count": len(report.Recommendations),
				"tool_count":           report.ToolCount,
				"call_count":           report.CallCount,
			})
		}
	}()

	return nil
}

func (a *Analyzer) doAnalysis(ctx context.Context) (*AnalysisReport, error) {
	a.mu.RLock()
	catalog := a.toolCatalog
	stats := make(map[string]*toolCallStat, len(a.toolStats))
	for k, v := range a.toolStats {
		cp := *v
		stats[k] = &cp
	}
	a.mu.RUnlock()

	if len(catalog) == 0 {
		return &AnalysisReport{
			Status:          "insufficient_data",
			Message:         "No tools discovered yet. Wait for the MCP server to connect.",
			Recommendations: []Recommendation{},
		}, nil
	}

	// Count totals for report header
	totalCalls, totalErrors := 0, 0
	for _, s := range stats {
		totalCalls += s.CallCount
		totalErrors += s.ErrorCount
	}

	prompt := a.buildPrompt(catalog, stats)

	client := anthropic.NewClient(option.WithAPIKey(a.apiKey))

	ctxWithTimeout, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	msg, err := client.Messages.New(ctxWithTimeout, anthropic.MessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: 4096,
		System: []anthropic.TextBlockParam{
			{Text: "You are an expert in MCP (Model Context Protocol) tool design and LLM performance optimization. You analyze tool usage patterns and provide specific, actionable recommendations. Always respond with valid JSON only — no markdown, no prose outside the JSON object."},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("LLM API error: %w", err)
	}

	// Extract text from response
	var responseText string
	for _, block := range msg.Content {
		if tb, ok := block.AsAny().(anthropic.TextBlock); ok {
			responseText += tb.Text
		}
	}

	// Parse the JSON response
	// Strip any markdown code fences if present
	responseText = strings.TrimSpace(responseText)
	if strings.HasPrefix(responseText, "```") {
		lines := strings.Split(responseText, "\n")
		if len(lines) > 2 {
			responseText = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}

	var llmResult struct {
		Summary         string           `json:"summary"`
		Recommendations []Recommendation `json:"recommendations"`
	}
	if err := json.Unmarshal([]byte(responseText), &llmResult); err != nil {
		truncLen := 500
		if len(responseText) < truncLen {
			truncLen = len(responseText)
		}
		return nil, fmt.Errorf("failed to parse LLM response as JSON: %w\n\nRaw response: %s", err, responseText[:truncLen])
	}

	// Ensure IDs are set
	for i := range llmResult.Recommendations {
		if llmResult.Recommendations[i].ID == "" {
			llmResult.Recommendations[i].ID = fmt.Sprintf("rec_%03d", i+1)
		}
		if llmResult.Recommendations[i].Evidence == nil {
			llmResult.Recommendations[i].Evidence = []string{}
		}
	}

	return &AnalysisReport{
		Status:          "complete",
		AnalyzedAt:      time.Now().UTC(),
		Model:           a.model,
		ToolCount:       len(catalog),
		CallCount:       totalCalls,
		ErrorCount:      totalErrors,
		Summary:         llmResult.Summary,
		Recommendations: llmResult.Recommendations,
	}, nil
}

func (a *Analyzer) buildPrompt(catalog []map[string]interface{}, stats map[string]*toolCallStat) string {
	catalogJSON, _ := json.MarshalIndent(catalog, "", "  ")

	// Build per-tool usage summary
	type toolUsage struct {
		Name          string                   `json:"name"`
		CallCount     int                      `json:"call_count"`
		ErrorCount    int                      `json:"error_count"`
		ErrorRate     string                   `json:"error_rate"`
		AvgDurationMs string                   `json:"avg_duration_ms"`
		SampleArgs    []map[string]interface{} `json:"sample_arguments,omitempty"`
		RecentErrors  []string                 `json:"recent_errors,omitempty"`
	}

	usages := make([]toolUsage, 0, len(stats))
	for _, s := range stats {
		var avgDur string
		if s.CallCount > 0 {
			avgDur = fmt.Sprintf("%.1f", float64(s.TotalDurMs)/float64(s.CallCount))
		} else {
			avgDur = "n/a"
		}
		errRate := "0%"
		if s.CallCount > 0 {
			errRate = fmt.Sprintf("%.1f%%", float64(s.ErrorCount)/float64(s.CallCount)*100)
		}
		usages = append(usages, toolUsage{
			Name:          s.Name,
			CallCount:     s.CallCount,
			ErrorCount:    s.ErrorCount,
			ErrorRate:     errRate,
			AvgDurationMs: avgDur,
			SampleArgs:    s.SampleArgs,
			RecentErrors:  s.RecentErrors,
		})
	}
	usageJSON, _ := json.MarshalIndent(usages, "", "  ")

	return fmt.Sprintf(`You are analyzing an MCP (Model Context Protocol) server that was observed through a transparent proxy.

## Tool Catalog
The following %d tools are exposed by the real MCP server:
%s

## Usage Statistics
The following usage data was collected from real LLM interactions with these tools:
%s

## Your Task
Analyze the tool catalog and usage data to produce specific, actionable recommendations to improve:

1. **Tool Descriptions**: Are descriptions clear and specific enough for LLMs to choose the right tool and use it correctly? Vague descriptions cause LLMs to call the wrong tool or supply wrong arguments.
2. **Missing Parameters**: Are there parameters that would reduce round-trips or improve specificity? Look for patterns in sample arguments that suggest common use cases not served by existing params.
3. **New Tools**: Do usage patterns reveal that a composite or helper tool would reduce the number of calls needed to accomplish common tasks?
4. **Performance Issues**: Do high average durations or error rates point to tools that need optimization?
5. **Error Patterns**: Do repeated errors suggest LLMs are misusing a tool due to unclear documentation?

For each recommendation cite SPECIFIC evidence from the usage statistics (call counts, error rates, sample argument patterns).

Respond with ONLY a valid JSON object — no markdown fences, no preamble:
{
  "summary": "2-3 sentence executive summary of the biggest opportunities",
  "recommendations": [
    {
      "id": "rec_001",
      "category": "description|parameter|new_tool|performance|error_pattern",
      "priority": "high|medium|low",
      "tool_name": "exact tool name or null for new_tool category",
      "title": "short action-oriented title (max 60 chars)",
      "problem": "specific problem observed with evidence",
      "suggestion": "specific actionable change to make",
      "evidence": ["specific stat or pattern observed", "..."],
      "original_text": "current description or schema excerpt if applicable",
      "improved_text": "improved version if applicable"
    }
  ]
}`, len(catalog), string(catalogJSON), string(usageJSON))
}
