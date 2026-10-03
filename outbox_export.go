package main

// Owner export of saved outbox compositions.
//
// A composition that was never sent (pending, failed, cancelled) or whose
// outcome is unknown (ambiguous) exists nowhere but here: there is no provider
// original in the mailbox export to fall back on. That puts it on the same
// side of the export contract as notes and board cards, so the owner's
// personal export carries it, decrypted. The accepted SMTP copy of a message
// whose Sent filing did not complete is exported too, for the same reason.
// Submitted messages that were filed are in the provider's Sent folder and
// travel in the mailbox export.
//
// The export is session-only. Agent tokens are fenced away from mail content
// (they cannot dump the mailbox archive), so an agent-authenticated personal
// export omits compositions and says so in its manifest.

import (
	"archive/zip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type outboxExportEntry struct {
	ID           string   `json:"id"`
	Status       string   `json:"status"`
	FilingStatus string   `json:"filing_status"`
	ErrorCode    string   `json:"error_code,omitempty"`
	Account      string   `json:"account,omitempty"`
	CreatedAt    string   `json:"created_at"`
	Files        []string `json:"files"`
}

type outboxExportUnreadable struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type outboxExportSummary struct {
	Included   bool                     `json:"included"`
	Note       string                   `json:"note,omitempty"`
	Entries    []outboxExportEntry      `json:"entries,omitempty"`
	Unreadable []outboxExportUnreadable `json:"unreadable,omitempty"`
}

// writeOutboxExport adds outbox/<id>/... members to zw. It reads one row at a
// time, so memory stays bounded by a single composition.
func (a *App) writeOutboxExport(ctx context.Context, zw *zip.Writer, uid string) (outboxExportSummary, error) {
	summary := outboxExportSummary{Included: true}
	rows, err := a.db.QueryContext(ctx, `SELECT id::text FROM outbox_jobs WHERE user_id=$1 AND (payload_ciphertext<>'' OR sent_ciphertext<>'') ORDER BY created_at,id`, uid)
	if err != nil {
		return summary, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return summary, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return summary, err
	}
	for _, id := range ids {
		var state, filing, code, payload, sent, created string
		var account string
		err := a.db.QueryRowContext(ctx, `SELECT j.state,j.filing_state,j.error_code,j.payload_ciphertext,j.sent_ciphertext,to_char(j.created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),COALESCE(ea.address,'')
 FROM outbox_jobs j LEFT JOIN email_accounts ea ON ea.mirror_account_id=j.account_id AND ea.user_id=j.user_id WHERE j.id::text=$1 AND j.user_id=$2`, id, uid).
			Scan(&state, &filing, &code, &payload, &sent, &created, &account)
		if err != nil {
			return summary, err
		}
		entry := outboxExportEntry{ID: id, Status: state, FilingStatus: filing, ErrorCode: code, Account: account, CreatedAt: created}
		dir := "outbox/" + id + "/"
		add := func(name string, data []byte) error {
			f, err := zw.Create(dir + name)
			if err != nil {
				return err
			}
			if _, err := f.Write(data); err != nil {
				return err
			}
			entry.Files = append(entry.Files, dir+name)
			return nil
		}
		if payload != "" {
			plain, openErr := openBound(a.cfg, "payload", uid, id, payload)
			if openErr != nil {
				summary.Unreadable = append(summary.Unreadable, outboxExportUnreadable{ID: id, Status: state, Reason: exportSealReason(openErr, "composition")})
			} else if files, err := composeOutboxExport(plain); err != nil {
				summary.Unreadable = append(summary.Unreadable, outboxExportUnreadable{ID: id, Status: state, Reason: "composition is not valid: " + err.Error()})
			} else {
				for _, f := range files {
					if err := add(f.name, f.data); err != nil {
						return summary, err
					}
				}
			}
		}
		if sent != "" {
			plain, openErr := openBound(a.cfg, "sent", uid, id, sent)
			if openErr != nil {
				summary.Unreadable = append(summary.Unreadable, outboxExportUnreadable{ID: id, Status: state, Reason: exportSealReason(openErr, "saved Sent copy")})
			} else if err := add("submitted-message.eml", []byte(plain)); err != nil {
				return summary, err
			}
		}
		if len(entry.Files) > 0 {
			summary.Entries = append(summary.Entries, entry)
		}
	}
	return summary, nil
}

func exportSealReason(err error, what string) string {
	if errors.Is(err, errSealedInvalid) {
		return what + " is damaged or does not belong to this entry"
	}
	return what + " cannot be opened with the current SECRET_KEY; restore the key it was saved with and export again"
}

type exportFile struct {
	name string
	data []byte
}

// composeOutboxExport turns one stored composition into readable members:
// message.txt for a person, composition.json for tools, attachments/ as files.
func composeOutboxExport(plain string) ([]exportFile, error) {
	var payload outboxPayload
	if err := json.Unmarshal([]byte(plain), &payload); err != nil {
		return nil, err
	}
	var req map[string]any
	if err := json.Unmarshal(payload.Request, &req); err != nil {
		return nil, err
	}
	var files []exportFile
	var attachments []map[string]any
	if list, ok := req["attachments"].([]any); ok {
		for i, item := range list {
			att, _ := item.(map[string]any)
			name, _ := att["filename"].(string)
			encoded, _ := att["data_base64"].(string)
			data, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return nil, fmt.Errorf("attachment %d is not valid base64", i+1)
			}
			member := fmt.Sprintf("attachments/%d-%s", i+1, safeExportName(name))
			files = append(files, exportFile{member, data})
			attachments = append(attachments, map[string]any{"filename": name, "content_type": att["content_type"], "file": member})
		}
	}
	delete(req, "attachments")
	if len(attachments) > 0 {
		req["attachments"] = attachments
	}
	structured, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return nil, err
	}
	str := func(key string) string { s, _ := req[key].(string); return s }
	var text strings.Builder
	fmt.Fprintf(&text, "To: %s\n", str("to"))
	if cc := str("cc"); cc != "" {
		fmt.Fprintf(&text, "Cc: %s\n", cc)
	}
	if bcc := str("bcc"); bcc != "" {
		fmt.Fprintf(&text, "Bcc: %s\n", bcc)
	}
	fmt.Fprintf(&text, "Subject: %s\n\n", str("subject"))
	body := str("text")
	if body == "" {
		body = str("html")
	}
	text.WriteString(body + "\n")
	return append([]exportFile{{"message.txt", []byte(text.String())}, {"composition.json", structured}}, files...), nil
}

// outboxExportRequestAllowed reports whether this request may receive saved
// compositions: an interactive session, never an agent token.
func outboxExportRequestAllowed(r *http.Request) bool { return !hasAgentBearer(r) }
