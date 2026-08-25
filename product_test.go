package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSplitStatementsStripsComments(t *testing.T) {
	in := `-- a comment with a ; semicolon
CREATE TABLE a (x int);

-- another
CREATE TABLE b (y text)`
	stmts := splitStatements(in)
	if len(stmts) != 2 {
		t.Fatalf("want 2 statements, got %d: %q", len(stmts), stmts)
	}
	for _, s := range stmts {
		if s == "" {
			t.Error("empty statement")
		}
	}
}

func TestFirstSenderEmail(t *testing.T) {
	cases := []struct{ in, want string }{
		{`[{"name":"A","email":"a@x.com"}]`, "a@x.com"},
		{`[{"name":"A","email":"Mixed@X.com"}]`, "mixed@x.com"},
		{`[{"email":"only@x.com"}]`, "only@x.com"},
		{`[]`, ""},
		{``, ""},
		{`not json`, ""},
	}
	for _, c := range cases {
		if got := firstSenderEmail(c.in); got != c.want {
			t.Errorf("firstSenderEmail(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	cfg := &Config{SecretKey: "0123456789abcdef0123456789abcdef"}
	ct, err := sealSecret(cfg, "apppassword")
	if err != nil {
		t.Fatal(err)
	}
	if ct == "apppassword" {
		t.Fatal("ciphertext equals plaintext")
	}
	pt, err := openSecret(cfg, ct)
	if err != nil {
		t.Fatal(err)
	}
	if pt != "apppassword" {
		t.Fatalf("round trip got %q", pt)
	}
	// Wrong key must fail, not mis-decrypt.
	if _, err := openSecret(&Config{SecretKey: "another-key-32-characters-long!!!"}, ct); err == nil {
		t.Fatal("wrong key decrypted successfully")
	}
	if s, _ := sealSecret(&Config{}, "x"); s != "" {
		t.Fatal("sealing without SECRET_KEY must fail")
	}
}

func TestSecurityHeadersCoverEveryResponse(t *testing.T) {
	h := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	for name, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := rec.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "object-src 'none'") {
		t.Fatalf("security policy is missing framing/object protections: %q", csp)
	}
}
