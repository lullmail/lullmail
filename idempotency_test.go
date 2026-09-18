package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The offline (no-database) half of the idempotency contract: hashing and
// the response recorder. The concurrency and replay proofs run against
// real PostgreSQL in idempotency_pg_test.go.

func TestMutationRequestHashBindsRequestShape(t *testing.T) {
	base := mutationRequestHash("POST", "/notes", "", []byte(`{"a":1}`))
	if base != mutationRequestHash("POST", "/notes", "", []byte(`{"a":1}`)) {
		t.Fatal("hash is not deterministic")
	}
	for name, different := range map[string]string{
		"method": mutationRequestHash("PUT", "/notes", "", []byte(`{"a":1}`)),
		"path":   mutationRequestHash("POST", "/notes/2", "", []byte(`{"a":1}`)),
		"query":  mutationRequestHash("POST", "/notes", "x=1", []byte(`{"a":1}`)),
		"body":   mutationRequestHash("POST", "/notes", "", []byte(`{"a":2}`)),
	} {
		if different == base {
			t.Fatalf("hash ignores the %s", name)
		}
	}
}

func TestResponseRecorderDefaultsAndCapture(t *testing.T) {
	rec := newResponseRecorder()
	rec.Header().Set("Content-Type", "application/json")
	rec.Write([]byte(`{"ok":true}`))
	if rec.status != http.StatusOK {
		t.Fatalf("implicit status = %d, want 200", rec.status)
	}
	rec2 := newResponseRecorder()
	rec2.WriteHeader(http.StatusUnprocessableEntity)
	rec2.WriteHeader(http.StatusOK) // first value wins, like net/http
	if rec2.status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want the first WriteHeader", rec2.status)
	}
}

// A wrapped handler that never touches the database (no key) must pass
// through unchanged — the contract is opt-in per request.
func TestWithIdempotencyPassthroughWithoutKey(t *testing.T) {
	var app App
	called := false
	handler := app.withIdempotency(func(w http.ResponseWriter, r *http.Request) {
		called = true
		writeJSON(w, map[string]any{"ok": true})
	})
	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest("POST", "/notes", nil))
	if !called || w.Code != http.StatusOK {
		t.Fatalf("passthrough: called=%v code=%d", called, w.Code)
	}
}
