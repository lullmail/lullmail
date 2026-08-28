package main

import (
	"context"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerTools wires every tool to the HTTP API. Responses pass through as
// JSON text: the server already emits agent-friendly shapes, and a pass-
// through adapter cannot drift out of sync with field renames.
func registerTools(s *mcp.Server, c *client) {

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_accounts",
		Description: "List connected mailboxes with sync status, message counts, and waiting-screener counts.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return text(c.get(ctx, "/accounts", nil))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "add_account",
		Description: "Connect a mailbox via IMAP or JMAP. Credentials are stored encrypted by the server. " +
			"For Purelymail: provider imap, host imap.purelymail.com, smtp_host smtp.purelymail.com, username = full address. " +
			"Backfill pulls existing mail (default 90 days).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		Provider     string `json:"provider" jsonschema:"imap or jmap (default imap)"`
		Address      string `json:"address" jsonschema:"full email address, e.g. hello@domain.com"`
		Username     string `json:"username" jsonschema:"login username; defaults to the address"`
		Password     string `json:"password" jsonschema:"mailbox password or app password"`
		Host         string `json:"host" jsonschema:"IMAP host, e.g. mail.purelymail.com"`
		Port         int    `json:"port" jsonschema:"IMAP port; default 993"`
		SMTPHost     string `json:"smtp_host" jsonschema:"sending host; default same as host"`
		SMTPPort     int    `json:"smtp_port" jsonschema:"sending port; default 587"`
		Label        string `json:"label" jsonschema:"optional display label"`
		BackfillDays int    `json:"backfill_days" jsonschema:"days of existing mail to import; default 90"`
	}) (*mcp.CallToolResult, any, error) {
		if args.Address == "" || args.Password == "" || args.Host == "" {
			return nil, nil, errArgs("address, password, and host are required")
		}
		return text(c.post(ctx, "/accounts", args))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_account",
		Description: "Disconnect a mailbox and delete its local mirror. Mail at the provider is never touched.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		ID string `json:"id" jsonschema:"account id from list_accounts"`
	}) (*mcp.CallToolResult, any, error) {
		id, err := safeSegment(args.ID)
		if err != nil {
			return nil, nil, errArgs("id must be an account id from list_accounts")
		}
		return text(c.del(ctx, "/accounts/"+id))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "sync_account",
		Description: "Trigger an on-demand sync of one connected mailbox.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		ID string `json:"id" jsonschema:"account id from list_accounts"`
	}) (*mcp.CallToolResult, any, error) {
		id, err := safeSegment(args.ID)
		if err != nil {
			return nil, nil, errArgs("id must be an account id from list_accounts")
		}
		return text(c.post(ctx, "/accounts/"+id+"?op=sync", struct{}{}))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "today_briefing",
		Description: "The owner's daily briefing: what needs a reply, who is being waited on, what arrived quietly.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return text(c.get(ctx, "/briefing", nil))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_bucket",
		Description: "List threads in a bucket view. Buckets: screener (new senders awaiting a decision), " +
			"imbox (allowed in), paper_trail, feed, snoozed, set_aside, later.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		Bucket string `json:"bucket" jsonschema:"screener|imbox|paper_trail|feed|snoozed|set_aside|later"`
	}) (*mcp.CallToolResult, any, error) {
		switch args.Bucket {
		case "screener", "imbox", "paper_trail", "feed", "snoozed", "set_aside", "later":
		default:
			return nil, nil, errArgs("bucket must be one of screener, imbox, paper_trail, feed, snoozed, set_aside, later")
		}
		return text(c.get(ctx, "/buckets/"+args.Bucket, nil))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "search_mail",
		Description: "Search subjects, participants, and previews across every connected mailbox.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		Query string `json:"query" jsonschema:"free-text search"`
	}) (*mcp.CallToolResult, any, error) {
		if args.Query == "" {
			return nil, nil, errArgs("query is required")
		}
		return text(c.get(ctx, "/search", url.Values{"q": {args.Query}}))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "read_thread",
		Description: "Read every message in one thread, oldest first, including bodies.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		ThreadID string `json:"thread_id" jsonschema:"thread id from list_bucket or search_mail"`
	}) (*mcp.CallToolResult, any, error) {
		if args.ThreadID == "" {
			return nil, nil, errArgs("thread_id is required")
		}
		// Thread ids may contain "/" (GitHub notification pattern); the
		// server route is a suffix wildcard, so encoding is the only need.
		return text(c.get(ctx, "/threads/"+url.PathEscape(args.ThreadID), nil))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "send_mail",
		Description: "Send a plain-text email from a connected mailbox. Omit account_id to use the first " +
			"connected account. Reply threading is built server-side from reply_to_message_id.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID    string `json:"account_id,omitempty" jsonschema:"sending account id; default first connected"`
		To           string `json:"to" jsonschema:"recipient address"`
		Subject      string `json:"subject" jsonschema:"subject line"`
		Text         string `json:"text" jsonschema:"plain-text body"`
		ReplyToMsgID string `json:"reply_to_message_id,omitempty" jsonschema:"message id being replied to, if any"`
	}) (*mcp.CallToolResult, any, error) {
		if args.To == "" || args.Subject == "" || args.Text == "" {
			return nil, nil, errArgs("to, subject, and text are required")
		}
		payload := map[string]string{
			"to": args.To, "subject": args.Subject, "text": args.Text,
			"reply_to_message_id": args.ReplyToMsgID,
		}
		if args.AccountID != "" {
			payload["account_id"] = args.AccountID
		}
		return text(c.post(ctx, "/send", payload))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "screener_list",
		Description: "List senders waiting in the Screener for a one-time allow/block decision.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return text(c.get(ctx, "/screener", nil))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "screener_decide",
		Description: "Decide a sender once; every future message follows the rule. allow=true routes to " +
			"inbox (route imbox), reading (paper_trail), or newsletters (feed); allow=false blocks. " +
			"Waiting messages are re-routed immediately.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		Sender string `json:"sender" jsonschema:"sender email address from screener_list"`
		Allow  bool   `json:"allow" jsonschema:"true to allow, false to block"`
		Route  string `json:"route,omitempty" jsonschema:"imbox|paper_trail|feed when allowing; default imbox"`
	}) (*mcp.CallToolResult, any, error) {
		if args.Sender == "" {
			return nil, nil, errArgs("sender is required")
		}
		return text(c.post(ctx, "/screener/decide", map[string]any{
			"sender": args.Sender, "allow": args.Allow, "route": args.Route,
		}))
	})
}

// text turns a (body, err) pair into a pass-through tool result. The API's
// JSON is already the shape an agent wants; re-wrapping it would only drift.
func text(data []byte, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}, nil, nil
}

// errArgs marks client-side validation failures so agents get a fixable
// message instead of a round trip.
func errArgs(message string) error {
	return &apiError{Status: 400, Title: "Invalid Arguments", Detail: message}
}
