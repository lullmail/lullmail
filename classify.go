package main

// HEY mechanics (TASKS 1.4): classify mirror messages into buckets using
// per-sender decisions, expose Screener and bucket views, message actions.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	netmail "net/mail"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

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
		LEFT JOIN hey_messages h ON h.account_id = m.account_id AND h.message_id = m.id AND h.user_id = $1
		WHERE h.message_id IS NULL
		  AND (ea.backfill_days = 0
		       OR m.received_at IS NULL
		       OR (m.received_at AT TIME ZONE 'UTC') > now() - make_interval(days => ea.backfill_days))`, uid)
	if err != nil {
		return err
	}

	// The unclassified batch is drained and CLOSED before any other query
	// runs. An open result set holds its pool connection; the correspondent
	// queries below need another connection from the same capped pool, so
	// enough overlapping classifiers could hold-and-wait every connection
	// and deadlock the pool (audit 4 F08).
	type pending struct {
		acct, id, sender string
		historical       bool
	}
	var batch []pending
	for rows.Next() {
		var acct, id, fromJSON string
		var received, connected sql.NullTime
		if err := rows.Scan(&acct, &id, &fromJSON, &received, &connected); err != nil {
			rows.Close()
			return err
		}
		// A NULL INTERNALDATE must not wedge the whole pass; undated mail is
		// treated as new (it screens like any unknown sender).
		batch = append(batch, pending{
			acct: acct, id: id, sender: firstSenderEmail(fromJSON),
			historical: received.Valid && connected.Valid && received.Time.Before(connected.Time),
		})
	}
	scanErr := rows.Err()
	closeErr := rows.Close()
	if err := errors.Join(scanErr, closeErr); err != nil {
		return err
	}

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
			// A missed row here screens someone the owner already corresponds
			// with — the exact mail the Screener is meant to let through.
			if err := cr.Scan(&addr); err != nil {
				return err
			}
			if addr.Valid && addr.String != "" {
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
	// Outbound evidence requires genuine Sent-folder membership: a forged
	// From header equals the owner's address on inbound mail too, and
	// trusting it let anyone seed correspondents by self-spoofing (audit
	// DATA-03). Still a heuristic — From on mail the provider filed as
	// Sent — but no longer spoofable by arbitrary senders. The SAME
	// predicate guards every outbound-evidence query, the thread query
	// included (audit 3 DATA-01).
	sentMembership := func(alias string) string {
		return `
		AND EXISTS (
		  SELECT 1 FROM mail_message_mailboxes mm
		  JOIN mail_mailboxes mb
		    ON mb.account_id = mm.account_id AND mb.id = mm.mailbox_id
		  WHERE mm.account_id = ` + alias + `.account_id AND mm.message_id = ` + alias + `.id
		    AND mb.role = 'sent')`
	}
	if err := collect(`
		SELECT DISTINCT lower(t->>'email')
		FROM mail_messages m
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
		CROSS JOIN LATERAL json_array_elements(` + arrayOf("m.to_addrs") + `) t
		WHERE lower(COALESCE(m.from_addrs, '[]')::json->0->>'email') = lower(ea.address)` + sentMembership("m")); err != nil {
		return fmt.Errorf("classify: correspondent evidence (recipients): %w", err)
	}
	if err := collect(`
		SELECT DISTINCT lower(t->>'email')
		FROM mail_messages m
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
		CROSS JOIN LATERAL json_array_elements(` + arrayOf("m.cc_addrs") + `) t
		WHERE lower(COALESCE(m.from_addrs, '[]')::json->0->>'email') = lower(ea.address)` + sentMembership("m")); err != nil {
		return fmt.Errorf("classify: correspondent evidence (cc): %w", err)
	}
	if err := collect(`SELECT DISTINCT lower(address) FROM email_accounts WHERE user_id = $1`); err != nil {
		return fmt.Errorf("classify: correspondent evidence (own addresses): %w", err)
	}
	if err := collect(`
		SELECT DISTINCT lower(COALESCE(other.from_addrs, '[]')::json->0->>'email')
		FROM mail_messages mine
		JOIN email_accounts ea ON ea.mirror_account_id = mine.account_id AND ea.user_id = $1
		JOIN mail_messages other ON other.account_id = mine.account_id AND other.thread_id = mine.thread_id
		WHERE mine.thread_id IS NOT NULL
		  AND lower(COALESCE(mine.from_addrs, '[]')::json->0->>'email') = lower(ea.address)` + sentMembership("mine") + `
		  AND lower(COALESCE(other.from_addrs, '[]')::json->0->>'email') <> lower(ea.address)
		  AND COALESCE(other.from_addrs, '') <> ''`); err != nil {
		return fmt.Errorf("classify: correspondent evidence (threads): %w", err)
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Serialize classification against sender decisions: the owner row
	// lock orders this pass against a concurrent decide/undecide, so a
	// decision cannot commit between the batch read and the self-heal
	// below and strand decided mail in the Screener (audit DATA-02). The
	// screening preference is read under the SAME lock: a classifier that
	// read "off" before the lock must not drain the Screener after the
	// user re-enabled it (audit 3 DATA-03).
	if _, err := lockAuthUser(ctx, tx, uid); err != nil {
		return err
	}
	screening, err := a.screeningEnabledTx(ctx, tx, uid)
	if err != nil {
		return err
	}
	for _, p := range batch {
		var route string
		var allowed bool
		decided := tx.QueryRowContext(ctx,
			`SELECT route, allowed FROM hey_senders WHERE user_id = $1 AND sender_key = $2`,
			uid, p.sender).Scan(&route, &allowed)
		bucket := classifySender(decided == nil, allowed, route, correspondents[p.sender], p.historical, screening)
		// Broken envelopes (no parsable From) can never be decided or shown;
		// they file to Receipts instead of inflating the Screener forever.
		if p.sender == "" {
			bucket = "paper_trail"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO hey_messages (user_id, account_id, message_id, bucket)
			VALUES ($1, $2, $3, $4) ON CONFLICT (user_id, account_id, message_id) DO NOTHING`,
			uid, p.acct, p.id, bucket); err != nil {
			return err
		}
	}
	// Self-heal runs even when this pass classified nothing new: a decision
	// that raced an earlier batch's insert is repaired by the next pass,
	// but only if the repair query still executes when the batch is empty
	// (audit DATA-02).
	if _, err := tx.ExecContext(ctx, `
		UPDATE hey_messages h SET bucket = CASE WHEN s.allowed THEN s.route ELSE 'dropped' END
		FROM hey_senders s, mail_messages m
		WHERE h.user_id = $1 AND h.bucket = 'screener'
		  AND h.account_id = m.account_id AND h.message_id = m.id AND s.user_id = $1 AND s.sender_key <> ''
		  AND s.sender_key = lower(COALESCE(m.from_addrs, '[]')::json->0->>'email')`, uid); err != nil {
		return err
	}
	// Malformed-From mail parked in 'screener' before the rule above existed
	// also moves to Receipts, so the badge stops counting the unshowable.
	if _, err := tx.ExecContext(ctx, `
		UPDATE hey_messages h SET bucket = 'paper_trail'
		FROM mail_messages m
		WHERE h.user_id = $1 AND h.bucket = 'screener'
		  AND h.account_id = m.account_id AND h.message_id = m.id
		  AND (m.from_addrs IS NULL
		       OR json_typeof(COALESCE(m.from_addrs, '[]')::json) <> 'array'
		       OR COALESCE(m.from_addrs, '[]')::json->0->>'email' IS NULL
		       OR lower(COALESCE(m.from_addrs, '[]')::json->0->>'email') = '')`, uid); err != nil {
		return err
	}
	// Screening off drains the waiting room: anything already parked moves to
	// the Imbox. Undecided senders only — a blocked sender's mail is in
	// 'dropped', not 'screener', so nothing the owner rejected comes back.
	if !screening {
		if _, err := tx.ExecContext(ctx, `
			UPDATE hey_messages SET bucket = 'imbox'
			WHERE user_id = $1 AND bucket = 'screener'`, uid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// classifySender is the one place the routing rules are written. Precedence:
// an explicit decision always wins; then correspondence (replying to someone
// is a decision); then the history rule — mail that predates the mailbox's
// connection is reference, not a decision queue, so it files to Receipts.
// Only mail arriving after connection screens.
//
// With screening off, the one thing that changes is the last step: an unknown
// sender's new mail lands in the Imbox instead of waiting in the Screener.
// Decisions already made are still honoured, so blocking a sender keeps
// working and turning screening back on loses nothing.
func likeContains(q string) string {
	q = strings.ReplaceAll(q, `\`, `\\`)
	q = strings.ReplaceAll(q, `%`, `\%`)
	q = strings.ReplaceAll(q, `_`, `\_`)
	return "%" + q + "%"
}

func classifySender(decided, allowed bool, route string, correspondent, historical, screening bool) string {
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
	case !screening:
		return "imbox"
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
	if err := a.sweepSnoozed(r.Context(), uid, ""); err != nil {
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
		JOIN mail_messages m ON m.account_id = h.account_id AND m.id = h.message_id
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
// preview. Same row shape as bucket listings so the client reuses
// rendering, and the same keyset continuation (audit DATA-06).
func (a *App) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeRowsPage[struct{}](w, nil, false, listCursor{})
		return
	}
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	limit, cursor, err := pageParams(r, 60)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Bad Cursor", "the cursor is not a valid continuation token")
		return
	}
	like := likeContains(q)
	query := `
		SELECT m.account_id, m.thread_id, m.id, COALESCE(m.subject,''), COALESCE(m.from_addrs,'[]'), m.received_at,
		       h.read_at IS NOT NULL AS is_read, m.has_attachment, COALESCE(m.preview,''),
		       COALESCE(h.bucket,''),
		       (SELECT count(*) FROM mail_messages t
	          WHERE t.account_id = m.account_id AND t.thread_id = m.thread_id) AS thread_len
		FROM mail_messages m
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
		LEFT JOIN hey_messages h ON h.account_id = m.account_id AND h.message_id = m.id AND h.user_id = $1
		WHERE (m.subject ILIKE $2 ESCAPE '\' OR m.from_addrs ILIKE $2 ESCAPE '\' OR m.to_addrs ILIKE $2 ESCAPE '\' OR m.preview ILIKE $2 ESCAPE '\')` +
		accountClause(r)
	args := []any{uid, like}
	if cursor.ID != "" {
		query += cursorPredicate("$3", "$4")
		args = append(args, cursorArgs(cursor)...)
	}
	query += ` ORDER BY m.received_at DESC NULLS LAST, m.id DESC
		LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit+1)
	rows, err := a.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}
	defer rows.Close()

	type rowOut struct {
		Account    string `json:"account"`
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
	next := listCursor{}
	more := false
	for rows.Next() {
		if len(out) == limit {
			more = true
			continue
		}
		var row rowOut
		var fromJSON string
		var received sql.NullTime
		if err := rows.Scan(&row.Account, &row.ThreadID, &row.MessageID, &row.Subject, &fromJSON,
			&received, &row.Read, &row.Attachment, &row.Preview, &row.Bucket, &row.ThreadLen); err != nil {
			writeProblem(w, http.StatusInternalServerError, "Scan Failed", err.Error())
			return
		}
		row.From = firstSenderName(fromJSON)
		if received.Valid {
			row.ReceivedAt = received.Time.Format(time.RFC3339)
			t := received.Time
			next.ReceivedAt = &t
		} else {
			next.ReceivedAt = nil
		}
		next.ID = row.MessageID
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}
	writeRowsPage(w, out, more, next)
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
		       COALESCE((array_agg(m.subject ORDER BY m.received_at DESC NULLS LAST))[1], '') AS sample_subject
		FROM hey_messages h
		JOIN mail_messages m ON m.account_id = h.account_id AND m.id = h.message_id
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
	if err := rows.Err(); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
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
	// The owner-row lock serializes the decision with classification's
	// batch insert, closing the window where both commit and neither sees
	// the other (audit DATA-02).
	if _, err := lockAuthUser(r.Context(), tx, uid); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Decide Failed", err.Error())
		return
	}
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
		UPDATE hey_messages h SET bucket = $3
		FROM mail_messages m
		WHERE h.user_id = $1 AND h.bucket = 'screener'
		  AND h.account_id = m.account_id AND h.message_id = m.id
		  AND lower(COALESCE(m.from_addrs, '[]')::json->0->>'email') = $2`,
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
	// The previous rule is read INSIDE the transaction, under the same
	// owner lock the decision path uses: reading it before the lock let a
	// competing decision commit in between, so undecide could delete the
	// NEW rule while recalling mail bucketed by the OLD one (audit 3
	// DATA-02).
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Begin Failed", err.Error())
		return
	}
	defer tx.Rollback()
	if _, err := lockAuthUser(r.Context(), tx, uid); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Undecide Failed", err.Error())
		return
	}
	// The recall set is the buckets the decision itself placed (its route, or
	// 'dropped' for blocked) plus anything still sitting in the Screener —
	// mail the user filed by hand afterwards keeps the bucket they chose.
	var prevRoute string
	var prevAllowed bool
	err = tx.QueryRowContext(r.Context(),
		`SELECT route, allowed FROM hey_senders WHERE user_id = $1 AND sender_key = $2`,
		uid, req.Sender).Scan(&prevRoute, &prevAllowed)
	if errors.Is(err, sql.ErrNoRows) {
		// Already absent: commit the no-op and answer the documented shape.
		if err := tx.Commit(); err != nil {
			writeProblem(w, http.StatusInternalServerError, "Commit Failed", err.Error())
			return
		}
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
	if _, err := tx.ExecContext(r.Context(),
		`DELETE FROM hey_senders WHERE user_id = $1 AND sender_key = $2`,
		uid, req.Sender); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Undecide Failed", err.Error())
		return
	}
	if _, err := tx.ExecContext(r.Context(), `
		UPDATE hey_messages h SET bucket = 'screener'
		FROM mail_messages m
		WHERE h.user_id = $1 AND h.bucket = ANY($3)
		  AND h.account_id = m.account_id AND h.message_id = m.id
		  AND lower(COALESCE(m.from_addrs, '[]')::json->0->>'email') = $2`,
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
	// Keyset pagination (audit DATA-06): the page is the next `limit`
	// rows strictly after the cursor's sort key, so newer arrivals never
	// shift what older pages delivered.
	limit, cursor, err := pageParams(r, maxPageLimit)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "Bad Cursor", "the cursor is not a valid continuation token")
		return
	}
	// The Snoozed list (and the calendar fed by it) must never show a return
	// date already past — sweep before listing.
	if r.PathValue("bucket") == "snoozed" {
		if err := a.sweepSnoozed(r.Context(), uid, ""); err != nil {
			a.log.Error("bucket sweep failed", "err", err)
		}
	}
	query := `
		SELECT m.account_id, m.thread_id, m.id, COALESCE(m.subject,''), COALESCE(m.from_addrs,'[]'), m.received_at,
		       h.read_at IS NOT NULL AS is_read, m.has_attachment, COALESCE(m.preview,''), h.bucket,
		       h.set_aside_until,
		       (SELECT count(*) FROM mail_messages t
	          WHERE t.account_id = m.account_id AND t.thread_id = m.thread_id) AS thread_len
		FROM hey_messages h
		JOIN mail_messages m ON m.account_id = h.account_id AND m.id = h.message_id
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
	    JOIN mail_messages m2 ON m2.account_id = h2.account_id AND m2.id = h2.message_id
	    WHERE h2.user_id = $1 AND h2.bucket = ANY($2)
	      AND m2.account_id = m.account_id AND m2.thread_id = m.thread_id
	    ORDER BY m2.received_at DESC NULLS LAST, m2.id DESC LIMIT 1)`
	nextArg := len(args) + 1
	if cursor.ID != "" {
		query += cursorPredicate("$"+strconv.Itoa(nextArg), "$"+strconv.Itoa(nextArg+1))
		args = append(args, cursorArgs(cursor)...)
	}
	query += `
		ORDER BY m.received_at DESC NULLS LAST, m.id DESC
		LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit+1)
	rows, err := a.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}
	defer rows.Close()

	type threadRow struct {
		Account     string `json:"account"`
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
	next := listCursor{}
	more := false
	for rows.Next() {
		// The limit+1 probe row exists only to prove has_more; it is
		// never scanned into the page.
		if len(out) == limit {
			more = true
			continue
		}
		var row threadRow
		var fromJSON string
		var received sql.NullTime
		var snoozeUntil sql.NullTime
		if err := rows.Scan(&row.Account, &row.ThreadID, &row.MessageID, &row.Subject, &fromJSON,
			&received, &row.Read, &row.Attachment, &row.Preview, &row.Bucket,
			&snoozeUntil, &row.ThreadLen); err != nil {
			writeProblem(w, http.StatusInternalServerError, "Scan Failed", err.Error())
			return
		}
		row.From = firstSenderName(fromJSON)
		if received.Valid {
			row.ReceivedAt = received.Time.Format(time.RFC3339)
			t := received.Time
			next.ReceivedAt = &t
		} else {
			next.ReceivedAt = nil
		}
		next.ID = row.MessageID
		if snoozeUntil.Valid {
			row.SnoozeUntil = snoozeUntil.Time.Format(time.RFC3339)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}
	writeRowsPage(w, out, more, next)
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
	account := r.URL.Query().Get("account")
	// Provider/native IDs and RFC Message-IDs are not globally unique
	// across connected accounts; a missing account used to resolve
	// duplicates with LIMIT 1, acting on whatever matched first (audit 3
	// DATA-06).
	if account == "" {
		writeProblem(w, http.StatusBadRequest, "Missing Account", "account is required for thread operations")
		return
	}
	// The composer's default recipients are computed HERE, from the stored
	// envelope and the owner's connected addresses — never client-side from
	// the rendered From line, which ignored Reply-To and addressed replies
	// to the owner whenever the thread's newest message was her own
	// (audit 4 F06). Read BEFORE the thread rows open: two open result
	// sets on one pool is the hold-and-wait shape audit 4 F08 removed.
	ownRows, err := a.db.QueryContext(r.Context(), `SELECT lower(address) FROM email_accounts WHERE user_id = $1`, uid)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}
	ownAddresses := map[string]bool{}
	for ownRows.Next() {
		var addr string
		if err := ownRows.Scan(&addr); err != nil {
			ownRows.Close()
			writeProblem(w, http.StatusInternalServerError, "Scan Failed", err.Error())
			return
		}
		if addr != "" {
			ownAddresses[addr] = true
		}
	}
	scanErr := ownRows.Err()
	closeErr := ownRows.Close()
	if err := errors.Join(scanErr, closeErr); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}

	threadQuery := `
		SELECT m.id, m.account_id, m.subject, m.from_addrs, m.to_addrs, m.reply_to_addrs, m.received_at,
		       COALESCE(h.bucket,''), b.text_body, b.html_body, b.parts, b.fetched_at
		FROM mail_messages m
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
		LEFT JOIN hey_messages h ON h.account_id = m.account_id AND h.message_id = m.id AND h.user_id = $1
		LEFT JOIN mail_bodies b ON b.account_id = m.account_id AND b.message_id = m.id
		WHERE m.thread_id = $2 AND (m.account_id = $3 OR ea.id::text = $3)`
	threadArgs := []any{uid, thread, account}
	threadQuery += ` ORDER BY m.received_at ASC NULLS LAST`
	rows, err := a.db.QueryContext(r.Context(), threadQuery, threadArgs...)
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
		// "ready" (cached or fetched now), "missing" (never fetched; the
		// eager pass was capped or skipped), "failed" (an eager fetch
		// errored). Empty content with status "ready" is authoritative —
		// a genuinely empty message (audit 3 DATA-12).
		BodyStatus string `json:"body_status"`
		// Server-computed default recipients for a reply to this message
		// ("Name <a@b>, c@d"): the sender's Reply-To when set, else From —
		// and the message's own recipients when the message came from one
		// of the owner's addresses, so following up on sent mail goes back
		// to the conversation, not to the owner. Empty means "ask".
		ReplyTo string `json:"reply_to"`
	}
	out := []msgRow{}
	type ref struct {
		idx      int
		id, acct string
	}
	var refs []ref
	for rows.Next() {
		var row msgRow
		var fromJSON, toJSON, replyToJSON, parts sql.NullString
		var textBody, htmlBody sql.NullString
		var fetched sql.NullTime
		var received sql.NullTime
		if err := rows.Scan(&row.ID, &row.Account, &row.Subject, &fromJSON, &toJSON, &replyToJSON,
			&received, &row.Bucket, &textBody, &htmlBody, &parts, &fetched); err != nil {
			writeProblem(w, http.StatusInternalServerError, "Scan Failed", err.Error())
			return
		}
		row.Body = textBody.String
		row.HTML = htmlBody.String
		if fetched.Valid {
			row.BodyStatus = "ready"
		} else {
			row.BodyStatus = "missing"
		}
		row.Attachments = parseAttachments(parts)
		row.From = firstSenderName(fromJSON.String)
		var to []mail.Address
		if json.Unmarshal([]byte(toJSON.String), &to) == nil && len(to) > 0 {
			row.To = to[0].Email
		}
		row.ReplyTo = replyDefault(ownAddresses, fromJSON.String, replyToJSON.String, toJSON.String)
		if received.Valid {
			row.ReceivedAt = received.Time.Format(time.RFC3339)
		}
		if !fetched.Valid {
			refs = append(refs, ref{len(out), row.ID, row.Account})
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}
	if len(out) == 0 {
		writeProblem(w, http.StatusNotFound, "Not Found", "no such thread")
		return
	}

	// On-demand body fetch for messages the sync never fetched. Bounded two
	// ways — at most threadEagerBodyLimit fetches, all inside an overall
	// deadline — so one huge thread cannot turn into a mailbox download or
	// hold request resources indefinitely; the rest arrive on the next sync
	// or an explicit open (audit DATA-05). The engine selects the message's
	// mailbox first on IMAP (mail.MailboxSelector), so a freshly dialed
	// adapter works here.
	if len(refs) > 0 {
		fetchCtx, cancelFetch := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancelFetch()
		adapters := map[string]mail.Adapter{}
		releases := map[string]func(){}
		defer func() {
			for _, rel := range releases {
				rel()
			}
		}()
		fetched := 0
		for _, rf := range refs {
			if fetched >= threadEagerBodyLimit || fetchCtx.Err() != nil {
				break
			}
			ad, ok := adapters[rf.acct]
			if !ok {
				cred, err := a.Token(fetchCtx, mail.AccountID(rf.acct))
				if err != nil {
					// A credential or dial failure is a failed fetch, not
					// "not fetched yet": the reader must offer a retry, not
					// a false sync-in-progress promise (audit 4 F19).
					a.log.Error("body fetch: token", "err", err)
					out[rf.idx].BodyStatus = "failed"
					continue
				}
				resolve := newResolver()
				var release func()
				ad, release, err = resolve(fetchCtx, mail.AccountID(rf.acct), cred)
				if err != nil {
					a.log.Error("body fetch: dial", "err", err)
					out[rf.idx].BodyStatus = "failed"
					continue
				}
				adapters[rf.acct] = ad
				releases[rf.acct] = release
			}
			fetched++
			b, err := a.eng.Body(fetchCtx, mail.AccountID(rf.acct), mail.MessageID(rf.id), ad)
			if err != nil {
				a.log.Error("body fetch: engine", "msg", rf.id, "err", err)
				out[rf.idx].BodyStatus = "failed"
				continue
			}
			out[rf.idx].Body = b.Text
			out[rf.idx].HTML = b.HTML
			out[rf.idx].BodyStatus = "ready"
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

// replyDefault computes the composer's default recipients for a reply to
// one message from the stored envelope and the owner's connected addresses
// (audit 4 F06):
//
//   - Reply-To wins when the sender set one, otherwise From.
//   - When the message itself came from one of the owner's addresses (a
//     sent message the owner is following up on), the default is that
//     message's recipients — never the owner.
//   - The owner's own addresses are always filtered out, duplicates collapse.
//
// An empty result means "ask the user", never a silent self-address.
func replyDefault(own map[string]bool, fromJSON, replyToJSON, toJSON string) string {
	parse := func(s string) []mail.Address {
		var addrs []mail.Address
		if json.Unmarshal([]byte(s), &addrs) != nil {
			return nil
		}
		return addrs
	}
	normalize := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

	from := parse(fromJSON)
	fromOwn := false
	for _, address := range from {
		if own[normalize(address.Email)] {
			fromOwn = true
			break
		}
	}
	candidates := parse(replyToJSON)
	if fromOwn {
		candidates = parse(toJSON)
	} else if len(candidates) == 0 {
		candidates = from
	}

	result := make([]mail.Address, 0, len(candidates))
	seen := map[string]bool{}
	for _, address := range candidates {
		key := normalize(address.Email)
		if key == "" || own[key] || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, address)
	}
	if len(result) == 0 {
		return ""
	}
	formatted := make([]string, 0, len(result))
	for _, address := range result {
		formatted = append(formatted, (&netmail.Address{Name: address.Name, Address: address.Email}).String())
	}
	return strings.Join(formatted, ", ")
}

// handleAttachment streams one attachment's decoded content. Attachment is
// an adapter call, not an engine one, so the mailbox select the engine does
// for Body is requested explicitly via Locate.
func (a *App) handleAttachment(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	msgID := r.PathValue("message")
	partID := r.PathValue("part")
	requestedAccount := r.URL.Query().Get("account")
	// Message ids are only account-scoped at the provider: without an
	// account, a duplicate id resolves arbitrarily (audit 3 DATA-06).
	if requestedAccount == "" {
		writeProblem(w, http.StatusBadRequest, "Missing Account", "account is required for attachment downloads")
		return
	}
	var acct string
	attachmentQuery := `
		SELECT m.account_id FROM mail_messages m
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
		WHERE m.id = $2 AND (m.account_id = $3 OR ea.id::text = $3)`
	attachmentArgs := []any{uid, msgID, requestedAccount}
	attachmentQuery += ` LIMIT 1`
	err = a.db.QueryRowContext(r.Context(), attachmentQuery, attachmentArgs...).Scan(&acct)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			a.log.Error("attachment account lookup failed", "err", err)
		}
		writeLookupProblem(w, err, "message")
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

	if err := a.eng.Locate(r.Context(), mail.AccountID(acct), mail.MessageID(msgID), ad); err != nil {
		writeProblem(w, http.StatusBadGateway, "Select Failed", err.Error())
		return
	}
	rc, err := ad.Attachment(r.Context(), mail.MessageID(msgID), partID)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "Not Found", "part not found")
		return
	}
	defer rc.Close()

	// Filename from the stored parts list; harmless fallback if absent.
	var partsRaw sql.NullString
	// attachmentFilename reduces a provider-supplied name to a safe single
	// path component: separators and backslashes drop their prefixes,
	// control characters vanish, and hostile/dot names fall back to a
	// generic one. Never used as a server filesystem path — only as the
	// Content-Disposition display value (audit 3 DATA-14).
	filename := attachmentFilename("attachment")
	if a.db.QueryRowContext(r.Context(),
		`SELECT parts FROM mail_bodies WHERE account_id = $1 AND message_id = $2`, acct, msgID).Scan(&partsRaw) == nil && partsRaw.Valid {
		var raw []mail.BodyPart
		if json.Unmarshal([]byte(partsRaw.String), &raw) == nil {
			for _, p := range raw {
				if p.PartID == partID && p.Filename != "" {
					filename = attachmentFilename(p.Filename)
				}
			}
		}
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment",
		map[string]string{"filename": filename}))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := io.Copy(w, rc); err != nil {
		// Headers and possibly some body bytes are already out. Returning
		// normally would let the HTTP server close the partial 200 cleanly,
		// which the browser reads as a complete download — a corrupt file
		// saved without an error. Aborting tears the connection down so the
		// client sees a failed transfer instead (audit 4 F15).
		a.log.Warn("attachment stream interrupted", "account", acct, "message", msgID, "part", partID, "err", err)
		panic(http.ErrAbortHandler)
	}
}

// attachmentFilename normalizes a provider filename for the
// Content-Disposition header: one path component, no controls, never
// empty or a dot-name.
func attachmentFilename(s string) string {
	s = strings.ReplaceAll(s, "\\", "/")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if s == "" || s == "." || s == ".." {
		return "attachment"
	}
	return s
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
// bucket, set aside with an absolute return date.
//
// Snooze contract (audit DATA-07/DATA-05): `until` is an absolute UTC
// RFC3339 instant captured at user-intent time, so offline replay applies
// the exact intended deadline no matter when replay runs, and undo
// restores the exact prior instant rather than a rounded day count.
// `until: null` on set_aside means someday — stored as the "later" bucket
// with no return date. The legacy `until_days` remains accepted (server
// computes from its own clock — the drift the absolute form removes) and
// is deprecated compat for callers that have not migrated.
func (a *App) handleMessageAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action    string          `json:"action"`
		Until     json.RawMessage `json:"until"`
		UntilDays int             `json:"until_days"`
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
	account := r.URL.Query().Get("account")
	// Same account-scoping rule as the thread and attachment endpoints:
	// duplicate message ids across the owner's accounts must not resolve
	// arbitrarily (audit 3 DATA-06).
	if account == "" {
		writeProblem(w, http.StatusBadRequest, "Missing Account", "account is required for message actions")
		return
	}
	var acct, thread string
	lookup := `SELECT m.account_id, m.thread_id
		FROM mail_messages m
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
		WHERE m.id = $2 AND (m.account_id = $3 OR ea.id::text = $3)`
	lookupArgs := []any{uid, msg, account}
	lookup += ` ORDER BY m.received_at DESC NULLS LAST LIMIT 1`
	if err := a.db.QueryRowContext(r.Context(), lookup, lookupArgs...).Scan(&acct, &thread); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			a.log.Error("message action lookup failed", "err", err)
		}
		writeLookupProblem(w, err, "message")
		return
	}

	// parseSnoozeUntil resolves the set_aside deadline: absolute instant,
	// explicit null (someday), or the deprecated relative forms.
	parseSnoozeUntil := func() (until any, someday bool, problem string) {
		if len(req.Until) > 0 {
			raw := strings.TrimSpace(string(req.Until))
			if raw == "null" {
				return nil, true, ""
			}
			var encoded string
			if err := json.Unmarshal(req.Until, &encoded); err != nil {
				return nil, false, "until must be an RFC3339 timestamp string (or null for someday)"
			}
			t, err := time.Parse(time.RFC3339, strings.TrimSpace(encoded))
			if err != nil {
				return nil, false, "until must be an RFC3339 timestamp (or null for someday)"
			}
			return t.UTC(), false, ""
		}
		if req.UntilDays > 0 {
			if req.UntilDays > 3650 {
				return nil, false, "until_days must be 1 through 3650"
			}
			return req.UntilDays, false, "" // relative: handled by the query shape below
		}
		if req.UntilDays < 0 {
			return nil, false, "until_days must be 1 through 3650"
		}
		return nil, false, "" // legacy default (3 days) applies
	}

	var q string
	var args []any
	switch req.Action {
	case "read":
		q = `UPDATE hey_messages h SET read_at = now() FROM mail_messages m
		     WHERE h.user_id=$1 AND h.account_id=$2 AND h.account_id=m.account_id
		       AND h.message_id=m.id AND m.thread_id=$3`
		args = []any{uid, acct, thread}
	case "unread":
		q = `UPDATE hey_messages h SET read_at = NULL FROM mail_messages m
		     WHERE h.user_id=$1 AND h.account_id=$2 AND h.account_id=m.account_id
		       AND h.message_id=m.id AND m.thread_id=$3`
		args = []any{uid, acct, thread}
	case "imbox", "paper_trail", "feed", "later", "screener":
		// Leaving set_aside must drop the return date too, or the Snoozed
		// list shows a stale (possibly past) promise the message no longer keeps.
		q = `UPDATE hey_messages h SET bucket=$4, set_aside_until=NULL FROM mail_messages m
		     WHERE h.user_id=$1 AND h.account_id=$2 AND h.account_id=m.account_id
		       AND h.message_id=m.id AND m.thread_id=$3`
		args = []any{uid, acct, thread, req.Action}
	case "set_aside":
		until, someday, problem := parseSnoozeUntil()
		if problem != "" {
			writeProblem(w, http.StatusUnprocessableEntity, "Invalid Snooze", problem)
			return
		}
		if someday {
			// until:null — someday is the "later" bucket with no date.
			q = `UPDATE hey_messages h SET bucket='later', set_aside_until=NULL FROM mail_messages m
			     WHERE h.user_id=$1 AND h.account_id=$2 AND h.account_id=m.account_id
			       AND h.message_id=m.id AND m.thread_id=$3`
			args = []any{uid, acct, thread}
			break
		}
		if days, relative := until.(int); relative {
			if days == 0 {
				days = 3 // deprecated default
			}
			q = `UPDATE hey_messages h SET bucket='set_aside', set_aside_until = now() + make_interval(days => $4)
			     FROM mail_messages m
			     WHERE h.user_id=$1 AND h.account_id=$2 AND h.account_id=m.account_id
			       AND h.message_id=m.id AND m.thread_id=$3`
			args = []any{uid, acct, thread, days}
			break
		}
		q = `UPDATE hey_messages h SET bucket='set_aside', set_aside_until = $4
		     FROM mail_messages m
		     WHERE h.user_id=$1 AND h.account_id=$2 AND h.account_id=m.account_id
		       AND h.message_id=m.id AND m.thread_id=$3`
		args = []any{uid, acct, thread, until}
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
	// Echo the applied state: the caller's undo snapshot is exact when it
	// can see what landed (audit DATA-07).
	var bucket string
	var appliedUntil sql.NullTime
	if err := a.db.QueryRowContext(r.Context(),
		`SELECT bucket, set_aside_until FROM hey_messages h
		 WHERE h.user_id=$1 AND h.account_id=$2 AND h.message_id=$3`, uid, acct, msg).
		Scan(&bucket, &appliedUntil); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	out := map[string]any{"ok": true, "bucket": bucket}
	if appliedUntil.Valid {
		out["snooze_until"] = appliedUntil.Time.UTC().Format(time.RFC3339Nano)
	}
	writeJSON(w, out)
}

// screeningEnabled reads the owner's Screener preference. A missing row is
// treated as screening on: the column defaults to true, and failing open to
// the stricter behaviour is the safer default for a gate.
func (a *App) screeningEnabled(ctx context.Context, uid string) (bool, error) {
	var on bool
	err := a.db.QueryRowContext(ctx,
		`SELECT screening_enabled FROM users WHERE id = $1`, uid).Scan(&on)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return on, err
}

// screeningEnabledTx is screeningEnabled inside a transaction the caller
// already holds the owner lock on, so the read cannot race a preference
// change (audit 3 DATA-03).
func (a *App) screeningEnabledTx(ctx context.Context, tx *sql.Tx, uid string) (bool, error) {
	var on bool
	err := tx.QueryRowContext(ctx,
		`SELECT screening_enabled FROM users WHERE id = $1 FOR UPDATE`, uid).Scan(&on)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return on, err
}

// handlePrefs reads and writes the owner's product preferences. Only the
// Screener switch lives here today; appearance stays client-side.
func (a *App) handlePrefs(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	if r.Method == http.MethodGet {
		on, err := a.screeningEnabled(r.Context(), uid)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
			return
		}
		writeJSON(w, map[string]any{"screening_enabled": on})
		return
	}

	var req struct {
		ScreeningEnabled *bool `json:"screening_enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.ScreeningEnabled == nil {
		writeProblem(w, http.StatusUnprocessableEntity, "Missing Field", "screening_enabled is required")
		return
	}
	// Preference and drain are ONE transition under the owner lock: a
	// failure between them used to leave the preference flipped with the
	// waiting room intact, and the drain raced a concurrent classifier
	// reading the old value (audit 3 DATA-03).
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Begin Failed", err.Error())
		return
	}
	defer tx.Rollback()
	if _, err := lockAuthUser(r.Context(), tx, uid); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Update Failed", err.Error())
		return
	}
	if _, err := tx.ExecContext(r.Context(),
		`UPDATE users SET screening_enabled = $2 WHERE id = $1`, uid, *req.ScreeningEnabled); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Update Failed", err.Error())
		return
	}
	// Switching off empties the waiting room now rather than on the next
	// sync — the point of the switch is to stop mail sitting behind a gate.
	// Blocked senders are in 'dropped', so nothing rejected is resurrected.
	moved := int64(0)
	if !*req.ScreeningEnabled {
		res, err := tx.ExecContext(r.Context(), `
			UPDATE hey_messages SET bucket = 'imbox'
			WHERE user_id = $1 AND bucket = 'screener'`, uid)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "Update Failed", err.Error())
			return
		}
		moved, _ = res.RowsAffected()
	}
	if err := tx.Commit(); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Commit Failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"screening_enabled": *req.ScreeningEnabled, "released": moved})
}

// threadEagerBodyLimit caps how many uncached bodies one thread open will
// fetch before answering; the rest stay lazy (audit DATA-05).
const threadEagerBodyLimit = 8
