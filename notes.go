package main

// Sticky notes (the canvas): position, text, color. Thoughts, not tasks —
// no done state, no thread. A note leaves by being thrown away, and the
// undo for that is re-creating it where it stood.

import (
	"net/http"
)

type stickyNote struct {
	ID    string `json:"id"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
	Text  string `json:"text"`
	Color int    `json:"color"`
}

func (a *App) handleNotes(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `
		SELECT id::text, x, y, text, color FROM sticky_notes
		WHERE user_id = $1 ORDER BY created_at`, uid)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}
	defer rows.Close()
	out := []stickyNote{}
	for rows.Next() {
		var n stickyNote
		// A dropped row here is a note that silently vanishes from the wall.
		// Failing the request is the honest answer: the board is wrong either
		// way, and only one of the two says so.
		if err := rows.Scan(&n.ID, &n.X, &n.Y, &n.Text, &n.Color); err != nil {
			writeProblem(w, http.StatusInternalServerError, "Scan Failed", err.Error())
			return
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}
	writeJSON(w, out)
}

func (a *App) handleNoteCreate(w http.ResponseWriter, r *http.Request) {
	var req stickyNote
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	if req.Color < 0 || req.Color > 4 {
		req.Color = 0
	}
	var n stickyNote
	err = a.db.QueryRowContext(r.Context(), `
		INSERT INTO sticky_notes (user_id, x, y, text, color)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id::text, x, y, text, color`,
		uid, req.X, req.Y, req.Text, req.Color).Scan(&n.ID, &n.X, &n.Y, &n.Text, &n.Color)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Create Failed", err.Error())
		return
	}
	writeJSON(w, n)
}

// handleNoteUpdate: partial by NULL — an unset field keeps its value. An
// empty string is a real value (new notes start blank), so text arrives as
// a pointer.
func (a *App) handleNoteUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		X     *int    `json:"x"`
		Y     *int    `json:"y"`
		Text  *string `json:"text"`
		Color *int    `json:"color"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	if req.Color != nil && (*req.Color < 0 || *req.Color > 4) {
		writeProblem(w, http.StatusUnprocessableEntity, "Bad Color", "color must be 0-4")
		return
	}
	res, err := a.db.ExecContext(r.Context(), `
		UPDATE sticky_notes SET
		  x = COALESCE($3, x), y = COALESCE($4, y),
		  text = COALESCE($5, text), color = COALESCE($6, color),
		  updated_at = now()
		WHERE user_id = $1 AND id = $2`,
		uid, r.PathValue("id"), req.X, req.Y, req.Text, req.Color)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Update Failed", err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeProblem(w, http.StatusNotFound, "Not Found", "no such note")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (a *App) handleNoteDelete(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	res, err := a.db.ExecContext(r.Context(),
		`DELETE FROM sticky_notes WHERE user_id = $1 AND id = $2`, uid, r.PathValue("id"))
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Delete Failed", err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeProblem(w, http.StatusNotFound, "Not Found", "no such note")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
