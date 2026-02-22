package proxy

// ClientInfo holds metadata about the connected MCP client.
// Used for per-request logging and usage correlation.
type ClientInfo struct {
	ClientID   string            `json:"client_id"`             // generated UUID per connection
	Transport  string            `json:"transport"`             // "stdio" or "http"
	RemoteAddr string            `json:"remote_addr,omitempty"` // HTTP client IP
	UserAgent  string            `json:"user_agent,omitempty"`  // HTTP User-Agent
	Headers    map[string]string `json:"headers,omitempty"`     // selected HTTP headers
}
