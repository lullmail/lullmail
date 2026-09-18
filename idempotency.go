package main

// Server-side mutation idempotency (audit WEB-04 pass 6, WEB-03 pass 7,
// R07 round 3 — the offline-v2 API contract). Every mutable product
// endpoint is wrapped in withIdempotency: a client MAY send
// Idempotency-Key; when present, exactly one execution's response is
// recorded per (user, key) in api_mutations and every retry — a lost
// acknowledgment, a second tab replaying the shared offline queue, an
// agent retrying a timeout — receives the recorded answer instead of a
// second application. A key reused with a different request is a 409.
//
// Concurrency design: the row insert and the response record commit in
// ONE transaction that stays open while the handler runs. Concurrent
// same-key requests find the uncommitted row via SELECT ... FOR UPDATE
// and block until that transaction finishes, then replay the recorded
// response. A crash rolls the insert back, so a committed row is always
// complete and there is no "running" state to expire.

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const (
	// idempotencyKeyLimit keeps the ledger's key column sane; keys are
	// opaque client-generated strings (the dashboard queue's item ids).
	idempotencyKeyLimit = 128
	// idempotencyBodyLimit bounds the buffered request copy the hash and
	// the replayed handler body need. Every wrapped endpoint decodes a
	// small JSON document; the send route (34 MiB) is deliberately NOT
	// wrapped — its undo window is its own contract.
	idempotencyBodyLimit = 1 << 20
	// idempotencyRetentionBounds how long a recorded answer stays
	// replayable: an offline queue can sit for weeks, and a pruned row
	// means the retried mutation applies again.
	idempotencyRetentionDays = 30
)

// mutationRequestHash binds a key to one request shape: method, path,
// query, and body. A different shape under the same key is a conflict.
func mutationRequestHash(method, path, query string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write([]byte(query))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// responseRecorder captures one handler answer. WriteHeader defaults to
// 200 on first write, exactly like net/http.
type responseRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newResponseRecorder() *responseRecorder {
	return &responseRecorder{header: make(http.Header)}
}

func (r *responseRecorder) Header() http.Header { return r.header }

func (r *responseRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *responseRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(p)
}

// withIdempotency wraps a mutable handler with the api_mutations
// contract. Requests without Idempotency-Key pass straight through.
func (a *App) withIdempotency(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			next(w, r)
			return
		}
		if len(key) > idempotencyKeyLimit {
			writeProblem(w, http.StatusRequestEntityTooLarge, "Key Too Long",
				fmt.Sprintf("Idempotency-Key must be at most %d characters", idempotencyKeyLimit))
			return
		}
		uid, err := a.userID(r.Context())
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, idempotencyBodyLimit))
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				writeProblem(w, http.StatusRequestEntityTooLarge, "Request Too Large",
					"the request body exceeds the idempotency buffer")
				return
			}
			writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
		hash := mutationRequestHash(r.Method, r.URL.Path, r.URL.RawQuery, body)

		// Two passes are enough: the only way the insert loses twice is
		// the previous holder rolling back between the two statements.
		for pass := 0; pass < 2; pass++ {
			tx, err := a.db.BeginTx(r.Context(), nil)
			if err != nil {
				writeProblem(w, http.StatusInternalServerError, "Begin Failed", err.Error())
				return
			}
			res, err := tx.ExecContext(r.Context(), `
				INSERT INTO api_mutations
					(user_id, mutation_key, request_hash, response_status, response_content_type, response_body)
				VALUES ($1, $2, $3, 0, '', '\x')
				ON CONFLICT (user_id, mutation_key) DO NOTHING`,
				uid, key, hash)
			if err != nil {
				tx.Rollback()
				writeProblem(w, http.StatusInternalServerError, "Idempotency Failed", err.Error())
				return
			}
			inserted, _ := res.RowsAffected()
			if inserted == 1 {
				a.recordIdempotentResponse(w, r, tx, next, uid, key, body)
				return
			}
			var storedHash, contentType string
			var status int
			var stored []byte
			err = tx.QueryRowContext(r.Context(), `
				SELECT request_hash, response_status, response_content_type, response_body
				FROM api_mutations WHERE user_id = $1 AND mutation_key = $2
				FOR UPDATE`, uid, key).Scan(&storedHash, &status, &contentType, &stored)
			tx.Rollback()
			if errors.Is(err, sql.ErrNoRows) {
				continue // the previous holder rolled back; take the key
			}
			if err != nil {
				writeProblem(w, http.StatusInternalServerError, "Idempotency Failed", err.Error())
				return
			}
			if storedHash != hash {
				writeProblem(w, http.StatusConflict, "Key Reused",
					"this Idempotency-Key was already used for a different request")
				return
			}
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("X-Idempotent-Replay", "true")
			w.WriteHeader(status)
			w.Write(stored)
			return
		}
		// Unreachable in practice: the second insert attempt won the race.
		writeProblem(w, http.StatusServiceUnavailable, "Idempotency Busy",
			"could not claim the mutation key — retry the same request")
	}
}

// recordIdempotentResponse owns the key for the duration of one handler
// run: execute against a recorder, store the answer, commit, then replay
// the recorded bytes to the real writer. A handler panic unwinds the
// transaction (the row vanishes) and propagates as it would unwrapped.
func (a *App) recordIdempotentResponse(w http.ResponseWriter, r *http.Request, tx *sql.Tx, next http.HandlerFunc, uid, key string, body []byte) {
	defer tx.Rollback()
	r.Body = io.NopCloser(bytes.NewReader(body))
	rec := newResponseRecorder()
	next(rec, r)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	if _, err := tx.ExecContext(r.Context(), `
		UPDATE api_mutations
		SET response_status = $3, response_content_type = $4, response_body = $5
		WHERE user_id = $1 AND mutation_key = $2`,
		uid, key, rec.status, rec.header.Get("Content-Type"), rec.body.Bytes()); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Idempotency Failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		// The handler's own writes may have committed while the ledger
		// row did not; the retry re-applies. Same exposure as an
		// unwrapped request — recorded honestly, never silent.
		a.log.Error("idempotency ledger commit failed", "key", key, "err", err)
		writeProblem(w, http.StatusInternalServerError, "Idempotency Failed", err.Error())
		return
	}
	for name, values := range rec.header {
		for _, v := range values {
			w.Header().Add(name, v)
		}
	}
	w.WriteHeader(rec.status)
	w.Write(rec.body.Bytes())
}
