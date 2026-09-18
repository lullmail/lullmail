package main

// Keyset pagination for the outer list endpoints (audit DATA-06/DATA-11):
// buckets and search answer has_more + next_cursor and accept ?cursor=
// and ?limit=. The cursor pins the LAST row's sort key
// (received_at DESC NULLS LAST, id DESC), so insertion of newer rows
// between pages never duplicates or skips what older pages already
// delivered — an OFFSET page under the same insertion would do both.

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"
)

const (
	defaultPageLimit = 50
	maxPageLimit     = 200
)

// listCursor is the sort key of the last delivered row. ReceivedAt nil
// means the tail of NULLS LAST has been reached (rows ordered by id
// alone).
type listCursor struct {
	ReceivedAt *time.Time `json:"r,omitempty"`
	ID         string     `json:"i"`
}

func encodeListCursor(c listCursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeListCursor(s string) (listCursor, error) {
	var c listCursor
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(raw, &c)
	if err == nil && c.ID == "" {
		err = errInvalidCursor
	}
	return c, err
}

type cursorError struct{}

func (cursorError) Error() string { return "invalid cursor" }

var errInvalidCursor = cursorError{}

// pageParams reads ?limit= and ?cursor= with the shared bounds. A bad
// cursor is a 400, not a silent first page: pretending the client asked
// for page one would silently duplicate rows they already have.
func pageParams(r *http.Request, defaultLimit int) (limit int, cursor listCursor, err error) {
	limit = defaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n := 0
		for _, c := range v {
			if c < '0' || c > '9' {
				return 0, cursor, errInvalidCursor
			}
			n = n*10 + int(c-'0')
			if n > maxPageLimit {
				break
			}
		}
		if n < 1 {
			n = 1
		}
		if n > maxPageLimit {
			n = maxPageLimit
		}
		limit = n
	}
	if c := r.URL.Query().Get("cursor"); c != "" {
		cursor, err = decodeListCursor(c)
		if err != nil {
			return 0, listCursor{}, err
		}
	}
	return limit, cursor, nil
}

// cursorPredicate is the keyset condition for ordering
// (received_at DESC NULLS LAST, id DESC). ts is the cursor's timestamp
// (nil = inside the NULL tail); tsArg/idArg are the numbered
// placeholders as strings ("$3"/"$4"), so callers embed the positions
// their query already reached.
//
// The mirror's received_at is a naive TIMESTAMP holding UTC digits
// (engine convention: nullTime writes t.UTC()); AT TIME ZONE 'UTC'
// reinterprets those digits as UTC instants so the comparison cannot
// skew with the session timezone.
func cursorPredicate(tsArg, idArg string) string {
	return ` AND ((` + tsArg + `::timestamptz IS NULL AND m.received_at IS NULL AND m.id < ` + idArg + `)
		  OR (` + tsArg + `::timestamptz IS NOT NULL AND (m.received_at IS NULL
		      OR (m.received_at AT TIME ZONE 'UTC') < ` + tsArg + `
		      OR ((m.received_at AT TIME ZONE 'UTC') = ` + tsArg + ` AND m.id < ` + idArg + `))))`
}

func cursorArgs(c listCursor) []any {
	return []any{c.ReceivedAt, c.ID}
}

// writeRowsPage answers the versioned envelope: the rows, whether more
// exist, and the continuation when they do.
func writeRowsPage[T any](w http.ResponseWriter, rows []T, hasMore bool, next listCursor) {
	out := struct {
		Rows       []T    `json:"rows"`
		HasMore    bool   `json:"has_more"`
		NextCursor string `json:"next_cursor,omitempty"`
	}{Rows: rows, HasMore: hasMore}
	if hasMore {
		out.NextCursor = encodeListCursor(next)
	}
	writeJSON(w, out)
}
