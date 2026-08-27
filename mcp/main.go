package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// email-soft-mcp: an agnostic MCP adapter over email-soft's HTTP API.
// Any MCP-capable client (Claude Desktop, opencode, anything else) can use
// it; email-soft itself has no knowledge of MCP. Configure with:
//
//	EMAILSOFT_URL           e.g. https://mail.example.com
//	EMAILSOFT_AGENT_TOKEN   an es_... token from Settings -> Security
func main() {
	c, err := newClient(os.Getenv("EMAILSOFT_URL"), os.Getenv("EMAILSOFT_AGENT_TOKEN"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "email-soft-mcp:", err)
		os.Exit(1)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "email-soft", Version: "0.1.0"}, nil)
	registerTools(server, c)
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
