package app

import (
	"net/http"
	"strings"
)

// Demo mode (LULL_DEMO=1) turns an install into a public showroom: no
// sign-in, an owner that exists only to hold made-up mail, and a short list
// of things a visitor may change. It is for an instance whose database is
// re-seeded on a timer by its deployment (tools/demo/) — never for one that
// holds real mail, because in this mode every visitor is the owner.
//
// What a visitor can do is an allowlist, not a blocklist: filing, snoozing
// and reading threads, screening senders, pinning to the board. Those are
// the product, they touch nothing outside the database, and the next reset
// undoes them. Everything else is refused — connecting a mailbox, sending,
// credentials, exports, tokens, and anything that stores a visitor's own
// text where the next visitor would read it (notes, board cards).

// demoOwnerEmail is the address the demo owner is created under; the seed
// in dashboard/e2e/seed.sql looks the owner up by it.
const demoOwnerEmail = "owner@owner.local"

// demoMutations are the path suffixes (after /api) a visitor may POST to.
var demoMutations = []string{
	"/screener/decide",
	"/screener/undecide",
	"/board/pin",
	"/board/unpin",
}

// demoAllows reports whether a request may run in demo mode.
func demoAllows(r *http.Request) bool {
	path := strings.TrimPrefix(r.URL.Path, "/api")
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		// Reading is the point. Exports are the exception: a full mailbox
		// archive is real work for no showroom value.
		return !strings.Contains(path, "/export")
	case http.MethodPost:
		for _, allowed := range demoMutations {
			if path == allowed {
				return true
			}
		}
		// Thread verbs: read, unread, file, snooze, undo.
		if strings.HasPrefix(path, "/messages/") && strings.HasSuffix(path, "/action") {
			return true
		}
		// Finishing a derived board card.
		return strings.HasPrefix(path, "/board/cards/") && strings.HasSuffix(path, "/done")
	}
	return false
}

func writeDemoRefusal(w http.ResponseWriter) {
	writeProblem(w, http.StatusForbidden, "Not In The Demo",
		"this is a public demo on made-up mail, so that is switched off here — it works on your own install")
}
