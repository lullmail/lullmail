package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTMLFallbackText(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			name: "paragraphs and line breaks become newlines",
			in:   "<p>Hello there.</p><p>Second line.<br/>Third.</p>",
			want: "Hello there.\n\nSecond line.\nThird.",
		},
		{
			name: "entities decode and tags vanish",
			in:   "<div>Fish &amp; <b>chips</b></div>",
			want: "Fish & chips",
		},
		{
			name: "list items keep their bullets",
			in:   "<ul><li>one</li><li>two</li></ul>",
			want: "- one\n- two",
		},
		{
			name: "case-insensitive markup, content case preserved",
			in:   "<P>Keep MY Case</P><BR>After the break",
			want: "Keep MY Case\n\nAfter the break",
		},
		{
			name: "attributes inside tags are dropped",
			in:   `<a href="https://x.test/r?u=1">Read &quot;this&quot;</a>`,
			want: `Read "this"`,
		},
		{
			name: "whitespace is trimmed",
			in:   "   <p>  padded  </p>   ",
			want: "padded",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := htmlFallbackText(tc.in); got != tc.want {
				t.Errorf("htmlFallbackText(%q)\n got  %q\n want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestOutboundRecipients(t *testing.T) {
	for _, tc := range []struct{ name, list string }{
		{"header injection", "person@example.com\r\nBcc: victim@example.com"},
		{"not an address", "not an address"},
		{"one bad spoils the list", "person@example.com, garbage"},
	} {
		if _, ok := outboundRecipients(tc.list); ok {
			t.Errorf("outboundRecipients(%q) accepted", tc.list)
		}
	}
	if got, ok := outboundRecipients(""); !ok || len(got) != 0 {
		t.Errorf("empty list = %+v, %v; want empty ok", got, ok)
	}
	got, ok := outboundRecipients("Person <person@example.com>, other@example.com")
	if !ok || len(got) != 2 || got[0].Name != "Person" || got[0].Email != "person@example.com" || got[1].Email != "other@example.com" {
		t.Fatalf("valid list = %+v, %v", got, ok)
	}
}

func TestUndoSendOnlyCancelsPendingDelivery(t *testing.T) {
	for _, tc := range []struct {
		name       string
		state      sendState
		wantStatus int
		wantCancel bool
	}{
		{"pending", sendPending, http.StatusOK, true},
		{"delivering", sendDelivering, http.StatusGone, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			q := newSendQueue()
			q.sends["send-1"] = &pendingSend{cancel: cancel, state: tc.state}
			a := &App{sendq: q}
			r := httptest.NewRequest(http.MethodDelete, "/api/send/send-1", nil)
			r.SetPathValue("id", "send-1")
			w := httptest.NewRecorder()

			a.handleUndoSend(w, r)

			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantStatus)
			}
			select {
			case <-ctx.Done():
				if !tc.wantCancel {
					t.Fatal("delivery context was cancelled after delivery started")
				}
			default:
				if tc.wantCancel {
					t.Fatal("pending delivery context was not cancelled")
				}
			}
			cancel()
		})
	}
}
