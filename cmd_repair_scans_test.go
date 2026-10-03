package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/neutron-build/neutron/mail"
)

func TestRepairScansRequiresSpecificTargetForMutation(t *testing.T) {
	var out bytes.Buffer
	if err := repairScansWithOutput([]string{"--apply"}, &out); err == nil || !strings.Contains(err.Error(), "exact --scan ID") {
		t.Fatalf("mutation without a target: %v", err)
	}
}

func TestIntegrationRepairScanPreservesLiveStateAndGeneration(t *testing.T) {
	p := newProductPG(t)
	ctx := context.Background()
	acct := mail.AccountID("repair-account")
	if err := p.app.store.PutAccount(ctx, &mail.Account{ID: acct, Provider: mail.ProviderIMAP, Email: "synthetic@example.test"}); err != nil {
		t.Fatal(err)
	}
	if err := p.app.store.PutEnvelopes(ctx, acct, []mail.Envelope{{ID: "n:kept", MailboxIDs: []mail.MailboxID{"INBOX"}}}); err != nil {
		t.Fatal(err)
	}
	if err := p.app.store.PutBody(ctx, acct, &mail.Body{MessageID: "n:kept", Text: "cached private body"}); err != nil {
		t.Fatal(err)
	}
	if err := p.app.store.PutCursor(ctx, acct, "INBOX", "live-cursor"); err != nil {
		t.Fatal(err)
	}
	scan, err := p.app.store.BeginScanGeneration(ctx, acct, "INBOX", 17)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.app.store.ApplyScanPage(ctx, scan.ID, nil, []mail.MessageID{"n:kept"}, nil, "lost-terminal-evidence"); err != nil {
		t.Fatal(err)
	}
	replacement, err := restartStagedScan(ctx, p.db, string(scan.ID))
	if err != nil {
		t.Fatal(err)
	}
	if replacement == "" || replacement == string(scan.ID) {
		t.Fatal("scan identity was not replaced")
	}
	current, err := p.app.store.RunningScan(ctx, acct, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if current.Generation != 17 || current.Continuation != "" || string(current.ID) != replacement {
		t.Fatalf("reset contract lost: %+v", current)
	}
	if _, err := p.app.store.Envelope(ctx, acct, "n:kept"); err != nil {
		t.Fatal("repair destroyed live mail", err)
	}
	if err := p.app.store.ApplyScanPage(ctx, scan.ID, []mail.Envelope{{ID: "stale", MailboxIDs: []mail.MailboxID{"INBOX"}}}, nil, nil, "old"); err == nil {
		t.Fatal("in-flight retired scan was allowed to write")
	}
	if _, err := p.app.store.Envelope(ctx, acct, "stale"); err == nil {
		t.Fatal("stale page changed live state")
	}
	if _, err := p.app.store.FinishScan(ctx, acct, "INBOX", scan.ID, "stale-terminal"); err == nil {
		t.Fatal("retired scan finalized")
	}
	if _, err := p.app.store.ApplyFinalScanPage(ctx, scan.ID, nil, nil, nil, "stale-terminal"); err == nil {
		t.Fatal("retired scan final page applied")
	}
	if cursor, err := p.app.store.Cursor(ctx, acct, "INBOX"); err != nil || cursor != "live-cursor" {
		t.Fatalf("live cursor changed: %s %v", cursor, err)
	}
	if body, err := p.app.store.Body(ctx, acct, "n:kept"); err != nil || body.Text != "cached private body" {
		t.Fatalf("cached body changed: %+v %v", body, err)
	}
	var seen int
	if err := p.db.QueryRow(`SELECT COUNT(*) FROM mirror_scan_seen WHERE scan_id IN ($1,$2)`, string(scan.ID), replacement).Scan(&seen); err != nil || seen != 0 {
		t.Fatalf("old seen set leaked into restart: %d %v", seen, err)
	}
	again, err := restartStagedScan(ctx, p.db, string(scan.ID))
	if err != nil || again != "" {
		t.Fatalf("retired target restarted a newer scan: %s %v", again, err)
	}
}

func TestIntegrationRepairScanRollbackPreservesStagedProgress(t *testing.T) {
	p := newProductPG(t)
	ctx := context.Background()
	acct := mail.AccountID("repair-rollback")
	if err := p.app.store.PutAccount(ctx, &mail.Account{ID: acct, Provider: mail.ProviderIMAP, Email: "synthetic@example.test"}); err != nil {
		t.Fatal(err)
	}
	scan, err := p.app.store.BeginScanGeneration(ctx, acct, "INBOX", 23)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.app.store.ApplyScanPage(ctx, scan.ID, nil, []mail.MessageID{"seen"}, nil, "old-page"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`CREATE FUNCTION reject_repair_seen_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic rollback'; END $$; CREATE TRIGGER reject_repair_seen_delete BEFORE DELETE ON mirror_scan_seen FOR EACH ROW EXECUTE FUNCTION reject_repair_seen_delete()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		p.db.Exec(`DROP TRIGGER IF EXISTS reject_repair_seen_delete ON mirror_scan_seen; DROP FUNCTION IF EXISTS reject_repair_seen_delete()`)
	})
	if _, err := restartStagedScan(ctx, p.db, string(scan.ID)); err == nil {
		t.Fatal("forced delete failure did not abort repair")
	}
	current, err := p.app.store.RunningScan(ctx, acct, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != scan.ID || current.Continuation != "old-page" || current.Generation != 23 {
		t.Fatalf("failed repair changed scan: %+v", current)
	}
	var seen int
	if err := p.db.QueryRow(`SELECT COUNT(*) FROM mirror_scan_seen WHERE scan_id=$1`, string(scan.ID)).Scan(&seen); err != nil || seen != 1 {
		t.Fatalf("failed repair lost seen rows: %d %v", seen, err)
	}
}
