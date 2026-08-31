package main

// The Board (branch experiment): the briefing's derived sections presented as
// the columns of a personal kanban. The cards write themselves from mail
// semantics — unread Imbox mail needs you, a two-sided thread you spoke last
// in is one you're waiting on, a dated snooze comes back on its day. Pins and
// manual notes are the only user-authored cards, and a pin never moves mail:
// it is a marker on top of whatever bucket the thread lives in, so it cannot
// fight the classifier or the sweep.

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// sweepSnoozed returns dated snoozes whose day has arrived (TASKS 1.4). A
// Snoozed column that never gave the mail back would just be a second Set
// Aside — the sweep is what makes deferral a promise.
func (a *App) sweepSnoozed(ctx context.Context, uid string) error {
	_, err := a.db.ExecContext(ctx, `
		UPDATE hey_messages SET bucket = 'imbox'
		WHERE user_id = $1 AND bucket = 'set_aside'
		  AND set_aside_until IS NOT NULL AND set_aside_until <= now()`, uid)
	return err
}

type boardCard struct {
	CardID     string `json:"card_id,omitempty"`
	Account    string `json:"account,omitempty"`
	ThreadID   string `json:"thread_id,omitempty"`
	MessageID  string `json:"message_id,omitempty"`
	Subject    string `json:"subject"`
	From       string `json:"from,omitempty"`
	ReceivedAt string `json:"received_at,omitempty"`
	Preview    string `json:"preview,omitempty"`
	Note       string `json:"note,omitempty"`
	Manual     bool   `json:"manual,omitempty"`
}

func cardFromThread(t briefThread) boardCard {
	return boardCard{
		Account:    t.Account,
		ThreadID:   t.ThreadID,
		MessageID:  t.MessageID,
		Subject:    t.Subject,
		From:       t.From,
		ReceivedAt: t.ReceivedAt,
		Preview:    t.Preview,
	}
}

// handleBoard assembles the board: needs-you (derived unread Imbox threads,
// then pinned threads not already among them, then manual notes — derived
// first, so the keyboard list and the rendered column stay index-aligned),
// you're-waiting, and the done pile. Waiting and Snoozed stay pure
// derivations: those cards clear themselves when the mail changes, which is
// the point of the whole experiment.
func (a *App) handleBoard(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	if err := a.sweepSnoozed(r.Context(), uid); err != nil {
		a.log.Error("board sweep failed", "err", err)
	}

	needsYou, waiting := a.briefThreads(r.Context(), uid, r.URL.Query().Get("account"))
	cards := make([]boardCard, 0, len(needsYou))
	derived := map[string]bool{}
	for _, t := range needsYou {
		derived[t.Account+"\x00"+t.ThreadID] = true
		cards = append(cards, cardFromThread(t))
	}

	// Live data for pinned threads: subject and date follow the thread's
	// newest message; the stored title only outlives a disconnected account.
	type cardRow struct{ id, account, thread, title, note string }
	var open []cardRow
	rows, err := a.db.QueryContext(r.Context(), `
		SELECT id::text, COALESCE(account_id,''), COALESCE(thread_key,''), title, note FROM board_cards
		WHERE user_id = $1 AND done_at IS NULL AND thread_key IS NOT NULL
		ORDER BY created_at`, uid)
	if err == nil {
		for rows.Next() {
			var c cardRow
			if rows.Scan(&c.id, &c.account, &c.thread, &c.title, &c.note) == nil {
				open = append(open, c)
			}
		}
		rows.Close()
	}
	if len(open) > 0 {
		live := map[string]briefThread{}
		r2, err := a.db.QueryContext(r.Context(), `
			SELECT DISTINCT ON (b.id)
			       m.account_id, m.thread_id, m.id, m.subject, m.from_addrs, m.received_at, m.preview
			FROM board_cards b
			JOIN mail_messages m ON m.account_id = b.account_id AND m.thread_id = b.thread_key
			JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
			WHERE b.user_id = $1 AND b.done_at IS NULL AND b.thread_key IS NOT NULL
			ORDER BY b.id, m.received_at DESC NULLS LAST`, uid)
		if err == nil {
			for r2.Next() {
				var t briefThread
				var fromJSON string
				var received *time.Time
				if r2.Scan(&t.Account, &t.ThreadID, &t.MessageID, &t.Subject, &fromJSON, &received, &t.Preview) == nil {
					if received != nil {
						t.ReceivedAt = received.Format(time.RFC3339)
					}
					t.From = firstSenderName(fromJSON)
					live[t.Account+"\x00"+t.ThreadID] = t
				}
			}
			r2.Close()
		}
		for _, c := range open {
			if derived[c.account+"\x00"+c.thread] {
				continue // already on the board on its own
			}
			if t, ok := live[c.account+"\x00"+c.thread]; ok {
				card := cardFromThread(t)
				card.CardID, card.Note = c.id, c.note
				cards = append(cards, card)
			} else {
				cards = append(cards, boardCard{CardID: c.id, Account: c.account, ThreadID: c.thread, Subject: c.title, Note: c.note})
			}
		}
	}

	// Manual notes last: they have no thread, so they must not shift the
	// indices of the openable cards above them.
	r3, err := a.db.QueryContext(r.Context(), `
		SELECT id::text, title, note FROM board_cards
		WHERE user_id = $1 AND done_at IS NULL AND thread_key IS NULL
		ORDER BY created_at`, uid)
	if err == nil {
		for r3.Next() {
			var c boardCard
			if r3.Scan(&c.CardID, &c.Subject, &c.Note) == nil {
				c.Manual = true
				cards = append(cards, c)
			}
		}
		r3.Close()
	}

	waitingCards := make([]boardCard, 0, len(waiting))
	for _, t := range waiting {
		waitingCards = append(waitingCards, cardFromThread(t))
	}

	done := []boardCard{}
	r4, err := a.db.QueryContext(r.Context(), `
		SELECT id::text, COALESCE(account_id,''), COALESCE(thread_key,''), title, note FROM board_cards
		WHERE user_id = $1 AND done_at IS NOT NULL
		ORDER BY done_at DESC LIMIT 20`, uid)
	if err == nil {
		for r4.Next() {
			var id, account, thread, title, note string
			if r4.Scan(&id, &account, &thread, &title, &note) == nil {
				done = append(done, boardCard{CardID: id, Account: account, ThreadID: thread, Subject: title, Note: note})
			}
		}
		r4.Close()
	}

	writeJSON(w, map[string]any{
		"needs_you":  cards,
		"waiting_on": waitingCards,
		"done":       done,
	})
}

// handleBoardPin pins a thread. The current subject is snapshotted as the
// card title so the card survives its account being disconnected.
func (a *App) handleBoardPin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Account  string `json:"account"`
		ThreadID string `json:"thread_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Account == "" || req.ThreadID == "" {
		writeProblem(w, http.StatusUnprocessableEntity, "Missing Thread", "account and thread_id are required")
		return
	}
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}

	var subject string
	if err := a.db.QueryRowContext(r.Context(), `
		SELECT m.subject FROM mail_messages m
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
		WHERE m.account_id = $2 AND m.thread_id = $3
		ORDER BY m.received_at DESC NULLS LAST LIMIT 1`, uid, req.Account, req.ThreadID).Scan(&subject); err != nil {
		writeProblem(w, http.StatusNotFound, "Not Found", "no such thread")
		return
	}

	var id string
	err = a.db.QueryRowContext(r.Context(), `
		INSERT INTO board_cards (user_id, account_id, thread_key, title) VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, account_id, thread_key) WHERE thread_key IS NOT NULL
		DO UPDATE SET done_at = NULL, title = EXCLUDED.title
		RETURNING id::text`, uid, req.Account, req.ThreadID, subject).Scan(&id)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Pin Failed", err.Error())
		return
	}
	writeJSON(w, boardCard{CardID: id, Account: req.Account, ThreadID: req.ThreadID, Subject: subject})
}

// handleBoardCard creates a manual note card — the only card with no mail
// behind it.
func (a *App) handleBoardCard(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
		Note  string `json:"note"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Title == "" {
		writeProblem(w, http.StatusUnprocessableEntity, "Missing Title", "title is required")
		return
	}
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	var id string
	err = a.db.QueryRowContext(r.Context(), `
		INSERT INTO board_cards (user_id, thread_key, title, note)
		VALUES ($1, NULL, $2, $3) RETURNING id::text`, uid, req.Title, req.Note).Scan(&id)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Card Failed", err.Error())
		return
	}
	writeJSON(w, boardCard{CardID: id, Subject: req.Title, Note: req.Note, Manual: true})
}

// handleBoardCardDone checks a card off (or restores it) without touching any
// mail — derived cards are resolved by reading instead.
func (a *App) handleBoardCardDone(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Done bool `json:"done"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	res, err := a.db.ExecContext(r.Context(), `
		UPDATE board_cards SET done_at = CASE WHEN $3 THEN now() ELSE NULL END
		WHERE user_id = $1 AND id = $2`, uid, r.PathValue("id"), req.Done)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Update Failed", err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeProblem(w, http.StatusNotFound, "Not Found", "no such card")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleBoardUnpin removes a card — the undo for a pin, and the delete for a
// note. The mail itself never moved, so there is nothing to restore.
func (a *App) handleBoardUnpin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CardID string `json:"card_id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.CardID == "" {
		writeProblem(w, http.StatusUnprocessableEntity, "Missing Card", "card_id is required")
		return
	}
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	res, err := a.db.ExecContext(r.Context(),
		`DELETE FROM board_cards WHERE user_id = $1 AND id = $2`, uid, req.CardID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Delete Failed", err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeProblem(w, http.StatusNotFound, "Not Found", "no such card")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
