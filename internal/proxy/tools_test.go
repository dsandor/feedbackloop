package proxy

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/dsandor/feedbackloop/internal/logger"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolCacheCreation(t *testing.T) {
	log := logger.New()
	cache := NewToolCache(log)

	if cache == nil {
		t.Fatal("NewToolCache returned nil")
	}

	if cache.tools == nil {
		t.Error("tools map not initialized")
	}

	if cache.logger == nil {
		t.Error("logger not set")
	}
}

func TestToolCacheManualPopulation(t *testing.T) {
	log := logger.New()
	cache := NewToolCache(log)

	// Manually populate cache (simulating what DiscoverTools would do)
	cache.mu.Lock()
	cache.tools["test-tool-1"] = &mcp.Tool{
		Name:        "test-tool-1",
		Description: "A test tool",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"param1": map[string]interface{}{"type": "string"},
			},
		},
	}
	cache.tools["test-tool-2"] = &mcp.Tool{
		Name:        "test-tool-2",
		Description: "Another test tool",
		InputSchema: map[string]interface{}{
			"type": "object",
		},
	}
	cache.mu.Unlock()

	// Verify all tools were cached
	tools := cache.GetTools()
	if len(tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(tools))
	}

	// Verify specific tools
	tool1, ok := cache.GetTool("test-tool-1")
	if !ok {
		t.Error("test-tool-1 not found in cache")
	} else {
		if tool1.Name != "test-tool-1" {
			t.Errorf("tool name mismatch: got %s", tool1.Name)
		}
		if tool1.Description != "A test tool" {
			t.Errorf("description mismatch: got %s", tool1.Description)
		}
	}

	tool2, ok := cache.GetTool("test-tool-2")
	if !ok {
		t.Error("test-tool-2 not found in cache")
	} else {
		if tool2.Name != "test-tool-2" {
			t.Errorf("tool name mismatch: got %s", tool2.Name)
		}
	}
}

func TestSchemaFidelity(t *testing.T) {
	log := logger.New()
	cache := NewToolCache(log)

	// Create tool with complex schema
	complexSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url": map[string]interface{}{
				"type":        "string",
				"description": "The URL to navigate to",
			},
			"timeout": map[string]interface{}{
				"type":        "number",
				"description": "Timeout in milliseconds",
			},
			"waitUntil": map[string]interface{}{
				"type": "string",
				"enum": []interface{}{"load", "domcontentloaded", "networkidle"},
			},
		},
		"required": []interface{}{"url"},
	}

	// Manually populate cache
	cache.mu.Lock()
	cache.tools["navigate_page"] = &mcp.Tool{
		Name:        "navigate_page",
		Description: "Navigates the currently selected page to a URL.",
		InputSchema: complexSchema,
	}
	cache.mu.Unlock()

	// Verify schema is preserved exactly
	tool, ok := cache.GetTool("navigate_page")
	if !ok {
		t.Fatal("navigate_page not found")
	}

	// Check schema structure
	schema, ok := tool.InputSchema.(map[string]interface{})
	if !ok {
		t.Fatal("InputSchema is not a map")
	}

	if schema["type"] != "object" {
		t.Errorf("schema type mismatch: got %v", schema["type"])
	}

	properties, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("properties is not a map")
	}

	if len(properties) != 3 {
		t.Errorf("expected 3 properties, got %d", len(properties))
	}

	// Verify specific property
	urlProp, ok := properties["url"].(map[string]interface{})
	if !ok {
		t.Fatal("url property is not a map")
	}

	if urlProp["type"] != "string" {
		t.Errorf("url type mismatch: got %v", urlProp["type"])
	}

	if urlProp["description"] != "The URL to navigate to" {
		t.Errorf("url description mismatch: got %v", urlProp["description"])
	}
}

func TestToolCacheThreadSafety(t *testing.T) {
	log := logger.New()
	cache := NewToolCache(log)

	// Populate cache with initial tools
	cache.mu.Lock()
	cache.tools["tool-1"] = &mcp.Tool{Name: "tool-1", Description: "Tool 1"}
	cache.tools["tool-2"] = &mcp.Tool{Name: "tool-2", Description: "Tool 2"}
	cache.tools["tool-3"] = &mcp.Tool{Name: "tool-3", Description: "Tool 3"}
	cache.mu.Unlock()

	// Test concurrent reads
	var wg sync.WaitGroup
	errChan := make(chan error, 100)

	// Start multiple goroutines reading from cache
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Perform multiple read operations
			for j := 0; j < 10; j++ {
				// GetTools
				tools := cache.GetTools()
				if len(tools) != 3 {
					errChan <- fmt.Errorf("unexpected tool count in concurrent read: got %d, want 3", len(tools))
					return
				}

				// GetTool
				_, ok := cache.GetTool("tool-1")
				if !ok {
					errChan <- errors.New("tool-1 not found in concurrent read")
					return
				}
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	// Check for errors
	for err := range errChan {
		t.Errorf("concurrent read error: %v", err)
	}
}

func TestProxyHandlerCreation(t *testing.T) {
	log := logger.New()
	cache := NewToolCache(log)

	// Create proxy handler (session can be nil for this test - we're just testing creation)
	handler := cache.CreateProxyHandler("test-tool", nil, nil)

	// Verify handler is not nil
	if handler == nil {
		t.Error("CreateProxyHandler returned nil")
	}

	// Note: Full integration testing of proxy handler is done in integration tests
	// since it requires proper MCP SDK request/response structures and active session
}

func TestGetArgumentKeys(t *testing.T) {
	tests := []struct {
		name     string
		args     map[string]interface{}
		expected int
	}{
		{
			name:     "empty arguments",
			args:     map[string]interface{}{},
			expected: 0,
		},
		{
			name: "single argument",
			args: map[string]interface{}{
				"url": "https://example.com",
			},
			expected: 1,
		},
		{
			name: "multiple arguments",
			args: map[string]interface{}{
				"url":     "https://example.com",
				"timeout": 5000,
				"method":  "GET",
			},
			expected: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys := getArgumentKeys(tt.args)
			if len(keys) != tt.expected {
				t.Errorf("expected %d keys, got %d", tt.expected, len(keys))
			}

			// Verify all expected keys are present
			for key := range tt.args {
				found := false
				for _, k := range keys {
					if k == key {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected key %s not found in result", key)
				}
			}
		})
	}
}

func TestGetToolNotFound(t *testing.T) {
	log := logger.New()
	cache := NewToolCache(log)

	// Try to get a tool that doesn't exist
	_, ok := cache.GetTool("nonexistent-tool")
	if ok {
		t.Error("GetTool returned true for nonexistent tool")
	}
}

func TestGetToolsEmpty(t *testing.T) {
	log := logger.New()
	cache := NewToolCache(log)

	tools := cache.GetTools()
	if len(tools) != 0 {
		t.Errorf("expected 0 tools in empty cache, got %d", len(tools))
	}
}

func TestGetToolsReturnsNewSlice(t *testing.T) {
	log := logger.New()
	cache := NewToolCache(log)

	// Populate cache
	cache.mu.Lock()
	cache.tools["tool-1"] = &mcp.Tool{Name: "tool-1"}
	cache.mu.Unlock()

	// Get tools twice
	tools1 := cache.GetTools()
	tools2 := cache.GetTools()

	// Verify they are different slices (defensive copy)
	tools1[0] = nil
	if tools2[0] == nil {
		t.Error("GetTools did not return a defensive copy - modifying one slice affected the other")
	}
}
