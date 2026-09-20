package main

// Versioned product migrations (audit OPS-02, pass 7). The boot path used
// to re-execute schema.sql plus the account-scope statement list on every
// start with no ledger and no lock; this runner replaces it. The design
// contract is the engine runner's (mail-engine/migrate.go): a version
// ledger applied exactly once, checksums that refuse a modified applied
// migration, and a session advisory lock serializing concurrent runners.
//
// Convergence contract: version 1 is schema.sql verbatim (every statement
// idempotent), so an existing database records it as applied without
// doing anything; versions 2+ are real one-time transitions. Boot order
// everywhere is ENGINE migrations first, then product: version 2 reads
// mail_messages, which the engine owns.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// productLockKey is the product runner's advisory-lock key, distinct from
// the engine runner's and from every per-account maintenance key.
const productLockKey = 7812634095511101

type productMigration struct {
	Version    int64
	Name       string
	Statements []string
}

// accountScopedStatements moved verbatim from the old boot-time
// migrateAccountScopedState: early builds keyed product state by message
// id alone, but provider ids are only account-scoped.
var accountScopedStatements = []string{
	`UPDATE hey_messages h SET account_id = (
		SELECT min(m.account_id) FROM mail_messages m
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = h.user_id
		WHERE m.id = h.message_id
	) WHERE h.account_id IS NULL`,
	`DELETE FROM hey_messages WHERE account_id IS NULL`,
	`ALTER TABLE hey_messages ALTER COLUMN account_id SET NOT NULL`,
	`ALTER TABLE hey_messages DROP CONSTRAINT IF EXISTS hey_messages_pkey`,
	`CREATE UNIQUE INDEX IF NOT EXISTS hey_messages_identity ON hey_messages (user_id, account_id, message_id)`,
	`UPDATE push_deliveries p SET account_id = (
		SELECT min(m.account_id) FROM mail_messages m
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = p.user_id
		WHERE m.id = p.message_id
	) WHERE p.account_id IS NULL`,
	`DELETE FROM push_deliveries WHERE account_id IS NULL`,
	`ALTER TABLE push_deliveries ALTER COLUMN account_id SET NOT NULL`,
	`ALTER TABLE push_deliveries DROP CONSTRAINT IF EXISTS push_deliveries_pkey`,
	`DROP INDEX IF EXISTS push_deliveries_identity`,
	`CREATE UNIQUE INDEX IF NOT EXISTS push_deliveries_per_subscription
		ON push_deliveries (user_id, account_id, message_id, subscription_hash)`,
	`UPDATE board_cards b SET account_id = (
		SELECT min(m.account_id) FROM mail_messages m
		JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = b.user_id
		WHERE m.thread_id = b.thread_key
	) WHERE b.thread_key IS NOT NULL AND b.account_id IS NULL`,
	`DROP INDEX IF EXISTS board_cards_one_pin`,
	`CREATE UNIQUE INDEX IF NOT EXISTS board_cards_one_account_pin ON board_cards (user_id, account_id, thread_key) WHERE thread_key IS NOT NULL`,
}

// policyStatements is the DATA-08/SYNC-05 machinery: desired policy is
// versioned separately from applied policy, and every policy change
// records a durable reconciliation job in the same transaction.
var policyStatements = []string{
	`ALTER TABLE email_accounts ADD COLUMN IF NOT EXISTS policy_version bigint NOT NULL DEFAULT 0`,
	`ALTER TABLE email_accounts ADD COLUMN IF NOT EXISTS applied_policy_version bigint NOT NULL DEFAULT 0`,
	`CREATE TABLE IF NOT EXISTS account_reconcile_jobs (
		account_id        text PRIMARY KEY
			REFERENCES email_accounts (mirror_account_id) ON DELETE CASCADE,
		policy_version    bigint NOT NULL,
		state             text NOT NULL CHECK (state IN ('pending','running','failed','complete')),
		full_enumeration  boolean NOT NULL DEFAULT false,
		last_error        text,
		requested_at      timestamptz NOT NULL DEFAULT now()
	)`,
}

// idempotencyStatements is the WEB-04/WEB-03/R07 machinery: one recorded
// response per (user, Idempotency-Key). A row exists only in its complete
// form — the recorder inserts, runs the handler, and stores the response in
// ONE transaction, so a crash mid-request leaves no row and the retry
// applies cleanly; a committed row is always a replayable answer.
var idempotencyStatements = []string{
	`CREATE TABLE IF NOT EXISTS api_mutations (
		user_id                uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		mutation_key           text NOT NULL,
		request_hash           text NOT NULL,
		response_status        integer NOT NULL,
		response_content_type  text NOT NULL DEFAULT 'application/json',
		response_body          bytea NOT NULL,
		created_at             timestamptz NOT NULL DEFAULT now(),
		PRIMARY KEY (user_id, mutation_key)
	)`,
}

// reauthStatements is the AUTH-02/AUTH-06 machinery: sessions record when
// the owner last re-proved the credential (reauthenticated_at), and failed
// standalone-TOTP guesses accumulate in a durable per-user fixed window so
// distributed guessing across peers hits one shared account budget.
var reauthStatements = []string{
	`ALTER TABLE auth_sessions ADD COLUMN IF NOT EXISTS reauthenticated_at timestamptz`,
	`CREATE TABLE IF NOT EXISTS auth_factor_windows (
		user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		factor       text NOT NULL,
		window_start timestamptz NOT NULL,
		attempts     integer NOT NULL CHECK (attempts > 0),
		PRIMARY KEY (user_id, factor, window_start)
	)`,
}

var productMigrations = []productMigration{
	{Version: 1, Name: "product baseline schema", Statements: splitStatements(schemaSQL)},
	{Version: 2, Name: "account-scoped product state", Statements: accountScopedStatements},
	{Version: 3, Name: "reconciliation policy machinery", Statements: policyStatements},
	{Version: 4, Name: "mutation idempotency ledger", Statements: idempotencyStatements},
	{Version: 5, Name: "reauthentication and factor budgets", Statements: reauthStatements},
	{Version: 6, Name: "reconcile job claim stamps", Statements: reconcileClaimStatements},
}

// reconcileClaimStatements stamps account_reconcile_jobs with the claim
// time (audit 5 SYNC-06): a worker that dies or cannot finalize leaves a
// 'running' row, and the periodic pass can now tell a stale claim from a
// live one and requeue it — boot-time reset alone never recovered a job
// wedged inside a still-serving process.
var reconcileClaimStatements = []string{
	`ALTER TABLE account_reconcile_jobs ADD COLUMN IF NOT EXISTS claimed_at timestamptz`,
}

const productLedgerDDL = `CREATE TABLE IF NOT EXISTS app_migrations (
	version    BIGINT PRIMARY KEY,
	name       TEXT NOT NULL,
	checksum   TEXT NOT NULL,
	applied_at TIMESTAMP NOT NULL
)`

func productMigrationChecksum(stmts []string) string {
	h := sha256.New()
	for _, s := range stmts {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// runProductMigrations converges the product schema through the versioned
// ledger under the runner's advisory lock. The lock is taken BEFORE the
// ledger bootstrap: two concurrent CREATE TABLE IF NOT EXISTS race on the
// PostgreSQL catalog even with the guard.
func runProductMigrations(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	supported, err := productAdvisoryCheck(ctx, conn)
	if err != nil {
		return fmt.Errorf("product migration lock: %w", err)
	}
	if supported {
		if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, productLockKey); err != nil {
			return fmt.Errorf("product migration lock: %w", err)
		}
		defer productReleaseMigrationLock(ctx, conn)
	}

	if _, err := conn.ExecContext(ctx, productLedgerDDL); err != nil {
		return fmt.Errorf("product migration ledger: %w", err)
	}

	applied := map[int64]string{}
	rows, err := conn.QueryContext(ctx, `SELECT version, checksum FROM app_migrations`)
	if err != nil {
		return fmt.Errorf("product migration ledger read: %w", err)
	}
	for rows.Next() {
		var version int64
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			rows.Close()
			return err
		}
		applied[version] = checksum
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, m := range productMigrations {
		sum, ok := applied[m.Version]
		if !ok {
			if err := applyProductMigration(ctx, conn, m); err != nil {
				return err
			}
			continue
		}
		if sum != productMigrationChecksum(m.Statements) {
			return fmt.Errorf("product migration %d (%s) was modified after application; refusing to boot", m.Version, m.Name)
		}
	}
	return nil
}

func applyProductMigration(ctx context.Context, conn *sql.Conn, m productMigration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("product migration %d begin: %w", m.Version, err)
	}
	defer tx.Rollback()
	for _, stmt := range m.Statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("product migration %d (%s): %w\nstatement: %.80s", m.Version, m.Name, err, stmt)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO app_migrations (version, name, checksum, applied_at) VALUES ($1, $2, $3, $4)`,
		m.Version, m.Name, productMigrationChecksum(m.Statements), time.Now().UTC()); err != nil {
		return fmt.Errorf("product migration %d ledger: %w", m.Version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("product migration %d commit: %w", m.Version, err)
	}
	return nil
}

var productAdvisoryUnsupported, productAdvisorySupported atomic.Bool

// productAdvisoryCheck probes pg_advisory_lock support once per process
// (the product always runs on PostgreSQL; the probe exists so exotic
// backends fail softly rather than deadlock the boot).
func productAdvisoryCheck(ctx context.Context, conn *sql.Conn) (bool, error) {
	if productAdvisoryUnsupported.Load() {
		return false, nil
	}
	if productAdvisorySupported.Load() {
		return true, nil
	}
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, productLockKey); err != nil {
		productAdvisoryUnsupported.Store(true)
		return false, nil
	}
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, productLockKey); err != nil {
		return false, err
	}
	productAdvisorySupported.Store(true)
	return true, nil
}

// productReleaseMigrationLock drops the session lock; on failure the
// connection is marked bad so the pool discards it rather than returning
// a locked connection (the audit's exact warning).
func productReleaseMigrationLock(ctx context.Context, conn *sql.Conn) {
	if _, err := conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, productLockKey); err != nil {
		_ = conn.Raw(func(driverConn any) error { return driver.ErrBadConn })
	}
}

func splitStatements(s string) []string {
	// Full-line comments out first: they may contain semicolons, and the
	// splitter has no parser.
	var kept []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		kept = append(kept, line)
	}
	var out []string
	for _, part := range strings.Split(strings.Join(kept, "\n"), ";") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}
