# feedback loop

This project is intended to be a transparent proxy for mcp servers. This code will run as an mcp server but in front of a real mcp server. It will discover the tools available in the real mcp server and present them from this proxy as if they are part of the proxy. The proxy will transparently log all inbound requests and outbound responses. This proxy must not get in the way of any reqests or responses.

Platform:

Build this proxy mcp server in golang. It must be configured with a claude desktop compliant mcpservers configuration of a real mcp server. 

Use the golang mcp server sdk from here: https://github.com/modelcontextprotocol/go-sdk

Configure and test with the chrome dev tools mcp server:

"chrome-devtools": {
      "command": "npx",
      "args": [
        "-y",
        "chrome-devtools-mcp@latest"
      ]
    }

We will add functionality to this proxy mcp over time. For now we just want a simple functional mcp proxy. This mcp proxy will support http streaming and stdio connections from the client. It will also support the same mechanisms to connect to the 'real' mcp servers.

