package main

import (
	"strings"
	"testing"
)

func TestClassifySender(t *testing.T) {
	cases := []struct {
		name             string
		decided, allowed bool
		route            string
		correspondent    bool
		historical       bool
		screening        bool
		want             string
	}{
		{"allowed decision routes in", true, true, "feed", false, false, true, "feed"},
		{"blocked decision parks in dropped", true, false, "", false, false, true, "dropped"},
		{"correspondent always reaches the inbox", false, false, "", true, false, true, "imbox"},
		{"correspondent history still reaches the inbox", false, false, "", true, true, true, "imbox"},
		{"unknown history files to receipts", false, false, "", false, true, true, "paper_trail"},
		{"unknown new mail screens", false, false, "", false, false, true, "screener"},
		{"decision beats history and correspondence", true, true, "imbox", true, true, true, "imbox"},

		// Screening off: only the unknown-new-sender case changes.
		{"screening off sends unknown new mail to the inbox", false, false, "", false, false, false, "imbox"},
		{"screening off still honours a block", true, false, "", false, false, false, "dropped"},
		{"screening off still honours an allow route", true, true, "feed", false, false, false, "feed"},
		{"screening off still files history to receipts", false, false, "", false, true, false, "paper_trail"},
	}
	for _, tc := range cases {
		if got := classifySender(tc.decided, tc.allowed, tc.route, tc.correspondent, tc.historical, tc.screening); got != tc.want {
			t.Errorf("%s: classifySender(%v,%v,%q,%v,%v,%v) = %q, want %q",
				tc.name, tc.decided, tc.allowed, tc.route, tc.correspondent, tc.historical, tc.screening, got, tc.want)
		}
	}
}

func TestLikeContainsEscapesWildcards(t *testing.T) {
	if got := likeContains("100%"); got != `%100\%%` {
		t.Fatalf("likeContains(100%%) = %q", got)
	}
	if got := likeContains("a_b"); got != `%a\_b%` {
		t.Fatalf("likeContains(a_b) = %q", got)
	}
}

// Provider filenames must not smuggle separators, controls, or dot-names
// into Content-Disposition (audit 3 DATA-14).
func TestAttachmentFilename(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"invoice.pdf", "invoice.pdf"},
		{"../folder\\résumé\n.txt", "résumé.txt"},
		{"..", "attachment"},
		{".", "attachment"},
		{"", "attachment"},
		{"  ", "attachment"},
		{"a/b/c.png", "c.png"},
		{"line\nbreak.txt", "linebreak.txt"},
	} {
		if got := attachmentFilename(tc.in); got != tc.want {
			t.Errorf("attachmentFilename(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestReplyDefault(t *testing.T) {
	addr := func(name, email string) string {
		return `{"name":"` + name + `","email":"` + email + `"}` //nolint:dupl
	}
	list := func(items ...string) string {
		return "[" + strings.Join(items, ",") + "]"
	}
	own := map[string]bool{"owner@example.com": true}
	cases := []struct {
		name      string
		own       map[string]bool
		from      string
		replyTo   string
		to        string
		wantIn    string
		wantEmpty bool
	}{
		{
			name:   "inbound mail replies to its From",
			own:    own,
			from:   list(addr("Ada", "ada@example.com")),
			wantIn: "ada@example.com",
		},
		{
			name:    "Reply-To beats From",
			own:     own,
			from:    list(addr("Newsletter", "news@example.com")),
			replyTo: list(addr("Support", "help@example.com")),
			wantIn:  "help@example.com",
		},
		{
			name:   "follow-up on own sent mail goes to its recipients, never the owner",
			own:    own,
			from:   list(addr("Owner", "owner@example.com")),
			to:     list(addr("Bob", "bob@example.com")),
			wantIn: "bob@example.com",
		},
		{
			name:      "own sent mail with only own recipients asks",
			own:       own,
			from:      list(addr("Owner", "owner@example.com")),
			to:        list(addr("Owner", "owner@example.com")),
			wantEmpty: true,
		},
		{
			name:   "a second connected account counts as own",
			own:    map[string]bool{"owner@example.com": true, "alt@example.com": true},
			from:   list(addr("Alt", "alt@example.com")),
			to:     list(addr("Cara", "cara@example.com")),
			wantIn: "cara@example.com",
		},
		{
			name:      "broken envelope asks rather than guessing",
			own:       own,
			from:      "not json",
			wantEmpty: true,
		},
	}
	for _, tc := range cases {
		got := replyDefault(tc.own, tc.from, tc.replyTo, tc.to)
		if tc.wantEmpty {
			if got != "" {
				t.Errorf("%s: got %q, want empty", tc.name, got)
			}
			continue
		}
		if !strings.Contains(got, tc.wantIn) {
			t.Errorf("%s: got %q, want it to contain %q", tc.name, got, tc.wantIn)
		}
		if strings.Contains(strings.ToLower(got), "owner@example.com") {
			t.Errorf("%s: default addresses the owner: %q", tc.name, got)
		}
	}
}
