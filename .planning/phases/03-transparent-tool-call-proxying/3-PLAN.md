# Phase 3 Execution Plan: Transparent Tool Call Proxying

**Phase:** 3 - Transparent Tool Call Proxying
**Milestone:** Milestone 1: Transparent MCP Proxy v1
**Status:** Ready for execution
**Created:** 2026-02-20
**Estimated Complexity:** Medium (4-6 hours)

---

## Objective

Replace stub handlers with real tool call proxying to enable transparent forwarding of tool calls from clients to upstream servers. The proxy will:

1. Forward `tools/call` requests from client to upstream server
2. Return upstream responses to client unmodified
3. Log inbound requests with correlation ID, timestamp, tool name, and arguments
4. Log outbound responses with same correlation ID, timestamp, and result/error
5. Ensure zero latency overhead through async logging
6. Handle errors from upstream gracefully and transparently

**Success means:** A client can invoke any upstream tool through the proxy, receive identical results as if calling directly, and all operations are fully logged with correlation IDs for traceability.

---

## Execution Context

**SDK Documentation:**
- Official Go SDK: https://github.com/modelcontextprotocol/go-sdk
- API Docs: https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp

**Key SDK Components:**
- `ClientSession.CallTool()` - call tool on upstream server
- `CallToolRequest` - request structure with tool name and arguments
- `CallToolResult` - result structure with content or error
- `ToolHandler` - function signature: `func(ctx context.Context, req *CallToolRequest) (*CallToolResult, error)`

**MCP Protocol:**
- `tools/call` - invoke a tool (request/response)
- Request: `{"method": "tools/call", "params": {"name": "tool-name", "arguments": {...}}}`
- Response: `{"result": {"content": [...], "isError": false}}`

**Current State:**
- Phase 2 complete: tool discovery and re-exposure working
- ToolCache exists with discovered tools
- Stub handlers return "Phase 3" error message
- UpstreamManager.Session() provides active ClientSession
- Correlation ID infrastructure in place

---

## Context

**From PROJECT.md:**
- Proxy must be transparent - zero modification of messages
- Structured JSON logging with correlation IDs
- Test against chrome-devtools-mcp (26 tools)

**From ROADMAP.md Phase 3 Exit Criteria:**
- Client can invoke any upstream tool through proxy
- Tool results are identical to direct upstream calls
- Request and response logs are correlated (same ID)
- No message loss, delay, or modification
- Errors from upstream are passed through unchanged

**From Phase 2 SUMMARY.md:**
- ToolCache holds 26 tools from chrome-devtools-mcp
- CreateStubHandler() creates placeholder handlers
- All tools registered with server using server.AddTool()
- UUID v4 correlation IDs already in use

**Architectural Decisions:**
- Handler replacement: replace CreateStubHandler with CreateProxyHandler
- Error passthrough: upstream errors returned as-is to maintain transparency
- Async logging: all logging non-blocking to avoid latency overhead
- Same correlation ID: request and response share same UUID for tracing

---

## Tasks

### Task 1: Create Proxy Handler Factory
**Objective:** Replace stub handler with real proxying handler

**Implementation:** Update `internal/proxy/tools.go`

**Requirements:**
- Rename `CreateStubHandler` to `CreateProxyHandler`
- Handler receives `CallToolRequest` from client
- Handler calls `session.CallTool()` with upstream session
- Handler returns upstream result unmodified
- Log request before calling upstream (inbound direction)
- Log response after receiving from upstream (outbound direction)
- Use same correlation ID for request and response
- Handle upstream errors gracefully (pass through as-is)

**API Signature:**
```go
func (tc *ToolCache) CreateProxyHandler(toolName string, session *mcp.ClientSession) mcp.ToolHandler {
    return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        correlationID := logger.GenerateCorrelationID()

        // Log inbound request
        tc.logger.LogInbound("tool_call_request", correlationID, map[string]interface{}{
            "tool":      req.Params.Name,
            "arguments": req.Params.Arguments,
        })

        // Call upstream tool
        result, err := session.CallTool(ctx, req.Params)

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
```

**Verification:**
- Handler forwards requests to upstream
- Handler returns upstream results unmodified
- Request and response share same correlation ID
- Errors passed through unchanged
- No panics or data races

**Files Modified:**
- UPDATE: `internal/proxy/tools.go` (replace CreateStubHandler with CreateProxyHandler)

---

### Task 2: Update Server Tool Registration
**Objective:** Use proxy handlers instead of stub handlers

**Implementation:** Update `internal/proxy/server.go`

**Requirements:**
- Change `CreateStubHandler()` call to `CreateProxyHandler()`
- Pass upstream session to handler factory
- Keep all other registration logic unchanged
- Tool registration logging stays the same

**Changes:**
```go
// Before:
server.AddTool(tool, toolCache.CreateStubHandler(tool.Name))

// After:
server.AddTool(tool, toolCache.CreateProxyHandler(tool.Name, upstream.Session()))
```

**Verification:**
- All tools registered with proxy handlers
- No stub handlers remain
- Registration logs unchanged
- No compilation errors

**Files Modified:**
- UPDATE: `internal/proxy/server.go` (use CreateProxyHandler)

---

### Task 3: Add Request/Response Logging Details
**Objective:** Enhance logging to capture full request/response details for debugging

**Implementation:** Update `internal/proxy/tools.go`

**Requirements:**
- Log argument count and keys (not full values for privacy)
- Log result content count
- Log execution duration
- Preserve correlation ID consistency
- Keep logs structured and parseable

**Enhanced Logging:**
```go
// Request logging
tc.logger.LogInbound("tool_call_request", correlationID, map[string]interface{}{
    "tool":           req.Params.Name,
    "argument_keys":  getArgumentKeys(req.Params.Arguments),
    "argument_count": len(req.Params.Arguments),
    "timestamp":      time.Now().UnixNano(),
})

// Response logging
tc.logger.LogOutbound("tool_call_response", correlationID, map[string]interface{}{
    "tool":          req.Params.Name,
    "isError":       result.IsError,
    "content_count": len(result.Content),
    "duration_ns":   time.Since(start).Nanoseconds(),
})
```

**Helper Function:**
```go
func getArgumentKeys(args map[string]interface{}) []string {
    keys := make([]string, 0, len(args))
    for k := range args {
        keys = append(keys, k)
    }
    return keys
}
```

**Verification:**
- Logs include timing information
- Argument keys logged (not sensitive values)
- Content count logged
- Correlation IDs consistent
- No performance impact

**Files Modified:**
- UPDATE: `internal/proxy/tools.go` (enhance logging)

---

### Task 4: Build and Integration Test
**Objective:** Verify end-to-end tool call proxying with chrome-devtools-mcp

**Test Procedure:**
1. Build binary: `go build -o feedbackloop ./cmd/feedbackloop`
2. Run proxy: `./feedbackloop 2>&1 | jq`
3. Create or update test client to call tools
4. Test at least 3 different tools from chrome-devtools-mcp:
   - Simple tool (e.g., `list_pages`)
   - Tool with arguments (e.g., `navigate_page` with URL)
   - Tool with complex response (e.g., `take_screenshot`)
5. Verify responses match direct upstream calls
6. Verify correlation IDs in logs
7. Test error scenarios:
   - Invalid tool name
   - Missing required arguments
   - Upstream tool returns error

**Test Client Updates:**
```go
// test-client/main.go - add tool call testing
func testToolCalls(session *mcp.ClientSession) error {
    ctx := context.Background()

    // Test 1: list_pages (simple, no args)
    fmt.Println("\nTest 1: Calling list_pages...")
    result1, err := session.CallTool(ctx, &mcp.CallToolParams{
        Name:      "list_pages",
        Arguments: map[string]interface{}{},
    })
    if err != nil {
        return fmt.Errorf("list_pages failed: %w", err)
    }
    fmt.Printf("  Result: %d content items, isError=%v\n", len(result1.Content), result1.IsError)

    // Test 2: navigate_page (with arguments)
    fmt.Println("\nTest 2: Calling navigate_page...")
    result2, err := session.CallTool(ctx, &mcp.CallToolParams{
        Name: "navigate_page",
        Arguments: map[string]interface{}{
            "url": "https://example.com",
        },
    })
    if err != nil {
        return fmt.Errorf("navigate_page failed: %w", err)
    }
    fmt.Printf("  Result: %d content items, isError=%v\n", len(result2.Content), result2.IsError)

    // Test 3: Invalid tool (error case)
    fmt.Println("\nTest 3: Calling invalid_tool (expect error)...")
    _, err = session.CallTool(ctx, &mcp.CallToolParams{
        Name:      "invalid_tool_name",
        Arguments: map[string]interface{}{},
    })
    if err != nil {
        fmt.Printf("  Expected error received: %v\n", err)
    } else {
        fmt.Printf("  WARNING: Expected error but got success!\n")
    }

    return nil
}
```

**Expected Logs:**
```json
{"timestamp":"...","level":"info","event_type":"tool_call_request","correlation_id":"abc-123","direction":"inbound","message":{"tool":"list_pages","argument_keys":[],"argument_count":0}}
{"timestamp":"...","level":"info","event_type":"tool_call_response","correlation_id":"abc-123","direction":"outbound","message":{"tool":"list_pages","isError":false,"content_count":1,"duration_ns":123456}}

{"timestamp":"...","level":"info","event_type":"tool_call_request","correlation_id":"def-456","direction":"inbound","message":{"tool":"navigate_page","argument_keys":["url"],"argument_count":1}}
{"timestamp":"...","level":"info","event_type":"tool_call_response","correlation_id":"def-456","direction":"outbound","message":{"tool":"navigate_page","isError":false,"content_count":1,"duration_ns":234567}}

{"timestamp":"...","level":"info","event_type":"tool_call_request","correlation_id":"ghi-789","direction":"inbound","message":{"tool":"invalid_tool_name","argument_keys":[],"argument_count":0}}
{"timestamp":"...","level":"error","event_type":"tool_call_error","correlation_id":"ghi-789","direction":"outbound","message":{"tool":"invalid_tool_name","error":"tool not found"}}
```

**Success Criteria:**
- Binary builds successfully
- Client can call upstream tools through proxy
- Tool results match direct upstream calls (verify at least 3 tools)
- Correlation IDs match between request and response logs
- Errors from upstream passed through unchanged
- No panics or crashes
- No performance degradation (timing logged, should be < 50ms overhead)

**Files Modified:**
- UPDATE: `test_client.go` (add tool call testing)

---

### Task 5: Add Unit Tests for Proxy Handler
**Objective:** Test handler logic in isolation

**Implementation:** Update `internal/proxy/tools_test.go`

**Requirements:**
- Test successful tool call proxying
- Test error handling (upstream returns error)
- Test correlation ID generation and usage
- Test logging calls (verify logs are generated)
- Mock upstream session for testing

**Test Cases:**
```go
func TestProxyHandler(t *testing.T) {
    // Test successful tool call
    // Mock session that returns success
    // Verify result passed through
    // Verify request/response logged with same correlation ID
}

func TestProxyHandlerError(t *testing.T) {
    // Test error handling
    // Mock session that returns error
    // Verify error passed through unchanged
    // Verify error logged with correlation ID
}

func TestProxyHandlerLogging(t *testing.T) {
    // Verify logging calls
    // Check correlation ID consistency
    // Check timing information logged
}
```

**Note:** Due to MCP SDK's unexported fields, may need to use integration tests for full validation. Unit tests should focus on mockable parts.

**Verification:**
- All new tests pass
- No race conditions: `go test -race ./internal/proxy/...`
- Existing tests still pass
- Coverage maintained or improved

**Files Modified:**
- UPDATE: `internal/proxy/tools_test.go` (add proxy handler tests)

---

### Task 6: Add Error Handling Edge Cases
**Objective:** Handle failure scenarios gracefully

**Scenarios to Handle:**
1. Context cancellation during tool call
2. Upstream timeout
3. Invalid arguments (schema validation errors)
4. Upstream server crash mid-call
5. Large response handling

**Implementation:** Update `internal/proxy/tools.go`

**Enhancements:**
```go
func (tc *ToolCache) CreateProxyHandler(toolName string, session *mcp.ClientSession) mcp.ToolHandler {
    return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        correlationID := logger.GenerateCorrelationID()
        start := time.Now()

        // Log request
        tc.logger.LogInbound("tool_call_request", correlationID, map[string]interface{}{
            "tool":           req.Params.Name,
            "argument_keys":  getArgumentKeys(req.Params.Arguments),
            "argument_count": len(req.Params.Arguments),
        })

        // Call upstream with context
        result, err := session.CallTool(ctx, req.Params)
        duration := time.Since(start)

        if err != nil {
            // Detect context cancellation
            if errors.Is(err, context.Canceled) {
                tc.logger.LogErrorOutbound("tool_call_cancelled", correlationID, map[string]interface{}{
                    "tool":        req.Params.Name,
                    "duration_ns": duration.Nanoseconds(),
                })
            } else if errors.Is(err, context.DeadlineExceeded) {
                tc.logger.LogErrorOutbound("tool_call_timeout", correlationID, map[string]interface{}{
                    "tool":        req.Params.Name,
                    "duration_ns": duration.Nanoseconds(),
                })
            } else {
                tc.logger.LogErrorOutbound("tool_call_error", correlationID, map[string]interface{}{
                    "tool":        req.Params.Name,
                    "error":       err.Error(),
                    "duration_ns": duration.Nanoseconds(),
                })
            }
            return nil, err
        }

        // Log successful response
        tc.logger.LogOutbound("tool_call_response", correlationID, map[string]interface{}{
            "tool":          req.Params.Name,
            "isError":       result.IsError,
            "content_count": len(result.Content),
            "duration_ns":   duration.Nanoseconds(),
        })

        return result, nil
    }
}
```

**Verification:**
- Context cancellation handled
- Timeouts detected and logged
- Errors distinguished by type
- All paths return appropriate errors
- No resource leaks

**Files Modified:**
- UPDATE: `internal/proxy/tools.go` (add error handling)

---

### Task 7: Performance and Load Testing
**Objective:** Verify zero latency overhead and handle concurrent calls

**Test Scenarios:**
1. Single tool call latency measurement
2. Concurrent tool calls (10+ simultaneous)
3. Large argument handling (complex JSON)
4. Large response handling (screenshots, etc.)
5. Sustained load (100+ sequential calls)

**Test Implementation:**
Create `internal/proxy/performance_test.go` for benchmarks:

```go
func BenchmarkToolCallProxy(b *testing.B) {
    // Measure proxy overhead
    // Compare to direct upstream call
    // Should be < 50ms difference
}

func TestConcurrentToolCalls(t *testing.T) {
    // Launch 20 goroutines calling tools
    // Verify all succeed
    // Verify correlation IDs unique
    // Check for race conditions
}
```

**Success Criteria:**
- Proxy overhead < 50ms per call
- Concurrent calls work without blocking
- No race conditions under load
- Memory usage stable
- No goroutine leaks

**Files Modified:**
- NEW: `internal/proxy/performance_test.go` (benchmarks and load tests)

---

## Verification

**Build Verification:**
```bash
go build -o feedbackloop ./cmd/feedbackloop
echo $?  # Should be 0
```

**Tool Call Verification:**
```bash
# Run proxy
./feedbackloop 2>&1 | jq

# In another terminal, run test client
go run test_client.go
```

**Log Verification:**
```bash
# Check correlation IDs match for request/response pairs
./feedbackloop 2>&1 | jq -c 'select(.event_type | test("tool_call"))' | jq -s 'group_by(.correlation_id) | map({correlation_id: .[0].correlation_id, events: map(.event_type)})'
```

**Performance Verification:**
```bash
go test -bench=. ./internal/proxy/...
go test -race ./internal/proxy/...
```

**Manual Checklist:**
- [ ] `go build` succeeds with no errors
- [ ] Binary runs without crashes
- [ ] Client can call upstream tools through proxy
- [ ] Tool results match direct upstream calls (verified 3+ tools)
- [ ] Request/response logs have matching correlation IDs
- [ ] Errors from upstream passed through unchanged
- [ ] Correlation IDs are valid UUIDs
- [ ] Logs include timing information
- [ ] Context cancellation handled gracefully
- [ ] Concurrent tool calls work correctly
- [ ] No race conditions detected
- [ ] Unit tests pass
- [ ] Performance overhead < 50ms per call

---

## Success Criteria

**Phase 3 is complete when:**

1. ✅ Client can invoke any upstream tool through proxy (tested with 3+ tools)
2. ✅ Tool results are identical to direct upstream calls (verified via comparison)
3. ✅ Request and response logs are correlated with same UUID
4. ✅ No message loss, delay, or modification (transparency verified)
5. ✅ Errors from upstream passed through unchanged (tested error cases)
6. ✅ Correlation IDs link request and response in logs
7. ✅ Timing information logged for all tool calls
8. ✅ Context cancellation and timeouts handled gracefully
9. ✅ Concurrent tool calls work without blocking or race conditions
10. ✅ Proxy overhead < 50ms per call (measured via benchmarks)
11. ✅ All unit tests pass with no race conditions
12. ✅ Integration tests validate end-to-end functionality

**Deliverables:**
- Updated binary: `feedbackloop`
- Updated files:
  - `internal/proxy/tools.go` (proxy handler implementation)
  - `internal/proxy/server.go` (use proxy handlers)
  - `internal/proxy/tools_test.go` (proxy handler tests)
- New files:
  - `internal/proxy/performance_test.go` (benchmarks)
- Updated test client: `test_client.go` (tool call testing)

---

## Output

**What gets enhanced:**
- Full transparent tool call proxying
- Zero-modification forwarding of requests/responses
- Complete correlation ID tracing
- Performance-optimized async logging
- Robust error handling

**Example tool call flow:**
```
Client → Proxy: tools/call {name: "list_pages", arguments: {}}
  └─ Log: [inbound] tool_call_request {correlation_id: "abc-123", tool: "list_pages"}

Proxy → Upstream: tools/call {name: "list_pages", arguments: {}}
Upstream → Proxy: {result: {...}, isError: false}
  └─ Log: [outbound] tool_call_response {correlation_id: "abc-123", duration_ns: 123456}

Proxy → Client: {result: {...}, isError: false}
```

**Log Traceability:**
```bash
# Find all events for a specific tool call
./feedbackloop 2>&1 | jq 'select(.correlation_id == "abc-123")'

# Output:
# {"correlation_id":"abc-123","event_type":"tool_call_request",...}
# {"correlation_id":"abc-123","event_type":"tool_call_response",...}
```

---

## Notes

**Out of Scope for Phase 3:**
- HTTP/SSE transport → Phase 4 & 5
- Configuration file parsing → Phase 6
- Request/response modification → Post-Milestone 1 (if ever needed)
- Multiple upstream servers → Post-Milestone 1
- Resource proxying → Future enhancement
- Prompt proxying → Future enhancement

**Dependencies:**
- Phase 2 complete ✅ (tool discovery working)
- chrome-devtools-mcp for testing (via npx)
- `github.com/google/uuid` already added in Phase 2

**Risks:**
- Large responses (screenshots) may have memory impact (mitigation: streaming if needed)
- Upstream latency directly affects proxy latency (expected, transparent)
- Tool schema changes in chrome-devtools-mcp (mitigation: pinned version)

**Design Decisions:**
- **Transparent passthrough:** No modification of requests/responses preserves compatibility
- **Same correlation ID:** Request and response share ID for easy tracing
- **Async logging:** Non-blocking to avoid adding latency to tool calls
- **Error passthrough:** Upstream errors returned as-is maintains transparency
- **Context propagation:** Allows client to cancel long-running tool calls

**Next Phase:**
After Phase 3 completion, Phase 4 will add HTTP/SSE transport for client-facing connections, allowing browsers and web clients to connect to the proxy.

---

*Plan created: 2026-02-20*
*Ready for execution with `/gsd:execute-plan`*
