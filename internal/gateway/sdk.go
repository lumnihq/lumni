package gateway

import "github.com/modelcontextprotocol/go-sdk/mcp"

// Implementation identifies the gateway during MCP protocol negotiation. The
// proxy keeps unknown JSON-RPC methods wire-transparent while the official SDK
// supplies the versioned protocol identity and compatibility surface.
func Implementation() *mcp.Implementation {
	return &mcp.Implementation{Name: "lumni-gateway", Version: "0.1.0"}
}
