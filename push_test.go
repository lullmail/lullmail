package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// subscriptionJSON builds a browser-shaped subscription for a local test
// push service. The encryption keys must be real curve points: the webpush
// client encrypts the payload against them before any HTTP happens.
func subscriptionJSON(t *testing.T, endpoint string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{
		"endpoint": endpoint,
		"keys": map[string]string{
			"auth":   base64.RawURLEncoding.EncodeToString(auth),
			"p256dh": base64.RawURLEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), key.X, key.Y)),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// The audited defect pair: dispatches claimed work only after the external
// submit (two dispatches could double-notify), and the receipt was one
// global row per message (one device's 2xx suppressed another device's
// retry). With per-subscription claims: a concurrent claim is a no-op, a
// failed device's claim is released and retried, and a delivered device is
// never re-sent.
func TestPushClaimsPerSubscriptionAndRetriesOnlyFailures(t *testing.T) {
	vapidPrivate, vapidPublic, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		SecretKey:    "0123456789abcdef0123456789abcdef",
		VAPIDPublic:  vapidPublic,
		VAPIDPrivate: vapidPrivate,
		VAPIDSubject: "mailto:tests@example.com",
	}

	var hitsA, hitsB atomic.Int32
	serviceA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hitsA.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(serviceA.Close)
	serviceB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hitsB.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(serviceB.Close)

	subA := subscriptionJSON(t, serviceA.URL)
	subB := subscriptionJSON(t, serviceB.URL)
	sealedA, err := sealSecret(cfg, subA)
	if err != nil {
		t.Fatal(err)
	}
	sealedB, err := sealSecret(cfg, subB)
	if err != nil {
		t.Fatal(err)
	}

	subsStep := func() dbStep {
		return dbStep{kind: "query", rows: &testRows{
			columns: []string{"subscription_ciphertext"},
			values:  [][]driver.Value{{sealedA}, {sealedB}},
		}}
	}
	messageStep := func() dbStep {
		return dbStep{kind: "query", rows: &testRows{
			columns: []string{"account_id", "id", "thread_id"},
			values:  [][]driver.Value{{"acct-1", "msg-1", "thread-1"}},
		}}
	}
	claim := func() dbStep { return dbStep{kind: "exec"} }
	claimDenied := func() dbStep { return dbStep{kind: "exec", zeroAffected: true} }

	// First dispatch: A delivers, B's service answers 503 and its claim is
	// released for retry.
	db1, log1 := openRecordingDB(t, subsStep(), messageStep(),
		claim(), // claim A
		claim(), // A delivered
		claim(), // claim B
		claim()) // B failed: claim released
	a := &App{cfg: cfg, log: discardLogger(), db: db1}
	a.sendPushForUser(context.Background(), "owner-1")
	if hits := hitsA.Load(); hits != 1 {
		t.Fatalf("device A submissions = %d, want 1", hits)
	}
	if hits := hitsB.Load(); hits != 1 {
		t.Fatalf("device B submissions = %d, want 1 before retry", hits)
	}
	released := 0
	for _, q := range log1.log() {
		if q.kind == "exec" && strings.Contains(q.query, "DELETE FROM push_deliveries") {
			released++
		}
	}
	if released == 0 {
		t.Fatal("failed device's claim was not released for retry")
	}

	// Second dispatch: A's receipt still covers it (claim denied, no
	// submission), B alone is retried and now delivers.
	db2, log2 := openRecordingDB(t, subsStep(), messageStep(),
		claimDenied(), // A: already delivered
		claim(),       // claim B
		claim())       // B delivered
	a = &App{cfg: cfg, log: discardLogger(), db: db2}
	a.sendPushForUser(context.Background(), "owner-1")
	if hits := hitsA.Load(); hits != 1 {
		t.Fatalf("device A was re-sent after a successful delivery: %d submissions", hits)
	}
	if hits := hitsB.Load(); hits != 2 {
		t.Fatalf("device B submissions = %d, want 2 (initial + retry)", hits)
	}
	var updates int
	for _, q := range log2.log() {
		if q.kind == "exec" && strings.Contains(q.query, "UPDATE push_deliveries SET delivered_at") {
			updates++
		}
	}
	if updates != 1 {
		t.Fatalf("delivered receipts written in retry round = %d, want 1 (B only)", updates)
	}
}
