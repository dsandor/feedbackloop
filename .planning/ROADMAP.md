# feedbackloop Roadmap

## Milestone 1: Transparent MCP Proxy v1

### Phase 1: Foundation & SDK Integration ✅
**Goal:** Set up Go project structure with official MCP SDK and basic stdio transport

**Scope:**
- Initialize Go module with `github.com/modelcontextprotocol/go-sdk`
- Implement basic stdio transport for client-facing connections
- Create upstream stdio connection to launch configured MCP server (chrome-devtools)
- Establish bidirectional message flow (no tool proxying yet, just initialization/ping)
- Structured JSON logging to stderr for all messages (stdout reserved for MCP protocol)

**Exit Criteria:** ✅ All Met
- ✅ Go project builds successfully
- ✅ Can launch upstream MCP server via stdio (chrome-devtools-mcp)
- ✅ Client can connect via stdio to proxy
- ✅ All protocol messages logged as JSON to stderr

**Status:** Complete (2026-02-20)
**Plans:** 1 plan executed (1-PLAN.md)
**Research Needed:** No

---

### Phase 2: Tool Discovery & Re-exposure
**Goal:** Discover tools from upstream server and re-expose them identically to clients

**Scope:**
- On startup, send `tools/list` request to upstream server
- Parse and cache discovered tools (name, schema, description)
- Re-expose discovered tools in proxy's `tools/list` response to clients
- Ensure schema fidelity (tools appear identical to client)
- Add correlation IDs (UUID) to every request/response pair

**Exit Criteria:**
- Proxy calls `tools/list` on upstream at startup
- Client calling `tools/list` sees all upstream tools
- Tool schemas match exactly (name, inputSchema, description)
- Request/response logs include correlation IDs

**Research Needed:** No

---

### Phase 3: Transparent Tool Call Proxying
**Goal:** Proxy tool call requests/responses without modification

**Scope:**
- Forward `tools/call` requests from client to upstream
- Return upstream responses to client unmodified
- Log inbound request with correlation ID, timestamp, tool name, arguments
- Log outbound response with same correlation ID, timestamp, result/error
- Ensure zero latency overhead (async logging)
- Handle errors from upstream gracefully

**Exit Criteria:**
- Client can invoke any upstream tool through proxy
- Tool results are identical to direct upstream calls
- Request and response logs are correlated (same ID)
- No message loss, delay, or modification
- Errors from upstream are passed through unchanged

**Research Needed:** No

---

### Phase 4: HTTP Streaming (SSE) Transport - Client-Facing
**Goal:** Add HTTP/SSE transport option for client connections

**Scope:**
- Implement HTTP server with SSE endpoint for client connections
- Support same tool discovery and call proxying over HTTP/SSE
- Maintain JSON logging for HTTP/SSE connections
- Allow transport selection via CLI flag or config
- Ensure parity with stdio behavior (same logging, same transparency)

**Exit Criteria:**
- Proxy can accept HTTP/SSE client connections
- All tool operations work identically over HTTP/SSE
- Logging includes transport type
- Can run in stdio or HTTP mode (configurable)

**Research Needed:** Yes
- How SSE transport is implemented in go-sdk for server side
- SSE event stream format expected by MCP clients

---

### Phase 5: HTTP Streaming (SSE) Transport - Upstream-Facing
**Goal:** Support connecting to upstream MCP servers via HTTP/SSE

**Scope:**
- Implement HTTP/SSE client to connect to upstream servers
- Support config specifying HTTP endpoint instead of command/args
- Maintain same proxying behavior regardless of upstream transport
- Add transport detection logic (stdio command vs HTTP URL)

**Exit Criteria:**
- Proxy can connect to upstream via HTTP/SSE
- Can proxy between any client/upstream transport combination (stdio↔stdio, stdio↔HTTP, HTTP↔stdio, HTTP↔HTTP)
- All transport combinations tested
- Config clearly specifies transport type

**Research Needed:** Yes
- HTTP/SSE client implementation in go-sdk
- MCP server HTTP endpoint conventions

---

### Phase 6: Configuration & Testing
**Goal:** Finalize config format, packaging, and validation against chrome-devtools-mcp

**Scope:**
- Implement Claude Desktop-compatible JSON config parser (`mcpservers` format)
- Support command + args for stdio upstream servers
- Support URL for HTTP upstream servers
- Build single static binary
- End-to-end testing with chrome-devtools-mcp as upstream
- Verify all chrome-devtools tools work through proxy
- Documentation: README with setup, usage, config examples

**Exit Criteria:**
- Config file matches Claude Desktop `mcpservers` JSON format
- Single `feedbackloop` binary compiles
- All chrome-devtools tools functional through proxy
- README documents installation, configuration, usage
- Example config provided

**Research Needed:** No

---

## Post-Milestone 1 (Future)

- Web UI for log visualization
- Request filtering/modification (optional transparency break)
- Multiple upstream server merging
- Authentication/authorization layer
- Metrics and observability integrations

---

*Roadmap created: 2026-02-20*
