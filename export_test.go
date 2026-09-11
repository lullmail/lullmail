package main

import (
	"archive/zip"
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	stdmail "net/mail"
	"strings"
	"testing"
	"time"

	nmail "github.com/neutron-build/neutron/mail"
)

func TestAccountExportFallsBackToMirrorWhenProviderUnreachable(t *testing.T) {
	// A resolver failure must degrade the export to the local mirror, not
	// 502: the product promise is that a user's exit never depends on a
	// healthy upstream. The credential below is an IMAP account with no
	// host, so the resolver refuses it before any dialing.
	cfg := &Config{SecretKey: "0123456789abcdef0123456789abcdef"}
	sealed, err := sealSecret(cfg, "password")
	if err != nil {
		t.Fatal(err)
	}
	a := &App{
		cfg: cfg,
		log: discardLogger(),
		db: openStepDB(t,
			dbStep{kind: "query", rows: &testRows{
				columns: []string{"mirror_account_id", "address"},
				values:  [][]driver.Value{{"mirror-1", "user@example.com"}},
			}},
			dbStep{kind: "query", rows: emptyRows("id", "name")},
			dbStep{kind: "query", rows: &testRows{
				columns: []string{"provider", "address", "username", "host", "port", "cred_ciphertext"},
				values:  [][]driver.Value{{"imap", "user@example.com", "user", "", int64(0), sealed}},
			}},
		),
	}
	w := httptest.NewRecorder()
	a.handleAccountExport(w, requestAsOwner(http.MethodGet, "/api/accounts/1/export"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/zip" {
		t.Fatalf("content type = %q, want application/zip", got)
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatalf("response is not a readable zip: %v", err)
	}
	var manifest exportManifest
	for _, f := range zr.File {
		if f.Name != "export-manifest.json" {
			continue
		}
		rc, _ := f.Open()
		raw, _ := io.ReadAll(rc)
		rc.Close()
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatalf("manifest is not JSON: %v", err)
		}
	}
	if len(manifest.Warnings) == 0 || !strings.Contains(manifest.Warnings[0], "local mirror") {
		t.Fatalf("manifest does not note mirror-only mode: %v", manifest.Warnings)
	}
}

func TestWriteMboxRDEscapesOnlyBodyFromLines(t *testing.T) {
	envelope := &nmail.Envelope{
		From:   []nmail.Address{{Email: "sender@example.com"}},
		SentAt: time.Date(2026, time.August, 25, 10, 11, 12, 0, time.UTC),
	}
	raw := []byte("From: sender@example.com\r\nSubject: test\r\n\r\nFrom body\r\n>From quoted\r\nordinary")
	var got bytes.Buffer
	if err := writeMboxRD(&got, envelope, raw); err != nil {
		t.Fatal(err)
	}
	want := "From sender@example.com Tue Aug 25 10:11:12 2026\n" +
		"From: sender@example.com\nSubject: test\n\n" +
		">From body\n>>From quoted\nordinary\n\n"
	if got.String() != want {
		t.Fatalf("mboxrd mismatch\n--- got ---\n%s\n--- want ---\n%s", got.String(), want)
	}
}

func TestMirrorEMLIsParseableAndHonestAboutFallback(t *testing.T) {
	envelope := &nmail.Envelope{
		ID:              "h:abc",
		From:            []nmail.Address{{Name: "Sender", Email: "sender@example.com"}},
		To:              []nmail.Address{{Email: "you@example.com"}},
		Subject:         "A useful subject",
		SentAt:          time.Date(2026, time.August, 25, 10, 0, 0, 0, time.UTC),
		MessageIDHeader: "<real@example.com>",
		HasAttachment:   true,
	}
	body := &nmail.Body{Text: "hello", HTML: "<p>hello</p>"}
	raw := mirrorEML(envelope, body, io.ErrUnexpectedEOF)
	parsed, err := stdmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("fallback is not valid RFC 5322: %v\n%s", err, raw)
	}
	if parsed.Header.Get("Message-ID") != "<real@example.com>" {
		t.Fatalf("message-id = %q", parsed.Header.Get("Message-ID"))
	}
	if !strings.Contains(parsed.Header.Get("X-Lullmail-Export-Warning"), "mirror") {
		t.Fatalf("missing fallback warning: %q", parsed.Header.Get("X-Lullmail-Export-Warning"))
	}
	if parsed.Header.Get("X-Lullmail-Attachment-Warning") == "" {
		t.Fatal("attachment loss was not disclosed")
	}
	if !strings.HasPrefix(parsed.Header.Get("Content-Type"), "multipart/alternative") {
		t.Fatalf("content-type = %q", parsed.Header.Get("Content-Type"))
	}
}

func TestSafeExportNameCannotCreateArchivePaths(t *testing.T) {
	got := safeExportName(" ../Work/Receipts:\n2026 ")
	if got != "Work-Receipts-2026" {
		t.Fatalf("safeExportName = %q", got)
	}
	if strings.ContainsAny(got, "/\\") {
		t.Fatalf("unsafe separator in %q", got)
	}
}

func TestAppendExportWarningIsBounded(t *testing.T) {
	var warnings []string
	for i := 0; i < 130; i++ {
		warnings = appendExportWarning(warnings, "warning")
	}
	if len(warnings) != 101 {
		t.Fatalf("got %d warnings, want 101", len(warnings))
	}
	if warnings[100] != "Further warnings omitted from this manifest." {
		t.Fatalf("missing truncation marker: %q", warnings[100])
	}
}
