package main

// Outbound with an undo window (SPEC §6.1): sends sit in-process for a few
// seconds before SMTP submission; DELETE cancels. Five seconds is the whole
// feature — no queue table, no worker, just a timer map.

import (
	"context"
	"encoding/json"
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
}

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
		Subject   string `json:"subject"`
		Text      string `json:"text"`
		HTML      string `json:"html"` // optional rich body; sent as multipart/alternative with Text
		ReplyToID string `json:"reply_to_message_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.To == "" || req.Subject == "" || (req.Text == "" && strings.TrimSpace(req.HTML) == "") {
		writeProblem(w, http.StatusUnprocessableEntity, "Missing Fields", "to, subject and a body (text or html) are required")
		return
	}
	recipient, ok := outboundRecipient(req.To, req.Subject)
	if !ok {
		writeProblem(w, http.StatusUnprocessableEntity, "Invalid Headers", "to must be one email address and headers cannot contain newlines")
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
		deliver, from, ok := a.deliveryFor(r.Context(), mail.AccountID(parentAcct))
		if !ok {
			writeProblem(w, http.StatusPreconditionFailed, "No Send Credential", "cannot send for this account")
			return
		}
		outgoing = mail.ReplyTo(parent, from, req.Text)
		outgoing.HTML = req.HTML
		a.enqueue(w, deliver, outgoing)
		return
	}

	deliver, from, ok := a.deliveryFor(r.Context(), mail.AccountID(mirror))
	if !ok {
		writeProblem(w, http.StatusPreconditionFailed, "No Send Credential", "cannot send for this account")
		return
	}
	outgoing = &mail.Outgoing{
		From:    from,
		To:      []mail.Address{recipient},
		Subject: req.Subject,
		Text:    req.Text,
		HTML:    req.HTML,
	}
	a.enqueue(w, deliver, outgoing)
}

func outboundRecipient(to, subject string) (mail.Address, bool) {
	if strings.ContainsAny(to+subject, "\r\n") {
		return mail.Address{}, false
	}
	parsed, err := netmail.ParseAddress(to)
	if err != nil || parsed.Address == "" {
		return mail.Address{}, false
	}
	return mail.Address{Name: parsed.Name, Email: parsed.Address}, true
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

func (a *App) deliveryFor(ctx context.Context, account mail.AccountID) (deliverFunc, mail.Address, bool) {
	var provider, address string
	if err := a.db.QueryRowContext(ctx, `SELECT provider,address FROM email_accounts WHERE mirror_account_id=$1`, string(account)).Scan(&provider, &address); err != nil {
		return nil, mail.Address{}, false
	}
	if provider == "gmail" || provider == "graph" {
		return func(ctx context.Context, outgoing *mail.Outgoing) error {
			return a.sendOAuth(ctx, provider, string(account), outgoing)
		}, mail.Address{Email: address}, true
	}
	sender, from, ok := a.SMTPFor(ctx, account)
	if !ok {
		return nil, mail.Address{}, false
	}
	return func(ctx context.Context, outgoing *mail.Outgoing) error {
		_, err := sender.Send(ctx, outgoing)
		return err
	}, from, true
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
	if ok {
		delete(a.sendq.sends, id)
	}
	a.sendq.mu.Unlock()
	if !ok {
		writeProblem(w, http.StatusGone, "Too Late", "send already left the building")
		return
	}
	p.cancel()
	writeJSON(w, map[string]any{"cancelled": id})
}
