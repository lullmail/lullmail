package main

import "testing"

func TestClassifySender(t *testing.T) {
	cases := []struct {
		name             string
		decided, allowed bool
		route            string
		correspondent    bool
		historical       bool
		want             string
	}{
		{"allowed decision routes in", true, true, "feed", false, false, "feed"},
		{"blocked decision parks", true, false, "", false, false, "screener"},
		{"correspondent always reaches the inbox", false, false, "", true, false, "imbox"},
		{"correspondent history still reaches the inbox", false, false, "", true, true, "imbox"},
		{"unknown history files to receipts", false, false, "", false, true, "paper_trail"},
		{"unknown new mail screens", false, false, "", false, false, "screener"},
		{"decision beats history and correspondence", true, true, "imbox", true, true, "imbox"},
	}
	for _, tc := range cases {
		if got := classifySender(tc.decided, tc.allowed, tc.route, tc.correspondent, tc.historical); got != tc.want {
			t.Errorf("%s: classifySender(%v,%v,%q,%v,%v) = %q, want %q",
				tc.name, tc.decided, tc.allowed, tc.route, tc.correspondent, tc.historical, got, tc.want)
		}
	}
}
