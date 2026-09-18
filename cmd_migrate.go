package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/neutron-build/neutron/mail"
)

// migrate converges both schema owners through their versioned runners
// (audit OPS-02). The engine runs FIRST: the product's account-scope
// migration (version 2) reads mail_messages, which the engine owns, so
// the engine tables must exist before the product ledger advances. The
// order is fixed here and in connectApp — two fixed orders are what make
// the two runners' advisory locks deadlock-free.
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
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
	return runProductMigrations(ctx, db)
}
