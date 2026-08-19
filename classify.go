package main

// HEY mechanics (TASKS 1.4): classify mirror messages into buckets using
// per-sender decisions, expose Screener and bucket views, message actions.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/neutron-build/neutron/mail"
)

// classifyUser assigns a bucket to every unclassified mirror message within
// the account's backfill window. Unknown senders land in the Screener; a
// decision made later re-routes everything still sitting there.
func (a *App) classifyUser(ctx context.Context, uid string) error {
	rows, err := a.db.QueryContext(ctx, `
		SELECT m.account_id, m.id, m.from_addrs
		FROM mail_messages m
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
		LEFT JOIN hey_messages h ON h.message_id = m.id AND h.user_id = $1
		WHERE h.message_id IS NULL
		  AND m.received_at > now() - make_interval(days => ea.backfill_days)`, uid)
	if err != nil {
		return err
	}
	defer rows.Close()

	type pending struct{ acct, id, sender string }
	var batch []pending
	for rows.Next() {
		var acct, id, fromJSON string
		if err := rows.Scan(&acct, &id, &fromJSON); err != nil {
			return err
		}
		batch = append(batch, pending{acct, id, firstSenderEmail(fromJSON)})
	}
	if len(batch) == 0 {
		return nil
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range batch {
		var route string
		var allowed bool
		decided := tx.QueryRowContext(ctx,
			`SELECT route, allowed FROM hey_senders WHERE user_id = $1 AND sender_key = $2`,
			uid, p.sender).Scan(&route, &allowed)
		bucket := "screener"
		if decided == nil && allowed {
			bucket = route
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO hey_messages (user_id, message_id, bucket)
			VALUES ($1, $2, $3) ON CONFLICT (user_id, message_id) DO NOTHING`,
			uid, p.id, bucket); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// firstSenderEmail pulls the first From address out of the mirror's JSON.
// A malformed row classifies by empty sender, which simply stays unscreened
// until decided; envelope archaeology is not worth failing sync over.
func firstSenderEmail(fromJSON string) string {
	if fromJSON == "" {
		return ""
	}
	var addrs []mail.Address
	if err := json.Unmarshal([]byte(fromJSON), &addrs); err != nil || len(addrs) == 0 {
		return ""
	}
	return strings.ToLower(addrs[0].Email)
}

// handleCounts feeds the nav badges: unread per bucket, total for screener.
func (a *App) handleCounts(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	var imbox, screener, feed, paper, aside, later int64
	err = a.db.QueryRowContext(r.Context(), `
		SELECT
		  count(*) FILTER (WHERE h.bucket='imbox' AND h.read_at IS NULL),
		  count(*) FILTER (WHERE h.bucket='screener'),
		  count(*) FILTER (WHERE h.bucket='feed' AND h.read_at IS NULL),
		  count(*) FILTER (WHERE h.bucket='paper_trail' AND h.read_at IS NULL),
		  count(*) FILTER (WHERE h.bucket='set_aside'),
		  count(*) FILTER (WHERE h.bucket='later')
		FROM hey_messages h WHERE h.user_id = $1`, uid,
	).Scan(&imbox, &screener, &feed, &paper, &aside, &later)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"imbox": int(imbox), "screener": int(screener), "feed": int(feed),
		"paper_trail": int(paper), "set_aside": int(aside), "later": int(later),
	})
}

// handleSearch full-text-ish search over the mirror: subject, participants,
// preview. Same row shape as bucket listings so the client reuses rendering.
func (a *App) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, []any{})
		return
	}
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	like := "%" + q + "%"
	rows, err := a.db.QueryContext(r.Context(), `
		SELECT m.thread_id, m.id, m.subject, m.from_addrs, m.received_at,
		       h.read_at IS NOT NULL AS is_read, m.has_attachment, m.preview,
		       COALESCE(h.bucket,''),
		       (SELECT count(*) FROM mail_messages t
		          WHERE t.account_id = m.account_id AND t.thread_id = m.thread_id) AS thread_len
		FROM mail_messages m
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
		LEFT JOIN hey_messages h ON h.message_id = m.id AND h.user_id = $1
		WHERE m.subject ILIKE $2 OR m.from_addrs ILIKE $2 OR m.to_addrs ILIKE $2 OR m.preview ILIKE $2
		ORDER BY m.received_at DESC NULLS LAST
		LIMIT 60`, uid, like)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}
	defer rows.Close()

	type rowOut struct {
		ThreadID   string `json:"thread_id"`
		MessageID  string `json:"message_id"`
		Subject    string `json:"subject"`
		From       string `json:"from"`
		ReceivedAt string `json:"received_at"`
		Read       bool   `json:"read"`
		Attachment bool   `json:"has_attachment"`
		Preview    string `json:"preview"`
		Bucket     string `json:"bucket"`
		ThreadLen  int    `json:"thread_len"`
	}
	out := []rowOut{}
	for rows.Next() {
		var row rowOut
		var fromJSON string
		var received sql.NullTime
		if err := rows.Scan(&row.ThreadID, &row.MessageID, &row.Subject, &fromJSON,
			&received, &row.Read, &row.Attachment, &row.Preview, &row.Bucket, &row.ThreadLen); err != nil {
			writeProblem(w, http.StatusInternalServerError, "Scan Failed", err.Error())
			return
		}
		row.From = firstSenderName(fromJSON)
		if received.Valid {
			row.ReceivedAt = received.Time.Format(time.RFC3339)
		}
		out = append(out, row)
	}
	writeJSON(w, out)
}

// handleScreener lists undecided senders, newest message first.
func (a *App) handleScreener(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `
		SELECT lower(m.from_addrs::json->0->>'email') AS sender,
		       count(*) AS waiting,
		       max(m.received_at) AS newest,
		       max(m.subject) AS sample_subject
		FROM hey_messages h
		JOIN mail_messages m ON m.id = h.message_id
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = h.user_id
		WHERE h.user_id = $1 AND h.bucket = 'screener'
		  AND NOT EXISTS (
		    SELECT 1 FROM hey_senders s
		    WHERE s.user_id = h.user_id
		      AND s.sender_key = lower(m.from_addrs::json->0->>'email'))
		GROUP BY 1
		ORDER BY newest DESC`, uid)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}
	defer rows.Close()

	type senderRow struct {
		Sender  string `json:"sender"`
		Waiting int    `json:"waiting"`
		Newest  string `json:"newest"`
		Sample  string `json:"sample_subject"`
	}
	out := []senderRow{}
	for rows.Next() {
		var row senderRow
		if err := rows.Scan(&row.Sender, &row.Waiting, &row.Newest, &row.Sample); err != nil {
			writeProblem(w, http.StatusInternalServerError, "Scan Failed", err.Error())
			return
		}
		out = append(out, row)
	}
	writeJSON(w, out)
}

// handleDecide records a Screener decision and re-routes everything from
// that sender still sitting in the Screener (HEY semantics: one decision,
// all their mail, past and future).
func (a *App) handleDecide(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Sender string `json:"sender"`
		Allow  bool   `json:"allow"`
		Route  string `json:"route"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	req.Sender = strings.ToLower(strings.TrimSpace(req.Sender))
	if req.Sender == "" {
		writeProblem(w, http.StatusUnprocessableEntity, "Missing Sender", "sender is required")
		return
	}
	if req.Allow {
		switch req.Route {
		case "imbox", "paper_trail", "feed":
		default:
			req.Route = "imbox"
		}
	} else {
		req.Route = "blocked"
	}

	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Begin Failed", err.Error())
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `
		INSERT INTO hey_senders (user_id, sender_key, allowed, route, decided_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (user_id, sender_key)
		DO UPDATE SET allowed = $3, route = $4, decided_at = now()`,
		uid, req.Sender, req.Allow, req.Route); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Decide Failed", err.Error())
		return
	}
	// Re-route their Screener mail. Substring match against the JSON column
	// is deliberately loose (false positives need an email that contains
	// another decided sender's address); tightening needs a generated
	// sender column if it ever matters.
	if _, err := tx.ExecContext(r.Context(), `
		UPDATE hey_messages SET bucket = $3
		WHERE user_id = $1 AND bucket = 'screener' AND message_id IN (
		  SELECT m.id FROM mail_messages m
		  WHERE m.from_addrs LIKE '%' || $2 || '%')`,
		uid, req.Sender, req.Route); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Reroute Failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Commit Failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"sender": req.Sender, "route": req.Route})
}

var bucketNames = map[string]bool{
	"screener": true, "imbox": true, "paper_trail": true, "feed": true,
	"set_aside": true, "later": true,
}

// handleBucket lists threads for one bucket view: latest message per thread.
func (a *App) handleBucket(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	if !bucketNames[bucket] {
		writeProblem(w, http.StatusNotFound, "No Such Bucket", "unknown bucket "+bucket)
		return
	}
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `
		SELECT m.thread_id, m.id, m.subject, m.from_addrs, m.received_at,
		       h.read_at IS NOT NULL AS is_read, m.has_attachment, m.preview,
		       (SELECT count(*) FROM mail_messages t
		          WHERE t.account_id = m.account_id AND t.thread_id = m.thread_id) AS thread_len
		FROM hey_messages h
		JOIN mail_messages m ON m.id = h.message_id
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = h.user_id
		WHERE h.user_id = $1 AND h.bucket = $2
		  AND m.id = (
		    SELECT m2.id FROM hey_messages h2
		    JOIN mail_messages m2 ON m2.id = h2.message_id
		    WHERE h2.user_id = $1 AND h2.bucket = $2
		      AND m2.account_id = m.account_id AND m2.thread_id = m.thread_id
		    ORDER BY m2.received_at DESC NULLS LAST LIMIT 1)
		ORDER BY m.received_at DESC NULLS LAST
		LIMIT 200`, uid, bucket)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}
	defer rows.Close()

	type threadRow struct {
		ThreadID   string `json:"thread_id"`
		MessageID  string `json:"message_id"`
		Subject    string `json:"subject"`
		From       string `json:"from"`
		ReceivedAt string `json:"received_at"`
		Read       bool   `json:"read"`
		Attachment bool   `json:"has_attachment"`
		Preview    string `json:"preview"`
		ThreadLen  int    `json:"thread_len"`
	}
	out := []threadRow{}
	for rows.Next() {
		var row threadRow
		var fromJSON string
		var received sql.NullTime
		if err := rows.Scan(&row.ThreadID, &row.MessageID, &row.Subject, &fromJSON,
			&received, &row.Read, &row.Attachment, &row.Preview, &row.ThreadLen); err != nil {
			writeProblem(w, http.StatusInternalServerError, "Scan Failed", err.Error())
			return
		}
		row.From = firstSenderName(fromJSON)
		if received.Valid {
			row.ReceivedAt = received.Time.Format(time.RFC3339)
		}
		out = append(out, row)
	}
	writeJSON(w, out)
}

// firstSenderName renders "Name <email>" for list rows.
func firstSenderName(fromJSON string) string {
	var addrs []mail.Address
	if err := json.Unmarshal([]byte(fromJSON), &addrs); err != nil || len(addrs) == 0 {
		return ""
	}
	if addrs[0].Name != "" {
		return fmt.Sprintf("%s <%s>", addrs[0].Name, addrs[0].Email)
	}
	return addrs[0].Email
}

// handleThread returns one thread: messages with their hey state.
func (a *App) handleThread(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	thread := r.PathValue("thread")
	rows, err := a.db.QueryContext(r.Context(), `
		SELECT m.id, m.account_id, m.subject, m.from_addrs, m.to_addrs, m.received_at,
		       COALESCE(h.bucket,''), COALESCE(b.text_body,''), COALESCE(b.html_body,'')
		FROM mail_messages m
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
		LEFT JOIN hey_messages h ON h.message_id = m.id AND h.user_id = $1
		LEFT JOIN mail_bodies b ON b.account_id = m.account_id AND b.message_id = m.id
		WHERE m.thread_id = $2
		ORDER BY m.received_at ASC NULLS LAST`, uid, thread)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}
	defer rows.Close()

	type msgRow struct {
		ID         string `json:"id"`
		Account    string `json:"account"`
		Subject    string `json:"subject"`
		From       string `json:"from"`
		To         string `json:"to"`
		ReceivedAt string `json:"received_at"`
		Bucket     string `json:"bucket"`
		Body       string `json:"body"`
		HTML       string `json:"html,omitempty"`
	}
	out := []msgRow{}
	for rows.Next() {
		var row msgRow
		var fromJSON, toJSON string
		var received sql.NullTime
		if err := rows.Scan(&row.ID, &row.Account, &row.Subject, &fromJSON, &toJSON,
			&received, &row.Bucket, &row.Body, &row.HTML); err != nil {
			writeProblem(w, http.StatusInternalServerError, "Scan Failed", err.Error())
			return
		}
		row.From = firstSenderName(fromJSON)
		var to []mail.Address
		if json.Unmarshal([]byte(toJSON), &to) == nil && len(to) > 0 {
			row.To = to[0].Email
		}
		if received.Valid {
			row.ReceivedAt = received.Time.Format(time.RFC3339)
		}
		out = append(out, row)
	}
	if len(out) == 0 {
		writeProblem(w, http.StatusNotFound, "Not Found", "no such thread")
		return
	}
	writeJSON(w, out)
}

// handleMessageAction applies a user action to one message: mark read, move
// bucket, set aside with a return date.
func (a *App) handleMessageAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action    string `json:"action"`
		UntilDays int    `json:"until_days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	msg := r.PathValue("message")

	var q string
	var args []any
	switch req.Action {
	case "read":
		q = `UPDATE hey_messages SET read_at = now() WHERE user_id=$1 AND message_id=$2`
		args = []any{uid, msg}
	case "unread":
		q = `UPDATE hey_messages SET read_at = NULL WHERE user_id=$1 AND message_id=$2`
		args = []any{uid, msg}
	case "imbox", "paper_trail", "feed", "later", "screener":
		q = `UPDATE hey_messages SET bucket=$3 WHERE user_id=$1 AND message_id=$2`
		args = []any{uid, msg, req.Action}
	case "set_aside":
		days := req.UntilDays
		if days == 0 {
			days = 3
		}
		q = `UPDATE hey_messages SET bucket='set_aside', set_aside_until = now() + make_interval(days => $3)
		     WHERE user_id=$1 AND message_id=$2`
		args = []any{uid, msg, days}
	default:
		writeProblem(w, http.StatusUnprocessableEntity, "Unknown Action", req.Action)
		return
	}
	res, err := a.db.ExecContext(r.Context(), q, args...)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Update Failed", err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeProblem(w, http.StatusNotFound, "Not Found", "no such message")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
