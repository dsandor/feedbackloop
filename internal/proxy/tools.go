package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

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
			"tool":          tool.Name,
			"title":         tool.Title,
			"description":   tool.Description,
			"input_schema":  tool.InputSchema,
			"output_schema": tool.OutputSchema,
			"annotations":   tool.Annotations,
			"count":         len(tc.tools),
		})
	}

	// Emit a catalog event with full schemas for all discovered tools
	catalog := make([]map[string]interface{}, 0, len(result.Tools))
	for _, tool := range result.Tools {
		catalog = append(catalog, map[string]interface{}{
			"name":          tool.Name,
			"title":         tool.Title,
			"description":   tool.Description,
			"input_schema":  tool.InputSchema,
			"output_schema": tool.OutputSchema,
			"annotations":   tool.Annotations,
		})
	}
	tc.logger.LogEvent("tool_catalog", correlationID, map[string]interface{}{
		"tools":      catalog,
		"total":      len(result.Tools),
		"session_id": session.ID(),
	})

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

// getArgumentKeys returns a list of argument keys from a map
func getArgumentKeys(args map[string]interface{}) []string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	return keys
}

// CreateProxyHandler creates a handler that proxies tool calls to upstream server
func (tc *ToolCache) CreateProxyHandler(toolName string, session *mcp.ClientSession, clientInfo *ClientInfo) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		correlationID := logger.GenerateCorrelationID()
		start := time.Now()

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

		// Extract MCP client info from the initialization handshake
		mcpClientMeta := map[string]interface{}{}
		if initParams := req.Session.InitializeParams(); initParams != nil && initParams.ClientInfo != nil {
			mcpClientMeta["name"] = initParams.ClientInfo.Name
			mcpClientMeta["version"] = initParams.ClientInfo.Version
			mcpClientMeta["title"] = initParams.ClientInfo.Title
		}

		// Extract HTTP headers from transport layer (populated for SSE/HTTP transports)
		transportHeaders := map[string]string{}
		if req.Extra != nil && req.Extra.Header != nil {
			for key, vals := range req.Extra.Header {
				if len(vals) > 0 {
					transportHeaders[key] = vals[0]
				}
			}
		}

		// Log inbound request with full arguments, client info, and transport metadata
		tc.logger.LogInbound("tool_call_request", correlationID, map[string]interface{}{
			"tool":               req.Params.Name,
			"arguments":          arguments,
			"argument_count":     len(arguments),
			"session_id":         session.ID(),
			"client_info":        clientInfo,
			"mcp_client":         mcpClientMeta,
			"transport_headers":  transportHeaders,
		})

		// Create params for upstream call
		params := &mcp.CallToolParams{
			Name:      req.Params.Name,
			Arguments: arguments,
		}

		// Call upstream tool
		result, err := session.CallTool(ctx, params)
		duration := time.Since(start)

		if err != nil {
			// Detect different error types for better logging
			if errors.Is(err, context.Canceled) {
				tc.logger.LogErrorOutbound("tool_call_cancelled", correlationID, map[string]interface{}{
					"tool":        req.Params.Name,
					"duration_ns": duration.Nanoseconds(),
					"client_info": clientInfo,
				})
			} else if errors.Is(err, context.DeadlineExceeded) {
				tc.logger.LogErrorOutbound("tool_call_timeout", correlationID, map[string]interface{}{
					"tool":        req.Params.Name,
					"duration_ns": duration.Nanoseconds(),
					"client_info": clientInfo,
				})
			} else {
				tc.logger.LogErrorOutbound("tool_call_error", correlationID, map[string]interface{}{
					"tool":        req.Params.Name,
					"error":       err.Error(),
					"duration_ns": duration.Nanoseconds(),
					"client_info": clientInfo,
				})
			}
			return nil, err
		}

		// Log successful response with full content and client info
		tc.logger.LogOutbound("tool_call_response", correlationID, map[string]interface{}{
			"tool":          req.Params.Name,
			"is_error":      result.IsError,
			"content_count": len(result.Content),
			"content":       result.Content,
			"duration_ms":   duration.Milliseconds(),
			"duration_ns":   duration.Nanoseconds(),
			"session_id":    session.ID(),
			"client_info":   clientInfo,
		})

		return result, nil
	}
}
