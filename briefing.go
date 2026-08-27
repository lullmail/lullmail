package main

// The Briefing (TASKS 1.7): today-shaped view over the same mirror.
//
// Rules are deliberately conservative — a briefing that says "2 things need
// you" and is wrong once loses the user to Classic forever. "Needs you"
// requires: Imbox bucket, unread, and the thread's latest message is from
// someone else. "You're waiting" requires the user to have spoken last in a
// thread someone else started. Everything else stays a count.

import (
	"context"
	"net/http"
	"time"
)

type briefThread struct {
	ThreadID   string `json:"thread_id"`
	MessageID  string `json:"message_id"`
	Subject    string `json:"subject"`
	From       string `json:"from"`
	ReceivedAt string `json:"received_at"`
	Preview    string `json:"preview"`
}

// briefThreads computes the two derived card sets the Briefing and the Board
// share. Rules are deliberately conservative — a briefing that says "2 things
// need you" and is wrong once loses the user to Classic forever. "Needs you"
// requires: Imbox bucket, unread, and the thread's latest message is from
// someone else. "You're waiting" requires the user to have spoken last in a
// thread someone else started.
func (a *App) briefThreads(ctx context.Context, uid, account string) (needsYou, waiting []briefThread) {
	// The user's own addresses, for whose-turn analysis.
	myAddr := map[string]bool{}
	{
		myQuery := `SELECT lower(address) FROM email_accounts WHERE user_id = $1`
		myArgs := []any{uid}
		if account != "" {
			myQuery += ` AND id = $2`
			myArgs = append(myArgs, account)
		}
		rows, err := a.db.QueryContext(ctx, myQuery, myArgs...)
		if err == nil {
			for rows.Next() {
				var s string
				rows.Scan(&s)
				myAddr[s] = true
			}
			rows.Close()
		}
	}

	// Latest message of every Imbox thread the user owns.
	listQuery := `
		SELECT DISTINCT ON (m.thread_id)
		       m.thread_id, m.id, COALESCE(m.subject,''), COALESCE(m.from_addrs,'[]'), m.received_at, COALESCE(m.preview,'')
		FROM mail_messages m
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
		JOIN hey_messages h ON h.message_id = m.id AND h.user_id = $1
		WHERE h.bucket = 'imbox'`
	listArgs := []any{uid}
	if account != "" {
		listQuery += ` AND ea.id = $2`
		listArgs = append(listArgs, account)
	}
	listQuery += ` ORDER BY m.thread_id, m.received_at DESC NULLS LAST`
	rows, err := a.db.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return nil, nil
	}
	defer rows.Close()

	// Unread state per latest message + whether each thread has messages from
	// both sides — "you're waiting" requires someone else in the thread;
	// a note-to-self is not a conversation.
	readState := map[string]bool{} // message id -> read
	spoke := map[string][2]bool{}  // thread -> {i_spoke, other_spoke}
	{
		r2, err := a.db.QueryContext(ctx, `
			SELECT m.thread_id,
			       bool_or(lower(m.from_addrs::json->0->>'email') = ANY ($2)),
			       bool_or(lower(m.from_addrs::json->0->>'email') <> ALL ($2))
			FROM mail_messages m
			JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
			GROUP BY m.thread_id`, uid, addrList(myAddr))
		if err == nil {
			for r2.Next() {
				var th string
				var mine, theirs bool
				if r2.Scan(&th, &mine, &theirs) == nil {
					spoke[th] = [2]bool{mine, theirs}
				}
			}
			r2.Close()
		}
		r3, err := a.db.QueryContext(ctx, `
			SELECT h.message_id, h.read_at IS NOT NULL
			FROM hey_messages h WHERE h.user_id = $1 AND h.bucket = 'imbox'`, uid)
		if err == nil {
			for r3.Next() {
				var id string
				var read bool
				if r3.Scan(&id, &read) == nil {
					readState[id] = read
				}
			}
			r3.Close()
		}
	}

	for rows.Next() {
		var bt briefThread
		var fromJSON string
		var received *time.Time
		if err := rows.Scan(&bt.ThreadID, &bt.MessageID, &bt.Subject, &fromJSON, &received, &bt.Preview); err != nil {
			continue
		}
		if received != nil {
			bt.ReceivedAt = received.Format(time.RFC3339)
		}
		sender := firstSenderEmail(fromJSON)
		fromMe := myAddr[sender]
		bt.From = firstSenderName(fromJSON)

		switch {
		case !fromMe && !readState[bt.MessageID]:
			needsYou = append(needsYou, bt)
		case fromMe && spoke[bt.ThreadID][0] && spoke[bt.ThreadID][1]:
			// My move was the last one and someone else is in the thread.
			// Their turn — and if it stays theirs for long, this is the nudge.
			waiting = append(waiting, bt)
		}
	}
	return needsYou, waiting
}

func (a *App) handleBriefing(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	// Returned snoozes re-enter the picture before it is drawn.
	if err := a.sweepSnoozed(r.Context(), uid); err != nil {
		a.log.Error("briefing sweep failed", "err", err)
	}
	account := r.URL.Query().Get("account")
	needsYou, waiting := a.briefThreads(r.Context(), uid, account)

	// Feed + Paper Trail unread counts, screener total.
	var feedN, paperN, screenerN int
	briefCounts := `
		SELECT
		  count(*) FILTER (WHERE h.bucket='feed' AND h.read_at IS NULL),
		  count(*) FILTER (WHERE h.bucket='paper_trail' AND h.read_at IS NULL),
		  count(*) FILTER (WHERE h.bucket='screener')
		FROM hey_messages h
		JOIN mail_messages m ON m.id = h.message_id
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
		WHERE h.user_id = $1`
	briefArgs := []any{uid}
	if account != "" {
		briefCounts += ` AND ea.id = $2`
		briefArgs = append(briefArgs, account)
	}
	a.db.QueryRowContext(r.Context(), briefCounts, briefArgs...).Scan(&feedN, &paperN, &screenerN)

	writeJSON(w, map[string]any{
		"needs_you":    orEmpty(needsYou),
		"waiting_on":   orEmpty(waiting),
		"feed_unread":  feedN,
		"paper_unread": paperN,
		"screener":     screenerN,
	})
}

// handleRecent: latest message of the most recent threads, any bucket —
// the palette's zero-query state and Today's "past mail" access.
func (a *App) handleRecent(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	a.threadList(w, r, uid, `
		SELECT DISTINCT ON (m.thread_id)
		       m.thread_id, m.id, m.subject, m.from_addrs, m.received_at,
		       COALESCE(h.read_at IS NOT NULL, false), m.preview, COALESCE(h.bucket,'')
		FROM mail_messages m
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
		LEFT JOIN hey_messages h ON h.message_id = m.id AND h.user_id = $1
		ORDER BY m.thread_id, m.received_at DESC NULLS LAST
		LIMIT 40`, uid)
}

// handleFolder: messages in a provider mailbox by name (Sent, Trash, ...)
// across the user's accounts — the palette's folder browser.
func (a *App) handleFolder(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		writeProblem(w, http.StatusUnprocessableEntity, "Missing Name", "name is required")
		return
	}
	a.threadList(w, r, uid, `
		SELECT DISTINCT ON (m.thread_id)
		       m.thread_id, m.id, m.subject, m.from_addrs, m.received_at,
		       COALESCE(h.read_at IS NOT NULL, false), m.preview, COALESCE(h.bucket,'')
		FROM mail_message_mailboxes mm
		JOIN mail_mailboxes mb ON mb.account_id = mm.account_id AND mb.id = mm.mailbox_id
		JOIN mail_messages m ON m.account_id = mm.account_id AND m.id = mm.message_id
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
		LEFT JOIN hey_messages h ON h.message_id = m.id AND h.user_id = $1
		WHERE lower(mb.name) = lower($2)
		ORDER BY m.thread_id, m.received_at DESC NULLS LAST
		LIMIT 100`, uid, name)
}

// handleMailboxList: distinct provider mailbox names for the palette's
// Lists section. INBOX is skipped — buckets already cover it.
func (a *App) handleMailboxList(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `
		SELECT DISTINCT ON (lower(mb.name)) lower(mb.name), mb.role
		FROM mail_mailboxes mb
		JOIN email_accounts ea ON ea.mirror_account_id = mb.account_id AND ea.user_id = $1
		ORDER BY lower(mb.name)`, uid)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}
	defer rows.Close()
	type mbx struct {
		Name string  `json:"name"`
		Role *string `json:"role,omitempty"`
	}
	out := []mbx{}
	for rows.Next() {
		var m mbx
		if rows.Scan(&m.Name, &m.Role) == nil && m.Name != "inbox" {
			out = append(out, m)
		}
	}
	writeJSON(w, out)
}

// threadList runs a latest-per-thread query and writes the shared row shape.
func (a *App) threadList(w http.ResponseWriter, r *http.Request, uid, query string, args ...any) {
	rows, err := a.db.QueryContext(r.Context(), `
		SELECT * FROM (`+query+`) t
		ORDER BY t.received_at DESC NULLS LAST`, args...)
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
		Preview    string `json:"preview"`
		Bucket     string `json:"bucket"`
	}
	out := []rowOut{}
	for rows.Next() {
		var row rowOut
		var fromJSON string
		var received *time.Time
		if err := rows.Scan(&row.ThreadID, &row.MessageID, &row.Subject, &fromJSON,
			&received, &row.Read, &row.Preview, &row.Bucket); err != nil {
			continue
		}
		row.From = firstSenderName(fromJSON)
		if received != nil {
			row.ReceivedAt = received.Format(time.RFC3339)
		}
		out = append(out, row)
	}
	writeJSON(w, out)
}

// handlePeople: the roster — every decided sender with activity stats.
func (a *App) handlePeople(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `
		SELECT s.sender_key, s.route, s.allowed,
		       count(h.message_id) AS total,
		       max(m.received_at) AS last_at,
		       max(m.subject) AS last_subject
		FROM hey_senders s
		LEFT JOIN mail_messages m ON lower(m.from_addrs::json->0->>'email') = s.sender_key
		LEFT JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = s.user_id
		LEFT JOIN hey_messages h ON h.message_id = m.id AND h.user_id = s.user_id
		WHERE s.user_id = $1
		GROUP BY s.sender_key, s.route, s.allowed
		ORDER BY last_at DESC NULLS LAST`, uid)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}
	defer rows.Close()

	type person struct {
		Sender  string  `json:"sender"`
		Route   string  `json:"route"`
		Allowed bool    `json:"allowed"`
		Total   int     `json:"total"`
		LastAt  *string `json:"last_at"`
		Subject *string `json:"last_subject"`
	}
	out := []person{}
	for rows.Next() {
		var p person
		var lastAt, lastSub *string
		if err := rows.Scan(&p.Sender, &p.Route, &p.Allowed, &p.Total, &lastAt, &lastSub); err != nil {
			continue
		}
		p.LastAt = lastAt
		p.Subject = lastSub
		out = append(out, p)
	}
	writeJSON(w, out)
}

func addrList(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
