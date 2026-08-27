package main

// HEY mechanics (TASKS 1.4): classify mirror messages into buckets using
// per-sender decisions, expose Screener and bucket views, message actions.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/neutron-build/neutron/mail"
)

// classifyUser assigns a bucket to every unclassified mirror message within
// the account's backfill window. The Screener is a forward-looking gate, not
// a chore: mail that arrived before the mailbox was connected is history —
// correspondents go to the Inbox, everything else files to Receipts — and
// only mail arriving after connection screens. Senders the owner has already
// emailed skip the Screener entirely; replying to someone is a decision.
func (a *App) classifyUser(ctx context.Context, uid string) error {
	rows, err := a.db.QueryContext(ctx, `
		SELECT m.account_id, m.id, COALESCE(m.from_addrs, ''), m.received_at, ea.created_at
		FROM mail_messages m
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
		LEFT JOIN hey_messages h ON h.message_id = m.id AND h.user_id = $1
		WHERE h.message_id IS NULL
		  AND (m.received_at AT TIME ZONE 'UTC') > now() - make_interval(days => ea.backfill_days)`, uid)
	if err != nil {
		return err
	}
	defer rows.Close()

	// Correspondents: people the owner has already exchanged mail with —
	// explicit recipients of user-sent messages, plus anyone sharing a
	// thread with one (the mirror's to_addrs is spotty on sent mail, but
	// thread_id is reliable). Backfill makes this load-bearing: an imported
	// mailbox should not re-screen years of existing conversation partners.
	correspondents := map[string]bool{}
	collect := func(query string) error {
		cr, err := a.db.QueryContext(ctx, query, uid)
		if err != nil {
			return err
		}
		defer cr.Close()
		for cr.Next() {
			var addr sql.NullString
			if cr.Scan(&addr) == nil && addr.Valid && addr.String != "" {
				correspondents[addr.String] = true
			}
		}
		return cr.Err()
	}
	// arrayOf guards json_array_elements against the mirror occasionally
	// storing a JSON scalar in the address columns.
	arrayOf := func(col string) string {
		return `CASE WHEN json_typeof(COALESCE(` + col + `, '[]')::json) = 'array'
		       THEN COALESCE(` + col + `, '[]')::json ELSE '[]'::json END`
	}
	if err := collect(`
		SELECT DISTINCT lower(t->>'email')
		FROM mail_messages m
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
		CROSS JOIN LATERAL json_array_elements(` + arrayOf("m.to_addrs") + `) t
		WHERE lower(COALESCE(m.from_addrs, '[]')::json->0->>'email') = lower(ea.address)`); err != nil {
		a.log.Error("correspondent extraction (recipients) failed", "err", err)
	}
	if err := collect(`
		SELECT DISTINCT lower(t->>'email')
		FROM mail_messages m
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
		CROSS JOIN LATERAL json_array_elements(` + arrayOf("m.cc_addrs") + `) t
		WHERE lower(COALESCE(m.from_addrs, '[]')::json->0->>'email') = lower(ea.address)`); err != nil {
		a.log.Error("correspondent extraction (cc) failed", "err", err)
	}
	if err := collect(`
		SELECT DISTINCT lower(COALESCE(other.from_addrs, '[]')::json->0->>'email')
		FROM mail_messages mine
		JOIN email_accounts ea ON ea.mirror_account_id = mine.account_id AND ea.user_id = $1
		JOIN mail_messages other ON other.account_id = mine.account_id AND other.thread_id = mine.thread_id
		WHERE mine.thread_id IS NOT NULL
		  AND lower(COALESCE(mine.from_addrs, '[]')::json->0->>'email') = lower(ea.address)
		  AND lower(COALESCE(other.from_addrs, '[]')::json->0->>'email') <> lower(ea.address)
		  AND COALESCE(other.from_addrs, '') <> ''`); err != nil {
		a.log.Error("correspondent extraction (threads) failed", "err", err)
	}

	type pending struct {
		acct, id, sender string
		historical       bool
	}
	var batch []pending
	for rows.Next() {
		var acct, id, fromJSON string
		var received, connected sql.NullTime
		if err := rows.Scan(&acct, &id, &fromJSON, &received, &connected); err != nil {
			return err
		}
		// A NULL INTERNALDATE must not wedge the whole pass; undated mail is
		// treated as new (it screens like any unknown sender).
		batch = append(batch, pending{
			acct: acct, id: id, sender: firstSenderEmail(fromJSON),
			historical: received.Valid && connected.Valid && received.Time.Before(connected.Time),
		})
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
		bucket := classifySender(decided == nil, allowed, route, correspondents[p.sender], p.historical)
		// Broken envelopes (no parsable From) can never be decided or shown;
		// they file to Receipts instead of inflating the Screener forever.
		if p.sender == "" {
			bucket = "paper_trail"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO hey_messages (user_id, message_id, bucket)
			VALUES ($1, $2, $3) ON CONFLICT (user_id, message_id) DO NOTHING`,
			uid, p.id, bucket); err != nil {
			return err
		}
	}
	// Self-heal the decide-vs-classify race: a decision committed while this
	// batch was being read parks mail in 'screener' that the re-route already
	// missed. Re-running the re-route here closes the window.
	if _, err := tx.ExecContext(ctx, `
		UPDATE hey_messages h SET bucket = CASE WHEN s.allowed THEN s.route ELSE 'dropped' END
		FROM hey_senders s, mail_messages m
		WHERE h.user_id = $1 AND h.bucket = 'screener'
		  AND h.message_id = m.id AND s.user_id = $1 AND s.sender_key <> ''
		  AND s.sender_key = lower(COALESCE(m.from_addrs, '[]')::json->0->>'email')`, uid); err != nil {
		return err
	}
	// Malformed-From mail parked in 'screener' before the rule above existed
	// also moves to Receipts, so the badge stops counting the unshowable.
	if _, err := tx.ExecContext(ctx, `
		UPDATE hey_messages h SET bucket = 'paper_trail'
		FROM mail_messages m
		WHERE h.user_id = $1 AND h.bucket = 'screener' AND h.message_id = m.id
		  AND (m.from_addrs IS NULL
		       OR json_typeof(COALESCE(m.from_addrs, '[]')::json) <> 'array'
		       OR COALESCE(m.from_addrs, '[]')::json->0->>'email' IS NULL
		       OR lower(COALESCE(m.from_addrs, '[]')::json->0->>'email') = '')`, uid); err != nil {
		return err
	}
	return tx.Commit()
}

// classifySender is the one place the routing rules are written. Precedence:
// an explicit decision always wins; then correspondence (replying to someone
// is a decision); then the history rule — mail that predates the mailbox's
// connection is reference, not a decision queue, so it files to Receipts.
// Only mail arriving after connection screens.
func classifySender(decided, allowed bool, route string, correspondent, historical bool) string {
	switch {
	case decided:
		if allowed {
			return route
		}
		// Blocked senders park in 'dropped' — outside every view AND outside
		// the screener count, so the badge never shows invisible mail.
		return "dropped"
	case correspondent:
		return "imbox"
	case historical:
		return "paper_trail"
	default:
		return "screener"
	}
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
	// Counts feed the nav numerals; sweep first so a returned snooze stops
	// counting as snoozed the moment anything asks.
	if err := a.sweepSnoozed(r.Context(), uid); err != nil {
		a.log.Error("counts sweep failed", "err", err)
	}
	var imbox, screener, feed, paper, aside, later int64
	countsQuery := `
		SELECT
		  count(*) FILTER (WHERE h.bucket='imbox' AND h.read_at IS NULL),
		  count(*) FILTER (WHERE h.bucket='screener'),
		  count(*) FILTER (WHERE h.bucket='feed' AND h.read_at IS NULL),
		  count(*) FILTER (WHERE h.bucket='paper_trail' AND h.read_at IS NULL),
		  count(*) FILTER (WHERE h.bucket='set_aside'),
		  count(*) FILTER (WHERE h.bucket='later')
		FROM hey_messages h
		JOIN mail_messages m ON m.id = h.message_id
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
		WHERE h.user_id = $1`
	countsArgs := []any{uid}
	if account := r.URL.Query().Get("account"); account != "" {
		countsQuery += ` AND ea.id = $2`
		countsArgs = append(countsArgs, account)
	}
	err = a.db.QueryRowContext(r.Context(), countsQuery, countsArgs...).
		Scan(&imbox, &screener, &feed, &paper, &aside, &later)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"imbox": int(imbox), "screener": int(screener), "feed": int(feed),
		"paper_trail": int(paper), "set_aside": int(aside), "later": int(later),
		"snoozed": int(aside + later),
	})
}

// accountClause is the ?account= lens for queries that already join
// email_accounts as ea: empty (all mailboxes) or narrowed to one. The value
// must be a uuid or the clause is dropped — interpolation stays safe by
// construction rather than by escaping discipline.
func accountClause(r *http.Request) string {
	account := r.URL.Query().Get("account")
	if !isUUID(account) {
		return ""
	}
	return ` AND ea.id = '` + account + `'`
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
	}
	return true
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
		SELECT m.thread_id, m.id, COALESCE(m.subject,''), COALESCE(m.from_addrs,'[]'), m.received_at,
		       h.read_at IS NOT NULL AS is_read, m.has_attachment, COALESCE(m.preview,''),
		       COALESCE(h.bucket,''),
		       (SELECT count(*) FROM mail_messages t
		          WHERE t.account_id = m.account_id AND t.thread_id = m.thread_id) AS thread_len
		FROM mail_messages m
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
		LEFT JOIN hey_messages h ON h.message_id = m.id AND h.user_id = $1
		WHERE (m.subject ILIKE $2 OR m.from_addrs ILIKE $2 OR m.to_addrs ILIKE $2 OR m.preview ILIKE $2)`+
		accountClause(r)+
		` ORDER BY m.received_at DESC NULLS LAST
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
		SELECT COALESCE(lower(m.from_addrs::json->0->>'email'), '') AS sender,
		       count(*) AS waiting,
		       COALESCE(max(m.received_at)::text, '') AS newest,
		       COALESCE(max(m.subject), '') AS sample_subject
		FROM hey_messages h
		JOIN mail_messages m ON m.id = h.message_id
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = h.user_id
		WHERE h.user_id = $1 AND h.bucket = 'screener'
		  AND json_typeof(m.from_addrs::json) = 'array'
		  AND m.from_addrs::json->0->>'email' IS NOT NULL
		  AND NOT EXISTS (
		    SELECT 1 FROM hey_senders s
		    WHERE s.user_id = h.user_id
		      AND s.sender_key = lower(m.from_addrs::json->0->>'email'))`+
		accountClause(r)+
		` GROUP BY 1
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
	// Re-route their Screener mail. Exact first-From match: a substring LIKE
	// here made blocking a@x.com also swallow not-a@x.com.
	parked := req.Route
	if !req.Allow {
		parked = "dropped"
	}
	if _, err := tx.ExecContext(r.Context(), `
		UPDATE hey_messages SET bucket = $3
		WHERE user_id = $1 AND bucket = 'screener' AND message_id IN (
		  SELECT m.id FROM mail_messages m
		  WHERE lower(COALESCE(m.from_addrs, '[]')::json->0->>'email') = $2)`,
		uid, req.Sender, parked); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Reroute Failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Commit Failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"sender": req.Sender, "route": req.Route})
}

// handleUndecide reverses a Screener decision: the sender's rule is dropped and
// every message routed by it returns to the Screener. Without this a mis-click
// is permanent -- there is no other path back from "blocked", which makes the
// four Screener buttons far riskier than they look.
func (a *App) handleUndecide(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Sender string `json:"sender"`
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
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	// The recall set is the buckets the decision itself placed (its route, or
	// 'dropped' for blocked) plus anything still sitting in the Screener —
	// mail the user filed by hand afterwards keeps the bucket they chose.
	var prevRoute string
	var prevAllowed bool
	err = a.db.QueryRowContext(r.Context(),
		`SELECT route, allowed FROM hey_senders WHERE user_id = $1 AND sender_key = $2`,
		uid, req.Sender).Scan(&prevRoute, &prevAllowed)
	if err == sql.ErrNoRows {
		writeJSON(w, map[string]any{"sender": req.Sender, "route": "screener"})
		return
	}
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	wasParked := prevRoute
	if !prevAllowed {
		wasParked = "dropped"
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Begin Failed", err.Error())
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(),
		`DELETE FROM hey_senders WHERE user_id = $1 AND sender_key = $2`,
		uid, req.Sender); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Undecide Failed", err.Error())
		return
	}
	if _, err := tx.ExecContext(r.Context(), `
		UPDATE hey_messages SET bucket = 'screener'
		WHERE user_id = $1 AND bucket = ANY($3) AND message_id IN (
		  SELECT m.id FROM mail_messages m
		  WHERE lower(COALESCE(m.from_addrs, '[]')::json->0->>'email') = $2)`,
		uid, req.Sender, []string{"screener", wasParked}); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Reroute Failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Commit Failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"sender": req.Sender, "route": "screener"})
}

// Listable buckets, mapped to the underlying hey_messages.bucket values.
//
// "snoozed" is the merge of set_aside and later: deferring mail is one idea, and
// whether it comes back on a date or someday is an attribute of the deferral,
// not a different place to keep it. The two storage values stay distinct so a
// dated snooze can still be swept back (TASKS 1.4) without a migration.
var bucketNames = map[string][]string{
	"screener":    {"screener"},
	"imbox":       {"imbox"},
	"paper_trail": {"paper_trail"},
	"feed":        {"feed"},
	"snoozed":     {"set_aside", "later"},
	"set_aside":   {"set_aside"},
	"later":       {"later"},
}

// handleBucket lists threads for one bucket view: latest message per thread.
func (a *App) handleBucket(w http.ResponseWriter, r *http.Request) {
	buckets, ok := bucketNames[r.PathValue("bucket")]
	if !ok {
		writeProblem(w, http.StatusNotFound, "No Such Bucket", "unknown bucket "+r.PathValue("bucket"))
		return
	}
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	// The per-mailbox lens: ?account=<email_accounts.id> narrows any list to
	// one mailbox. Empty means all mailboxes — the default unified view.
	account := r.URL.Query().Get("account")
	// The Snoozed list (and the calendar fed by it) must never show a return
	// date already past — sweep before listing.
	if r.PathValue("bucket") == "snoozed" {
		if err := a.sweepSnoozed(r.Context(), uid); err != nil {
			a.log.Error("bucket sweep failed", "err", err)
		}
	}
	query := `
		SELECT m.thread_id, m.id, COALESCE(m.subject,''), COALESCE(m.from_addrs,'[]'), m.received_at,
		       h.read_at IS NOT NULL AS is_read, m.has_attachment, COALESCE(m.preview,''), h.bucket,
		       h.set_aside_until,
		       (SELECT count(*) FROM mail_messages t
	          WHERE t.account_id = m.account_id AND t.thread_id = m.thread_id) AS thread_len
		FROM hey_messages h
		JOIN mail_messages m ON m.id = h.message_id
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
		WHERE h.user_id = $1 AND h.bucket = ANY($2)`
	args := []any{uid, buckets}
	if account != "" {
		query += ` AND ea.id = $3`
		args = append(args, account)
	}
	query += `
		  AND m.id = (
		    SELECT m2.id FROM hey_messages h2
		    JOIN mail_messages m2 ON m2.id = h2.message_id
		    WHERE h2.user_id = $1 AND h2.bucket = ANY($2)
		      AND m2.account_id = m.account_id AND m2.thread_id = m.thread_id
		    ORDER BY m2.received_at DESC NULLS LAST LIMIT 1)
		ORDER BY m.received_at DESC NULLS LAST
		LIMIT 200`
	rows, err := a.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}
	defer rows.Close()

	type threadRow struct {
		ThreadID    string `json:"thread_id"`
		MessageID   string `json:"message_id"`
		Subject     string `json:"subject"`
		From        string `json:"from"`
		ReceivedAt  string `json:"received_at"`
		Read        bool   `json:"read"`
		Attachment  bool   `json:"has_attachment"`
		Preview     string `json:"preview"`
		Bucket      string `json:"bucket"`
		ThreadLen   int    `json:"thread_len"`
		SnoozeUntil string `json:"snooze_until,omitempty"`
	}
	out := []threadRow{}
	for rows.Next() {
		var row threadRow
		var fromJSON string
		var received sql.NullTime
		var snoozeUntil sql.NullTime
		if err := rows.Scan(&row.ThreadID, &row.MessageID, &row.Subject, &fromJSON,
			&received, &row.Read, &row.Attachment, &row.Preview, &row.Bucket,
			&snoozeUntil, &row.ThreadLen); err != nil {
			writeProblem(w, http.StatusInternalServerError, "Scan Failed", err.Error())
			return
		}
		row.From = firstSenderName(fromJSON)
		if received.Valid {
			row.ReceivedAt = received.Time.Format(time.RFC3339)
		}
		if snoozeUntil.Valid {
			row.SnoozeUntil = snoozeUntil.Time.Format(time.RFC3339)
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

// handleThread returns one thread: messages with their hey state. Bodies are
// lazy upstream — any message without one is fetched on demand here (bounded
// per request), so opening a thread always shows content.
func (a *App) handleThread(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	thread := r.PathValue("thread")
	rows, err := a.db.QueryContext(r.Context(), `
		SELECT m.id, m.account_id, m.subject, m.from_addrs, m.to_addrs, m.received_at,
		       COALESCE(h.bucket,''), b.text_body, b.html_body, b.parts, b.fetched_at
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
		ID          string       `json:"id"`
		Account     string       `json:"account"`
		Subject     string       `json:"subject"`
		From        string       `json:"from"`
		To          string       `json:"to"`
		ReceivedAt  string       `json:"received_at"`
		Bucket      string       `json:"bucket"`
		Body        string       `json:"body"`
		HTML        string       `json:"html,omitempty"`
		Attachments []attachment `json:"attachments,omitempty"`
	}
	out := []msgRow{}
	type ref struct {
		idx      int
		id, acct string
	}
	var refs []ref
	for rows.Next() {
		var row msgRow
		var fromJSON, toJSON, parts sql.NullString
		var textBody, htmlBody sql.NullString
		var fetched sql.NullTime
		var received sql.NullTime
		if err := rows.Scan(&row.ID, &row.Account, &row.Subject, &fromJSON, &toJSON,
			&received, &row.Bucket, &textBody, &htmlBody, &parts, &fetched); err != nil {
			writeProblem(w, http.StatusInternalServerError, "Scan Failed", err.Error())
			return
		}
		row.Body = textBody.String
		row.HTML = htmlBody.String
		row.Attachments = parseAttachments(parts)
		row.From = firstSenderName(fromJSON.String)
		var to []mail.Address
		if json.Unmarshal([]byte(toJSON.String), &to) == nil && len(to) > 0 {
			row.To = to[0].Email
		}
		if received.Valid {
			row.ReceivedAt = received.Time.Format(time.RFC3339)
		}
		if !fetched.Valid {
			refs = append(refs, ref{len(out), row.ID, row.Account})
		}
		out = append(out, row)
	}
	if len(out) == 0 {
		writeProblem(w, http.StatusNotFound, "Not Found", "no such thread")
		return
	}

	// On-demand body fetch for messages the sync never fetched. Bounded to
	// keep one huge thread from turning into a mailbox download.
	//
	// Upstream gap (NEUTRON_BUGS N6): imap Adapter.Body assumes a mailbox
	// is already selected — true mid-sync, false on a fresh dial, so their
	// own HTTP body endpoint fails the same way on GreenMail-class
	// servers. Workaround: resolve the message's mailbox and run a
	// cursor-only Sync (a SELECT plus usually-empty delta) before Body.
	if len(refs) > 0 {
		adapters := map[string]mail.Adapter{}
		releases := map[string]func(){}
		defer func() {
			for _, rel := range releases {
				rel()
			}
		}()
		limit := 6
		for i, rf := range refs {
			if i >= limit {
				break
			}
			var boxID string
			a.db.QueryRowContext(r.Context(),
				`SELECT mailbox_id FROM mail_message_mailboxes WHERE account_id = $1 AND message_id = $2 LIMIT 1`,
				rf.acct, rf.id).Scan(&boxID)
			if boxID == "" {
				continue
			}
			ad, ok := adapters[rf.acct]
			if !ok {
				cred, err := a.Token(r.Context(), mail.AccountID(rf.acct))
				if err != nil {
					a.log.Error("body fetch: token", "err", err)
					continue
				}
				resolve := newResolver()
				var release func()
				ad, release, err = resolve(r.Context(), mail.AccountID(rf.acct), cred)
				if err != nil {
					a.log.Error("body fetch: dial", "err", err)
					continue
				}
				adapters[rf.acct] = ad
				releases[rf.acct] = release
			}
			if cur, err := a.store.Cursor(r.Context(), mail.AccountID(rf.acct), mail.MailboxID(boxID)); err == nil {
				if _, err := ad.Sync(r.Context(), mail.MailboxID(boxID), cur); err != nil {
					a.log.Error("body fetch: select", "err", err)
					continue
				}
			}
			b, err := a.eng.Body(r.Context(), mail.AccountID(rf.acct), mail.MessageID(rf.id), ad)
			if err != nil {
				a.log.Error("body fetch: engine", "msg", rf.id, "err", err)
				continue
			}
			out[rf.idx].Body = b.Text
			out[rf.idx].HTML = b.HTML
			if atts := b.Attachments(); len(atts) > 0 {
				list := []attachment{}
				for _, p := range atts {
					list = append(list, attachment{
						PartID: p.PartID, Filename: p.Filename,
						Type: p.Type, Size: p.Size,
					})
				}
				out[rf.idx].Attachments = list
			}
		}
	}
	writeJSON(w, out)
}

type attachment struct {
	PartID   string `json:"part_id"`
	Filename string `json:"filename"`
	Type     string `json:"type"`
	Size     int64  `json:"size"`
}

// handleAttachment streams one attachment's decoded content. Same
// select-before-fetch workaround as the body path (NEUTRON_BUGS N6).
func (a *App) handleAttachment(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	msgID := r.PathValue("message")
	partID := r.PathValue("part")
	var acct string
	err = a.db.QueryRowContext(r.Context(), `
		SELECT m.account_id FROM mail_messages m
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
		WHERE m.id = $2`, uid, msgID).Scan(&acct)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "Not Found", "no such message")
		return
	}
	cred, err := a.Token(r.Context(), mail.AccountID(acct))
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Credential Failed", err.Error())
		return
	}
	resolve := newResolver()
	ad, release, err := resolve(r.Context(), mail.AccountID(acct), cred)
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "Connect Failed", err.Error())
		return
	}
	defer release()

	var boxID string
	a.db.QueryRowContext(r.Context(),
		`SELECT mailbox_id FROM mail_message_mailboxes WHERE account_id = $1 AND message_id = $2 LIMIT 1`,
		acct, msgID).Scan(&boxID)
	if boxID == "" {
		writeProblem(w, http.StatusNotFound, "Not Found", "message has no mailbox")
		return
	}
	if cur, err := a.store.Cursor(r.Context(), mail.AccountID(acct), mail.MailboxID(boxID)); err == nil {
		if _, err := ad.Sync(r.Context(), mail.MailboxID(boxID), cur); err != nil {
			writeProblem(w, http.StatusBadGateway, "Select Failed", err.Error())
			return
		}
	}
	rc, err := ad.Attachment(r.Context(), mail.MessageID(msgID), partID)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "Not Found", "part not found")
		return
	}
	defer rc.Close()

	// Filename from the stored parts list; harmless fallback if absent.
	var partsRaw sql.NullString
	filename := "attachment"
	if a.db.QueryRowContext(r.Context(),
		`SELECT parts FROM mail_bodies WHERE account_id = $1 AND message_id = $2`, acct, msgID).Scan(&partsRaw) == nil && partsRaw.Valid {
		var raw []mail.BodyPart
		if json.Unmarshal([]byte(partsRaw.String), &raw) == nil {
			for _, p := range raw {
				if p.PartID == partID && p.Filename != "" {
					filename = p.Filename
				}
			}
		}
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(filename, `"`, "")+`"`)
	w.Header().Set("Content-Type", "application/octet-stream")
	io.Copy(w, rc)
}

// parseAttachments reads the parts JSON the engine stores with bodies.
func parseAttachments(parts sql.NullString) []attachment {
	if !parts.Valid || parts.String == "" {
		return nil
	}
	var raw []mail.BodyPart
	if err := json.Unmarshal([]byte(parts.String), &raw); err != nil {
		return nil
	}
	var out []attachment
	for _, p := range raw {
		if p.IsAttachment() {
			out = append(out, attachment{
				PartID: p.PartID, Filename: p.Filename,
				Type: p.Type, Size: p.Size,
			})
		}
	}
	return out
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
		// Leaving set_aside must drop the return date too, or the Snoozed
		// list shows a stale (possibly past) promise the message no longer keeps.
		q = `UPDATE hey_messages SET bucket=$3, set_aside_until=NULL WHERE user_id=$1 AND message_id=$2`
		args = []any{uid, msg, req.Action}
	case "set_aside":
		days := req.UntilDays
		if days == 0 {
			days = 3
		}
		if days < 1 || days > 3650 {
			writeProblem(w, http.StatusUnprocessableEntity, "Invalid Snooze", "until_days must be 1 through 3650")
			return
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
