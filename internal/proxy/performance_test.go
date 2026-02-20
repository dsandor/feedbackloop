package proxy

import (
	"sync"
	"testing"

	"github.com/dsandor/feedbackloop/internal/logger"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestConcurrentToolCalls verifies that multiple tool calls can execute concurrently
// without blocking or causing race conditions
func TestConcurrentToolCalls(t *testing.T) {
	log := logger.New()
	cache := NewToolCache(log)

	// Populate cache with test tools
	cache.mu.Lock()
	for i := 0; i < 10; i++ {
		cache.tools[string(rune('a'+i))] = &mcp.Tool{
			Name:        string(rune('a' + i)),
			Description: "Test tool",
		}
	}
	cache.mu.Unlock()

	// Test concurrent reads from cache (simulating concurrent tool calls)
	var wg sync.WaitGroup
	errCount := 0
	var mu sync.Mutex

	// Launch many concurrent goroutines
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Each goroutine performs multiple operations
			for j := 0; j < 10; j++ {
				// Get tools (simulates tool discovery during request)
				tools := cache.GetTools()
				if len(tools) != 10 {
					mu.Lock()
					errCount++
					mu.Unlock()
					return
				}

				// Get specific tool (simulates tool lookup during call)
				toolName := string(rune('a' + (id % 10)))
				_, ok := cache.GetTool(toolName)
				if !ok {
					mu.Lock()
					errCount++
					mu.Unlock()
					return
				}
			}
		}(i)
	}

	wg.Wait()

	if errCount > 0 {
		t.Errorf("concurrent tool calls failed with %d errors", errCount)
	}
}

// BenchmarkGetArgumentKeys benchmarks argument key extraction
func BenchmarkGetArgumentKeys(b *testing.B) {
	args := map[string]interface{}{
		"url":       "https://example.com",
		"timeout":   5000,
		"method":    "GET",
		"headers":   map[string]string{"Content-Type": "application/json"},
		"body":      `{"key": "value"}`,
		"retries":   3,
		"waitUntil": "load",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		keys := getArgumentKeys(args)
		if len(keys) != 7 {
			b.Errorf("expected 7 keys, got %d", len(keys))
		}
	}
}

// BenchmarkToolCacheGetTools benchmarks retrieving all tools from cache
func BenchmarkToolCacheGetTools(b *testing.B) {
	log := logger.New()
	cache := NewToolCache(log)

	// Populate cache with realistic number of tools (26 like chrome-devtools-mcp)
	cache.mu.Lock()
	for i := 0; i < 26; i++ {
		cache.tools[string(rune('a'+i))] = &mcp.Tool{
			Name:        string(rune('a' + i)),
			Description: "Test tool",
		}
	}
	cache.mu.Unlock()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tools := cache.GetTools()
		if len(tools) != 26 {
			b.Errorf("expected 26 tools, got %d", len(tools))
		}
	}
}

// BenchmarkToolCacheGetTool benchmarks retrieving a specific tool from cache
func BenchmarkToolCacheGetTool(b *testing.B) {
	log := logger.New()
	cache := NewToolCache(log)

	// Populate cache
	cache.mu.Lock()
	for i := 0; i < 26; i++ {
		cache.tools[string(rune('a'+i))] = &mcp.Tool{
			Name:        string(rune('a' + i)),
			Description: "Test tool",
		}
	}
	cache.mu.Unlock()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tool, ok := cache.GetTool("m") // Middle of alphabet
		if !ok {
			b.Error("tool 'm' not found")
		}
		if tool == nil {
			b.Error("got nil tool")
		}
	}
}

// TestProxyHandlerConcurrency tests that handlers can be created concurrently
func TestProxyHandlerConcurrency(t *testing.T) {
	log := logger.New()
	cache := NewToolCache(log)

	// Create multiple handlers concurrently
	var wg sync.WaitGroup
	handlers := make([]mcp.ToolHandler, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// Session is nil - we're just testing handler creation concurrency
			handlers[idx] = cache.CreateProxyHandler(string(rune('a'+idx)), nil)
		}(i)
	}

	wg.Wait()

	// Verify all handlers created
	for i, handler := range handlers {
		if handler == nil {
			t.Errorf("handler %d is nil", i)
		}
	}
}

// Note: Full performance testing with actual tool call proxying requires
// integration with a real upstream server and cannot be done in unit tests
// due to MCP SDK's unexported types. Integration tests validate end-to-end
// performance with chrome-devtools-mcp.
