package main

// Standards-based mail export. The provider is asked for the original RFC
// 5322 bytes first. If it is temporarily unavailable, the local mirror is
// rendered into a valid (but necessarily lossy) message instead of making a
// user's exit depend on a healthy upstream connection.

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	stdmail "net/mail"
	"net/textproto"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	nmail "github.com/neutron-build/neutron/mail"
)

type exportMessage struct {
	id       nmail.MessageID
	envelope *nmail.Envelope
}

type exportMailbox struct {
	id       nmail.MailboxID
	name     string
	filename string
	messages []exportMessage
}

type exportMailboxReport struct {
	Name     string `json:"name"`
	File     string `json:"file"`
	Messages int    `json:"messages"`
	Raw      int    `json:"raw_originals"`
	Fallback int    `json:"mirror_fallbacks"`
}

type exportManifest struct {
	Version     int                   `json:"version"`
	GeneratedAt string                `json:"generated_at"`
	Account     string                `json:"account"`
	Format      string                `json:"format"`
	Mailboxes   []exportMailboxReport `json:"mailboxes"`
	Warnings    []string              `json:"warnings,omitempty"`
	Note        string                `json:"note"`
}

func (a *App) handleAccountExport(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}

	publicID := r.PathValue("id")
	var mirrorID, address string
	err = a.db.QueryRowContext(r.Context(), `
		SELECT mirror_account_id, address FROM email_accounts
		WHERE id = $1 AND user_id = $2`, publicID, uid).Scan(&mirrorID, &address)
	if err == sql.ErrNoRows {
		writeProblem(w, http.StatusNotFound, "Not Found", "no such account")
		return
	}
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}

	plans, warnings, err := a.exportPlan(r.Context(), nmail.AccountID(mirrorID))
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Export Failed", err.Error())
		return
	}
	cred, err := a.Token(r.Context(), nmail.AccountID(mirrorID))
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Credential Failed", err.Error())
		return
	}
	adapter, release, err := newResolver()(r.Context(), nmail.AccountID(mirrorID), cred)
	if err != nil {
		writeProblem(w, http.StatusBadGateway, "Connect Failed", err.Error())
		return
	}
	defer release()

	filename := safeExportName(address) + "-mail-export.zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	zw := zip.NewWriter(w)
	manifest := exportManifest{
		Version:     1,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Account:     address,
		Format:      "mboxrd (RFC 4155)",
		Warnings:    warnings,
		Note:        "Original provider messages are preserved when available. Mirror fallbacks are valid RFC 5322 messages but may omit provider-only headers or attachment bytes.",
	}

	for _, box := range plans {
		report := exportMailboxReport{Name: box.name, File: box.filename, Messages: len(box.messages)}
		if adapter.Provider() == nmail.ProviderIMAP {
			if cursor, cursorErr := a.store.Cursor(r.Context(), nmail.AccountID(mirrorID), box.id); cursorErr == nil {
				if _, selectErr := adapter.Sync(r.Context(), box.id, cursor); selectErr != nil {
					manifest.Warnings = appendExportWarning(manifest.Warnings,
						fmt.Sprintf("%s could not be selected at the provider; mirror fallbacks may be used: %v", box.name, selectErr))
				}
			}
		}
		h := &zip.FileHeader{Name: box.filename, Method: zip.Deflate}
		h.SetModTime(time.Now())
		entry, err := zw.CreateHeader(h)
		if err != nil {
			manifest.Warnings = appendExportWarning(manifest.Warnings, box.name+": could not create archive entry")
			continue
		}
		for _, msg := range box.messages {
			raw, rawErr := readProviderRaw(r.Context(), adapter, msg.id)
			if rawErr == nil {
				report.Raw++
			} else {
				report.Fallback++
				body, _ := a.store.Body(r.Context(), nmail.AccountID(mirrorID), msg.id)
				raw = mirrorEML(msg.envelope, body, rawErr)
				manifest.Warnings = appendExportWarning(manifest.Warnings,
					fmt.Sprintf("%s/%s used the local mirror: %v", box.name, msg.id, rawErr))
			}
			if err := writeMboxRD(entry, msg.envelope, raw); err != nil {
				manifest.Warnings = appendExportWarning(manifest.Warnings,
					fmt.Sprintf("%s/%s could not be written: %v", box.name, msg.id, err))
			}
		}
		manifest.Mailboxes = append(manifest.Mailboxes, report)
	}

	manifestHeader := &zip.FileHeader{Name: "export-manifest.json", Method: zip.Deflate}
	manifestHeader.SetModTime(time.Now())
	manifestEntry, err := zw.CreateHeader(manifestHeader)
	if err == nil {
		enc := json.NewEncoder(manifestEntry)
		enc.SetIndent("", "  ")
		_ = enc.Encode(manifest)
	}
	_ = zw.Close()
}

// handleMessageEML downloads one original message. account is the mirror id
// already carried by the thread response; ownership is checked before dialing.
func (a *App) handleMessageEML(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	messageID := r.PathValue("message")
	mirrorID := r.URL.Query().Get("account")
	if mirrorID == "" {
		writeProblem(w, http.StatusBadRequest, "Missing Account", "the message account is required")
		return
	}
	var address string
	err = a.db.QueryRowContext(r.Context(), `
		SELECT ea.address, ea.mirror_account_id FROM email_accounts ea
		JOIN mail_messages m ON m.account_id = ea.mirror_account_id
		WHERE ea.user_id = $1 AND (ea.mirror_account_id = $2 OR ea.id::text = $2) AND m.id = $3`,
		uid, mirrorID, messageID).Scan(&address, &mirrorID)
	if err == sql.ErrNoRows {
		writeProblem(w, http.StatusNotFound, "Not Found", "no such message")
		return
	}
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
		return
	}
	envelope, err := a.store.Envelope(r.Context(), nmail.AccountID(mirrorID), nmail.MessageID(messageID))
	if err != nil {
		writeProblem(w, http.StatusNotFound, "Not Found", "message is not in the local mirror")
		return
	}

	var raw []byte
	var fallback bool
	cred, credErr := a.Token(r.Context(), nmail.AccountID(mirrorID))
	if credErr == nil {
		adapter, release, resolveErr := newResolver()(r.Context(), nmail.AccountID(mirrorID), cred)
		if resolveErr == nil {
			if adapter.Provider() == nmail.ProviderIMAP {
				var boxID nmail.MailboxID
				if boxErr := a.db.QueryRowContext(r.Context(), `
					SELECT mailbox_id FROM mail_message_mailboxes
					WHERE account_id = $1 AND message_id = $2 LIMIT 1`, mirrorID, messageID).Scan(&boxID); boxErr == nil {
					if cursor, cursorErr := a.store.Cursor(r.Context(), nmail.AccountID(mirrorID), boxID); cursorErr == nil {
						_, _ = adapter.Sync(r.Context(), boxID, cursor)
					}
				}
			}
			raw, err = readProviderRaw(r.Context(), adapter, nmail.MessageID(messageID))
			release()
		} else {
			err = resolveErr
		}
	} else {
		err = credErr
	}
	if err != nil {
		fallback = true
		body, _ := a.store.Body(r.Context(), nmail.AccountID(mirrorID), nmail.MessageID(messageID))
		raw = mirrorEML(envelope, body, err)
	}

	name := safeExportName(envelope.Subject)
	if name == "mail" {
		name = safeExportName(messageID)
	}
	w.Header().Set("Content-Type", "message/rfc822")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + ".eml"}))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if fallback {
		w.Header().Set("X-Email-Soft-Export-Source", "mirror-fallback")
	} else {
		w.Header().Set("X-Email-Soft-Export-Source", "provider-original")
	}
	_, _ = w.Write(raw)
}

func (a *App) exportPlan(ctx context.Context, account nmail.AccountID) ([]exportMailbox, []string, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT id, name FROM mail_mailboxes WHERE account_id = $1
		ORDER BY CASE role
		  WHEN 'inbox' THEN 0 WHEN 'sent' THEN 1 WHEN 'archive' THEN 2
		  WHEN 'drafts' THEN 3 WHEN 'junk' THEN 8 WHEN 'trash' THEN 9 ELSE 4 END,
		name`, string(account))
	if err != nil {
		return nil, nil, err
	}
	type mailboxRow struct{ id, name string }
	var boxes []mailboxRow
	for rows.Next() {
		var box mailboxRow
		if err := rows.Scan(&box.id, &box.name); err != nil {
			rows.Close()
			return nil, nil, err
		}
		boxes = append(boxes, box)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}

	usedNames := map[string]int{}
	var out []exportMailbox
	var warnings []string
	for _, box := range boxes {
		base := safeExportName(box.name)
		usedNames[base]++
		filename := base + ".mbox"
		if usedNames[base] > 1 {
			filename = fmt.Sprintf("%s-%d.mbox", base, usedNames[base])
		}
		plan := exportMailbox{id: nmail.MailboxID(box.id), name: box.name, filename: filename}
		ids, err := a.db.QueryContext(ctx, `
			SELECT mm.message_id FROM mail_message_mailboxes mm
			JOIN mail_messages m ON m.account_id = mm.account_id AND m.id = mm.message_id
			WHERE mm.account_id = $1 AND mm.mailbox_id = $2
			ORDER BY COALESCE(m.sent_at, m.received_at) ASC NULLS LAST, m.id`, string(account), box.id)
		if err != nil {
			return nil, nil, err
		}
		for ids.Next() {
			var id nmail.MessageID
			if err := ids.Scan(&id); err != nil {
				ids.Close()
				return nil, nil, err
			}
			envelope, err := a.store.Envelope(ctx, account, id)
			if err != nil {
				warnings = appendExportWarning(warnings, fmt.Sprintf("%s/%s was skipped: envelope unavailable", box.name, id))
				continue
			}
			plan.messages = append(plan.messages, exportMessage{id: id, envelope: envelope})
		}
		if err := ids.Close(); err != nil {
			return nil, nil, err
		}
		out = append(out, plan)
	}
	return out, warnings, nil
}

func readProviderRaw(ctx context.Context, adapter nmail.Adapter, id nmail.MessageID) ([]byte, error) {
	rc, err := adapter.Raw(ctx, id)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err == nil && len(raw) == 0 {
		err = fmt.Errorf("provider returned an empty message")
	}
	return raw, err
}

func writeMboxRD(w io.Writer, envelope *nmail.Envelope, raw []byte) error {
	sender := "MAILER-DAEMON"
	if envelope != nil && len(envelope.From) > 0 && envelope.From[0].Email != "" {
		sender = strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) || unicode.IsControl(r) {
				return -1
			}
			return r
		}, envelope.From[0].Email)
	}
	when := time.Now().UTC()
	if envelope != nil {
		if !envelope.SentAt.IsZero() {
			when = envelope.SentAt
		} else if !envelope.ReceivedAt.IsZero() {
			when = envelope.ReceivedAt
		}
	}
	if _, err := fmt.Fprintf(w, "From %s %s\n", sender, when.Format("Mon Jan _2 15:04:05 2006")); err != nil {
		return err
	}

	normalized := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	normalized = bytes.ReplaceAll(normalized, []byte("\r"), []byte("\n"))
	boundary := bytes.Index(normalized, []byte("\n\n"))
	if boundary < 0 {
		boundary = len(normalized)
	}
	if _, err := w.Write(normalized[:boundary]); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "\n\n"); err != nil {
		return err
	}
	bodyStart := boundary
	if bodyStart < len(normalized) {
		bodyStart += 2
	}
	for _, line := range bytes.SplitAfter(normalized[bodyStart:], []byte("\n")) {
		withoutNL := bytes.TrimSuffix(line, []byte("\n"))
		i := 0
		for i < len(withoutNL) && withoutNL[i] == '>' {
			i++
		}
		if bytes.HasPrefix(withoutNL[i:], []byte("From ")) {
			if _, err := io.WriteString(w, ">"); err != nil {
				return err
			}
		}
		if _, err := w.Write(line); err != nil {
			return err
		}
	}
	if len(normalized) == 0 || normalized[len(normalized)-1] != '\n' {
		if _, err := io.WriteString(w, "\n"); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func mirrorEML(envelope *nmail.Envelope, body *nmail.Body, rawErr error) []byte {
	var out bytes.Buffer
	writeAddressHeader := func(name string, addresses []nmail.Address) {
		if len(addresses) == 0 {
			return
		}
		values := make([]string, 0, len(addresses))
		for _, address := range addresses {
			values = append(values, (&stdmail.Address{Name: cleanHeader(address.Name), Address: cleanHeader(address.Email)}).String())
		}
		fmt.Fprintf(&out, "%s: %s\r\n", name, strings.Join(values, ", "))
	}
	writeAddressHeader("From", envelope.From)
	writeAddressHeader("To", envelope.To)
	writeAddressHeader("Cc", envelope.Cc)
	writeAddressHeader("Reply-To", envelope.ReplyTo)
	date := envelope.SentAt
	if date.IsZero() {
		date = envelope.ReceivedAt
	}
	if date.IsZero() {
		date = time.Now()
	}
	fmt.Fprintf(&out, "Date: %s\r\n", date.Format(time.RFC1123Z))
	subject := cleanHeader(envelope.Subject)
	if !utf8.ValidString(subject) {
		subject = strings.ToValidUTF8(subject, "�")
	}
	if strings.IndexFunc(subject, func(r rune) bool { return r > unicode.MaxASCII }) >= 0 {
		subject = mime.QEncoding.Encode("utf-8", subject)
	}
	fmt.Fprintf(&out, "Subject: %s\r\n", subject)
	messageID := nmail.NormalizeMessageIDHeader(cleanHeader(envelope.MessageIDHeader))
	if messageID == "" {
		messageID = fmt.Sprintf("email-soft-export-%s@local.invalid", safeExportName(envelope.ID))
	}
	fmt.Fprintf(&out, "Message-ID: <%s>\r\n", messageID)
	if len(envelope.InReplyTo) > 0 {
		fmt.Fprintf(&out, "In-Reply-To: <%s>\r\n", cleanHeader(envelope.InReplyTo[len(envelope.InReplyTo)-1]))
	}
	if len(envelope.References) > 0 {
		refs := make([]string, 0, len(envelope.References))
		for _, ref := range envelope.References {
			refs = append(refs, "<"+cleanHeader(ref)+">")
		}
		fmt.Fprintf(&out, "References: %s\r\n", strings.Join(refs, " "))
	}
	fmt.Fprint(&out, "MIME-Version: 1.0\r\n")
	if rawErr != nil {
		fmt.Fprintf(&out, "X-Email-Soft-Export-Warning: %s\r\n", cleanHeader("provider original unavailable; rendered from mirror: "+rawErr.Error()))
	}

	textBody, htmlBody := "", ""
	if body != nil {
		textBody, htmlBody = body.Text, body.HTML
	}
	if textBody == "" && htmlBody == "" {
		textBody = envelope.Preview
		fmt.Fprint(&out, "X-Email-Soft-Body-Source: envelope-preview\r\n")
	}
	if envelope.HasAttachment {
		fmt.Fprint(&out, "X-Email-Soft-Attachment-Warning: attachment bytes require the provider original and are omitted from this fallback\r\n")
	}

	if textBody != "" && htmlBody != "" {
		var parts bytes.Buffer
		mw := multipart.NewWriter(&parts)
		fmt.Fprintf(&out, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", mw.Boundary())
		plainHeader := textproto.MIMEHeader{"Content-Type": {"text/plain; charset=utf-8"}, "Content-Transfer-Encoding": {"8bit"}}
		plain, _ := mw.CreatePart(plainHeader)
		_, _ = io.WriteString(plain, textBody)
		htmlHeader := textproto.MIMEHeader{"Content-Type": {"text/html; charset=utf-8"}, "Content-Transfer-Encoding": {"8bit"}}
		html, _ := mw.CreatePart(htmlHeader)
		_, _ = io.WriteString(html, htmlBody)
		_ = mw.Close()
		_, _ = out.Write(parts.Bytes())
	} else if htmlBody != "" {
		fmt.Fprint(&out, "Content-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
		_, _ = io.WriteString(&out, htmlBody)
	} else {
		fmt.Fprint(&out, "Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
		_, _ = io.WriteString(&out, textBody)
	}
	return out.Bytes()
}

func cleanHeader(s string) string {
	return strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(s))
}

func safeExportName(s any) string {
	name := strings.TrimSpace(fmt.Sprint(s))
	name = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\' || r == ':' || unicode.IsControl(r):
			return '-'
		default:
			return r
		}
	}, name)
	for strings.Contains(name, "--") {
		name = strings.ReplaceAll(name, "--", "-")
	}
	name = strings.Trim(name, " .-")
	if name == "" {
		name = "mail"
	}
	runes := []rune(name)
	if len(runes) > 80 {
		name = string(runes[:80])
	}
	// filepath.Base is a final defence against platform-specific separators.
	return filepath.Base(name)
}

func appendExportWarning(warnings []string, warning string) []string {
	const maxWarnings = 100
	if len(warnings) < maxWarnings {
		return append(warnings, warning)
	}
	if len(warnings) == maxWarnings {
		return append(warnings, "Further warnings omitted from this manifest.")
	}
	return warnings
}
