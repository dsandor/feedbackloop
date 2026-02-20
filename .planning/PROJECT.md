# feedbackloop

> A transparent MCP proxy server in Go that sits in front of real MCP servers, logging all traffic without interfering.

## Overview

`feedbackloop` is a Model Context Protocol proxy written in Go. It discovers all tools exposed by a real (upstream) MCP server, re-exposes them identically through the proxy, and transparently logs every inbound request and outbound response as structured JSON to stdout. The proxy is invisible to both the MCP client and the real MCP server — it must not alter, delay, or drop any messages.

The proxy supports two client-facing transport modes (stdio and HTTP streaming) and two upstream connection modes (stdio and HTTP streaming), using the official Go MCP SDK from `github.com/modelcontextprotocol/go-sdk`.

## Core Principle

**Zero-friction transparency.** The real MCP server must behave identically whether or not the proxy is in front of it. Every tool call, every response, every error passes through unmodified. Logging is a side-effect, never an obstacle.

## Requirements

### Validated

*(None yet — ship to validate)*

### Active

- [ ] Proxy discovers all tools from a real (upstream) MCP server at startup via `tools/list`
- [ ] Proxy re-exposes discovered tools identically (same name, schema, description) to MCP clients
- [ ] All inbound requests (from client) are logged as structured JSON to stdout with timestamp and correlation ID
- [ ] All outbound responses (from upstream) are logged as structured JSON to stdout with timestamp and correlation ID
- [ ] Request/response pairs are correlated (same correlation ID so they can be matched)
- [ ] Proxy supports stdio transport for client connections
- [ ] Proxy supports HTTP streaming (SSE) transport for client connections
- [ ] Proxy supports stdio transport to connect to upstream MCP servers
- [ ] Proxy supports HTTP streaming (SSE) transport to connect to upstream MCP servers
- [ ] Upstream MCP server is configured via Claude Desktop-compatible `mcpservers` JSON config (command + args)
- [ ] Proxy must not modify, delay, or drop any requests or responses
- [ ] Build and test against `chrome-devtools-mcp` as the upstream reference server
- [ ] Ships as a single static Go binary

### Out of Scope

- Web UI / dashboard — not in v1, may add a log viewer in a later milestone
- Request modification / filtering — proxy is transparent only; no rewriting or blocking
- Authentication / access control — no API keys or user management on the proxy itself
- Metrics / tracing integration — no Prometheus, OpenTelemetry, or Datadog hooks in v1
- Multiple simultaneous upstream servers — one upstream per proxy instance; merge is a future milestone

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Go language | Project spec requires it; strong stdlib for network primitives | Go |
| Official Go MCP SDK (`go-sdk`) | Project spec mandates this SDK | `github.com/modelcontextprotocol/go-sdk` |
| Structured JSON to stdout | Machine-readable, zero dependencies, easily piped to any tooling | JSON lines to stdout |
| One upstream per proxy instance | Simplicity; avoids tool name collision; can run multiple proxies | Single upstream |
| Correlation IDs per request | Tie request log to response log without in-memory state | UUID per call |
| Claude Desktop config format | Allows drop-in use in Claude Desktop's `mcpservers` config | `{"command": "...", "args": [...]}` |

## Technical Stack

- **Language:** Go (latest stable)
- **MCP SDK:** `github.com/modelcontextprotocol/go-sdk`
- **Transports (client-facing):** stdio, HTTP/SSE streaming
- **Transports (upstream-facing):** stdio, HTTP/SSE streaming
- **Logging:** `encoding/json` → stdout (JSON lines format)
- **Config:** JSON file matching Claude Desktop `mcpservers` format

## Reference Upstream

The proxy is configured and tested against:

```json
"chrome-devtools": {
  "command": "npx",
  "args": ["-y", "chrome-devtools-mcp@latest"]
}
```

---
*Last updated: 2026-02-19 after initialization*
