package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// lullmail-mcp: an agnostic MCP adapter over Lull Mail's HTTP API.
// Any MCP-capable client (Claude Desktop, opencode, anything else) can use
// it; Lull Mail itself has no knowledge of MCP. Configure with:
//
//	LULL_URL              e.g. https://lullmail.com
//	LULL_AGENT_TOKEN      a lull_... token from Settings -> Security
func main() {
	c, err := newClient(os.Getenv("LULL_URL"), os.Getenv("LULL_AGENT_TOKEN"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "lullmail-mcp:", err)
		os.Exit(1)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "lullmail", Version: "0.1.0"}, nil)
	registerTools(server, c)
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
