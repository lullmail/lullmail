package main

import "testing"

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
