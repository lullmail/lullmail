package main

// Durable policy reconciliation (audit DATA-08/SYNC-05, pass 7 DATA-09).
//
// A settings change used to commit the new policy and then run its data
// transition inline: a later failure left the policy committed with an
// error response and no durable status, and widening the retention
// window could never restore older messages an incremental provider
// would not re-report. Now every retention/backfill change commits the
// desired policy, bumps policy_version, and records an
// account_reconcile_jobs row in the SAME transaction. A worker consumes
// the job: full_enumeration jobs complete only after the engine's staged
// rescan finished on every mailbox (nothing pruned, older mail restored,
// SYNC-03's machinery), and applied_policy_version advances only if the
// desired version is still the one this job captured — an older job can
// never mark a newer version applied.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/neutron-build/neutron/mail"
)

// errScansIncomplete marks a reconcile run whose staged scans are still
// in progress: progress is durable, so the job returns to pending rather
// than failing, and the next pass resumes the scans.
var errScansIncomplete = errors.New("reconcile scans still in progress")

// needsRetentionExpansion reports whether the new retention window may
// need to RESTORE messages previously pruned from the mirror: only a move
// away from a bounded window can, because an incremental provider will
// not re-report unchanged old mail (audit SYNC-05/DATA-09).
func needsRetentionExpansion(oldDays, newDays int) bool {
	return oldDays > 0 && (newDays == 0 || newDays > oldDays)
}

type reconcileJob struct {
	AccountID        string
	PolicyVersion    int64
	State            string
	FullEnumeration bool
	LastError        string
	HasError         bool
}

func (j *reconcileJob) asJSON() map[string]any {
	out := map[string]any{
		"state":            j.State,
		"policy_version":   j.PolicyVersion,
		"full_enumeration": j.FullEnumeration,
	}
	if j.HasError {
		out["last_error"] = j.LastError
	}
	return out
}

// upsertReconcileJobTx records (or replaces) the durable job inside the
// policy-change transaction (audit DATA-08). Replacing keeps exactly one
// job per account: a newer setting wins and the worker for the older
// request notices via the version guard at finalize time.
func upsertReconcileJobTx(ctx context.Context, tx *sql.Tx, mirror string, version int64, fullEnumeration bool) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO account_reconcile_jobs (account_id, policy_version, state, full_enumeration, requested_at)
		VALUES ($1, $2, 'pending', $3, now())
		ON CONFLICT (account_id) DO UPDATE SET
		  policy_version = excluded.policy_version,
		  state = 'pending',
		  full_enumeration = excluded.full_enumeration,
		  last_error = NULL,
		  requested_at = now()`,
		mirror, version, fullEnumeration)
	if err != nil {
		return fmt.Errorf("record reconcile job: %w", err)
	}
	return nil
}

// kickReconcileJob runs the durable job for one account on the task group:
// the HTTP response must not depend on a provider enumeration finishing,
// finalization must not outlive shutdown forever, and the drain joins the
// job before the pools close (the same two-sided rule finishSync follows,
// audit OPS-04).
func (a *App) kickReconcileJob(mirror string) {
	a.launch("reconcile-job", func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		if err := a.runReconcileJob(ctx, mirror); err != nil {
			a.log.Error("reconcile job failed", "account", mirror, "err", err)
		}
	})
}

// processReconcileJobs serves every pending or failed job of one owner
// on the background cadence. Serial on purpose: each job may drive a
// provider enumeration.
func (a *App) processReconcileJobs(ctx context.Context, uid string) error {
	rows, err := a.db.QueryContext(ctx, `
		SELECT j.account_id FROM account_reconcile_jobs j
		JOIN email_accounts ea ON ea.mirror_account_id = j.account_id AND ea.user_id = $1
		WHERE j.state IN ('pending','failed')
		ORDER BY j.requested_at`, uid)
	if err != nil {
		return err
	}
	var mirrors []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			rows.Close()
			return err
		}
		mirrors = append(mirrors, m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	var errs []error
	for _, m := range mirrors {
		if ctx.Err() != nil {
			break
		}
		if err := a.runReconcileJob(ctx, m); err != nil {
			a.log.Error("reconcile job failed", "account", m, "err", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// resetStaleReconcileJobs returns 'running' rows to pending at boot: a
// previous process died holding the claim, and the work is resumable by
// construction (staged scans keep their progress).
func (a *App) resetStaleReconcileJobs(ctx context.Context) error {
	_, err := a.db.ExecContext(ctx,
		`UPDATE account_reconcile_jobs SET state = 'pending' WHERE state = 'running'`)
	return err
}

// claimReconcileJob atomically moves a job to running, returning the
// captured policy version and mode.
func (a *App) claimReconcileJob(ctx context.Context, mirror string) (*reconcileJob, error) {
	row := a.db.QueryRowContext(ctx, `
		UPDATE account_reconcile_jobs SET state = 'running'
		WHERE account_id = $1 AND state IN ('pending','failed')
		RETURNING account_id, policy_version, full_enumeration, COALESCE(last_error, '')`,
		mirror)
	var job reconcileJob
	if err := row.Scan(&job.AccountID, &job.PolicyVersion, &job.FullEnumeration, &job.LastError); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &job, nil
}

// finalizeReconcileJob records the outcome. Every write is guarded by the
// job's captured policy_version: if a newer setting replaced the row
// while this worker ran, the guard makes this finalize a no-op and the
// newer job stays pending for its own run.
func (a *App) finalizeReconcileJob(ctx context.Context, job *reconcileJob, jobErr error) error {
	if jobErr != nil {
		state := "failed"
		if errors.Is(jobErr, errScansIncomplete) {
			// Durable progress; retry on the next pass rather than
			// surfacing a failure the user cannot act on.
			state = "pending"
		}
		if _, err := a.db.ExecContext(ctx, `
			UPDATE account_reconcile_jobs SET state = $2, last_error = $3
			WHERE account_id = $1 AND policy_version = $4`,
			job.AccountID, state, jobErr.Error(), job.PolicyVersion); err != nil {
			return err
		}
		return jobErr
	}

	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current int64
	if err := tx.QueryRowContext(ctx,
		`SELECT policy_version FROM email_accounts WHERE mirror_account_id = $1`,
		job.AccountID).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// The account was disconnected mid-job; the job row is gone
			// with it (ON DELETE CASCADE).
			return nil
		}
		return err
	}
	if current != job.PolicyVersion {
		// A newer policy owns the row now. It was rewritten pending; do
		// not mark anything applied (the DATA-08 version race).
		return nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE email_accounts SET applied_policy_version = $2 WHERE mirror_account_id = $1`,
		job.AccountID, job.PolicyVersion); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE account_reconcile_jobs SET state = 'complete', last_error = NULL
		WHERE account_id = $1 AND policy_version = $2`,
		job.AccountID, job.PolicyVersion); err != nil {
		return err
	}
	return tx.Commit()
}

// runReconcileJob consumes one durable reconciliation job end to end.
func (a *App) runReconcileJob(ctx context.Context, mirror string) error {
	job, err := a.claimReconcileJob(ctx, mirror)
	if err != nil {
		return fmt.Errorf("claim reconcile job: %w", err)
	}
	if job == nil {
		return nil
	}
	jobErr := a.executeReconcileJob(ctx, job)
	if ferr := a.finalizeReconcileJob(ctx, job, jobErr); ferr != nil {
		return errors.Join(jobErr, ferr)
	}
	return jobErr
}

func (a *App) executeReconcileJob(ctx context.Context, job *reconcileJob) error {
	acct := mail.AccountID(job.AccountID)

	var uid string
	var retentionDays, backfillDays int
	if err := a.db.QueryRowContext(ctx,
		`SELECT user_id, retention_days, backfill_days FROM email_accounts WHERE mirror_account_id = $1`,
		job.AccountID).Scan(&uid, &retentionDays, &backfillDays); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}

	if job.FullEnumeration {
		if err := a.reconcileByFullEnumeration(ctx, acct); err != nil {
			return err
		}
	}

	// Apply the (possibly restored) data under the current policy and
	// refresh product state for whatever the windows now include. Every
	// step is idempotent, so a retried job converges rather than repeats
	// harm.
	if err := a.applyBackfillWindow(ctx, uid, acct, backfillDays); err != nil {
		return err
	}
	if err := a.applyAccountRetention(ctx, uid, acct, retentionDays); err != nil {
		return err
	}
	if err := a.classifyUser(ctx, uid); err != nil {
		return err
	}
	return nil
}

// reconcileByFullEnumeration drives the engine's staged rescan to
// completion (SYNC-05): older messages the incremental feed would never
// re-report are restored by the enumeration itself, nothing is pruned
// until every mailbox's scan finished (SYNC-03), and the job only
// completes when no scans remain.
func (a *App) reconcileByFullEnumeration(ctx context.Context, acct mail.AccountID) error {
	// The work lease (not a plain use lease) so the enumeration's provider
	// I/O observes the account gate's cancellation: a deletion aborts the
	// rescan instead of waiting out every mailbox page (audit OPS-05).
	gateCtx, releaseUse, ok := a.beginAccountWork(acct)
	if !ok {
		return fmt.Errorf("account %s is being deleted", acct)
	}
	defer releaseUse()
	if ctx.Err() == nil && gateCtx.Err() != nil {
		ctx = gateCtx
	}

	cred, err := a.Token(ctx, acct)
	if err != nil {
		return err
	}
	resolve := a.accountResolver()
	adapter, release, err := resolve(ctx, acct, cred)
	if err != nil {
		return err
	}
	defer release()

	if err := a.eng.RequestRescan(ctx, acct, adapter); err != nil {
		return err
	}
	if _, err := a.eng.SyncAccount(ctx, acct, adapter); err != nil {
		return err
	}
	scans, err := a.store.RunningScans(ctx, acct)
	if err != nil {
		return err
	}
	if len(scans) > 0 {
		return errScansIncomplete
	}
	return nil
}
