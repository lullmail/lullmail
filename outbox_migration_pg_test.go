package main

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// revertToSchema7 returns the database to exactly what a pre-outbox release
// left: migration 8's table gone and its ledger row removed. Migration 8 only
// creates outbox objects, so this is also the documented rollback procedure
// (docs/durable-outbox.md).
func revertToSchema7(t *testing.T, p productPG) {
	t.Helper()
	for _, q := range []string{`DROP TABLE IF EXISTS outbox_jobs CASCADE`, `DELETE FROM app_migrations WHERE version=8`} {
		if _, err := p.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func outboxObjects(t *testing.T, p productPG) (columns, indexes map[string]bool) {
	t.Helper()
	columns, indexes = map[string]bool{}, map[string]bool{}
	rows, err := p.db.Query(`SELECT column_name FROM information_schema.columns WHERE table_name='outbox_jobs'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var c string
		rows.Scan(&c)
		columns[c] = true
	}
	rows.Close()
	rows, err = p.db.Query(`SELECT indexname FROM pg_indexes WHERE tablename='outbox_jobs'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var i string
		rows.Scan(&i)
		indexes[i] = true
	}
	rows.Close()
	return
}

func TestMigration8UpgradesASchema7DatabaseAndIsIdempotent(t *testing.T) {
	p := newProductPG(t)
	ctx := context.Background()
	if productMigrations[len(productMigrations)-1].Version != 8 {
		t.Fatalf("this test is written for the outbox as the newest migration")
	}
	if _, err := p.db.Exec(`INSERT INTO sticky_notes(user_id,x,y,text,color) VALUES($1,1,2,'survives the upgrade',0)`, p.uid); err != nil {
		t.Fatal(err)
	}
	revertToSchema7(t, p)
	if cols, _ := outboxObjects(t, p); len(cols) != 0 {
		t.Fatal("revert left outbox columns behind")
	}

	if err := runProductMigrations(ctx, p.db); err != nil {
		t.Fatalf("upgrade 7 -> 8: %v", err)
	}
	cols, idx := outboxObjects(t, p)
	for _, c := range []string{"id", "user_id", "account_id", "submission_key", "request_hash", "state", "payload_ciphertext", "payload_bytes", "undo_until", "attempt_id", "started_at", "filing_state", "sent_ciphertext", "error_code"} {
		if !cols[c] {
			t.Errorf("column %s missing", c)
		}
	}
	for _, i := range []string{"outbox_pending", "outbox_live", "outbox_settled", "outbox_payload", "outbox_jobs_pkey", "outbox_jobs_user_id_submission_key_key"} {
		if !idx[i] {
			t.Errorf("index %s missing (have %v)", i, idx)
		}
	}
	var note string
	if err := p.db.QueryRow(`SELECT text FROM sticky_notes WHERE user_id=$1`, p.uid).Scan(&note); err != nil || note != "survives the upgrade" {
		t.Fatalf("existing data lost in the upgrade: %q %v", note, err)
	}
	var sum, name string
	var appliedAt string
	if err := p.db.QueryRow(`SELECT checksum,name,applied_at::text FROM app_migrations WHERE version=8`).Scan(&sum, &name, &appliedAt); err != nil {
		t.Fatal(err)
	}
	if sum != productMigrationChecksum(outboxStatements) {
		t.Fatalf("ledger checksum %s does not match the migration", sum)
	}

	// Work in flight survives, and re-running the migration is a no-op.
	seedOutboxAccount(t, p)
	row := seedOutbox(t, p, "across-rerun")
	for i := 0; i < 3; i++ {
		if err := runProductMigrations(ctx, p.db); err != nil {
			t.Fatalf("rerun %d: %v", i, err)
		}
	}
	var sum2, applied2 string
	p.db.QueryRow(`SELECT checksum,applied_at::text FROM app_migrations WHERE version=8`).Scan(&sum2, &applied2)
	var n int
	p.db.QueryRow(`SELECT count(*) FROM app_migrations WHERE version=8`).Scan(&n)
	if n != 1 || sum2 != sum || applied2 != appliedAt {
		t.Fatalf("rerun rewrote the ledger: n=%d", n)
	}
	var state string
	if err := p.db.QueryRow(`SELECT state FROM outbox_jobs WHERE id=$1`, row.ID).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("migration rerun disturbed a pending send: %q %v", state, err)
	}
}

func TestMigration8RefusesAModifiedLedgerEntry(t *testing.T) {
	p := newProductPG(t)
	if _, err := p.db.Exec(`UPDATE app_migrations SET checksum='tampered' WHERE version=8`); err != nil {
		t.Fatal(err)
	}
	err := runProductMigrations(context.Background(), p.db)
	if err == nil || !strings.Contains(err.Error(), "modified after application") {
		t.Fatalf("a changed outbox migration was accepted: %v", err)
	}
	if _, err := p.db.Exec(`UPDATE app_migrations SET checksum=$1 WHERE version=8`, productMigrationChecksum(outboxStatements)); err != nil {
		t.Fatal(err)
	}
	if err := runProductMigrations(context.Background(), p.db); err != nil {
		t.Fatalf("restored ledger rejected: %v", err)
	}
}

// A statement failing partway through migration 8 must leave schema 7 intact:
// no half-built table, no ledger row, and a later retry succeeds.
func TestMigration8IsAtomic(t *testing.T) {
	p := newProductPG(t)
	revertToSchema7(t, p)
	// The fourth statement creates outbox_settled; make it collide.
	if _, err := p.db.Exec(`CREATE INDEX outbox_settled ON sticky_notes(created_at)`); err != nil {
		t.Fatal(err)
	}
	if err := runProductMigrations(context.Background(), p.db); err == nil {
		t.Fatal("a failing statement did not fail the migration")
	}
	if cols, _ := outboxObjects(t, p); len(cols) != 0 {
		t.Fatalf("partial migration left a table behind: %v", cols)
	}
	var n int
	p.db.QueryRow(`SELECT count(*) FROM app_migrations WHERE version=8`).Scan(&n)
	if n != 0 {
		t.Fatal("a failed migration was recorded as applied")
	}
	if _, err := p.db.Exec(`DROP INDEX outbox_settled`); err != nil {
		t.Fatal(err)
	}
	if err := runProductMigrations(context.Background(), p.db); err != nil {
		t.Fatalf("retry after fixing the cause: %v", err)
	}
	if cols, _ := outboxObjects(t, p); !cols["id"] {
		t.Fatal("retry did not create the table")
	}
}

// Rolling back is the manual procedure documented for operators: drop the
// table, remove the ledger row. It must leave every other product table alone
// and be reversible by simply migrating again.
func TestMigration8RollbackAndReapply(t *testing.T) {
	p := newProductPG(t)
	tables := func() int {
		var n int
		p.db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name <> 'outbox_jobs'`).Scan(&n)
		return n
	}
	before := tables()
	revertToSchema7(t, p)
	if tables() != before {
		t.Fatalf("rollback touched other tables: %d -> %d", before, tables())
	}
	var top int64
	p.db.QueryRow(`SELECT max(version) FROM app_migrations`).Scan(&top)
	if top != 7 {
		t.Fatalf("ledger after rollback ends at %d", top)
	}
	if err := runProductMigrations(context.Background(), p.db); err != nil {
		t.Fatal(err)
	}
	p.db.QueryRow(`SELECT max(version) FROM app_migrations`).Scan(&top)
	if top != 8 || tables() != before {
		t.Fatalf("reapply: ledger %d, tables %d/%d", top, tables(), before)
	}
}

func TestMigration8ConcurrentRunnersApplyOnce(t *testing.T) {
	p := newProductPG(t)
	revertToSchema7(t, p)
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- runProductMigrations(context.Background(), p.db)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent runner: %v", err)
		}
	}
	var n int
	p.db.QueryRow(`SELECT count(*) FROM app_migrations WHERE version=8`).Scan(&n)
	if n != 1 {
		t.Fatalf("ledger rows for version 8: %d", n)
	}
}

// A database that a newer build has migrated beyond this one still boots this
// build's migration runner (the fence, not the runner, keeps it from sending).
func TestMigrationRunnerToleratesANewerLedger(t *testing.T) {
	p := newProductPG(t)
	if _, err := p.db.Exec(`INSERT INTO app_migrations(version,name,checksum,applied_at) VALUES(99,'future','x',now())`); err != nil {
		t.Fatal(err)
	}
	if err := runProductMigrations(context.Background(), p.db); err != nil {
		t.Fatalf("older build refused to boot against a newer ledger: %v", err)
	}
}
