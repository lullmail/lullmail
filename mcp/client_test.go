package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestClient points a client at a handler that records what crossed the
// wire, so tests can pin auth headers, paths, and body shapes.
func newTestClient(handler http.HandlerFunc) (*client, *[]map[string]any, *httptest.Server) {
	var seen []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entry := map[string]any{
			"method": r.Method,
			"path":   r.URL.Path,
			"query":  r.URL.RawQuery,
			"auth":   r.Header.Get("Authorization"),
		}
		if r.Body != nil {
			var body any
			_ = json.NewDecoder(r.Body).Decode(&body)
			entry["body"] = body
		}
		seen = append(seen, entry)
		handler(w, r)
	}))
	c, err := newClient(server.URL, "es_testtoken")
	if err != nil {
		server.Close()
		panic(err)
	}
	return c, &seen, server
}

func TestClientSendsBearerAndPassesBodyThrough(t *testing.T) {
	c, seen, server := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/screener/decide" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Write([]byte(`{"ok":true}`))
	})
	defer server.Close()

	if _, err := c.post(context.Background(), "/screener/decide", map[string]any{"sender": "a@b.c", "allow": true}); err != nil {
		t.Fatal(err)
	}
	entry := (*seen)[0]
	if entry["auth"] != "Bearer es_testtoken" {
		t.Errorf("auth = %v", entry["auth"])
	}
	body := entry["body"].(map[string]any)
	if body["sender"] != "a@b.c" || body["allow"] != true {
		t.Errorf("body = %v", body)
	}
}

func TestClientMapsProblemDetail(t *testing.T) {
	c, _, server := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"title":"Missing Fields","detail":"address, password and host are required"}`))
	})
	defer server.Close()

	_, err := c.post(context.Background(), "/accounts", map[string]any{})
	apiErr, ok := err.(*apiError)
	if !ok {
		t.Fatalf("err type = %T", err)
	}
	if apiErr.Status != 422 || apiErr.Title != "Missing Fields" {
		t.Fatalf("apiErr = %+v", apiErr)
	}
	want := "Missing Fields: address, password and host are required"
	if apiErr.Error() != want {
		t.Fatalf("Error() = %q, want %q", apiErr.Error(), want)
	}
}

func TestClientRejectsBadConfig(t *testing.T) {
	if _, err := newClient("not-a-url", "es_x"); err == nil {
		t.Fatal("bad URL accepted")
	}
	if _, err := newClient("https://mail.example.com", ""); err == nil {
		t.Fatal("missing token accepted")
	}
}

func TestSafeSegmentRejectsMetacharacters(t *testing.T) {
	for _, bad := range []string{"a/b", "a b", "a%2Fb", "a?x=y", ""} {
		if _, err := safeSegment(bad); err == nil {
			t.Errorf("safeSegment(%q) accepted", bad)
		}
	}
	if got, err := safeSegment("0f8c2d61-9a44-4a1e-b4c2-2f9d3a7e5b88"); err != nil || got == "" {
		t.Errorf("uuid rejected: %v", err)
	}
}
