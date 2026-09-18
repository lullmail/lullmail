package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/neutron-build/neutron/mail"
)

// migrate converges both the product schema and the mail mirror schema. The
// product DDL still never owns mail_* tables; it runs first because its tables
// are independent, then the engine migration and cross-layer compatibility
// migrations run in the same order as a normal server boot.
func migrate() error {
	url := osGetenv("DATABASE_URL")
	if url == "" {
		return fmt.Errorf("DATABASE_URL not set")
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return err
	}
	if err := applyProductSchema(ctx, db); err != nil {
		return err
	}
	store, err := mail.Open(ctx, url)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		return err
	}
	return migrateAccountScopedState(ctx, db)
}

// applyProductSchema converges the product schema. Every statement is
// idempotent, and the whole file now applies inside ONE transaction: a
// half-applied migration is the recorded lockout risk for additive column
// pairs (auth_totp.last_used_step, the auth_epochs), because a table left
// without its column turns every insert or lookup on it into an error
// until the next boot converges. All-or-nothing closes that window
// (audit AUTH-05 remainder); the server still only serves after the
// migration succeeds, so a failed application degrades exactly as before.
func applyProductSchema(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range splitStatements(schemaSQL) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%w\nstatement: %.80s", err, stmt)
		}
	}
	return tx.Commit()
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
