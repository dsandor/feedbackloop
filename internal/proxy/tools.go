package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/dsandor/feedbackloop/internal/logger"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolCache manages discovered tools from upstream server
type ToolCache struct {
	tools  map[string]*mcp.Tool
	mu     sync.RWMutex
	logger *logger.Logger
}

// NewToolCache creates a new tool cache
func NewToolCache(log *logger.Logger) *ToolCache {
	return &ToolCache{
		tools:  make(map[string]*mcp.Tool),
		logger: log,
	}
}

// DiscoverTools queries upstream server for available tools and caches them
func (tc *ToolCache) DiscoverTools(ctx context.Context, session *mcp.ClientSession) error {
	correlationID := logger.GenerateCorrelationID()

	tc.logger.LogEvent("tool_discovery_started", correlationID, map[string]interface{}{
		"session_id": session.ID(),
	})

	// Call tools/list on upstream server
	result, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		tc.logger.LogErrorEvent("tool_discovery_failed", correlationID, map[string]interface{}{
			"error":      err.Error(),
			"session_id": session.ID(),
		})
		return fmt.Errorf("failed to list tools from upstream: %w", err)
	}

	// Cache discovered tools
	tc.mu.Lock()
	defer tc.mu.Unlock()

	for _, tool := range result.Tools {
		tc.tools[tool.Name] = tool

		tc.logger.LogEvent("tool_discovered", correlationID, map[string]interface{}{
			"tool":        tool.Name,
			"description": tool.Description,
			"count":       len(tc.tools),
		})
	}

	tc.logger.LogEvent("tool_discovery_complete", correlationID, map[string]interface{}{
		"total_tools": len(tc.tools),
		"session_id":  session.ID(),
	})

	return nil
}

// GetTools returns all cached tools (thread-safe)
func (tc *ToolCache) GetTools() []*mcp.Tool {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	tools := make([]*mcp.Tool, 0, len(tc.tools))
	for _, tool := range tc.tools {
		tools = append(tools, tool)
	}

	return tools
}

// GetTool retrieves a specific tool by name (thread-safe)
func (tc *ToolCache) GetTool(name string) (*mcp.Tool, bool) {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	tool, ok := tc.tools[name]
	return tool, ok
}

// CreateProxyHandler creates a handler that proxies tool calls to upstream server
func (tc *ToolCache) CreateProxyHandler(toolName string, session *mcp.ClientSession) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		correlationID := logger.GenerateCorrelationID()

		// Unmarshal arguments from raw JSON
		var arguments map[string]interface{}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &arguments); err != nil {
				tc.logger.LogErrorEvent("tool_call_unmarshal_failed", correlationID, map[string]interface{}{
					"tool":  req.Params.Name,
					"error": err.Error(),
				})
				return nil, fmt.Errorf("failed to unmarshal arguments: %w", err)
			}
		}

		// Log inbound request
		tc.logger.LogInbound("tool_call_request", correlationID, map[string]interface{}{
			"tool":      req.Params.Name,
			"arguments": arguments,
		})

		// Create params for upstream call
		params := &mcp.CallToolParams{
			Name:      req.Params.Name,
			Arguments: arguments,
		}

		// Call upstream tool
		result, err := session.CallTool(ctx, params)

		if err != nil {
			// Log error response
			tc.logger.LogErrorOutbound("tool_call_error", correlationID, map[string]interface{}{
				"tool":  req.Params.Name,
				"error": err.Error(),
			})
			return nil, err
		}

		// Log successful response
		tc.logger.LogOutbound("tool_call_response", correlationID, map[string]interface{}{
			"tool":    req.Params.Name,
			"isError": result.IsError,
		})

		return result, nil
	}
}
