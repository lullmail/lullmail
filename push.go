package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

func endpointHash(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:])
}
func (a *App) pushConfigured() bool {
	return a.cfg.VAPIDPublic != "" && a.cfg.VAPIDPrivate != "" && a.cfg.VAPIDSubject != ""
}

func (a *App) handlePush(w http.ResponseWriter, r *http.Request) {
	uid, _ := a.userID(r.Context())
	switch r.Method {
	case http.MethodGet:
		var count int
		_ = a.db.QueryRowContext(r.Context(), `SELECT count(*) FROM push_subscriptions WHERE user_id=$1`, uid).Scan(&count)
		writeJSON(w, map[string]any{"configured": a.pushConfigured(), "subscribed": count > 0, "public_key": a.cfg.VAPIDPublic})
	case http.MethodPost:
		if !a.pushConfigured() {
			writeProblem(w, 503, "Push Not Configured", "set VAPID_PUBLIC_KEY, VAPID_PRIVATE_KEY, and VAPID_SUBJECT")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil {
			writeProblem(w, 400, "Bad Subscription", err.Error())
			return
		}
		var subscription webpush.Subscription
		if json.Unmarshal(body, &subscription) != nil || subscription.Endpoint == "" || subscription.Keys.Auth == "" || subscription.Keys.P256dh == "" {
			writeProblem(w, 422, "Bad Subscription", "endpoint and browser keys are required")
			return
		}
		sealed, err := sealSecret(a.cfg, string(body))
		if err != nil {
			writeProblem(w, 500, "Push Failed", err.Error())
			return
		}
		_, err = a.db.ExecContext(r.Context(), `INSERT INTO push_subscriptions(endpoint_hash,user_id,subscription_ciphertext) VALUES($1,$2,$3) ON CONFLICT(endpoint_hash) DO UPDATE SET user_id=excluded.user_id,subscription_ciphertext=excluded.subscription_ciphertext`, endpointHash(subscription.Endpoint), uid, sealed)
		if err != nil {
			writeProblem(w, 500, "Push Failed", err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	case http.MethodDelete:
		var req struct {
			Endpoint string `json:"endpoint"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req) != nil || req.Endpoint == "" {
			writeProblem(w, 400, "Bad Subscription", "endpoint is required")
			return
		}
		_, err := a.db.ExecContext(r.Context(), `DELETE FROM push_subscriptions WHERE endpoint_hash=$1 AND user_id=$2`, endpointHash(req.Endpoint), uid)
		if err != nil {
			writeProblem(w, 500, "Push Failed", err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	default:
		writeProblem(w, 405, "Method Not Allowed", "use GET, POST, or DELETE")
	}
}

func (a *App) sendPushForUser(ctx context.Context, uid string) {
	if !a.pushConfigured() {
		return
	}
	rows, err := a.db.QueryContext(ctx, `SELECT subscription_ciphertext FROM push_subscriptions WHERE user_id=$1`, uid)
	if err != nil {
		return
	}
	type saved struct {
		hash string
		sub  webpush.Subscription
	}
	var subs []saved
	for rows.Next() {
		var sealed string
		if rows.Scan(&sealed) != nil {
			continue
		}
		plain, err := openSecret(a.cfg, sealed)
		if err != nil {
			continue
		}
		var sub webpush.Subscription
		if json.Unmarshal([]byte(plain), &sub) == nil {
			subs = append(subs, saved{endpointHash(sub.Endpoint), sub})
		}
	}
	rows.Close()
	if len(subs) == 0 {
		return
	}
	var accountID, messageID, threadID string
	err = a.db.QueryRowContext(ctx, `SELECT m.account_id,m.id,m.thread_id FROM hey_messages h JOIN mail_messages m ON m.account_id=h.account_id AND m.id=h.message_id JOIN email_accounts ea ON ea.mirror_account_id=m.account_id AND ea.user_id=h.user_id LEFT JOIN push_deliveries p ON p.user_id=h.user_id AND p.account_id=h.account_id AND p.message_id=h.message_id WHERE h.user_id=$1 AND h.bucket='imbox' AND h.read_at IS NULL AND p.message_id IS NULL ORDER BY m.received_at DESC NULLS LAST LIMIT 1`, uid).Scan(&accountID, &messageID, &threadID)
	if err != nil {
		return
	}
	payload, _ := json.Marshal(map[string]string{"title": "New mail needs you", "body": "Open email-soft to read it.", "path": "/today", "thread": threadID, "account": accountID})
	sent := false
	for _, item := range subs {
		response, err := webpush.SendNotificationWithContext(ctx, payload, &item.sub, &webpush.Options{HTTPClient: &http.Client{Timeout: 15 * time.Second}, Subscriber: a.cfg.VAPIDSubject, VAPIDPublicKey: a.cfg.VAPIDPublic, VAPIDPrivateKey: a.cfg.VAPIDPrivate, TTL: 3600, Topic: "new-mail", Urgency: webpush.UrgencyNormal})
		if err != nil {
			continue
		}
		response.Body.Close()
		if response.StatusCode == http.StatusGone || response.StatusCode == http.StatusNotFound {
			_, _ = a.db.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE endpoint_hash=$1`, item.hash)
			continue
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			sent = true
		}
	}
	if sent {
		// One collapsed notification represents everything currently waiting;
		// otherwise a first sync would emit one old-message alert every tick.
		_, _ = a.db.ExecContext(ctx, `INSERT INTO push_deliveries(user_id,account_id,message_id,delivered_at)
			SELECT h.user_id,h.account_id,h.message_id,$2 FROM hey_messages h
			WHERE h.user_id=$1 AND h.bucket='imbox' AND h.read_at IS NULL
			ON CONFLICT DO NOTHING`, uid, time.Now())
	}
}
