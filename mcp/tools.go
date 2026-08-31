package main

import (
	"context"
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"

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
			"Backfill organizes existing mail into the product views (default 90 days; 0 = all history).",
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
		BackfillDays *int   `json:"backfill_days" jsonschema:"days of existing mail to organize; omit for default 90; 0 = all history"`
	}) (*mcp.CallToolResult, any, error) {
		if args.Address == "" || args.Password == "" || args.Host == "" {
			return nil, nil, errArgs("address, password, and host are required")
		}
		return text(c.post(ctx, "/accounts", args))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "set_backfill",
		Description: "Set how much history email-soft organizes for a mailbox: 0 = all history, or days (30/90/365/1095). " +
			"Growing the window classifies older mail into the views; shrinking removes product rows outside the window " +
			"without touching the local mirror or provider mail.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		ID   string `json:"id" jsonschema:"account id from list_accounts"`
		Days int    `json:"days" jsonschema:"0 for all history, or days of history to organize"`
	}) (*mcp.CallToolResult, any, error) {
		id, err := safeSegment(args.ID)
		if err != nil {
			return nil, nil, errArgs("id must be an account id from list_accounts")
		}
		return text(c.postQuery(ctx, "/accounts/"+id, map[string]any{"days": args.Days}, url.Values{"op": {"backfill"}}))
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
		return text(c.postQuery(ctx, "/accounts/"+id, struct{}{}, url.Values{"op": {"sync"}}))
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
		ThreadID  string `json:"thread_id" jsonschema:"thread id from list_bucket or search_mail"`
		AccountID string `json:"account_id" jsonschema:"account id from list_accounts or account from the message row"`
	}) (*mcp.CallToolResult, any, error) {
		if args.ThreadID == "" || args.AccountID == "" {
			return nil, nil, errArgs("thread_id and account_id are required")
		}
		// Thread ids may contain "/" (GitHub notification pattern); the
		// server route is a suffix wildcard, so encoding is the only need.
		return text(c.get(ctx, "/threads/"+url.PathEscape(args.ThreadID), url.Values{"account": {args.AccountID}}))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "send_mail",
		Description: "Send an email from a connected mailbox, plain text or HTML. Omit account_id for a new message to use the first " +
			"connected account. HTML goes out as multipart/alternative with an automatic plain-text fallback. " +
			"Replies require account_id so the parent is unambiguous; threading is built server-side from reply_to_message_id.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID    string `json:"account_id,omitempty" jsonschema:"sending account id; default first connected"`
		To           string `json:"to" jsonschema:"recipient address"`
		Subject      string `json:"subject" jsonschema:"subject line"`
		Text         string `json:"text" jsonschema:"plain-text body; optional when html is given"`
		HTML         string `json:"html,omitempty" jsonschema:"optional HTML body; sent as a rich message with a derived plain-text alternative"`
		ReplyToMsgID string `json:"reply_to_message_id,omitempty" jsonschema:"message id being replied to, if any"`
	}) (*mcp.CallToolResult, any, error) {
		if args.To == "" || args.Subject == "" || (args.Text == "" && args.HTML == "") {
			return nil, nil, errArgs("to, subject, and a body (text or html) are required")
		}
		if args.ReplyToMsgID != "" && args.AccountID == "" {
			return nil, nil, errArgs("account_id is required when replying")
		}
		payload := map[string]string{
			"to": args.To, "subject": args.Subject, "text": args.Text, "html": args.HTML,
			"reply_to_message_id": args.ReplyToMsgID,
		}
		if args.AccountID != "" {
			payload["account_id"] = args.AccountID
		}
		return text(c.post(ctx, "/send", payload))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "undo_send",
		Description: "Cancel a send that is still inside its undo window (the queued id from send_mail).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		OutboxID string `json:"outbox_id" jsonschema:"queued id returned by send_mail"`
	}) (*mcp.CallToolResult, any, error) {
		id, err := safeSegment(args.OutboxID)
		if err != nil {
			return nil, nil, errArgs("outbox_id must be the queued id from send_mail")
		}
		return text(c.del(ctx, "/outbox/"+id))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "message_action",
		Description: "Act on a whole thread: mark read/unread, move it to a bucket (imbox, paper_trail, feed, " +
			"later, screener), or set_aside to snooze with an optional return date in days (default 3). " +
			"Every action is reversible with another call.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID string `json:"account_id" jsonschema:"account id from list_accounts"`
		MessageID string `json:"message_id" jsonschema:"message id from list_bucket, search_mail, or read_thread"`
		Action    string `json:"action" jsonschema:"read|unread|imbox|paper_trail|feed|later|screener|set_aside"`
		UntilDays int    `json:"until_days,omitempty" jsonschema:"set_aside return date in days, 1-3650; default 3"`
	}) (*mcp.CallToolResult, any, error) {
		if args.AccountID == "" || args.MessageID == "" {
			return nil, nil, errArgs("account_id and message_id are required")
		}
		switch args.Action {
		case "read", "unread", "imbox", "paper_trail", "feed", "later", "screener", "set_aside":
		default:
			return nil, nil, errArgs("action must be one of read, unread, imbox, paper_trail, feed, later, screener, set_aside")
		}
		query := url.Values{"account": {args.AccountID}}
		return text(c.postQuery(ctx, "/messages/"+url.PathEscape(args.MessageID)+"/action",
			map[string]any{"action": args.Action, "until_days": args.UntilDays}, query))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_attachment",
		Description: "Fetch one attachment's raw bytes, base64-encoded with its content type. " +
			"Decode before saving or inspecting.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID string `json:"account_id" jsonschema:"account id from list_accounts"`
		MessageID string `json:"message_id" jsonschema:"message id that carries the attachment"`
		Part      string `json:"part" jsonschema:"attachment part id from read_thread"`
	}) (*mcp.CallToolResult, any, error) {
		if args.AccountID == "" || args.MessageID == "" || args.Part == "" {
			return nil, nil, errArgs("account_id, message_id, and part are required")
		}
		query := url.Values{"account": {args.AccountID}}
		return b64(c.get(ctx, "/messages/"+url.PathEscape(args.MessageID)+"/attachment/"+url.PathEscape(args.Part), query))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_eml",
		Description: "Fetch one message's original RFC 5322 bytes, base64-encoded. " +
			"Useful for archival, forensic inspection, or re-importing elsewhere.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID string `json:"account_id" jsonschema:"account id from list_accounts"`
		MessageID string `json:"message_id" jsonschema:"message id"`
	}) (*mcp.CallToolResult, any, error) {
		if args.AccountID == "" || args.MessageID == "" {
			return nil, nil, errArgs("account_id and message_id are required")
		}
		query := url.Values{"account": {args.AccountID}}
		return b64(c.get(ctx, "/messages/"+url.PathEscape(args.MessageID)+"/eml", query))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "screener_list",
		Description: "List senders waiting in the Screener for a one-time allow/block decision.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return text(c.get(ctx, "/screener", nil))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "board_list",
		Description: "The personal kanban: unread Inbox mail as to-dos, answered threads as waiting-on, dated snoozes as deferred, plus pinned cards.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID string `json:"account_id,omitempty" jsonschema:"limit to one mailbox; default all"`
	}) (*mcp.CallToolResult, any, error) {
		query := url.Values{}
		if args.AccountID != "" {
			query.Set("account", args.AccountID)
		}
		return text(c.get(ctx, "/board", query))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "board_pin",
		Description: "Pin a thread to the board as a marker. Pinning never moves the mail itself.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID string `json:"account_id" jsonschema:"account id from list_accounts"`
		ThreadID  string `json:"thread_id" jsonschema:"thread id from list_bucket or search_mail"`
	}) (*mcp.CallToolResult, any, error) {
		if args.AccountID == "" || args.ThreadID == "" {
			return nil, nil, errArgs("account_id and thread_id are required")
		}
		return text(c.post(ctx, "/board/pin", map[string]string{"account": args.AccountID, "thread_id": args.ThreadID}))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "board_unpin",
		Description: "Remove a pinned card (the card_id from board_list).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		CardID string `json:"card_id" jsonschema:"card id from board_list"`
	}) (*mcp.CallToolResult, any, error) {
		if args.CardID == "" {
			return nil, nil, errArgs("card_id is required")
		}
		return text(c.post(ctx, "/board/unpin", map[string]string{"card_id": args.CardID}))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "board_add_card",
		Description: "Add a manual note card to the board — a to-do with no mail behind it.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		Title string `json:"title" jsonschema:"card title"`
		Note  string `json:"note,omitempty" jsonschema:"optional longer note"`
	}) (*mcp.CallToolResult, any, error) {
		if args.Title == "" {
			return nil, nil, errArgs("title is required")
		}
		return text(c.post(ctx, "/board/cards", map[string]string{"title": args.Title, "note": args.Note}))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "board_card_done",
		Description: "Check a card off, or uncheck it. Derived cards (made of mail) resolve by reading instead.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		CardID string `json:"card_id" jsonschema:"card id from board_list"`
		Done   bool   `json:"done" jsonschema:"true to complete, false to reopen"`
	}) (*mcp.CallToolResult, any, error) {
		if args.CardID == "" {
			return nil, nil, errArgs("card_id is required")
		}
		id, err := safeSegment(args.CardID)
		if err != nil {
			return nil, nil, errArgs("card_id must be a card id from board_list")
		}
		return text(c.post(ctx, "/board/cards/"+id+"/done", map[string]bool{"done": args.Done}))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "notes_list",
		Description: "The sticky-note canvas: free thoughts with positions and colours, not tasks.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return text(c.get(ctx, "/notes", nil))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "note_create",
		Description: "Stick a note on the canvas at a position (x,y) with one of five colours (0-4).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		X     int    `json:"x" jsonschema:"canvas x position"`
		Y     int    `json:"y" jsonschema:"canvas y position"`
		Text  string `json:"text" jsonschema:"note text"`
		Color int    `json:"color,omitempty" jsonschema:"colour index 0-4; default 0"`
	}) (*mcp.CallToolResult, any, error) {
		if args.Text == "" {
			return nil, nil, errArgs("text is required")
		}
		return text(c.post(ctx, "/notes", map[string]any{"x": args.X, "y": args.Y, "text": args.Text, "color": args.Color}))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "note_update",
		Description: "Move, recolour, or rewrite a note. Only the fields you pass change.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		ID    string  `json:"id" jsonschema:"note id from notes_list"`
		X     *int    `json:"x,omitempty" jsonschema:"new canvas x position"`
		Y     *int    `json:"y,omitempty" jsonschema:"new canvas y position"`
		Text  *string `json:"text,omitempty" jsonschema:"new note text"`
		Color *int    `json:"color,omitempty" jsonschema:"new colour index 0-4"`
	}) (*mcp.CallToolResult, any, error) {
		if args.ID == "" {
			return nil, nil, errArgs("id is required")
		}
		id, err := safeSegment(args.ID)
		if err != nil {
			return nil, nil, errArgs("id must be a note id from notes_list")
		}
		patch := map[string]any{}
		if args.X != nil {
			patch["x"] = *args.X
		}
		if args.Y != nil {
			patch["y"] = *args.Y
		}
		if args.Text != nil {
			patch["text"] = *args.Text
		}
		if args.Color != nil {
			patch["color"] = *args.Color
		}
		return text(c.post(ctx, "/notes/"+id, patch))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "note_delete",
		Description: "Throw a note away permanently.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		ID string `json:"id" jsonschema:"note id from notes_list"`
	}) (*mcp.CallToolResult, any, error) {
		if args.ID == "" {
			return nil, nil, errArgs("id is required")
		}
		id, err := safeSegment(args.ID)
		if err != nil {
			return nil, nil, errArgs("id must be a note id from notes_list")
		}
		return text(c.del(ctx, "/notes/"+id))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "people_list",
		Description: "Everyone the owner has exchanged mail with, with interaction counts and screener status.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return text(c.get(ctx, "/people", nil))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_mailboxes",
		Description: "The provider folders of one mailbox (INBOX, Sent, Archive...), with local mirror counts.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID string `json:"account_id" jsonschema:"account id from list_accounts"`
	}) (*mcp.CallToolResult, any, error) {
		if args.AccountID == "" {
			return nil, nil, errArgs("account_id is required")
		}
		return text(c.get(ctx, "/mailboxes", url.Values{"account": {args.AccountID}}))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_folder",
		Description: "List threads in a raw provider folder (e.g. Sent, Archive) rather than a product bucket.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		Name      string `json:"name" jsonschema:"folder name from list_mailboxes"`
		AccountID string `json:"account_id" jsonschema:"account id from list_accounts"`
	}) (*mcp.CallToolResult, any, error) {
		if args.Name == "" || args.AccountID == "" {
			return nil, nil, errArgs("name and account_id are required")
		}
		return text(c.get(ctx, "/folder", url.Values{"name": {args.Name}, "account": {args.AccountID}}))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "recent_mail",
		Description: "The most recently received mail across connected mailboxes.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID string `json:"account_id,omitempty" jsonschema:"limit to one mailbox; default all"`
	}) (*mcp.CallToolResult, any, error) {
		query := url.Values{}
		if args.AccountID != "" {
			query.Set("account", args.AccountID)
		}
		return text(c.get(ctx, "/recent", query))
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

	mcp.AddTool(s, &mcp.Tool{
		Name:        "screener_undecide",
		Description: "Return a sender to the Screener — the undo for screener_decide, and the unblock on People.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		Sender string `json:"sender" jsonschema:"sender email address"`
	}) (*mcp.CallToolResult, any, error) {
		if args.Sender == "" {
			return nil, nil, errArgs("sender is required")
		}
		return text(c.post(ctx, "/screener/undecide", map[string]string{"sender": args.Sender}))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_counts",
		Description: "Per-bucket counts (inbox, reading, screener, snoozed) for the nav badges.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID string `json:"account_id,omitempty" jsonschema:"limit to one mailbox; default all"`
	}) (*mcp.CallToolResult, any, error) {
		query := url.Values{}
		if args.AccountID != "" {
			query.Set("account", args.AccountID)
		}
		return text(c.get(ctx, "/counts", query))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "classify_now",
		Description: "Force a classification pass so views reflect the mirror immediately (normally runs after each sync).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return text(c.post(ctx, "/classify", struct{}{}))
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "update_account",
		Description: "Change one mailbox's settings: sync_enabled pauses background sync, retention_days sets how " +
			"long the local mirror keeps mail (0 = forever), backfill_days sets how much history the product " +
			"organizes (0 = all history). Only the fields you pass change.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct {
		AccountID     string `json:"account_id" jsonschema:"account id from list_accounts"`
		SyncEnabled   *bool  `json:"sync_enabled,omitempty" jsonschema:"pause or resume background sync"`
		RetentionDays *int   `json:"retention_days,omitempty" jsonschema:"local mirror retention in days, 0 = forever"`
		BackfillDays  *int   `json:"backfill_days,omitempty" jsonschema:"days of history to organize, 0 = all history"`
	}) (*mcp.CallToolResult, any, error) {
		if args.AccountID == "" {
			return nil, nil, errArgs("account_id is required")
		}
		base := "/accounts/" + url.PathEscape(args.AccountID)
		var results []string
		if args.SyncEnabled != nil {
			out, err := c.postQuery(ctx, base, map[string]bool{"enabled": *args.SyncEnabled}, url.Values{"op": {"sync_enabled"}})
			if err != nil {
				return nil, nil, err
			}
			results = append(results, "sync_enabled: "+string(out))
		}
		if args.RetentionDays != nil {
			out, err := c.postQuery(ctx, base, map[string]int{"days": *args.RetentionDays}, url.Values{"op": {"retention"}})
			if err != nil {
				return nil, nil, err
			}
			results = append(results, "retention: "+string(out))
		}
		if args.BackfillDays != nil {
			out, err := c.postQuery(ctx, base, map[string]int{"days": *args.BackfillDays}, url.Values{"op": {"backfill"}})
			if err != nil {
				return nil, nil, err
			}
			results = append(results, "backfill: "+string(out))
		}
		if len(results) == 0 {
			return nil, nil, errArgs("pass at least one of sync_enabled, retention_days, backfill_days")
		}
		return text([]byte(strings.Join(results, "\n")), nil)
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

// b64 wraps binary bytes (attachments, EML) as base64 JSON so they survive
// the text-only tool channel without mojibake.
func b64(data []byte, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return nil, nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: `{"encoding":"base64","bytes":` +
			strconv.Itoa(len(data)) + `,"data":"` + encoded + `"}`}},
	}, nil, nil
}

// errArgs marks client-side validation failures so agents get a fixable
// message instead of a round trip.
func errArgs(message string) error {
	return &apiError{Status: 400, Title: "Invalid Arguments", Detail: message}
}
