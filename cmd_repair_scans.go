package main

// Operator-controlled repair for scans stranded before atomic terminal-page
// completion. Completion evidence is missing, so restart enumeration; never
// infer completeness from a continuation cursor and never prune live mail here.

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/neutron-build/neutron/mail"
)

func repairScans(args []string) error { return repairScansWithOutput(args, os.Stdout) }
func repairScansWithOutput(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("repair-scans", flag.ContinueOnError)
	id := flags.String("scan", "", "exact scan ID to inspect or restart")
	apply := flags.Bool("apply", false, "restart this scan non-destructively; otherwise list only")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *apply && *id == "" {
		return errors.New("--apply requires one exact --scan ID from the inspection output")
	}
	url := osGetenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL not set")
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if *apply {
		replacement, err := restartStagedScan(ctx, db, *id)
		if err != nil {
			return err
		}
		if replacement == "" {
			fmt.Fprintln(out, "scan no longer exists; nothing changed")
			return nil
		}
		fmt.Fprintf(out, "restarted %s as %s; live mail, cursor and policy generation preserved\n", *id, replacement)
		return nil
	}
	rows, err := db.QueryContext(ctx, `SELECT id,account_id,mailbox_id,generation,started_at FROM mirror_scans WHERE ($1='' OR id=$1) ORDER BY account_id,mailbox_id`, *id)
	if err != nil {
		return err
	}
	defer rows.Close()
	fmt.Fprintln(out, "Inspection only. A running scan may be healthy; age alone is not evidence of stranding.")
	for rows.Next() {
		var id, account, box string
		var generation int64
		var started sql.NullTime
		if err := rows.Scan(&id, &account, &box, &generation, &started); err != nil {
			return err
		}
		fmt.Fprintf(out, "scan=%q account=%q mailbox=%q generation=%d started=%s\n", id, account, box, generation, started.Time.UTC().Format(time.RFC3339))
	}
	return rows.Err()
}

func restartStagedScan(ctx context.Context, db *sql.DB, id string) (string, error) {
	var account string
	if err := db.QueryRowContext(ctx, `SELECT account_id FROM mirror_scans WHERE id=$1`, id).Scan(&account); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	// Same advisory lock as scan staging, policy reconciliation and account
	// deletion. An in-flight old page rechecks its now-retired ID after this.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, mail.AccountLockKey(mail.AccountID(account))); err != nil {
		return "", err
	}
	replacement := "scan-repair-" + newID()
	res, err := tx.ExecContext(ctx, `UPDATE mirror_scans SET id=$3,continuation='',started_at=$4 WHERE id=$1 AND account_id=$2`, id, account, replacement, time.Now().UTC())
	if err != nil {
		return "", err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM mirror_scan_seen WHERE scan_id=$1`, id); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return replacement, nil
}
