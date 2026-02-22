package analysis

import "time"

// AnalysisReport is the complete output of an AI analysis run.
type AnalysisReport struct {
	Status          string           `json:"status"`            // pending|running|complete|error|unconfigured|insufficient_data
	Message         string           `json:"message,omitempty"` // human-readable status detail
	AnalyzedAt      time.Time        `json:"analyzed_at,omitempty"`
	Model           string           `json:"model,omitempty"`
	ToolCount       int              `json:"tool_count"`
	CallCount       int              `json:"call_count"`
	ErrorCount      int              `json:"error_count"`
	Summary         string           `json:"summary,omitempty"`
	Recommendations []Recommendation `json:"recommendations"`
}

// Recommendation is a single AI-generated improvement suggestion.
type Recommendation struct {
	ID           string   `json:"id"`
	Category     string   `json:"category"`             // description|parameter|new_tool|performance|error_pattern
	Priority     string   `json:"priority"`             // high|medium|low
	ToolName     string   `json:"tool_name,omitempty"`
	Title        string   `json:"title"`
	Problem      string   `json:"problem"`
	Suggestion   string   `json:"suggestion"`
	Evidence     []string `json:"evidence"`
	OriginalText string   `json:"original_text,omitempty"`
	ImprovedText string   `json:"improved_text,omitempty"`
}

// toolCallStat accumulates usage data per tool.
type toolCallStat struct {
	Name         string
	CallCount    int
	ErrorCount   int
	TotalDurMs   int64
	SampleArgs   []map[string]interface{} // up to 10 unique argument patterns
	RecentErrors []string                 // up to 20 recent error messages
}
