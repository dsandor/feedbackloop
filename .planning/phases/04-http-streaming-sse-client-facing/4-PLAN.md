# Phase 4 Execution Plan: HTTP Streaming (SSE) Transport - Client-Facing

**Phase:** 4 - HTTP Streaming (SSE) Transport - Client-Facing
**Milestone:** Milestone 1: Transparent MCP Proxy v1
**Status:** Ready for execution
**Created:** 2026-02-20
**Estimated Complexity:** Medium (5-7 hours)

---

## Objective

Add HTTP/SSE transport option for client-facing connections while maintaining stdio support. The proxy will support both transport types with identical behavior and logging. Clients will be able to connect via either stdio or HTTP/SSE and use all tools with the same transparency.

**Success means:** A client can connect to the proxy via HTTP/SSE, discover all tools, call tools, and receive identical results as stdio transport. Transport selection is configurable, and logging includes transport metadata.

---

## Execution Context

**SDK Documentation:**
- Official Go SDK: https://github.com/modelcontextprotocol/go-sdk
- SSE Transport Docs: https://modelcontextprotocol.io/specification/2024-11-05/basic/transports
- API Docs: https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp

**Key SDK Components:**
- `SSEHandler` - HTTP handler for SSE-based MCP sessions
- `NewSSEHandler(getServer func(*http.Request) *Server, opts *SSEOptions)` - creates SSE handler
- `SSEServerTransport` - server-side SSE transport (auto-managed by SSEHandler)
- `SSEOptions` - configuration for SSE handler (empty for now, future extensibility)
- `StdioTransport` - existing stdio transport (already working)

**MCP SSE Protocol:**
- Client initiates GET request with `Accept: text/event-stream`
- Server returns event stream with SSE format
- Each session gets unique endpoint for POST messages
- Server sends `endpoint` event with session URL
- Client POSTs messages to session endpoint
- Server responds via SSE events

**Current State:**
- Phase 3 complete: tool call proxying working over stdio
- ProxyServer has `Run(ctx, transport)` method accepting any Transport
- Transport abstraction already in place (stdio currently used)
- UpstreamManager uses stdio to connect to upstream (unchanged in this phase)
- All logging infrastructure works with stdio, needs transport awareness

---

## Context

**From PROJECT.md:**
- Proxy supports HTTP streaming (SSE) and stdio for client connections
- Same transparent proxying behavior regardless of transport
- Structured JSON logging with transport metadata

**From ROADMAP.md Phase 4 Exit Criteria:**
- Proxy can accept HTTP/SSE client connections
- All tool operations work identically over HTTP/SSE
- Logging includes transport type
- Can run in stdio or HTTP mode (configurable)

**From Phase 3 SUMMARY.md:**
- ProxyServer.Run() already accepts abstract Transport interface
- Tool discovery and proxying work transparently
- Logging uses correlation IDs and structured JSON
- All 26 chrome-devtools tools functional

**Architectural Decisions:**
- Transport selection via CLI flag: `--transport=stdio|http`
- Default to stdio for backward compatibility
- HTTP mode listens on configurable port (default 3000)
- SSEHandler creates ProxyServer per request (or reuses singleton)
- Logging enhanced with `transport_type` field
- No changes to upstream connection (stays stdio in Phase 4)

---

## Tasks

### Task 1: Add CLI Flag for Transport Selection
**Objective:** Allow user to choose transport type via command-line flag

**Implementation:** Update `cmd/feedbackloop/main.go`

**Requirements:**
- Add `--transport` flag with values: `stdio` (default) or `http`
- Add `--http-port` flag for HTTP mode (default: `3000`)
- Add `--http-host` flag for HTTP mode (default: `localhost`)
- Validate flags (invalid transport = error)
- Log transport configuration on startup

**Flag Parsing:**
```go
import (
	"flag"
	"fmt"
	"os"
)

var (
	transport = flag.String("transport", "stdio", "Transport type: stdio or http")
	httpHost  = flag.String("http-host", "localhost", "HTTP server host (http mode only)")
	httpPort  = flag.Int("http-port", 3000, "HTTP server port (http mode only)")
)

func main() {
	flag.Parse()

	// Validate transport
	if *transport != "stdio" && *transport != "http" {
		fmt.Fprintf(os.Stderr, "ERROR: Invalid transport '%s'. Must be 'stdio' or 'http'\n", *transport)
		os.Exit(1)
	}

	// Log transport config
	log.LogEvent("transport_configured", "main", map[string]interface{}{
		"transport": *transport,
		"http_host": *httpHost,
		"http_port": *httpPort,
	})
}
```

**Verification:**
- `./feedbackloop --help` shows flag documentation
- `./feedbackloop --transport=stdio` runs in stdio mode
- `./feedbackloop --transport=http` runs in HTTP mode
- `./feedbackloop --transport=invalid` exits with error
- Flags logged on startup

**Files Modified:**
- UPDATE: `cmd/feedbackloop/main.go` (add flag parsing)

---

### Task 2: Create HTTP Server with SSEHandler
**Objective:** Implement HTTP server with SSE endpoint for client connections

**Implementation:** Update `cmd/feedbackloop/main.go`

**Requirements:**
- Create HTTP server when `--transport=http`
- Use `mcp.NewSSEHandler()` to handle SSE connections
- Provide `getServer` function that returns ProxyServer
- Start HTTP server on configured host:port
- Graceful shutdown on context cancellation
- Log server start and stop events

**HTTP Server Setup:**
```go
import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dsandor/feedbackloop/internal/logger"
	"github.com/dsandor/feedbackloop/internal/proxy"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func runHTTPMode(ctx context.Context, upstream *proxy.UpstreamManager, log *logger.Logger, host string, port int) error {
	// Create SSE handler with getServer function
	sseHandler := mcp.NewSSEHandler(func(req *http.Request) *mcp.Server {
		// For now, create a new proxy server per request
		// (Alternative: reuse singleton ProxyServer if thread-safe)
		proxyServer, err := proxy.NewProxyServer(upstream, log)
		if err != nil {
			log.LogErrorEvent("proxy_server_creation_failed", "http_server", map[string]interface{}{
				"error": err.Error(),
			})
			return nil
		}
		return proxyServer.GetServer() // Need to expose underlying mcp.Server
	}, &mcp.SSEOptions{})

	// Create HTTP server
	addr := fmt.Sprintf("%s:%d", host, port)
	httpServer := &http.Server{
		Addr:    addr,
		Handler: sseHandler,
	}

	// Start server in goroutine
	serverErr := make(chan error, 1)
	go func() {
		log.LogEvent("http_server_starting", "http_server", map[string]interface{}{
			"address": addr,
		})
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	// Wait for shutdown signal or server error
	select {
	case <-ctx.Done():
		// Graceful shutdown
		log.LogEvent("http_server_shutting_down", "http_server", map[string]interface{}{
			"reason": "signal",
		})
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.LogErrorEvent("http_server_shutdown_error", "http_server", map[string]interface{}{
				"error": err.Error(),
			})
			return err
		}
		log.LogEvent("http_server_stopped", "http_server", map[string]interface{}{
			"reason": "graceful_shutdown",
		})
		return nil
	case err := <-serverErr:
		log.LogErrorEvent("http_server_error", "http_server", map[string]interface{}{
			"error": err.Error(),
		})
		return err
	}
}
```

**Verification:**
- HTTP server starts on configured port
- Server responds to GET requests at root endpoint
- Server shuts down gracefully on SIGINT
- Error logged if port already in use
- Startup/shutdown events logged

**Files Modified:**
- UPDATE: `cmd/feedbackloop/main.go` (add HTTP server)
- UPDATE: `internal/proxy/server.go` (add GetServer() method to expose mcp.Server)

---

### Task 3: Expose Underlying MCP Server
**Objective:** Allow SSEHandler to access the underlying mcp.Server from ProxyServer

**Implementation:** Update `internal/proxy/server.go`

**Requirements:**
- Add `GetServer()` method to ProxyServer
- Return the underlying `mcp.Server` instance
- Keep ProxyServer fields private (no direct access)
- Document that GetServer is for transport integration

**Method Addition:**
```go
// GetServer returns the underlying MCP server for transport integration.
// This is used by HTTP/SSE transport handlers to connect to the server.
func (ps *ProxyServer) GetServer() *mcp.Server {
	return ps.server
}
```

**Verification:**
- GetServer() returns non-nil mcp.Server
- Method accessible from main.go
- ProxyServer fields remain private
- Documentation clear

**Files Modified:**
- UPDATE: `internal/proxy/server.go` (add GetServer method)

---

### Task 4: Update Main to Support Both Transports
**Objective:** Route to stdio or HTTP mode based on CLI flag

**Implementation:** Update `cmd/feedbackloop/main.go`

**Requirements:**
- Check transport flag value
- Call stdio mode logic if transport=stdio (existing code)
- Call HTTP mode logic if transport=http (new code)
- Log transport mode selection
- Keep signal handling and cleanup in both paths

**Main Logic:**
```go
func main() {
	flag.Parse()

	// ... (existing setup: context, signals, logger, upstream)

	// Log selected transport mode
	log.LogEvent("transport_mode_selected", "main", map[string]interface{}{
		"transport": *transport,
	})

	// Route to appropriate transport
	switch *transport {
	case "stdio":
		if err := runStdioMode(ctx, upstream, log); err != nil {
			log.LogErrorEvent("stdio_mode_error", "main", map[string]interface{}{
				"error": err.Error(),
			})
			exitCode = 1
		}
	case "http":
		if err := runHTTPMode(ctx, upstream, log, *httpHost, *httpPort); err != nil {
			log.LogErrorEvent("http_mode_error", "main", map[string]interface{}{
				"error": err.Error(),
			})
			exitCode = 1
		}
	}

	log.LogEvent("shutdown_complete", "main", map[string]interface{}{
		"transport": *transport,
	})
}

func runStdioMode(ctx context.Context, upstream *proxy.UpstreamManager, log *logger.Logger) error {
	// Create proxy server
	proxyServer, err := proxy.NewProxyServer(upstream, log)
	if err != nil {
		return fmt.Errorf("proxy server creation failed: %w", err)
	}

	// Create stdio transport
	transport := &mcp.StdioTransport{}

	log.LogEvent("ready_for_client", "stdio_mode", map[string]interface{}{
		"transport": "stdio",
	})

	// Run server
	return proxyServer.Run(ctx, transport)
}

func runHTTPMode(ctx context.Context, upstream *proxy.UpstreamManager, log *logger.Logger, host string, port int) error {
	// ... (HTTP server logic from Task 2)
}
```

**Verification:**
- Stdio mode works as before (backward compatibility)
- HTTP mode starts HTTP server
- Both modes handle shutdown gracefully
- Logs include transport type
- No code duplication in shared logic

**Files Modified:**
- UPDATE: `cmd/feedbackloop/main.go` (add transport routing)

---

### Task 5: Add Transport Metadata to Logging
**Objective:** Include transport type in all relevant log events

**Implementation:** Update logging calls in multiple files

**Requirements:**
- Add `transport_type` field to startup logs
- Add `transport_type` to server lifecycle logs
- Keep logging structure consistent
- No changes to tool call logging (already has correlation IDs)

**Log Enhancements:**
```go
// In main.go - startup
log.LogEvent("ready_for_client", "main", map[string]interface{}{
	"transport":      "stdio",  // or "http"
	"transport_type": *transport,
})

// In HTTP mode - server events
log.LogEvent("http_server_starting", "http_server", map[string]interface{}{
	"address":        addr,
	"transport_type": "http",
})

// In stdio mode - server events
log.LogEvent("ready_for_client", "stdio_mode", map[string]interface{}{
	"transport":      "stdio",
	"transport_type": "stdio",
})
```

**Verification:**
- All startup logs include transport_type
- HTTP server logs include address and transport
- Stdio mode logs include transport marker
- Log format consistent across transports
- Easy to filter logs by transport type

**Files Modified:**
- UPDATE: `cmd/feedbackloop/main.go` (add transport_type to logs)

---

### Task 6: Build and Integration Test with HTTP Transport
**Objective:** Verify HTTP/SSE transport works end-to-end

**Test Procedure:**
1. Build binary: `go build -o feedbackloop ./cmd/feedbackloop`
2. Run in HTTP mode: `./feedbackloop --transport=http --http-port=3000 2>&1 | jq`
3. Test HTTP endpoints:
   - GET / (should initiate SSE session)
   - Verify SSE endpoint event
   - POST messages to session endpoint
4. Use SSE client to connect and test:
   - Tool discovery (`tools/list`)
   - Tool calls (at least 3 tools)
   - Error handling (invalid tool)
5. Compare results with stdio transport
6. Verify logs include transport metadata
7. Test graceful shutdown (Ctrl+C)

**SSE Test Client:**
Create `test_client_sse.go` for HTTP/SSE testing:

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	ctx := context.Background()

	// Create SSE client transport
	transport := &mcp.SSEClientTransport{
		Endpoint: "http://localhost:3000",
	}

	// Create MCP client
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "sse-test-client",
		Version: "0.1.0",
	}, nil)

	// Connect to proxy
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Connection failed: %v\n", err)
		os.Exit(1)
	}
	defer session.Close()

	fmt.Println("✓ Connected to proxy via HTTP/SSE")

	// List tools
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ListTools failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Discovered %d tools\n", len(tools.Tools))

	// Call a simple tool
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_pages",
		Arguments: map[string]interface{}{},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "CallTool failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Called list_pages: %d content items, isError=%v\n", len(result.Content), result.IsError)

	// Test invalid tool
	_, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "invalid_tool",
		Arguments: map[string]interface{}{},
	})
	if err != nil {
		fmt.Printf("✓ Invalid tool error: %v\n", err)
	} else {
		fmt.Println("✗ Expected error for invalid tool!")
	}

	fmt.Println("\n✅ All HTTP/SSE tests passed!")
}
```

**Expected Logs (HTTP Mode):**
```json
{"timestamp":"...","event_type":"feedbackloop_starting","component":"main","message":{"version":"0.1.0"}}
{"timestamp":"...","event_type":"transport_configured","component":"main","message":{"transport":"http","http_host":"localhost","http_port":3000}}
{"timestamp":"...","event_type":"transport_mode_selected","component":"main","message":{"transport":"http"}}
{"timestamp":"...","event_type":"http_server_starting","component":"http_server","message":{"address":"localhost:3000","transport_type":"http"}}
{"timestamp":"...","event_type":"tool_call_request","correlation_id":"...","direction":"inbound","message":{"tool":"list_pages",...}}
{"timestamp":"...","event_type":"tool_call_response","correlation_id":"...","direction":"outbound","message":{"tool":"list_pages",...}}
```

**Success Criteria:**
- Binary builds successfully
- HTTP server starts on specified port
- SSE client can connect
- Tool discovery works over HTTP/SSE (26 tools)
- Tool calls work over HTTP/SSE (tested 3+ tools)
- Results identical to stdio transport
- Logs include transport_type
- Graceful shutdown works
- No panics or crashes

**Files Modified:**
- NEW: `test_client_sse.go` (SSE integration test client)

---

### Task 7: Add Unit Tests for HTTP Transport Integration
**Objective:** Test HTTP transport setup and configuration

**Implementation:** Create `cmd/feedbackloop/main_test.go`

**Requirements:**
- Test flag parsing (valid and invalid transports)
- Test HTTP server creation (mock or integration)
- Test stdio mode still works (backward compatibility)
- Verify transport metadata in logs
- Test graceful shutdown for both transports

**Test Cases:**
```go
package main

import (
	"flag"
	"testing"
)

func TestTransportFlagParsing(t *testing.T) {
	tests := []struct {
		name      string
		transport string
		wantValid bool
	}{
		{"stdio transport", "stdio", true},
		{"http transport", "http", true},
		{"invalid transport", "grpc", false},
		{"empty transport", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset flags
			flag.CommandLine = flag.NewFlagSet("test", flag.ContinueOnError)

			// Test flag validation logic
			// (Extract validation to testable function)
		})
	}
}

func TestHTTPServerConfiguration(t *testing.T) {
	// Test HTTP server setup with different host:port combos
	// Verify address format
	// Test port validation
}

func TestTransportModeSelection(t *testing.T) {
	// Test routing logic (stdio vs http)
	// Verify correct mode selected
	// Test error handling
}
```

**Verification:**
- All new tests pass
- Existing functionality not broken
- Flag validation tested
- Transport routing tested

**Files Modified:**
- NEW: `cmd/feedbackloop/main_test.go` (transport tests)

---

### Task 8: Update README with HTTP Transport Usage
**Objective:** Document HTTP transport usage for users

**Implementation:** Update `README.md`

**Requirements:**
- Add HTTP transport section
- Show CLI flag usage examples
- Document SSE client connection
- Include example curl commands for testing
- Keep stdio examples (still default)

**README Additions:**
```markdown
## Transport Options

feedbackloop supports two transport types for client connections:

### Stdio Transport (Default)

Connect via stdin/stdout using the MCP protocol:

```bash
./feedbackloop
```

Configure in Claude Desktop:
```json
{
  "mcpServers": {
    "feedbackloop": {
      "command": "/path/to/feedbackloop"
    }
  }
}
```

### HTTP/SSE Transport

Run as HTTP server with Server-Sent Events:

```bash
./feedbackloop --transport=http --http-host=localhost --http-port=3000
```

Test with curl (SSE connection):
```bash
curl -N -H "Accept: text/event-stream" http://localhost:3000/
```

Connect from code:
```go
transport := &mcp.SSEClientTransport{
    Endpoint: "http://localhost:3000",
}
session, err := client.Connect(ctx, transport, nil)
```

### CLI Flags

- `--transport`: Transport type (`stdio` or `http`). Default: `stdio`
- `--http-host`: HTTP server host (HTTP mode only). Default: `localhost`
- `--http-port`: HTTP server port (HTTP mode only). Default: `3000`

Examples:
```bash
# Run with stdio (default)
./feedbackloop

# Run with HTTP on port 8080
./feedbackloop --transport=http --http-port=8080

# Run HTTP on all interfaces
./feedbackloop --transport=http --http-host=0.0.0.0 --http-port=3000
```
```

**Verification:**
- README includes both transport types
- Examples are correct and tested
- Clear, concise documentation
- No breaking changes to existing docs

**Files Modified:**
- UPDATE: `README.md` (add HTTP transport documentation)

---

## Verification

**Build Verification:**
```bash
go build -o feedbackloop ./cmd/feedbackloop
echo $?  # Should be 0
```

**Stdio Transport (Backward Compatibility):**
```bash
./feedbackloop 2>&1 | jq
# In another terminal:
go run test_client.go
```

**HTTP Transport:**
```bash
# Terminal 1: Start HTTP server
./feedbackloop --transport=http --http-port=3000 2>&1 | jq

# Terminal 2: Test with SSE client
go run test_client_sse.go

# Terminal 3: Test with curl
curl -N -H "Accept: text/event-stream" http://localhost:3000/
```

**Flag Validation:**
```bash
./feedbackloop --help  # Shows flag docs
./feedbackloop --transport=invalid  # Exits with error
./feedbackloop --transport=http --http-port=999999  # Validates port range
```

**Log Verification:**
```bash
# Check transport metadata in logs
./feedbackloop --transport=http 2>&1 | jq -c 'select(.transport_type)'
```

**Manual Checklist:**
- [ ] `go build` succeeds with no errors
- [ ] Stdio transport works (backward compatibility)
- [ ] HTTP server starts on configured port
- [ ] SSE client can connect to HTTP endpoint
- [ ] Tool discovery works over HTTP/SSE (26 tools)
- [ ] Tool calls work over HTTP/SSE (tested 3+ tools)
- [ ] Results identical between stdio and HTTP transports
- [ ] Logs include transport_type metadata
- [ ] CLI flags work correctly
- [ ] Invalid flags produce clear errors
- [ ] Graceful shutdown works for both transports
- [ ] No panics or crashes in either mode
- [ ] Unit tests pass
- [ ] README documents both transports

---

## Success Criteria

**Phase 4 is complete when:**

1. ✅ Proxy accepts HTTP/SSE client connections
2. ✅ All tool operations work identically over HTTP/SSE (26 tools functional)
3. ✅ Logging includes transport_type in relevant events
4. ✅ Can run in stdio or HTTP mode (via --transport flag)
5. ✅ Stdio transport still works (backward compatibility verified)
6. ✅ HTTP server starts/stops gracefully
7. ✅ SSE client can discover tools via HTTP
8. ✅ SSE client can call tools via HTTP
9. ✅ Results identical between transports (verified with test client)
10. ✅ CLI flags validated and documented
11. ✅ Unit tests cover flag parsing and transport selection
12. ✅ README documents both transport types with examples

**Deliverables:**
- Updated binary: `feedbackloop` (with HTTP/SSE support)
- Updated files:
  - `cmd/feedbackloop/main.go` (flag parsing, HTTP server, transport routing)
  - `internal/proxy/server.go` (GetServer method)
  - `README.md` (transport documentation)
- New files:
  - `test_client_sse.go` (SSE integration test client)
  - `cmd/feedbackloop/main_test.go` (transport tests)

---

## Output

**What gets added:**
- HTTP/SSE transport support for client connections
- CLI flag-based transport selection
- Dual-transport capability (stdio + HTTP/SSE)
- Transport metadata in logs
- Graceful shutdown for HTTP server

**Example HTTP/SSE Flow:**
```
Client → HTTP GET http://localhost:3000/ (Accept: text/event-stream)
  └─ Log: [info] http_server_starting {address: "localhost:3000", transport_type: "http"}

Server → SSE stream: event: endpoint
         data: {"endpoint": "/sessions/abc-123"}

Client → HTTP POST http://localhost:3000/sessions/abc-123
         {jsonrpc: "2.0", method: "tools/list", id: 1}
  └─ Log: [inbound] tool_discovery_request {correlation_id: "xyz"}

Server → SSE event: message
         data: {jsonrpc: "2.0", result: {tools: [...]}, id: 1}
  └─ Log: [outbound] tool_discovery_response {correlation_id: "xyz", tool_count: 26}
```

**CLI Usage:**
```bash
# Stdio mode (default, backward compatible)
./feedbackloop

# HTTP mode on default port (3000)
./feedbackloop --transport=http

# HTTP mode on custom port
./feedbackloop --transport=http --http-port=8080

# HTTP mode on all interfaces
./feedbackloop --transport=http --http-host=0.0.0.0 --http-port=3000
```

---

## Notes

**Out of Scope for Phase 4:**
- HTTP transport for upstream connections → Phase 5
- Multiple simultaneous transports → Future enhancement
- Authentication/authorization → Post-Milestone 1
- TLS/HTTPS → Post-Milestone 1
- Configuration file parsing → Phase 6
- WebSocket transport → Not in roadmap

**In Scope for Phase 4:**
- Client-facing HTTP/SSE only (upstream still uses stdio)
- Single transport per proxy instance (not both simultaneously)
- Basic HTTP server (no TLS, auth, or advanced features)
- Logging transport metadata for observability

**Dependencies:**
- Phase 3 complete ✅ (tool proxying working)
- go-sdk v1.3.1 includes SSEHandler and SSEClientTransport
- chrome-devtools-mcp for testing (via npx, stdio upstream)

**Risks:**
- SSE connection handling complexity (mitigation: use SDK's SSEHandler)
- Port conflicts (mitigation: configurable port, clear error messages)
- HTTP server resource usage with many concurrent clients (mitigation: test with load, rely on SDK's implementation)

**Design Decisions:**
- **Transport selection via CLI flag:** Simple, clear, no config file needed yet
- **Default to stdio:** Backward compatibility, safe upgrade path
- **SSEHandler from SDK:** Leverages official implementation, less custom code
- **GetServer() method:** Clean way to expose mcp.Server for SSE integration
- **Transport metadata in logs:** Easy filtering and debugging per transport
- **Single transport per instance:** Simplifies implementation, clear operational model

**Next Phase:**
After Phase 4 completion, Phase 5 will add HTTP/SSE transport for upstream connections, enabling full HTTP↔HTTP proxying scenarios.

---

*Plan created: 2026-02-20*
*Ready for execution with `/gsd:execute-plan`*
