package main

import (
	"bytes"
	"io"
	stdmail "net/mail"
	"strings"
	"testing"
	"time"

	nmail "github.com/neutron-build/neutron/mail"
)

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
	if !strings.Contains(parsed.Header.Get("X-Email-Soft-Export-Warning"), "mirror") {
		t.Fatalf("missing fallback warning: %q", parsed.Header.Get("X-Email-Soft-Export-Warning"))
	}
	if parsed.Header.Get("X-Email-Soft-Attachment-Warning") == "" {
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
