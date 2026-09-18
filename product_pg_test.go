package main

// Real-PostgreSQL integration harness for the product layer (audit OPS-07
// remainder). The engine module has run its store suite against a live
// database since pass 7; this file gives the product layer the same
// contract: every test here skips unless LULL_TEST_DATABASE_URL points at
// a DISPOSABLE scratch database, resets the product tables, and re-runs
// the REAL schema migration — the same statements a server boot applies —
// before seeding fixtures. Never aim this at a database you care about.

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/neutron-build/neutron/mail"
)

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("LULL_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set LULL_TEST_DATABASE_URL to run product integration tests")
	}
	return url
}

// productTables names every table schema.sql creates. The reset drops
// exactly these — product-owned state only. The engine-owned mail_* mirror
// is never dropped here; suites that need it reset through the engine's
// own Drop/Migrate so a shared scratch database loses nothing this suite
// does not own.
var productTables = []string{
	"agent_tokens", "app_settings", "board_cards", "sticky_notes",
	"email_accounts", "hey_messages", "hey_senders", "oauth_states",
	"push_deliveries", "push_subscriptions",
	"auth_passwords", "auth_totp", "auth_recovery_codes", "auth_challenges",
	"auth_sessions", "auth_credentials", "users",
}

func resetProductSchema(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, tbl := range productTables {
		if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS `+tbl+` CASCADE`); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return applyProductSchema(ctx, db)
}

// productPG is a fresh install: clean schema through the real migration
// entry point, one owner row, and an App wired to the live database.
type productPG struct {
	app *App
	db  *sql.DB
	cfg *Config
	uid string
}

func newProductPG(t *testing.T) productPG {
	t.Helper()
	url := testDatabaseURL(t)
	ctx := context.Background()
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := resetProductSchema(ctx, db); err != nil {
		t.Fatalf("product schema reset: %v", err)
	}
	cfg := &Config{SecretKey: "0123456789abcdef0123456789abcdef"}
	app := &App{
		cfg:          cfg,
		db:           db,
		log:          discardLogger(),
		authAttempts: map[string]authAttempt{},
		pwFails:      map[string]passwordFails{},
	}
	var uid string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO users (email,display_name) VALUES ('owner@example.com','Owner') RETURNING id`,
	).Scan(&uid); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	return productPG{app: app, db: db, cfg: cfg, uid: uid}
}

func jsonBody(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rd)
	// A stable per-worker peer keeps the shared auth limiter out of the
	// way; tests that want the limiter set their own address.
	r.RemoteAddr = "192.0.2.1:1111"
	return r
}

func TestProductTableResetMirrorsSchema(t *testing.T) {
	// A table created by schema.sql but missing from the reset list would
	// leak rows between integration tests. Offline on purpose.
	for _, tbl := range productTables {
		if !strings.Contains(schemaSQL, "CREATE TABLE IF NOT EXISTS "+tbl+" ") {
			t.Errorf("table %q is reset but not created by schema.sql", tbl)
		}
	}
}

// The push candidate statement is the OPS-07-named deliverable: the join
// crosses three product/join tables into the engine-owned mirror, and only
// real PostgreSQL can prove the column names and keying are right.
func TestIntegrationPushCandidateSelectsUnreadImboxMail(t *testing.T) {
	url := testDatabaseURL(t)
	p := newProductPG(t)
	ctx := context.Background()

	store, err := mail.Open(ctx, url)
	if err != nil {
		t.Fatalf("engine open: %v", err)
	}
	t.Cleanup(store.Close)
	if err := store.Drop(ctx); err != nil {
		t.Fatalf("engine drop: %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("engine migrate: %v", err)
	}

	acct := mail.AccountID("push-acct")
	if err := store.PutAccount(ctx, &mail.Account{
		ID: acct, Provider: mail.ProviderIMAP, Email: "it@example.com", Name: "Integration",
	}); err != nil {
		t.Fatalf("put account: %v", err)
	}
	msgID := mail.HeaderMessageID("<push-candidate@example.com>")
	if err := store.PutEnvelopes(ctx, acct, []mail.Envelope{{
		ID:         msgID,
		ThreadID:   "thread-push",
		MailboxIDs: []mail.MailboxID{"INBOX"},
		ReceivedAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatalf("put envelope: %v", err)
	}

	sealed, err := sealSecret(p.cfg, "app-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.ExecContext(ctx, `INSERT INTO email_accounts
		(user_id,mirror_account_id,provider,address,cred_ciphertext)
		VALUES ($1,'push-acct','imap','it@example.com',$2)`, p.uid, sealed); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.ExecContext(ctx, `INSERT INTO hey_messages
		(user_id,account_id,message_id,bucket) VALUES ($1,'push-acct',$2,'imbox')`,
		p.uid, string(msgID)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.ExecContext(ctx, `INSERT INTO push_subscriptions
		(endpoint_hash,user_id,subscription_ciphertext) VALUES ('sub-hash',$1,$2)`,
		p.uid, sealed); err != nil {
		t.Fatal(err)
	}

	var accountID, messageID, threadID string
	if err := p.db.QueryRowContext(ctx, pushCandidateSQL, p.uid).Scan(&accountID, &messageID, &threadID); err != nil {
		t.Fatalf("push candidate query: %v", err)
	}
	if accountID != "push-acct" || messageID != string(msgID) || threadID != "thread-push" {
		t.Fatalf("candidate = %q/%q/%q", accountID, messageID, threadID)
	}

	// Delivered mail is no longer push-pending.
	if _, err := p.db.ExecContext(ctx,
		`UPDATE push_deliveries SET delivered_at=now() WHERE user_id=$1 AND subscription_hash='sub-hash'`,
		p.uid); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.ExecContext(ctx,
		`UPDATE hey_messages SET read_at=now() WHERE user_id=$1`, p.uid); err != nil {
		t.Fatal(err)
	}
	err = p.db.QueryRowContext(ctx, pushCandidateSQL, p.uid).Scan(&accountID, &messageID, &threadID)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("read message still a push candidate: %v", err)
	}
}
