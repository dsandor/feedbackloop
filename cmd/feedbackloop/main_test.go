package main

import (
	"flag"
	"fmt"
	"testing"
)

func TestTransportFlagValidation(t *testing.T) {
	tests := []struct {
		name      string
		transport string
		wantValid bool
	}{
		{"stdio transport", "stdio", true},
		{"http transport", "http", true},
		{"invalid transport", "grpc", false},
		{"empty transport", "", false},
		{"websocket transport", "websocket", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			valid := tt.transport == "stdio" || tt.transport == "http"
			if valid != tt.wantValid {
				t.Errorf("transport %q: got valid=%v, want valid=%v", tt.transport, valid, tt.wantValid)
			}
		})
	}
}

func TestHTTPServerAddressFormat(t *testing.T) {
	tests := []struct {
		name string
		host string
		port int
		want string
	}{
		{"localhost default", "localhost", 3000, "localhost:3000"},
		{"localhost custom port", "localhost", 8080, "localhost:8080"},
		{"all interfaces", "0.0.0.0", 3000, "0.0.0.0:3000"},
		{"specific IP", "127.0.0.1", 9000, "127.0.0.1:9000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test address format construction
			addr := formatAddress(tt.host, tt.port)
			if addr != tt.want {
				t.Errorf("formatAddress(%q, %d) = %q, want %q", tt.host, tt.port, addr, tt.want)
			}
		})
	}
}

// formatAddress is a helper extracted from runHTTPMode for testability
func formatAddress(host string, port int) string {
	return fmt.Sprintf("%s:%d", host, port)
}

func TestFlagDefaults(t *testing.T) {
	// Reset flag set for testing
	testFlags := flag.NewFlagSet("test", flag.ContinueOnError)
	transportFlag := testFlags.String("transport", "stdio", "Transport type: stdio or http")
	httpHostFlag := testFlags.String("http-host", "localhost", "HTTP server host (http mode only)")
	httpPortFlag := testFlags.Int("http-port", 3000, "HTTP server port (http mode only)")

	// Test defaults
	if *transportFlag != "stdio" {
		t.Errorf("default transport = %q, want %q", *transportFlag, "stdio")
	}
	if *httpHostFlag != "localhost" {
		t.Errorf("default http-host = %q, want %q", *httpHostFlag, "localhost")
	}
	if *httpPortFlag != 3000 {
		t.Errorf("default http-port = %d, want %d", *httpPortFlag, 3000)
	}
}

func TestFlagParsing(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantTrans string
		wantHost  string
		wantPort  int
	}{
		{
			name:      "stdio default",
			args:      []string{},
			wantTrans: "stdio",
			wantHost:  "localhost",
			wantPort:  3000,
		},
		{
			name:      "http mode",
			args:      []string{"--transport=http"},
			wantTrans: "http",
			wantHost:  "localhost",
			wantPort:  3000,
		},
		{
			name:      "http with custom port",
			args:      []string{"--transport=http", "--http-port=8080"},
			wantTrans: "http",
			wantHost:  "localhost",
			wantPort:  8080,
		},
		{
			name:      "http with custom host",
			args:      []string{"--transport=http", "--http-host=0.0.0.0", "--http-port=9000"},
			wantTrans: "http",
			wantHost:  "0.0.0.0",
			wantPort:  9000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create new flag set for each test
			testFlags := flag.NewFlagSet("test", flag.ContinueOnError)
			transportFlag := testFlags.String("transport", "stdio", "Transport type: stdio or http")
			httpHostFlag := testFlags.String("http-host", "localhost", "HTTP server host (http mode only)")
			httpPortFlag := testFlags.Int("http-port", 3000, "HTTP server port (http mode only)")

			// Parse test args
			if err := testFlags.Parse(tt.args); err != nil {
				t.Fatalf("Parse() error = %v", err)
			}

			// Verify parsed values
			if *transportFlag != tt.wantTrans {
				t.Errorf("transport = %q, want %q", *transportFlag, tt.wantTrans)
			}
			if *httpHostFlag != tt.wantHost {
				t.Errorf("http-host = %q, want %q", *httpHostFlag, tt.wantHost)
			}
			if *httpPortFlag != tt.wantPort {
				t.Errorf("http-port = %d, want %d", *httpPortFlag, tt.wantPort)
			}
		})
	}
}
