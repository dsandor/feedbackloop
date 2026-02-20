# feedbackloop

A transparent MCP (Model Context Protocol) proxy server that sits between MCP clients and MCP servers, providing comprehensive logging and monitoring capabilities.

## Features

- Transparent proxying of all MCP tool calls
- Support for both stdio and HTTP/SSE transports
- Comprehensive structured JSON logging
- Tool discovery and re-exposure
- Correlation IDs for request/response tracking
- No-overhead proxying (transparent to clients and servers)

## Quick Start

### Building

```bash
go build -o feedbackloop ./cmd/feedbackloop
```

### Running

By default, feedbackloop runs in stdio mode:

```bash
./feedbackloop
```

For HTTP/SSE mode:

```bash
./feedbackloop --transport=http --http-port=3000
```

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

## CLI Flags

- `--transport`: Transport type (`stdio` or `http`). Default: `stdio`
- `--http-host`: HTTP server host (HTTP mode only). Default: `localhost`
- `--http-port`: HTTP server port (HTTP mode only). Default: `3000`

### Examples

```bash
# Run with stdio (default)
./feedbackloop

# Run with HTTP on port 8080
./feedbackloop --transport=http --http-port=8080

# Run HTTP on all interfaces
./feedbackloop --transport=http --http-host=0.0.0.0 --http-port=3000
```

## Configuration

Currently, feedbackloop is configured to proxy the `chrome-devtools-mcp` server. The upstream server is configured via:

```go
upstream := proxy.NewUpstreamManager(log, "npx", []string{"-y", "chrome-devtools-mcp@latest"})
```

Future versions will support configuration files for flexible upstream server configuration.

## Testing

### Build and Test

```bash
# Build
go build -o feedbackloop ./cmd/feedbackloop

# Run unit tests
go test ./...

# Test stdio mode
./feedbackloop &
go run test_client.go

# Test HTTP/SSE mode
./feedbackloop --transport=http --http-port=3000 &
go run test_client_sse.go
```

## Architecture

- `cmd/feedbackloop/main.go`: Entry point, flag parsing, transport routing
- `internal/proxy/server.go`: ProxyServer implementation
- `internal/proxy/upstream.go`: Upstream server management
- `internal/proxy/tools.go`: Tool discovery and proxying logic
- `internal/logger/`: Structured logging infrastructure

## Logging

All logs are output in structured JSON format to stderr:

```json
{"timestamp":"2026-02-20T10:00:00Z","event_type":"feedbackloop_starting","component":"main","message":{"version":"0.1.0"}}
{"timestamp":"2026-02-20T10:00:01Z","event_type":"transport_configured","component":"main","message":{"transport":"http","http_host":"localhost","http_port":3000}}
{"timestamp":"2026-02-20T10:00:02Z","event_type":"http_server_starting","component":"http_server","message":{"address":"localhost:3000","transport_type":"http"}}
```

To pretty-print logs, pipe through `jq`:

```bash
./feedbackloop --transport=http 2>&1 | jq
```

## License

MIT

## Contributing

Contributions welcome! Please open an issue or submit a pull request.
