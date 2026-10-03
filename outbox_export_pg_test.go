package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func exportZip(t *testing.T, p productPG, uid string, agent bool) (map[string]string, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/personal/export", nil)
	if agent {
		r.Header.Set("Authorization", "Bearer lull_synthetic")
	}
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, uid))
	w := httptest.NewRecorder()
	p.app.handlePersonalExport(w, r)
	if w.Code != 200 {
		t.Fatalf("export=%d %s", w.Code, w.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(b)
	}
	var manifest map[string]any
	if err := json.Unmarshal([]byte(files["export-manifest.json"]), &manifest); err != nil {
		t.Fatal(err)
	}
	return files, manifest
}

func seedComposition(t *testing.T, p productPG, uid, account, key, state, filing string) string {
	t.Helper()
	id := uuid.NewString()
	req := `{"to":"friend@example.test","cc":"","bcc":"","subject":"Subject ` + key + `","text":"private body ` + key + `","html":"","account_id":"x","reply_to_message_id":"","attachments":[{"filename":"note.txt","content_type":"text/plain","data_base64":"` + base64.StdEncoding.EncodeToString([]byte("attached bytes")) + `"}]}`
	payload, _ := json.Marshal(outboxPayload{Request: json.RawMessage(req)})
	cipher, err := sealBound(p.cfg, "payload", uid, id, string(payload))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.app.saveOutbox(context.Background(), uid, account, key, "h", id, cipher, int64(len(cipher))); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`UPDATE outbox_jobs SET state=$2,filing_state=$3 WHERE id=$1`, id, state, filing); err != nil {
		t.Fatal(err)
	}
	return id
}

// Unsent and unknown-outcome compositions have no provider original, so the
// owner's export carries them; another owner's and an agent's export do not.
func TestIntegrationOutboxCompositionsAreInTheOwnersExport(t *testing.T) {
	p := newProductPG(t)
	seedOutboxAccount(t, p)
	other, otherAcct := secondOwner(t, p, "other@example.test")
	failed := seedComposition(t, p, p.uid, "outbox-acct", "failed-one", "failed", "not_started")
	ambiguous := seedComposition(t, p, p.uid, "outbox-acct", "ambiguous-one", "ambiguous", "not_started")
	theirs := seedComposition(t, p, other, otherAcct, "theirs", "failed", "not_started")

	// A submitted message whose Sent filing did not complete: the saved copy is
	// the only local record.
	unfiled := uuid.NewString()
	sentCipher, _ := sealBound(p.cfg, "sent", p.uid, unfiled, "Subject: unfiled\r\n\r\nthe accepted bytes")
	if _, _, err := p.app.saveOutbox(context.Background(), p.uid, "outbox-acct", "unfiled", "h", unfiled, "x", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := p.db.Exec(`UPDATE outbox_jobs SET state='submitted',filing_state='ambiguous',payload_ciphertext='',sent_ciphertext=$2 WHERE id=$1`, unfiled, sentCipher); err != nil {
		t.Fatal(err)
	}
	// A cleared payload leaves nothing to export.
	cleared := seedComposition(t, p, p.uid, "outbox-acct", "cleared", "failed", "not_started")
	p.db.Exec(`UPDATE outbox_jobs SET payload_ciphertext='',payload_bytes=0 WHERE id=$1`, cleared)

	files, manifest := exportZip(t, p, p.uid, false)
	for _, id := range []string{failed, ambiguous} {
		msg := files["outbox/"+id+"/message.txt"]
		if !strings.Contains(msg, "private body") || !strings.Contains(msg, "To: friend@example.test") {
			t.Fatalf("composition %s missing from export: %q", id, msg)
		}
		if files["outbox/"+id+"/attachments/1-note.txt"] != "attached bytes" {
			t.Fatalf("attachment of %s not exported as a file", id)
		}
		if strings.Contains(files["outbox/"+id+"/composition.json"], "data_base64") {
			t.Fatal("attachment bytes duplicated inline in composition.json")
		}
	}
	if !strings.Contains(files["outbox/"+unfiled+"/submitted-message.eml"], "the accepted bytes") {
		t.Fatal("unfiled accepted copy missing from export")
	}
	for name := range files {
		if strings.Contains(name, theirs) || strings.Contains(name, cleared) {
			t.Fatalf("export contains %s", name)
		}
	}
	outbox := manifest["outbox"].(map[string]any)
	if outbox["included"] != true || len(outbox["entries"].([]any)) != 3 {
		t.Fatalf("manifest outbox: %v", outbox)
	}

	// An agent token never receives compositions, and the manifest says why.
	agentFiles, agentManifest := exportZip(t, p, p.uid, true)
	for name := range agentFiles {
		if strings.HasPrefix(name, "outbox/") {
			t.Fatalf("agent export contains %s", name)
		}
	}
	if agentManifest["outbox"].(map[string]any)["included"] != false {
		t.Fatalf("agent manifest: %v", agentManifest["outbox"])
	}
}

// A composition that cannot be decrypted must not break the owner's exit: the
// rest of the archive is delivered and the manifest names what was left out.
func TestIntegrationOutboxExportReportsUnreadableCompositions(t *testing.T) {
	p := newProductPG(t)
	seedOutboxAccount(t, p)
	id := seedComposition(t, p, p.uid, "outbox-acct", "locked", "failed", "not_started")
	p.app.cfg = &Config{SecretKey: "a-different-key-than-the-one-that-sealed-it"}
	files, manifest := exportZip(t, p, p.uid, false)
	if _, ok := files["notes.md"]; !ok {
		t.Fatal("archive lost its other contents")
	}
	for name := range files {
		if strings.HasPrefix(name, "outbox/") {
			t.Fatalf("undecryptable composition produced %s", name)
		}
	}
	unreadable := manifest["outbox"].(map[string]any)["unreadable"].([]any)
	if len(unreadable) != 1 || unreadable[0].(map[string]any)["id"] != id || !strings.Contains(unreadable[0].(map[string]any)["reason"].(string), "SECRET_KEY") {
		t.Fatalf("manifest does not name the unreadable composition: %v", unreadable)
	}
}
