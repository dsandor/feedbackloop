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

| Flag | Description | Default |
|---|---|---|
| `--transport` | Transport type: `stdio` or `http` | `stdio` |
| `--http-host` | HTTP server bind host (http mode only) | `localhost` |
| `--http-port` | HTTP server bind port (http mode only) | `3000` |
| `--ui-port` | Web UI server port (0 = auto-select 3070–3099) | `0` |
| `--api-key` | Anthropic API key (overrides `ANTHROPIC_API_KEY` env var) | `""` |
| `--config` | Path to config file | `config.json` |
| `--server` | Name of the `mcpServers` entry to proxy (defaults to first entry) | `""` |

### Examples

```bash
# Run with stdio (default)
./feedbackloop

# Run with HTTP on port 8080
./feedbackloop --transport=http --http-port=8080

# Run HTTP on all interfaces
./feedbackloop --transport=http --http-host=0.0.0.0 --http-port=3000

# Use a custom config file and select a specific server
./feedbackloop --config=/etc/feedbackloop/config.json --server=my-server
```

## Configuration

feedbackloop is configured via a JSON file (default: `config.json` in the working directory). The file uses the same `mcpServers` format as Claude Desktop, with an optional `settings` block for feedbackloop-specific options.

### Config File Format

```json
{
  "mcpServers": {
    "chrome-devtools": {
      "command": "npx",
      "args": ["-y", "chrome-devtools-mcp@latest"]
    }
  },
  "settings": {
    "model": "claude-sonnet-4-6",
    "transport": "stdio",
    "httpHost": "localhost",
    "httpPort": 3000,
    "uiPort": 0,
    "apiKey": "",
    "server": ""
  }
}
```

### `mcpServers`

Each entry follows the Claude Desktop format. feedbackloop proxies one upstream server at a time — by default the first entry in the map. Use `--server <name>` (or `settings.server`) to select a specific one when multiple servers are defined.

| Field | Description |
|---|---|
| `command` | Executable to run for the upstream MCP server |
| `args` | Arguments passed to the command |
| `env` | (optional) Additional environment variables |

### `settings`

All settings fields are optional. Priority order: **CLI flag > config file > environment variable > built-in default**.

| Field | CLI equivalent | Env var | Description |
|---|---|---|---|
| `model` | — | `FEEDBACKLOOP_LLM_MODEL` | Anthropic model for AI analysis |
| `transport` | `--transport` | — | Client-facing transport (`stdio` or `http`) |
| `httpHost` | `--http-host` | — | HTTP bind host |
| `httpPort` | `--http-port` | — | HTTP bind port |
| `uiPort` | `--ui-port` | `FEEDBACKLOOP_UI_PORT` | Web UI port (0 = auto) |
| `apiKey` | `--api-key` | `ANTHROPIC_API_KEY` | Anthropic API key |
| `server` | `--server` | — | `mcpServers` entry name to proxy |

### Claude Desktop Integration

To use feedbackloop as a transparent proxy in Claude Desktop, point the Claude Desktop config at feedbackloop instead of the real MCP server:

```json
{
  "mcpServers": {
    "feedbackloop": {
      "command": "/path/to/feedbackloop",
      "args": ["--config", "/path/to/config.json"]
    }
  }
}
```

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
