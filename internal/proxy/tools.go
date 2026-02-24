package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/dsandor/feedbackloop/internal/logger"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolRoute records which upstream server and original (unprefixed) tool name
// should be used when forwarding a tool call.
type ToolRoute struct {
	ServerKey    string
	OriginalName string // unprefixed name to send upstream
	Session      *mcp.ClientSession
}

// ToolCache manages discovered tools from upstream server(s).
type ToolCache struct {
	tools         map[string]*mcp.Tool  // keyed by exposed (possibly prefixed) name
	routes        map[string]*ToolRoute // keyed by exposed (possibly prefixed) name
	overrides     map[string]string     // keyed by exposed name -> override description
	overridesPath string                // file path for persisting overrides
	multiServer   bool
	mu            sync.RWMutex
	logger        *logger.Logger
}

// NewToolCache creates a new tool cache.
func NewToolCache(log *logger.Logger) *ToolCache {
	return &ToolCache{
		tools:     make(map[string]*mcp.Tool),
		routes:    make(map[string]*ToolRoute),
		overrides: make(map[string]string),
		logger:    log,
	}
}

// SetOverridesPath sets the file path for persisting description overrides and
// immediately loads any previously saved overrides from that file.
func (tc *ToolCache) SetOverridesPath(path string) {
	tc.mu.Lock()
	tc.overridesPath = path
	tc.mu.Unlock()
	tc.loadOverrides()
}

// GetOverrides returns a snapshot of the current overrides map (thread-safe).
func (tc *ToolCache) GetOverrides() map[string]string {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	result := make(map[string]string, len(tc.overrides))
	for k, v := range tc.overrides {
		result[k] = v
	}
	return result
}

// loadOverrides reads persisted overrides from disk. It is a no-op if the
// file does not exist yet.
func (tc *ToolCache) loadOverrides() {
	tc.mu.RLock()
	path := tc.overridesPath
	tc.mu.RUnlock()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return // file may not exist yet
	}
	var persisted map[string]string
	if err := json.Unmarshal(data, &persisted); err != nil {
		tc.logger.LogErrorEvent("overrides_load_failed", logger.GenerateCorrelationID(), map[string]interface{}{
			"error": err.Error(),
			"path":  path,
		})
		return
	}
	tc.mu.Lock()
	for k, v := range persisted {
		tc.overrides[k] = v
	}
	tc.mu.Unlock()
	tc.logger.LogEvent("overrides_loaded", logger.GenerateCorrelationID(), map[string]interface{}{
		"count": len(persisted),
		"path":  path,
	})
}

// saveOverrides writes the current overrides map to disk.
func (tc *ToolCache) saveOverrides() {
	tc.mu.RLock()
	path := tc.overridesPath
	snapshot := make(map[string]string, len(tc.overrides))
	for k, v := range tc.overrides {
		snapshot[k] = v
	}
	tc.mu.RUnlock()
	if path == "" {
		return
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		tc.logger.LogErrorEvent("overrides_save_failed", logger.GenerateCorrelationID(), map[string]interface{}{
			"error": err.Error(),
		})
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		tc.logger.LogErrorEvent("overrides_save_failed", logger.GenerateCorrelationID(), map[string]interface{}{
			"error": err.Error(),
			"path":  path,
		})
	}
}

// UpdateToolDescription stores an override description for a tool by its exposed name.
// The override is applied immediately to the in-memory tool definition if the tool
// is currently cached. It is also persisted in the overrides map so that the next
// time DiscoverAllTools runs (e.g. on client reconnect in HTTP mode) the override
// is re-applied to fresh tool definitions fetched from upstream. The override is
// also saved to disk so it survives process restarts.
func (tc *ToolCache) UpdateToolDescription(toolName, newDescription string) error {
	tc.mu.Lock()

	if _, exists := tc.tools[toolName]; !exists {
		tc.mu.Unlock()
		return fmt.Errorf("tool %q not found in cache", toolName)
	}

	// Persist the override for future discovery cycles.
	tc.overrides[toolName] = newDescription

	// Apply immediately to the current in-memory tool definition.
	tc.tools[toolName].Description = newDescription

	tc.mu.Unlock()

	tc.logger.LogEvent("tool_description_updated", logger.GenerateCorrelationID(), map[string]interface{}{
		"tool":         toolName,
		"description":  newDescription,
		"has_override": true,
	})

	// Persist to disk (after lock released).
	tc.saveOverrides()

	return nil
}

// applyOverrides applies any stored description overrides to the in-memory tools map.
// Caller must NOT hold tc.mu (this method acquires a write lock internally).
func (tc *ToolCache) applyOverrides() {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	for name, desc := range tc.overrides {
		if tool, ok := tc.tools[name]; ok {
			tool.Description = desc
		}
	}
}

// DiscoverTools queries a single upstream session for available tools and caches
// them (single-server backward-compat path).
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

	// Cache discovered tools (lock released before calling applyOverrides to avoid deadlock).
	tc.mu.Lock()
	for _, tool := range result.Tools {
		tc.tools[tool.Name] = tool
		tc.routes[tool.Name] = &ToolRoute{
			ServerKey:    "",
			OriginalName: tool.Name,
			Session:      session,
		}

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
	tc.mu.Unlock()

	// Apply any stored overrides on top of freshly discovered tools.
	tc.applyOverrides()

	// Build catalog from live tools (so it reflects overrides) and emit event.
	catalog := make([]map[string]interface{}, 0, len(result.Tools))
	tc.mu.RLock()
	for _, tool := range result.Tools {
		entry := map[string]interface{}{
			"name":          tool.Name,
			"title":         tool.Title,
			"description":   tool.Description,
			"input_schema":  tool.InputSchema,
			"output_schema": tool.OutputSchema,
			"annotations":   tool.Annotations,
		}
		// Use the (potentially overridden) description from tc.tools.
		if cached, ok := tc.tools[tool.Name]; ok {
			entry["description"] = cached.Description
		}
		if _, hasOverride := tc.overrides[tool.Name]; hasOverride {
			entry["has_override"] = true
		}
		catalog = append(catalog, entry)
	}
	totalTools := len(tc.tools)
	tc.mu.RUnlock()

	tc.logger.LogEvent("tool_catalog", correlationID, map[string]interface{}{
		"tools":      catalog,
		"total":      totalTools,
		"session_id": session.ID(),
	})

	tc.logger.LogEvent("tool_discovery_complete", correlationID, map[string]interface{}{
		"total_tools": totalTools,
		"session_id":  session.ID(),
	})

	return nil
}

// DiscoverAllTools queries all connected servers in the pool, applies namespace
// prefixing when multiServer is true, detects collisions, and emits a unified
// tool_catalog log event.
func (tc *ToolCache) DiscoverAllTools(ctx context.Context, pool *UpstreamPool) error {
	correlationID := logger.GenerateCorrelationID()
	multiServer := pool.IsMultiServer()

	tc.mu.Lock()
	tc.multiServer = multiServer
	tc.mu.Unlock()

	entries := pool.Entries()

	// collisionTracker maps original tool name -> first server key that claimed it.
	collisionTracker := make(map[string]string)

	// catalog accumulates tool descriptors for the final tool_catalog event.
	var catalog []map[string]interface{}

	// serversSummary accumulates per-server stats for the catalog event.
	serversSummary := make(map[string]map[string]interface{})

	for _, entry := range entries {
		if entry.Status != "connected" {
			serversSummary[entry.Key] = map[string]interface{}{
				"tool_count": 0,
				"status":     entry.Status,
			}
			continue
		}

		session := entry.Manager.Session()

		tc.logger.LogEvent("tool_discovery_started", correlationID, map[string]interface{}{
			"server":     entry.Key,
			"session_id": session.ID(),
		})

		result, err := session.ListTools(ctx, &mcp.ListToolsParams{})
		if err != nil {
			tc.logger.LogErrorEvent("tool_discovery_failed", correlationID, map[string]interface{}{
				"server":     entry.Key,
				"error":      err.Error(),
				"session_id": session.ID(),
			})
			serversSummary[entry.Key] = map[string]interface{}{
				"tool_count": 0,
				"status":     "failed",
			}
			continue
		}

		tc.mu.Lock()
		serverToolCount := 0
		for _, tool := range result.Tools {
			exposedName := tool.Name
			if multiServer {
				exposedName = entry.Key + "__" + tool.Name
			}

			// Collision detection (only meaningful in multi-server mode, but
			// we track original names regardless).
			if firstKey, seen := collisionTracker[tool.Name]; seen {
				tc.logger.LogEvent("tool_name_collision", correlationID, map[string]interface{}{
					"original_name": tool.Name,
					"server_a":      firstKey,
					"server_b":      entry.Key,
					"exposed_name":  exposedName,
				})
			} else {
				collisionTracker[tool.Name] = entry.Key
			}

			// Build an exposed tool with the (potentially prefixed) name.
			exposedTool := &mcp.Tool{
				Name:         exposedName,
				Title:        tool.Title,
				Description:  tool.Description,
				InputSchema:  tool.InputSchema,
				OutputSchema: tool.OutputSchema,
				Annotations:  tool.Annotations,
			}

			tc.tools[exposedName] = exposedTool
			tc.routes[exposedName] = &ToolRoute{
				ServerKey:    entry.Key,
				OriginalName: tool.Name,
				Session:      session,
			}

			catalog = append(catalog, map[string]interface{}{
				"name":          exposedName,
				"original_name": tool.Name,
				"server":        entry.Key,
				"title":         tool.Title,
				"description":   tool.Description,
				"input_schema":  tool.InputSchema,
				"output_schema": tool.OutputSchema,
				"annotations":   tool.Annotations,
			})
			serverToolCount++
		}
		tc.mu.Unlock()

		serversSummary[entry.Key] = map[string]interface{}{
			"tool_count": serverToolCount,
			"status":     "connected",
		}

		tc.logger.LogEvent("tool_discovery_complete", correlationID, map[string]interface{}{
			"server":      entry.Key,
			"tool_count":  serverToolCount,
			"session_id":  session.ID(),
		})
	}

	tc.mu.RLock()
	totalTools := len(tc.tools)
	tc.mu.RUnlock()

	// Apply any stored description overrides on top of freshly discovered tools.
	tc.applyOverrides()

	// Refresh catalog entries to reflect applied overrides and mark overridden tools.
	tc.mu.RLock()
	for i, entry := range catalog {
		name, _ := entry["name"].(string)
		if tool, ok := tc.tools[name]; ok {
			catalog[i]["description"] = tool.Description // may be overridden
		}
		if _, hasOverride := tc.overrides[name]; hasOverride {
			catalog[i]["has_override"] = true
		}
	}
	tc.mu.RUnlock()

	// Emit unified catalog event.
	tc.logger.LogEvent("tool_catalog", correlationID, map[string]interface{}{
		"tools":   catalog,
		"total":   totalTools,
		"servers": serversSummary,
	})

	tc.logger.LogEvent("tool_discovery_complete", correlationID, map[string]interface{}{
		"total_tools":  totalTools,
		"multi_server": multiServer,
	})

	return nil
}

// GetTools returns all cached tools (thread-safe).
func (tc *ToolCache) GetTools() []*mcp.Tool {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	tools := make([]*mcp.Tool, 0, len(tc.tools))
	for _, tool := range tc.tools {
		tools = append(tools, tool)
	}

	return tools
}

// GetTool retrieves a specific tool by (exposed/prefixed) name (thread-safe).
func (tc *ToolCache) GetTool(name string) (*mcp.Tool, bool) {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	tool, ok := tc.tools[name]
	return tool, ok
}

// getArgumentKeys returns a list of argument keys from a map.
func getArgumentKeys(args map[string]interface{}) []string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	return keys
}

// CreateProxyHandler creates a handler that proxies tool calls to the correct
// upstream server.  When routes are populated (multi-server path) the handler
// looks up the ToolRoute by exposed name to find the session and original name.
// The fallback session parameter is used for single-server backward compat.
func (tc *ToolCache) CreateProxyHandler(toolName string, fallbackSession *mcp.ClientSession, clientInfo *ClientInfo) mcp.ToolHandler {
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

		// Resolve the session and upstream (unprefixed) tool name via routes.
		tc.mu.RLock()
		route, hasRoute := tc.routes[toolName]
		tc.mu.RUnlock()

		var session *mcp.ClientSession
		upstreamToolName := toolName // default: use exposed name as-is (single-server)
		if hasRoute {
			session = route.Session
			upstreamToolName = route.OriginalName
		} else {
			session = fallbackSession
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

		// Determine server key for top-level logging (empty string for single-server path)
		serverKey := ""
		if hasRoute {
			serverKey = route.ServerKey
		}

		// Log inbound request with full arguments, client info, and transport metadata
		tc.logger.LogInbound("tool_call_request", correlationID, map[string]interface{}{
			"tool":              req.Params.Name,
			"upstream_tool":     upstreamToolName,
			"server_key":        serverKey,
			"arguments":         arguments,
			"argument_count":    len(arguments),
			"session_id":        session.ID(),
			"client_info":       clientInfo,
			"mcp_client":        mcpClientMeta,
			"transport_headers": transportHeaders,
		})

		// Create params for upstream call — use the ORIGINAL (unprefixed) name.
		params := &mcp.CallToolParams{
			Name:      upstreamToolName,
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
					"server_key":  serverKey,
					"duration_ns": duration.Nanoseconds(),
					"client_info": clientInfo,
				})
			} else if errors.Is(err, context.DeadlineExceeded) {
				tc.logger.LogErrorOutbound("tool_call_timeout", correlationID, map[string]interface{}{
					"tool":        req.Params.Name,
					"server_key":  serverKey,
					"duration_ns": duration.Nanoseconds(),
					"client_info": clientInfo,
				})
			} else {
				tc.logger.LogErrorOutbound("tool_call_error", correlationID, map[string]interface{}{
					"tool":        req.Params.Name,
					"server_key":  serverKey,
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
			"upstream_tool": upstreamToolName,
			"server_key":    serverKey,
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
