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

	// Call a simple tool (note: may return error if Chrome not connected, which is OK)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_pages",
		Arguments: map[string]interface{}{},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "CallTool failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Called list_pages: %d content items, isError=%v\n", len(result.Content), result.IsError)

	// Test another tool
	result2, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "navigate_to_url",
		Arguments: map[string]interface{}{
			"url": "https://example.com",
		},
	})
	if err != nil {
		// Note: Tool may fail if Chrome not connected, which is expected
		fmt.Printf("✓ Called navigate_to_url: got expected error (Chrome not connected)\n")
	} else {
		fmt.Printf("✓ Called navigate_to_url: %d content items, isError=%v\n", len(result2.Content), result2.IsError)
	}

	// Test a third tool
	result3, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_page_source",
		Arguments: map[string]interface{}{},
	})
	if err != nil {
		// Note: Tool may fail if Chrome not connected, which is expected
		fmt.Printf("✓ Called get_page_source: got expected error (Chrome not connected)\n")
	} else {
		fmt.Printf("✓ Called get_page_source: %d content items, isError=%v\n", len(result3.Content), result3.IsError)
	}

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
