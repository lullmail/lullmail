package main

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"testing"
)

func TestThreadToolsCarryPageAndBodyIdentity(t *testing.T) {
	c, seen, httpServer := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"rows":[],"has_more":false}`))
	})
	defer httpServer.Close()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	registerTools(server, c)
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	sdk := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := sdk.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	for _, call := range []*mcp.CallToolParams{{Name: "read_thread", Arguments: map[string]any{"thread_id": "thread/with/slash", "account_id": "a", "limit": 12, "cursor": "next"}}, {Name: "read_message", Arguments: map[string]any{"message_id": "message-id", "account_id": "a"}}} {
		res, err := cs.CallTool(ctx, call)
		if err != nil || res.IsError {
			t.Fatalf("call: %v %v", res, err)
		}
	}
	if len(*seen) != 2 {
		t.Fatal(*seen)
	}
	if (*seen)[0]["query"] != "account=a&cursor=next&limit=12&page=1" {
		t.Fatal((*seen)[0])
	}
	if (*seen)[1]["path"] != "/api/messages/message-id/body" || (*seen)[1]["query"] != "account=a" {
		t.Fatal((*seen)[1])
	}
}
