package main

// Outbound with an undo window (SPEC §6.1): sends sit in-process for a few
// seconds before SMTP submission; DELETE cancels. Five seconds is the whole
// feature — no queue table, no worker, just a timer map.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	netmail "net/mail"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/neutron-build/neutron/mail"
)

const undoWindow = 5 * time.Second

type pendingSend struct {
	cancel context.CancelFunc
	done   <-chan error
	state  sendState
}

type sendState uint8

const (
	sendPending sendState = iota
	sendDelivering
)

type sendQueue struct {
	mu    sync.Mutex
	sends map[string]*pendingSend
}

type deliverFunc func(context.Context, *mail.Outgoing) error

func newSendQueue() *sendQueue {
	return &sendQueue{sends: map[string]*pendingSend{}}
}

func (a *App) handleSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AccountID string `json:"account_id"` // email_accounts.id; empty = first
		To        string `json:"to"`
		Cc        string `json:"cc"`
		Bcc       string `json:"bcc"`
		Subject   string `json:"subject"`
		Text      string `json:"text"`
		HTML      string `json:"html"` // optional rich body; sent as multipart/alternative with Text
		ReplyToID string `json:"reply_to_message_id"`

		Attachments []sendAttachmentRequest `json:"attachments"`
	}
	// Attachments ride inside the JSON body, so the read cap has to cover
	// them at their wire size: base64 grows the decoded bytes by 4/3, so
	// the advertised 25 MiB total alone arrives as ~33.4 MiB of payload.
	// 34 MiB covers that plus the envelope; past it the request is refused
	// as too large, not reported as malformed JSON.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 34<<20)).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeProblem(w, http.StatusRequestEntityTooLarge, "Request Too Large",
				fmt.Sprintf("request exceeds the %d MiB cap; attachments are capped at 25 MiB decoded in total", maxErr.Limit>>20))
			return
		}
		writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.To == "" || req.Subject == "" || (req.Text == "" && strings.TrimSpace(req.HTML) == "") {
		writeProblem(w, http.StatusUnprocessableEntity, "Missing Fields", "to, subject and a body (text or html) are required")
		return
	}
	toAddrs, ok := outboundRecipients(req.To)
	if !ok || len(toAddrs) == 0 || strings.ContainsAny(req.Subject, "\r\n") {
		writeProblem(w, http.StatusUnprocessableEntity, "Invalid Headers", "to must be a comma-separated address list and headers cannot contain newlines")
		return
	}
	ccAddrs, ok := outboundRecipients(req.Cc)
	if !ok {
		writeProblem(w, http.StatusUnprocessableEntity, "Invalid Headers", "cc must be a comma-separated address list")
		return
	}
	bccAddrs, ok := outboundRecipients(req.Bcc)
	if !ok {
		writeProblem(w, http.StatusUnprocessableEntity, "Invalid Headers", "bcc must be a comma-separated address list")
		return
	}
	attachments, problem := decodeAttachments(req.Attachments, 25<<20)
	if problem != "" {
		writeProblem(w, http.StatusUnprocessableEntity, "Attachment Rejected", problem)
		return
	}
	if req.ReplyToID != "" && req.AccountID == "" {
		writeProblem(w, http.StatusUnprocessableEntity, "Missing Account", "account_id is required when replying")
		return
	}
	if strings.ContainsAny(strings.TrimSpace(req.HTML), "\r") {
		req.HTML = strings.ReplaceAll(strings.ReplaceAll(req.HTML, "\r\n", "\n"), "\r", "\n")
	}
	if req.Text == "" {
		req.Text = htmlFallbackText(req.HTML)
	}
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	var mirror string
	q := `SELECT mirror_account_id FROM email_accounts WHERE user_id = $1`
	args := []any{uid}
	if req.AccountID != "" {
		q += ` AND (id::text = $2 OR mirror_account_id = $2)`
		args = append(args, req.AccountID)
	}
	q += ` ORDER BY created_at LIMIT 1`
	if err := a.db.QueryRowContext(r.Context(), q, args...).Scan(&mirror); err != nil {
		writeProblem(w, http.StatusPreconditionFailed, "No Account", "connect an account first")
		return
	}

	// Resolve the reply parent from the mirror so threading headers are
	// built from the stored message, never trusted from the client. The
	// parent may live on any of the user's accounts — a reply must go out
	// through the account the thread belongs to, not whichever connected
	// first.
	var outgoing *mail.Outgoing
	if req.ReplyToID != "" {
		var parentAcct string
		err := a.db.QueryRowContext(r.Context(), `
			SELECT m.account_id FROM mail_messages m
			JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
			WHERE m.id = $2 AND (ea.id::text = $3 OR m.account_id = $3)`,
			uid, req.ReplyToID, req.AccountID).Scan(&parentAcct)
		if err != nil {
			writeProblem(w, http.StatusNotFound, "Parent Not Found", "reply_to_message_id does not resolve")
			return
		}
		parent, err := a.store.Envelope(r.Context(), mail.AccountID(parentAcct), mail.MessageID(req.ReplyToID))
		if err != nil {
			writeProblem(w, http.StatusNotFound, "Parent Not Found", "reply_to_message_id does not resolve")
			return
		}
		deliver, from, ok := a.deliveryFor(r.Context(), mail.AccountID(parentAcct), req.ReplyToID)
		if !ok {
			writeProblem(w, http.StatusPreconditionFailed, "No Send Credential", "cannot send for this account")
			return
		}
		outgoing = mail.ReplyTo(parent, from, req.Text)
		outgoing.HTML = req.HTML
		outgoing.Cc = ccAddrs
		outgoing.Bcc = bccAddrs
		outgoing.Attachments = attachments
		if len(toAddrs) > 0 {
			outgoing.To = toAddrs
		}
		if strings.TrimSpace(req.Subject) != "" {
			outgoing.Subject = req.Subject
		}
		a.enqueue(w, deliver, outgoing)
		return
	}

	deliver, from, ok := a.deliveryFor(r.Context(), mail.AccountID(mirror), "")
	if !ok {
		writeProblem(w, http.StatusPreconditionFailed, "No Send Credential", "cannot send for this account")
		return
	}
	outgoing = &mail.Outgoing{
		From:        from,
		To:          toAddrs,
		Cc:          ccAddrs,
		Bcc:         bccAddrs,
		Subject:     req.Subject,
		Text:        req.Text,
		HTML:        req.HTML,
		Attachments: attachments,
	}
	a.enqueue(w, deliver, outgoing)
}

type sendAttachmentRequest struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	DataB64     string `json:"data_base64"`
}

// decodeAttachments turns JSON-carried base64 attachments into engine
// attachments with hard caps: one file at most 15 MiB decoded, the set at
// most totalMiB. The engine sanitizes header values; these caps exist so a
// fat request cannot balloon server memory before composition.
func decodeAttachments(reqs []sendAttachmentRequest, totalBytes int) ([]mail.Attachment, string) {
	if len(reqs) == 0 {
		return nil, ""
	}
	if len(reqs) > 20 {
		return nil, "at most 20 attachments per message"
	}
	out := make([]mail.Attachment, 0, len(reqs))
	var total int
	for i, ra := range reqs {
		if ra.DataB64 == "" {
			return nil, fmt.Sprintf("attachment %d has no data", i+1)
		}
		data, err := base64.StdEncoding.DecodeString(ra.DataB64)
		if err != nil {
			return nil, fmt.Sprintf("attachment %d is not valid base64", i+1)
		}
		if len(data) > 15<<20 {
			return nil, fmt.Sprintf("attachment %d exceeds 15 MiB", i+1)
		}
		total += len(data)
		if total > totalBytes {
			return nil, "attachments exceed the 25 MiB total"
		}
		out = append(out, mail.Attachment{Filename: ra.Filename, ContentType: ra.ContentType, Data: data})
	}
	return out, ""
}

func outboundRecipients(list string) ([]mail.Address, bool) {
	if strings.ContainsAny(list, "\r\n") {
		return nil, false
	}
	list = strings.TrimSpace(list)
	if list == "" {
		return nil, true
	}
	parsed, err := netmail.ParseAddressList(list)
	if err != nil {
		return nil, false
	}
	out := make([]mail.Address, 0, len(parsed))
	for _, p := range parsed {
		if p.Address == "" {
			return nil, false
		}
		out = append(out, mail.Address{Name: p.Name, Email: p.Address})
	}
	return out, true
}

// htmlFallbackText derives the plain-text alternative for an HTML-only
// composition. It is a readability fallback, not a faithful conversion:
// block-level tags become line breaks, list items keep a bullet, entities
// decode, and the rest of the markup drops away so plain-text clients never
// see tags.
var htmlBreakRe = regexp.MustCompile(`(?i)<br\s*/?>|</?p[^>]*>|</div>|</tr>|</li>|<li>`)
var htmlCollapseRe = regexp.MustCompile(`\n{3,}`)

func htmlFallbackText(htmlBody string) string {
	text := htmlBreakRe.ReplaceAllStringFunc(htmlBody, func(tag string) string {
		if strings.EqualFold(tag, "<li>") {
			return "- "
		}
		return "\n"
	})
	text = stripTags(text)
	text = html.UnescapeString(text)
	text = htmlCollapseRe.ReplaceAllString(text, "\n\n")
	lines := strings.Split(text, "\n")
	var out []string
	for _, line := range lines {
		out = append(out, strings.TrimRight(line, " \t"))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func stripTags(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// deliveryFor resolves an account to its outbound sender: same host as
// IMAP, port 587 STARTTLS, unless the account overrides it. replyParent is
// the mirror message id being answered ("" for fresh sends); OAuth
// providers that cannot set threading headers on a flat send need it.
func (a *App) deliveryFor(ctx context.Context, account mail.AccountID, replyParent string) (deliverFunc, mail.Address, bool) {
	var provider, address string
	if err := a.db.QueryRowContext(ctx, `SELECT provider,address FROM email_accounts WHERE mirror_account_id=$1`, string(account)).Scan(&provider, &address); err != nil {
		return nil, mail.Address{}, false
	}
	if provider == "gmail" || provider == "graph" {
		return func(ctx context.Context, outgoing *mail.Outgoing) error {
			return a.sendOAuth(ctx, provider, string(account), outgoing, replyParent)
		}, mail.Address{Email: address}, true
	}
	sender, from, ok := a.SMTPFor(ctx, account)
	if !ok {
		return nil, mail.Address{}, false
	}
	return func(ctx context.Context, outgoing *mail.Outgoing) error {
		_, raw, err := sender.Send(ctx, outgoing)
		if err != nil {
			return err
		}
		a.fileSent(ctx, account, raw)
		return nil
	}, from, true
}

// fileSent appends the exact submitted bytes to the provider's Sent mailbox.
// Without it, mail sent from here never reaches the mirror, so anyone the
// owner writes to first stays an unknown sender and keeps hitting the
// Screener. Gmail and Graph never come down this path — their send APIs keep
// the sent copy themselves. Filing is best-effort: the message is already
// delivered, so a failure here is logged, never surfaced as a send failure.
func (a *App) fileSent(ctx context.Context, account mail.AccountID, raw []byte) {
	var box string
	err := a.db.QueryRowContext(ctx,
		`SELECT id FROM mail_mailboxes WHERE account_id=$1 AND role='sent' LIMIT 1`,
		string(account)).Scan(&box)
	if err != nil {
		a.log.Warn("sent copy not filed: no sent mailbox known for account", "account", account, "err", err)
		return
	}
	cred, err := a.Token(ctx, account)
	if err != nil {
		a.log.Warn("sent copy not filed: credential unavailable", "account", account, "err", err)
		return
	}
	adapter, release, err := a.accountResolver()(ctx, account, cred)
	if err != nil {
		a.log.Warn("sent copy not filed: connect failed", "account", account, "err", err)
		return
	}
	defer release()
	appender, ok := adapter.(mail.Appender)
	if !ok {
		return
	}
	if err := appender.Append(ctx, mail.MailboxID(box), raw); err != nil {
		a.log.Warn("sent copy not filed", "account", account, "mailbox", box, "err", err)
		return
	}
	// The mirror only sees the appended copy when the mailbox next syncs;
	// wake the scheduler so the thread completes within seconds.
	a.sched.Wake(account)
}

func (a *App) enqueue(w http.ResponseWriter, deliver deliverFunc, outgoing *mail.Outgoing) {
	a.sendq.mu.Lock()
	id := time.Now().Format("150405.000") + "-" + newID()[:6]
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	done := make(chan error, 1)
	a.sendq.sends[id] = &pendingSend{cancel: cancel, done: done}
	a.sendq.mu.Unlock()

	go func() {
		defer func() {
			a.sendq.mu.Lock()
			delete(a.sendq.sends, id)
			a.sendq.mu.Unlock()
			cancel()
		}()
		select {
		case <-ctx.Done():
			done <- ctx.Err()
			return
		case <-time.After(undoWindow):
		}
		a.sendq.mu.Lock()
		p, ok := a.sendq.sends[id]
		if ok && p.state == sendPending {
			p.state = sendDelivering
		}
		a.sendq.mu.Unlock()
		if !ok {
			return
		}
		err := deliver(ctx, outgoing)
		if err != nil {
			a.log.Error("send failed", "err", err)
		}
		done <- err
	}()

	writeJSON(w, map[string]any{"queued": id, "undo_seconds": int(undoWindow.Seconds())})
}

func (a *App) handleUndoSend(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a.sendq.mu.Lock()
	p, ok := a.sendq.sends[id]
	if ok && p.state == sendPending {
		delete(a.sendq.sends, id)
	} else {
		ok = false
	}
	a.sendq.mu.Unlock()
	if !ok {
		writeProblem(w, http.StatusGone, "Too Late", "send already left the building")
		return
	}
	p.cancel()
	writeJSON(w, map[string]any{"cancelled": id})
}
