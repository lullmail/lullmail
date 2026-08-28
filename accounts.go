package main

// Account connect flow (TASKS 1.3): create/list/delete connected mailboxes,
// store credentials encrypted, drive on-demand sync. The mirror account is
// created here; neutron-mail never learns the credential persistently.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/neutron-build/neutron/mail"
)

type accountJSON struct {
	ID            string  `json:"id"`
	Provider      string  `json:"provider"`
	Address       string  `json:"address"`
	Label         string  `json:"label"`
	BackfillDays  int     `json:"backfill_days"`
	RetentionDays int     `json:"retention_days"`
	SyncEnabled   bool    `json:"sync_enabled"`
	LastSyncAt    *string `json:"last_sync_at"`
	LastError     *string `json:"last_error"`
	MessageCount  int     `json:"message_count"`
	ScreenerCount int     `json:"screener_count"`
}

func (a *App) handleAccounts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.listAccountsJSON(w, r)
	case http.MethodPost:
		a.createAccount(w, r)
	default:
		writeProblem(w, http.StatusMethodNotAllowed, "Method Not Allowed", "use GET or POST")
	}
}

func (a *App) listAccountsJSON(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `
		SELECT ea.id, ea.provider, ea.address, ea.label, ea.backfill_days, ea.retention_days, ea.sync_enabled,
		       COALESCE(ea.last_sync_at::text,''), COALESCE(ea.last_error,''),
		       (SELECT count(*) FROM mail_messages m WHERE m.account_id = ea.mirror_account_id),
		       (SELECT count(*) FROM hey_messages h
		         JOIN mail_messages m ON m.id = h.message_id AND m.account_id = ea.mirror_account_id
		         WHERE h.user_id = ea.user_id AND h.bucket = 'screener')
		FROM email_accounts ea
		WHERE ea.user_id = $1 ORDER BY ea.created_at`, uid)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}
	defer rows.Close()

	out := []accountJSON{}
	for rows.Next() {
		var acc accountJSON
		var lastSync, lastErr string
		if err := rows.Scan(&acc.ID, &acc.Provider, &acc.Address, &acc.Label, &acc.BackfillDays, &acc.RetentionDays, &acc.SyncEnabled,
			&lastSync, &lastErr, &acc.MessageCount, &acc.ScreenerCount); err != nil {
			writeProblem(w, http.StatusInternalServerError, "Scan Failed", err.Error())
			return
		}
		if lastSync != "" {
			acc.LastSyncAt = &lastSync
		}
		if lastErr != "" {
			acc.LastError = &lastErr
		}
		out = append(out, acc)
	}
	writeJSON(w, out)
}

func (a *App) createAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider     string `json:"provider"`
		Address      string `json:"address"`
		Username     string `json:"username"`
		Password     string `json:"password"`
		Host         string `json:"host"`
		Port         int    `json:"port"`
		SMTPHost     string `json:"smtp_host"`
		SMTPPort     int    `json:"smtp_port"`
		Label        string `json:"label"`
		BackfillDays int    `json:"backfill_days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.Provider == "" {
		req.Provider = "imap"
	}
	if req.Provider != "imap" && req.Provider != "jmap" {
		writeProblem(w, http.StatusUnprocessableEntity, "Unsupported Provider", "v0 supports imap and jmap; gmail/graph arrive in Phase 1b")
		return
	}
	if req.Address == "" || req.Password == "" || req.Host == "" {
		writeProblem(w, http.StatusUnprocessableEntity, "Missing Fields", "address, password and host are required")
		return
	}
	if req.Username == "" {
		req.Username = req.Address
	}
	if req.Port == 0 {
		if req.Provider == "jmap" {
			req.Port = 443
		} else {
			req.Port = 993
		}
	}
	if req.BackfillDays == 0 {
		req.BackfillDays = 90
	}

	cred := mail.Credential{
		Provider: mail.Provider(req.Provider),
		Email:    req.Address,
		Password: req.Password,
		Host:     req.Host,
		Port:     req.Port,
	}

	// Validate before storing: dial and list mailboxes. A typo'd host must
	// not become a credential row that fails on every scheduler tick.
	resolve := newResolver()
	adapter, release, err := resolve(r.Context(), "verify", cred)
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "Connect Failed", err.Error())
		return
	}
	boxes, err := adapter.Mailboxes(r.Context())
	release()
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "Mailboxes Failed", err.Error())
		return
	}
	roleCount := 0
	for _, b := range boxes {
		if b.Role != "" {
			roleCount++
		}
	}

	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	ciphertext, err := sealSecret(a.cfg, req.Password)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Encrypt Failed", err.Error())
		return
	}

	mirrorID := newID()
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Begin Failed", err.Error())
		return
	}
	defer tx.Rollback()

	if err := a.store.PutAccount(r.Context(), &mail.Account{
		ID:       mail.AccountID(mirrorID),
		Provider: cred.Provider,
		Email:    req.Address,
		Name:     req.Label,
	}); err != nil {
		writeProblem(w, http.StatusBadGateway, "Mirror Account Failed", err.Error())
		return
	}
	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO email_accounts
		  (user_id, mirror_account_id, provider, address, label, username, host, port,
		   smtp_host, smtp_port, cred_ciphertext, backfill_days)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		uid, string(mirrorID), req.Provider, req.Address, req.Label, req.Username,
		req.Host, req.Port, req.SMTPHost, req.SMTPPort, ciphertext, req.BackfillDays)
	if err != nil {
		// Do not orphan the mirror row: nothing would own or clean it.
		_, _ = a.db.ExecContext(r.Context(), `DELETE FROM mail_accounts WHERE id=$1`, mirrorID)
		writeProblem(w, http.StatusInternalServerError, "Insert Failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Commit Failed", err.Error())
		return
	}

	// Initial sync in the background: the API answers immediately, the UI
	// polls account status while envelopes land.
	go func() {
		ctx := context.Background()
		a.syncAccount(ctx, mail.AccountID(mirrorID))
		a.classifyUser(ctx, uid)
		a.sendPushForUser(ctx, uid)
	}()

	writeJSON(w, map[string]any{"id": mirrorID, "mailboxes": len(boxes), "with_roles": roleCount})
}

func (a *App) handleAccountItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	switch r.Method {
	case http.MethodDelete:
		a.deleteAccount(w, r, id)
	case http.MethodPost:
		if r.URL.Query().Get("op") == "sync" {
			a.triggerSync(w, r, id)
			return
		}
		if r.URL.Query().Get("op") == "retention" {
			a.updateRetention(w, r, id)
			return
		}
		if r.URL.Query().Get("op") == "sync_enabled" {
			a.updateSyncEnabled(w, r, id)
			return
		}
		writeProblem(w, http.StatusBadRequest, "Unknown Op", "use ?op=sync, ?op=retention, or ?op=sync_enabled")
	default:
		writeProblem(w, http.StatusMethodNotAllowed, "Method Not Allowed", "use DELETE or POST ?op=sync")
	}
}

// updateSyncEnabled flips the background-sync pause. Manual ?op=sync stays
// available either way — pausing the scheduler is not pausing the owner.
func (a *App) updateSyncEnabled(w http.ResponseWriter, r *http.Request, id string) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, 500, "Lookup Failed", err.Error())
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeProblem(w, 422, "Invalid Flag", "body must be {\"enabled\": true|false}")
		return
	}
	result, err := a.db.ExecContext(r.Context(), `UPDATE email_accounts SET sync_enabled=$1 WHERE id=$2 AND user_id=$3`, req.Enabled, id, uid)
	if err != nil {
		writeProblem(w, 500, "Update Failed", err.Error())
		return
	}
	if n, _ := result.RowsAffected(); n != 1 {
		writeProblem(w, 404, "Not Found", "no such account")
		return
	}
	writeJSON(w, map[string]any{"sync_enabled": req.Enabled})
}

func (a *App) updateRetention(w http.ResponseWriter, r *http.Request, id string) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, 500, "Lookup Failed", err.Error())
		return
	}
	var req struct {
		Days int `json:"days"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil || req.Days < 0 || req.Days > 3650 {
		writeProblem(w, 422, "Invalid Retention", "days must be 0 (forever) through 3650")
		return
	}
	result, err := a.db.ExecContext(r.Context(), `UPDATE email_accounts SET retention_days=$1 WHERE id=$2 AND user_id=$3`, req.Days, id, uid)
	if err != nil {
		writeProblem(w, 500, "Retention Failed", err.Error())
		return
	}
	if n, _ := result.RowsAffected(); n != 1 {
		writeProblem(w, 404, "Not Found", "no such account")
		return
	}
	if err := a.applyRetention(r.Context(), uid); err != nil {
		writeProblem(w, 500, "Retention Failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"retention_days": req.Days})
}

// Retention affects only the local encrypted/mirrored copy; it never issues a
// delete operation to the mail provider. Zero means keep the mirror forever.
func (a *App) applyRetention(ctx context.Context, uid string) error {
	rows, err := a.db.QueryContext(ctx, `SELECT mirror_account_id,retention_days FROM email_accounts WHERE user_id=$1 AND retention_days>0`, uid)
	if err != nil {
		return err
	}
	type policy struct {
		mirror string
		days   int
	}
	var policies []policy
	for rows.Next() {
		var p policy
		if err := rows.Scan(&p.mirror, &p.days); err != nil {
			rows.Close()
			return err
		}
		policies = append(policies, p)
	}
	rows.Close()
	for _, p := range policies {
		tx, err := a.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		deletes := []struct {
			query string
			args  []any
		}{
			{`DELETE FROM hey_messages WHERE user_id=$1 AND message_id IN (SELECT id FROM mail_messages WHERE account_id=$2 AND (received_at AT TIME ZONE 'UTC') < now()-($3 * interval '1 day'))`, []any{uid, p.mirror, p.days}},
			{`DELETE FROM mail_message_mailboxes WHERE account_id=$1 AND message_id IN (SELECT id FROM mail_messages WHERE account_id=$1 AND (received_at AT TIME ZONE 'UTC') < now()-($2 * interval '1 day'))`, []any{p.mirror, p.days}},
			{`DELETE FROM mail_bodies WHERE account_id=$1 AND message_id IN (SELECT id FROM mail_messages WHERE account_id=$1 AND (received_at AT TIME ZONE 'UTC') < now()-($2 * interval '1 day'))`, []any{p.mirror, p.days}},
			{`DELETE FROM mail_messages WHERE account_id=$1 AND (received_at AT TIME ZONE 'UTC') < now()-($2 * interval '1 day')`, []any{p.mirror, p.days}},
		}
		for _, deletion := range deletes {
			if _, err := tx.ExecContext(ctx, deletion.query, deletion.args...); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) deleteAccount(w http.ResponseWriter, r *http.Request, id string) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	var mirror string
	err = a.db.QueryRowContext(r.Context(),
		`SELECT mirror_account_id FROM email_accounts WHERE id = $1 AND user_id = $2`, id, uid).Scan(&mirror)
	if err == sql.ErrNoRows {
		writeProblem(w, http.StatusNotFound, "Not Found", "no such account")
		return
	}
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}
	// Mirror rows are derived state; dropping them wholesale is a supported
	// operation upstream. hey_messages cascade via their own delete first.
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Begin Failed", err.Error())
		return
	}
	defer tx.Rollback()
	deletes := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM hey_messages WHERE user_id = $1 AND message_id IN
		   (SELECT id FROM mail_messages WHERE account_id = $2)`, []any{uid, mirror}},
		{`DELETE FROM mail_message_mailboxes WHERE account_id = $1`, []any{mirror}},
		{`DELETE FROM mail_bodies WHERE account_id = $1`, []any{mirror}},
		{`DELETE FROM mail_messages WHERE account_id = $1`, []any{mirror}},
		{`DELETE FROM mail_mailboxes WHERE account_id = $1`, []any{mirror}},
		{`DELETE FROM mail_sync_state WHERE account_id = $1`, []any{mirror}},
		{`DELETE FROM mail_accounts WHERE id = $1`, []any{mirror}},
		{`DELETE FROM email_accounts WHERE user_id = $1 AND id = $2`, []any{uid, id}},
	}
	for _, deletion := range deletes {
		if _, err := tx.ExecContext(r.Context(), deletion.query, deletion.args...); err != nil {
			writeProblem(w, http.StatusInternalServerError, "Delete Failed", err.Error())
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Commit Failed", err.Error())
		return
	}
	writeJSON(w, map[string]any{"deleted": id})
}

func (a *App) triggerSync(w http.ResponseWriter, r *http.Request, id string) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	var mirror string
	err = a.db.QueryRowContext(r.Context(),
		`SELECT mirror_account_id FROM email_accounts WHERE id = $1 AND user_id = $2`, id, uid).Scan(&mirror)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "Not Found", "no such account")
		return
	}
	go func() {
		ctx := context.Background()
		a.syncAccount(ctx, mail.AccountID(mirror))
		a.classifyUser(ctx, uid)
		a.sendPushForUser(ctx, uid)
	}()
	writeJSON(w, map[string]any{"syncing": id})
}

// syncAccount dials with the stored credential and runs one full sync,
// recording outcome on the account row.
func (a *App) syncAccount(ctx context.Context, acct mail.AccountID) {
	cred, err := a.Token(ctx, acct)
	if err != nil {
		a.log.Error("sync: credential lookup failed", "account", acct, "err", err)
		return
	}
	resolve := newResolver()
	adapter, release, err := resolve(ctx, acct, cred)
	if err != nil {
		a.db.ExecContext(ctx, `UPDATE email_accounts SET last_error = $1 WHERE mirror_account_id = $2`, err.Error(), string(acct))
		return
	}
	defer release()
	_, err = a.eng.SyncAccount(ctx, acct, adapter)
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	a.db.ExecContext(ctx,
		`UPDATE email_accounts SET last_sync_at = now(), last_error = $1 WHERE mirror_account_id = $2`,
		errMsg, string(acct))
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
