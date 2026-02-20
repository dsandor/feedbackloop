# Phase 2 Execution Plan: Tool Discovery & Re-exposure

**Phase:** 2 - Tool Discovery & Re-exposure
**Milestone:** Milestone 1: Transparent MCP Proxy v1
**Status:** Ready for execution
**Created:** 2026-02-20
**Estimated Complexity:** Medium (6-8 hours)

---

## Objective

Extend the MCP proxy to:
1. Discover tools from the upstream server on startup via `tools/list`
2. Cache discovered tools in memory
3. Re-expose all discovered tools to clients via proxy's `tools/list` response
4. Ensure schema fidelity - tools appear identical to clients
5. Add correlation IDs to all requests/responses for tracing

**Success means:** A client calling `tools/list` on the proxy sees exactly the same tools as if they connected directly to chrome-devtools-mcp, and all requests/responses are correlated in logs.

---

## Execution Context

**SDK Documentation:**
- Official Go SDK: https://github.com/modelcontextprotocol/go-sdk
- API Docs: https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp

**Key SDK Components:**
- `ClientSession.ListTools()` - get tools from upstream
- `ClientSession.Tools()` - iterator version for pagination
- `Server.AddTool()` - register tool with proxy server
- `Tool` struct - tool definition with name, description, inputSchema
- `ToolHandler` - function signature for tool call handling (Phase 3)

**MCP Protocol Methods:**
- `tools/list` - list available tools (request/response)
- JSON-RPC format with `id`, `method`, `params`, `result`

**Current State:**
- Phase 1 complete: basic proxy with stdio transport
- Upstream connection manager exists (`UpstreamManager`)
- Client-facing server exists (`ProxyServer`)
- JSON logging to stderr is working
- No tool discovery or proxying yet

---

## Context

**From PROJECT.md:**
- Must be transparent - zero modification of messages
- Structured JSON logging with correlation IDs
- Test against chrome-devtools-mcp

**From ROADMAP.md Phase 2 Exit Criteria:**
- Proxy calls `tools/list` on upstream at startup
- Client calling `tools/list` sees all upstream tools
- Tool schemas match exactly (name, inputSchema, description)
- Request/response logs include correlation IDs

**From Phase 1 SUMMARY.md:**
- `UpstreamManager.Session()` provides `*mcp.ClientSession`
- Logging uses correlation IDs (UUID format)
- Logger supports `LogInbound()` and `LogOutbound()` methods
- Server initialization happens in `ProxyServer.Run()`

**Architectural Decisions:**
- Tool discovery happens during startup (before accepting client connections)
- Tools are cached in memory (no persistence needed yet)
- For Phase 2, tool handlers will be stubs (Phase 3 implements actual proxying)
- Correlation IDs are UUIDs generated per request/response pair

---

## Tasks

### Task 1: Add UUID Correlation ID Generation
**Objective:** Create utility to generate UUIDs for correlation IDs

**Implementation:** `internal/logger/correlation.go`

**Requirements:**
- Use `github.com/google/uuid` package (standard UUID library)
- Function: `GenerateCorrelationID() string` returns UUID v4
- Used for all request/response pairs

**Example:**
```go
import "github.com/google/uuid"

func GenerateCorrelationID() string {
    return uuid.New().String()
}
```

**Verification:**
- Function returns valid UUID v4 format: `xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx`
- Each call returns unique ID
- No panics or errors

**Files Modified:**
- NEW: `internal/logger/correlation.go`

**Dependencies:**
```bash
go get github.com/google/uuid
```

---

### Task 2: Implement Tool Discovery from Upstream
**Objective:** Query upstream server for available tools and cache them

**Implementation:** `internal/proxy/tools.go`

**Requirements:**
- Create `ToolCache` struct to hold discovered tools
- Method: `DiscoverTools(ctx context.Context, session *mcp.ClientSession) error`
  - Call `session.ListTools(ctx, &mcp.ListToolsParams{})`
  - Parse `ListToolsResult` and extract tools
  - Store tools in cache (map[string]*mcp.Tool)
  - Log discovery with correlation ID
  - Handle pagination if needed (chrome-devtools has ~30 tools)
- Method: `GetTools() []*mcp.Tool` returns cached tools
- Thread-safe access (use `sync.RWMutex` if needed)

**API Signature:**
```go
type ToolCache struct {
    tools map[string]*mcp.Tool
    mu    sync.RWMutex
    logger *logger.Logger
}

func NewToolCache(logger *logger.Logger) *ToolCache
func (tc *ToolCache) DiscoverTools(ctx context.Context, session *mcp.ClientSession) error
func (tc *ToolCache) GetTools() []*mcp.Tool
func (tc *ToolCache) GetTool(name string) (*mcp.Tool, bool)
```

**Verification:**
- Can call `session.ListTools()` successfully
- All tools from chrome-devtools-mcp are cached (verify count ~30)
- Tool schemas are preserved exactly (name, description, inputSchema)
- Discovery logged with correlation ID
- No data races

**Files Modified:**
- NEW: `internal/proxy/tools.go`

---

### Task 3: Create Stub Tool Handlers
**Objective:** Create placeholder handlers for discovered tools (Phase 3 will implement actual proxying)

**Implementation:** Update `internal/proxy/tools.go`

**Requirements:**
- Create stub handler that returns error "tool call proxying not implemented yet"
- Signature matches `mcp.ToolHandler` type
- Log tool call attempts with correlation ID
- Used for all discovered tools in Phase 2

**API Signature:**
```go
func (tc *ToolCache) CreateStubHandler(toolName string) mcp.ToolHandler {
    return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        correlationID := logger.GenerateCorrelationID()
        tc.logger.LogInbound("tool_call_request", correlationID, map[string]interface{}{
            "tool": toolName,
            "params": req.Params,
        })
        return nil, fmt.Errorf("tool call proxying not implemented yet (Phase 3)")
    }
}
```

**Verification:**
- Handler logs call attempt
- Handler returns error with clear message
- Correlation ID is generated and logged
- No panics

**Files Modified:**
- UPDATE: `internal/proxy/tools.go` (add stub handler creation)

---

### Task 4: Integrate Tool Discovery into Proxy Server
**Objective:** Wire tool discovery into server startup before accepting clients

**Implementation:** Update `internal/proxy/server.go`

**Requirements:**
- Add `ToolCache` field to `ProxyServer` struct
- In `NewProxyServer()`:
  - Create `ToolCache` instance
  - Call `ToolCache.DiscoverTools()` with upstream session
  - Register all discovered tools with server using `server.AddTool()`
  - Each tool uses stub handler from `ToolCache.CreateStubHandler()`
- Handle discovery errors (fail fast if upstream unavailable)
- Log tool registration for each discovered tool

**Modified API:**
```go
type ProxyServer struct {
    server    *mcp.Server
    upstream  *UpstreamManager
    logger    *logger.Logger
    toolCache *ToolCache  // NEW
}

func NewProxyServer(upstream *UpstreamManager, logger *logger.Logger) (*ProxyServer, error) {
    // ...existing server creation...

    toolCache := NewToolCache(logger)

    // Discover tools from upstream
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    err := toolCache.DiscoverTools(ctx, upstream.Session())
    if err != nil {
        return nil, fmt.Errorf("tool discovery failed: %w", err)
    }

    // Register all tools with proxy server
    for _, tool := range toolCache.GetTools() {
        server.AddTool(tool, toolCache.CreateStubHandler(tool.Name))
        logger.LogEvent("tool_registered", logger.GenerateCorrelationID(), map[string]interface{}{
            "tool": tool.Name,
        })
    }

    return &ProxyServer{
        server:    server,
        upstream:  upstream,
        logger:    logger,
        toolCache: toolCache,
    }, nil
}
```

**Verification:**
- `NewProxyServer()` now returns `(*ProxyServer, error)`
- Discovery happens during server creation
- All tools registered before server starts
- Discovery failure prevents server startup
- Each tool registration logged

**Files Modified:**
- UPDATE: `internal/proxy/server.go` (add tool discovery and registration)

---

### Task 5: Update Main Entry Point Error Handling
**Objective:** Handle new error return from `NewProxyServer()`

**Implementation:** Update `cmd/feedbackloop/main.go`

**Requirements:**
- Change `NewProxyServer()` call to handle error
- Log and exit if server creation fails
- Ensure graceful cleanup on errors

**Changes:**
```go
// Before:
proxyServer := proxy.NewProxyServer(upstream, logger)

// After:
proxyServer, err := proxy.NewProxyServer(upstream, logger)
if err != nil {
    logger.LogErrorEvent("server_creation_failed", logger.GenerateCorrelationID(), map[string]interface{}{
        "error": err.Error(),
    })
    os.Exit(1)
}
```

**Verification:**
- Compiles without errors
- Error path is tested (mock upstream failure)
- Logs show clear error messages
- Exit code is 1 on failure

**Files Modified:**
- UPDATE: `cmd/feedbackloop/main.go` (handle NewProxyServer error)

---

### Task 6: Build and Integration Test
**Objective:** Verify tool discovery works end-to-end with chrome-devtools-mcp

**Test Procedure:**
1. Build binary: `go build -o feedbackloop ./cmd/feedbackloop`
2. Run proxy: `./feedbackloop 2>&1 | jq`
3. Observe startup logs:
   - `upstream_connected` event
   - `tool_discovery_started` event
   - Multiple `tool_registered` events (~30 for chrome-devtools)
   - `server_starting` event
4. In separate terminal, test `tools/list` with MCP client or write test client
5. Verify response contains all chrome-devtools tools
6. Compare tool schemas to direct chrome-devtools-mcp connection (should be identical)

**Test Client Example:**
```go
// test-client/main.go
package main

import (
    "context"
    "fmt"
    "os"
    "os/exec"

    "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
    impl := &mcp.Implementation{Name: "test-client", Version: "0.1.0"}
    client := mcp.NewClient(impl, nil)

    cmd := exec.Command("./feedbackloop")
    transport := &mcp.CommandTransport{Command: cmd}

    session, err := client.Connect(context.Background(), transport, nil)
    if err != nil {
        panic(err)
    }
    defer session.Close()

    result, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
    if err != nil {
        panic(err)
    }

    fmt.Printf("Discovered %d tools:\n", len(result.Tools))
    for _, tool := range result.Tools {
        fmt.Printf("  - %s: %s\n", tool.Name, tool.Description)
    }
}
```

**Expected Logs:**
```json
{"timestamp":"...","level":"info","event_type":"upstream_connected","correlation_id":"..."}
{"timestamp":"...","level":"info","event_type":"tool_discovery_started","correlation_id":"..."}
{"timestamp":"...","level":"info","event_type":"tool_discovered","correlation_id":"...","message":{"tool":"navigate_page","count":1}}
{"timestamp":"...","level":"info","event_type":"tool_discovered","correlation_id":"...","message":{"tool":"take_screenshot","count":2}}
...
{"timestamp":"...","level":"info","event_type":"tool_registered","correlation_id":"...","message":{"tool":"navigate_page"}}
...
{"timestamp":"...","level":"info","event_type":"server_starting","correlation_id":"..."}
```

**Success Criteria:**
- Binary builds successfully
- Proxy discovers ~30 tools from chrome-devtools-mcp
- Client calling `tools/list` receives all tools
- Tool schemas match exactly (verify at least 3 tools manually)
- All operations logged with correlation IDs
- No errors or panics

**Files Modified:**
- NEW: `test-client/main.go` (optional test client)

---

### Task 7: Add Schema Verification Tests
**Objective:** Ensure tool schema fidelity with automated verification

**Implementation:** Create `internal/proxy/tools_test.go`

**Requirements:**
- Test: `TestToolDiscovery` - verifies discovery works
- Test: `TestToolCacheThreadSafety` - concurrent access
- Test: `TestSchemaFidelity` - tool schemas are preserved exactly
- Use table-driven tests
- Mock upstream session if needed

**Example Test:**
```go
func TestToolDiscovery(t *testing.T) {
    // Create mock session with known tools
    mockSession := createMockSession(t, []mcp.Tool{
        {Name: "test-tool", Description: "A test tool", InputSchema: map[string]interface{}{"type": "object"}},
    })

    cache := NewToolCache(logger)
    err := cache.DiscoverTools(context.Background(), mockSession)
    if err != nil {
        t.Fatalf("discovery failed: %v", err)
    }

    tools := cache.GetTools()
    if len(tools) != 1 {
        t.Errorf("expected 1 tool, got %d", len(tools))
    }

    tool, ok := cache.GetTool("test-tool")
    if !ok {
        t.Error("test-tool not found")
    }
    if tool.Description != "A test tool" {
        t.Errorf("description mismatch: %s", tool.Description)
    }
}
```

**Verification:**
- All tests pass: `go test ./internal/proxy/...`
- Coverage > 70% for tools.go
- No race conditions: `go test -race ./internal/proxy/...`

**Files Modified:**
- NEW: `internal/proxy/tools_test.go`

---

## Verification

**Build Verification:**
```bash
go build -o feedbackloop ./cmd/feedbackloop
echo $?  # Should be 0
```

**Tool Discovery Verification:**
```bash
# Run proxy and check logs for tool discovery
./feedbackloop 2>&1 | jq -c 'select(.event_type | test("tool_"))'
```

**Schema Fidelity Verification:**
```bash
# Compare proxy tools/list to direct chrome-devtools-mcp
# (requires writing comparison script or manual verification)
```

**Test Verification:**
```bash
go test ./internal/proxy/...
go test -race ./internal/proxy/...
```

**Manual Checklist:**
- [ ] `go build` succeeds with no errors
- [ ] Binary discovers tools on startup
- [ ] Logs show ~30 tool registrations for chrome-devtools-mcp
- [ ] Client `tools/list` returns all tools
- [ ] Tool schemas match upstream (verify 3+ tools manually)
- [ ] All requests/responses have correlation IDs
- [ ] Correlation IDs are valid UUIDs
- [ ] No panics or errors during normal operation
- [ ] Unit tests pass with coverage > 70%
- [ ] No race conditions detected

---

## Success Criteria

**Phase 2 is complete when:**

1. ✅ Proxy calls `tools/list` on upstream during startup
2. ✅ All upstream tools are discovered and cached (verify count for chrome-devtools-mcp)
3. ✅ Client calling `tools/list` sees all upstream tools
4. ✅ Tool schemas match exactly:
   - Name preserved
   - Description preserved
   - InputSchema preserved (no modifications)
   - OutputSchema preserved (if present)
5. ✅ All requests/responses include correlation IDs (UUID format)
6. ✅ Tool discovery logged with correlation IDs
7. ✅ Tool registration logged for each tool
8. ✅ Stub handlers in place (Phase 3 will implement actual proxying)
9. ✅ Unit tests pass with no race conditions
10. ✅ Binary builds and runs successfully

**Deliverables:**
- Updated binary: `feedbackloop`
- New source files:
  - `internal/logger/correlation.go`
  - `internal/proxy/tools.go`
  - `internal/proxy/tools_test.go`
- Updated files:
  - `internal/proxy/server.go` (tool discovery integration)
  - `cmd/feedbackloop/main.go` (error handling)
  - `go.mod` (uuid dependency)
  - `go.sum`
- Test client (optional): `test-client/main.go`

---

## Output

**What gets enhanced:**
- Proxy now discovers and re-exposes upstream tools
- Clients see all upstream tools as if connecting directly
- Foundation for Phase 3 tool call proxying
- Comprehensive correlation ID tracking for all operations

**Example startup logs:**
```bash
./feedbackloop 2>&1 | jq
```

Output:
```json
{"timestamp":"2026-02-20T14:30:00.123Z","level":"info","event_type":"upstream_connected","correlation_id":"abc-123"}
{"timestamp":"2026-02-20T14:30:00.234Z","level":"info","event_type":"tool_discovery_started","correlation_id":"def-456"}
{"timestamp":"2026-02-20T14:30:00.345Z","level":"info","event_type":"tool_discovered","correlation_id":"def-456","message":{"tool":"navigate_page"}}
{"timestamp":"2026-02-20T14:30:00.456Z","level":"info","event_type":"tool_registered","correlation_id":"ghi-789","message":{"tool":"navigate_page"}}
...
{"timestamp":"2026-02-20T14:30:01.567Z","level":"info","event_type":"server_starting","correlation_id":"jkl-012"}
```

**Client tools/list response:**
```json
{
  "tools": [
    {
      "name": "navigate_page",
      "description": "Navigates the currently selected page to a URL.",
      "inputSchema": {
        "type": "object",
        "properties": {...},
        "required": [...]
      }
    },
    ...
  ]
}
```

---

## Notes

**Out of Scope for Phase 2:**
- Tool call proxying (tools/call) → Phase 3
- Tool call result logging → Phase 3
- HTTP/SSE transport → Phase 4 & 5
- Configuration file parsing → Phase 6

**Dependencies:**
- `github.com/google/uuid` for correlation IDs
- chrome-devtools-mcp for testing (via npx)

**Risks:**
- Tool schema changes in chrome-devtools-mcp (mitigation: use pinned version)
- Large tool count causing memory issues (mitigation: pagination support)
- Schema incompatibility between SDK versions (mitigation: test early)

**Design Decisions:**
- Tool discovery at startup (not lazy): ensures fast client responses, fail-fast if upstream unavailable
- In-memory cache (no persistence): simplicity, tools can change on restart
- Stub handlers for Phase 2: allows testing tools/list without implementing full proxying
- UUID v4 correlation IDs: standard, widely supported, good uniqueness guarantees

**Next Phase:**
After Phase 2 completion, Phase 3 will implement actual tool call proxying - forwarding `tools/call` requests to upstream and returning responses transparently.

---

*Plan created: 2026-02-20*
*Ready for execution with `/gsd:execute-plan`*
