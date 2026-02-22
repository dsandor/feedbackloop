# Phase 1 Execution Plan: Foundation & SDK Integration

**Phase:** 1 - Foundation & SDK Integration
**Milestone:** Milestone 1: Transparent MCP Proxy v1
**Status:** Ready for execution
**Created:** 2026-02-20
**Estimated Complexity:** Medium (8-12 hours)

---

## Objective

Establish a working Go-based MCP proxy that can:
1. Launch and connect to an upstream MCP server (chrome-devtools-mcp) via stdio
2. Accept client connections via stdio
3. Establish bidirectional message flow between client and upstream
4. Log all protocol messages as structured JSON to stdout
5. Support basic initialization and ping operations (no tool proxying yet)

**Success means:** A client can connect to the proxy via stdio, the proxy connects to chrome-devtools-mcp via stdio, and all initialization messages flow through and are logged.

---

## Execution Context

**SDK Documentation:**
- Official Go SDK: https://github.com/modelcontextprotocol/go-sdk
- API Docs: https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp

**Key SDK Components to Use:**
- `mcp.Server` - for client-facing server
- `mcp.Client` - for upstream server connection
- `mcp.StdioTransport` - for stdio communication (both sides)
- `mcp.CommandTransport` - for launching upstream server process

**Reference Upstream Server:**
```json
{
  "command": "npx",
  "args": ["-y", "chrome-devtools-mcp@latest"]
}
```

**Project Structure:**
```
feedbackloop/
├── cmd/
│   └── feedbackloop/
│       └── main.go          # Entry point
├── internal/
│   ├── proxy/
│   │   └── proxy.go         # Core proxy logic
│   └── logger/
│       └── logger.go        # JSON logging
├── go.mod
├── go.sum
└── CLAUDE.md
```

---

## Context

**From PROJECT.md:**
- Build in Go using official MCP SDK (`github.com/modelcontextprotocol/go-sdk`)
- Proxy must be transparent - zero modification of messages
- Structured JSON logging to stdout with timestamps and correlation IDs
- Test against chrome-devtools-mcp as upstream

**From ROADMAP.md Phase 1 Exit Criteria:**
- Go project builds successfully
- Can launch upstream MCP server via stdio (chrome-devtools-mcp)
- Client can connect via stdio to proxy
- All protocol messages logged as JSON to stdout

**From CLAUDE.md:**
- Must use BuildMethod: go1.x for any future Lambda functions (not applicable yet)
- No git commits - developer will handle version control

**Current State:**
- Empty project (no Go code exists yet)
- `.planning/` directory structure exists
- No `go.mod` file yet

---

## Tasks

### Task 1: Initialize Go Module and Dependencies
**Objective:** Set up Go project structure with MCP SDK dependency

**Steps:**
1. Initialize Go module: `go mod init github.com/dsandor/feedbackloop`
2. Add MCP SDK dependency: `go get github.com/modelcontextprotocol/go-sdk@latest`
3. Create directory structure:
   ```bash
   mkdir -p cmd/feedbackloop
   mkdir -p internal/proxy
   mkdir -p internal/logger
   ```
4. Verify dependencies resolve: `go mod tidy`

**Verification:**
- `go.mod` exists with module path `github.com/dsandor/feedbackloop`
- `go.mod` contains dependency on `github.com/modelcontextprotocol/go-sdk`
- `go.sum` exists
- Directory structure created

**Files Modified:**
- NEW: `go.mod`
- NEW: `go.sum`

---

### Task 2: Implement JSON Logger
**Objective:** Create structured logger that outputs JSON lines to stdout

**Implementation:** `internal/logger/logger.go`

**Requirements:**
- Log format: one JSON object per line
- Fields: `timestamp` (ISO8601), `level` (info/error), `event_type`, `correlation_id`, `direction` (inbound/outbound), `message` (structured)
- Use `encoding/json` and `log` packages
- Async logging to avoid blocking proxy operations

**Example Log Entry:**
```json
{
  "timestamp": "2026-02-20T10:30:45.123Z",
  "level": "info",
  "event_type": "initialize_request",
  "correlation_id": "550e8400-e29b-41d4-a716-446655440000",
  "direction": "inbound",
  "message": {
    "method": "initialize",
    "params": {...}
  }
}
```

**Verification:**
- Logger can be instantiated
- Logs output valid JSON (parseable by `jq`)
- Includes all required fields
- No panics or data races

**Files Modified:**
- NEW: `internal/logger/logger.go`

---

### Task 3: Implement Upstream Connection Manager
**Objective:** Launch and connect to upstream MCP server via stdio

**Implementation:** `internal/proxy/upstream.go`

**Requirements:**
- Create `mcp.Client` instance
- Use `mcp.CommandTransport` to launch upstream server process
  - Command: `npx`
  - Args: `["-y", "chrome-devtools-mcp@latest"]`
- Call `client.Connect()` with context and transport
- Handle connection lifecycle (connect, wait, close)
- Log all outbound requests and inbound responses with correlation IDs
- Return `*mcp.ClientSession` for use by proxy

**API Signature:**
```go
type UpstreamManager struct {
    client  *mcp.Client
    session *mcp.ClientSession
    logger  *logger.Logger
}

func NewUpstreamManager(logger *logger.Logger) *UpstreamManager
func (um *UpstreamManager) Connect(ctx context.Context) error
func (um *UpstreamManager) Session() *mcp.ClientSession
func (um *UpstreamManager) Close() error
```

**Verification:**
- Can launch `npx -y chrome-devtools-mcp@latest` as subprocess
- `ClientSession` is established
- Upstream server responds to ping
- Connection errors are propagated
- Logs show successful connection

**Files Modified:**
- NEW: `internal/proxy/upstream.go`

---

### Task 4: Implement Client-Facing MCP Server
**Objective:** Accept client connections via stdio and handle initialization

**Implementation:** `internal/proxy/server.go`

**Requirements:**
- Create `mcp.Server` instance with implementation metadata:
  - Name: `feedbackloop`
  - Version: `0.1.0`
- Use `mcp.StdioTransport` for client-facing transport
- Handle initialization flow (client → proxy)
- Log all inbound requests and outbound responses
- For now, just handle `initialize` and `ping` - no tools yet
- Call `server.Run()` with context and transport (blocking)

**API Signature:**
```go
type ProxyServer struct {
    server   *mcp.Server
    upstream *UpstreamManager
    logger   *logger.Logger
}

func NewProxyServer(upstream *UpstreamManager, logger *logger.Logger) *ProxyServer
func (ps *ProxyServer) Run(ctx context.Context) error
```

**Verification:**
- Server starts and listens on stdio
- Client can send `initialize` request
- Server responds with valid `initialize` response
- Ping works (client → proxy → client)
- All messages logged with correlation IDs

**Files Modified:**
- NEW: `internal/proxy/server.go`

---

### Task 5: Implement Main Entry Point
**Objective:** Wire up components and provide executable entry point

**Implementation:** `cmd/feedbackloop/main.go`

**Requirements:**
- Create logger instance
- Create upstream manager and connect to chrome-devtools-mcp
- Create proxy server with upstream manager
- Run proxy server (blocks until client disconnects or context cancelled)
- Handle graceful shutdown (SIGINT/SIGTERM)
- Exit with appropriate codes (0 = success, 1 = error)

**Flow:**
```
1. Initialize logger
2. Create upstream manager
3. Connect to upstream (blocks until connected)
4. Create proxy server
5. Run proxy server on stdio (blocks until client disconnects)
6. Cleanup and exit
```

**Verification:**
- Binary compiles: `go build -o feedbackloop ./cmd/feedbackloop`
- Can run: `./feedbackloop` (blocks waiting for client)
- Ctrl+C gracefully shuts down
- Logs appear on stdout as JSON
- No panics or goroutine leaks

**Files Modified:**
- NEW: `cmd/feedbackloop/main.go`

---

### Task 6: Build and Smoke Test
**Objective:** Verify end-to-end functionality with manual testing

**Test Procedure:**
1. Build binary: `go build -o feedbackloop ./cmd/feedbackloop`
2. Run proxy in one terminal: `./feedbackloop 2>&1 | jq`
3. In another terminal, test with MCP client (use `mcp` CLI if available, or write minimal test client)
4. Send `initialize` message from client
5. Verify response received
6. Send `ping` message
7. Verify pong received
8. Verify all messages appear in logs as valid JSON

**Expected Logs:**
```json
{"timestamp":"...","level":"info","event_type":"upstream_connect","correlation_id":"...","direction":"outbound","message":{...}}
{"timestamp":"...","level":"info","event_type":"initialize_request","correlation_id":"...","direction":"inbound","message":{...}}
{"timestamp":"...","level":"info","event_type":"initialize_response","correlation_id":"...","direction":"outbound","message":{...}}
{"timestamp":"...","level":"info","event_type":"ping_request","correlation_id":"...","direction":"inbound","message":{...}}
{"timestamp":"...","level":"info","event_type":"ping_response","correlation_id":"...","direction":"outbound","message":{...}}
```

**Success Criteria:**
- Binary runs without crashes
- Client can initialize session
- Ping/pong works
- All logs are valid JSON
- Correlation IDs present in all entries
- Upstream connection visible in logs

**Files Modified:**
- None (test only)

---

### Task 7: Add Error Handling and Edge Cases
**Objective:** Handle failure scenarios gracefully

**Scenarios to Handle:**
1. Upstream server fails to start (npx command not found, chrome-devtools-mcp unavailable)
2. Upstream server crashes mid-session
3. Client disconnects abruptly
4. Invalid messages from client or upstream
5. Context cancellation (graceful shutdown)

**Implementation:**
- Add error logging for all failure cases
- Propagate errors appropriately (don't swallow)
- Clean up resources (close connections, kill processes)
- Use `defer` for cleanup
- Add timeout to upstream connection (e.g., 10 seconds)

**Verification:**
- Kill upstream process → proxy logs error and exits cleanly
- Send malformed JSON → logged as error, proxy continues
- Ctrl+C proxy → upstream process terminated, no orphans
- Upstream unavailable → clear error message logged

**Files Modified:**
- UPDATE: `internal/proxy/upstream.go` (add error handling)
- UPDATE: `internal/proxy/server.go` (add error handling)
- UPDATE: `cmd/feedbackloop/main.go` (add signal handling)

---

## Verification

**Build Verification:**
```bash
go build -o feedbackloop ./cmd/feedbackloop
echo $? # Should be 0
```

**Smoke Test:**
```bash
# Terminal 1: Run proxy
./feedbackloop 2>&1 | jq

# Terminal 2: Test with mcp CLI or write test client
# (If mcp CLI not available, create minimal test client in Go)
```

**Log Verification:**
```bash
./feedbackloop 2>&1 | jq -e '.timestamp and .level and .correlation_id'
# Should exit 0 if all logs have required fields
```

**Manual Checklist:**
- [ ] `go build` succeeds with no errors
- [ ] Binary runs and logs to stdout
- [ ] Logs are valid JSON (parseable by `jq`)
- [ ] Client can connect via stdio
- [ ] Initialize handshake completes
- [ ] Ping/pong works
- [ ] Upstream connection to chrome-devtools-mcp succeeds
- [ ] Ctrl+C shuts down cleanly (no orphan processes)
- [ ] All logs include correlation IDs
- [ ] Timestamps are ISO8601 format

---

## Success Criteria

**Phase 1 is complete when:**

1. ✅ Go project structure exists with `go.mod` using `github.com/modelcontextprotocol/go-sdk`
2. ✅ Binary compiles: `go build -o feedbackloop ./cmd/feedbackloop`
3. ✅ Proxy can launch chrome-devtools-mcp via stdio (logged as successful connection)
4. ✅ Client can connect to proxy via stdio
5. ✅ Client can initialize session (send `initialize`, receive response)
6. ✅ Client can ping proxy (send `ping`, receive pong)
7. ✅ All protocol messages (initialize, ping) are logged as structured JSON to stdout
8. ✅ Logs include: `timestamp`, `level`, `event_type`, `correlation_id`, `direction`, `message`
9. ✅ Upstream process is cleaned up on exit (no zombie processes)
10. ✅ No tool proxying yet (Phase 2 scope)

**Deliverables:**
- Working Go binary: `feedbackloop`
- Source files:
  - `cmd/feedbackloop/main.go`
  - `internal/proxy/server.go`
  - `internal/proxy/upstream.go`
  - `internal/logger/logger.go`
  - `go.mod`
  - `go.sum`
- Verified with manual smoke test

---

## Output

**What gets built:**
- Single binary: `feedbackloop`
- Can be invoked by MCP clients via stdio (e.g., from Claude Desktop config)
- Connects to chrome-devtools-mcp as upstream server
- Logs all traffic as JSON to stdout

**Example usage (future - after Phase 6 config):**
```json
// Claude Desktop config
{
  "mcpServers": {
    "feedbackloop-proxy": {
      "command": "/path/to/feedbackloop",
      "args": []
    }
  }
}
```

**Logging output:**
```bash
./feedbackloop 2>&1 | tee logs.jsonl | jq
```

---

## Notes

**Out of Scope for Phase 1:**
- Tool discovery (`tools/list`) → Phase 2
- Tool call proxying → Phase 3
- HTTP/SSE transport → Phase 4 & 5
- Configuration file parsing → Phase 6
- Multiple upstream servers → Post-Milestone 1

**Dependencies:**
- Requires `npx` and `npm` installed (to run chrome-devtools-mcp)
- Go 1.23+ (for `iter.Seq` support in MCP SDK)

**Risks:**
- MCP SDK API may differ from docs (mitigation: test early, adjust)
- chrome-devtools-mcp may have breaking changes (mitigation: pin version with `-y`)
- Stdio buffering issues (mitigation: flush logs immediately)

**Next Phase:**
After Phase 1 completion, Phase 2 will add tool discovery - proxy will call `tools/list` on upstream and re-expose those tools to clients.

---

*Plan created: 2026-02-20*
*Ready for execution with `/gsd:execute-plan`*
