package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Personal data has no provider original to preserve. Markdown keeps every
// note/card readable without this app; JSON preserves canvas positions,
// colours, ids, completion state, and thread references for re-import tools.
func (a *App) handlePersonalExport(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, 500, "Export Failed", err.Error())
		return
	}

	type noteExport struct {
		ID        string    `json:"id"`
		X         int       `json:"x"`
		Y         int       `json:"y"`
		Text      string    `json:"text"`
		Color     int       `json:"color"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	notes := []noteExport{}
	rows, err := a.db.QueryContext(r.Context(), `SELECT id::text,x,y,text,color,created_at,updated_at FROM sticky_notes WHERE user_id=$1 ORDER BY created_at`, uid)
	if err != nil {
		writeProblem(w, 500, "Export Failed", err.Error())
		return
	}
	for rows.Next() {
		var n noteExport
		if err := rows.Scan(&n.ID, &n.X, &n.Y, &n.Text, &n.Color, &n.CreatedAt, &n.UpdatedAt); err != nil {
			rows.Close()
			writeProblem(w, 500, "Export Failed", err.Error())
			return
		}
		notes = append(notes, n)
	}
	if err := rows.Err(); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}
	rows.Close()

	type cardExport struct {
		ID        string     `json:"id"`
		ThreadID  string     `json:"thread_id,omitempty"`
		Title     string     `json:"title"`
		Note      string     `json:"note"`
		DoneAt    *time.Time `json:"done_at,omitempty"`
		CreatedAt time.Time  `json:"created_at"`
	}
	cards := []cardExport{}
	rows, err = a.db.QueryContext(r.Context(), `SELECT id::text,COALESCE(thread_key,''),title,note,done_at,created_at FROM board_cards WHERE user_id=$1 ORDER BY created_at`, uid)
	if err != nil {
		writeProblem(w, 500, "Export Failed", err.Error())
		return
	}
	for rows.Next() {
		var c cardExport
		if err := rows.Scan(&c.ID, &c.ThreadID, &c.Title, &c.Note, &c.DoneAt, &c.CreatedAt); err != nil {
			rows.Close()
			writeProblem(w, 500, "Export Failed", err.Error())
			return
		}
		cards = append(cards, c)
	}
	if err := rows.Err(); err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}
	rows.Close()

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="lullmail-personal-data.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	zw := zip.NewWriter(w)
	defer zw.Close()
	write := func(name string, data []byte) error {
		file, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = file.Write(data)
		return err
	}

	var notesMD strings.Builder
	notesMD.WriteString("# Notes\n\n")
	for _, n := range notes {
		fmt.Fprintf(&notesMD, "## Note · %s\n\n%s\n\n", n.CreatedAt.Format("2006-01-02"), n.Text)
	}
	var cardsMD strings.Builder
	cardsMD.WriteString("# Board cards\n\n")
	for _, c := range cards {
		state := "Open"
		if c.DoneAt != nil {
			state = "Done"
		}
		fmt.Fprintf(&cardsMD, "## %s\n\n- State: %s\n- Created: %s\n", c.Title, state, c.CreatedAt.Format(time.RFC3339))
		if c.ThreadID != "" {
			fmt.Fprintf(&cardsMD, "- Thread: `%s`\n", c.ThreadID)
		}
		if c.Note != "" {
			fmt.Fprintf(&cardsMD, "\n%s\n", c.Note)
		}
		cardsMD.WriteString("\n")
	}
	notesJSON, _ := json.MarshalIndent(notes, "", "  ")
	cardsJSON, _ := json.MarshalIndent(cards, "", "  ")
	manifest, _ := json.MarshalIndent(map[string]any{"format": "lullmail-personal-export", "version": 1, "exported_at": time.Now().UTC(), "notes": len(notes), "board_cards": len(cards)}, "", "  ")
	for name, data := range map[string][]byte{"notes.md": []byte(notesMD.String()), "notes-layout.json": notesJSON, "board.md": []byte(cardsMD.String()), "board.json": cardsJSON, "export-manifest.json": manifest} {
		if err := write(name, data); err != nil {
			a.log.Error("personal export stream failed", "err", err)
			return
		}
	}
}
