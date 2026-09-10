package gmail

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	"github.com/neutron-build/neutron/mail"
	"google.golang.org/api/gmail/v1"
)

// Body fetches and decodes a message body.
func (a *Adapter) Body(ctx context.Context, id mail.MessageID) (*mail.Body, error) {
	var m *gmail.Message
	err := a.call(ctx, costMessagesGet, func() (err error) {
		m, err = a.svc.Users.Messages.Get(a.user, nativeID(id)).
			Format("full").Context(ctx).Do()
		return err
	})
	if err != nil {
		return nil, err
	}

	body := &mail.Body{MessageID: id}
	if m.Payload != nil {
		collectParts(m.Payload, body)
	}
	return body, nil
}

// collectParts walks the MIME tree, decoding text and cataloguing the rest.
func collectParts(p *gmail.MessagePart, body *mail.Body) {
	switch {
	case strings.HasPrefix(p.MimeType, "multipart/"):
		for _, child := range p.Parts {
			collectParts(child, body)
		}
		return
	case p.MimeType == "text/plain" && p.Filename == "":
		body.Text += decodeBody(p)
	case p.MimeType == "text/html" && p.Filename == "":
		body.HTML += decodeBody(p)
	}

	if p.Filename != "" || p.Body != nil && p.Body.AttachmentId != "" {
		disposition := "attachment"
		var cid string
		for _, h := range p.Headers {
			if strings.EqualFold(h.Name, "Content-ID") {
				cid = strings.Trim(h.Value, "<>")
				disposition = "inline"
			}
		}
		var size int64
		if p.Body != nil {
			size = p.Body.Size
		}
		body.Parts = append(body.Parts, mail.BodyPart{
			PartID:      p.PartId,
			Type:        p.MimeType,
			Filename:    p.Filename,
			Disposition: disposition,
			Size:        size,
			ContentID:   cid,
		})
	}
}

func decodeBody(p *gmail.MessagePart) string {
	if p.Body == nil || p.Body.Data == "" {
		return ""
	}
	// Gmail uses base64url without padding.
	raw, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(p.Body.Data)
	if err != nil {
		return ""
	}
	return string(raw)
}

// Raw returns the original RFC 5322 message.
func (a *Adapter) Raw(ctx context.Context, id mail.MessageID) (io.ReadCloser, error) {
	var m *gmail.Message
	err := a.call(ctx, costMessagesGet, func() (err error) {
		m, err = a.svc.Users.Messages.Get(a.user, nativeID(id)).
			Format("raw").Context(ctx).Do()
		return err
	})
	if err != nil {
		return nil, err
	}
	raw, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(m.Raw)
	if err != nil {
		return nil, fmt.Errorf("gmail: decode raw: %w", err)
	}
	return io.NopCloser(strings.NewReader(string(raw))), nil
}

// Attachment streams one part's decoded content.
func (a *Adapter) Attachment(ctx context.Context, id mail.MessageID, partID string) (io.ReadCloser, error) {
	var m *gmail.Message
	err := a.call(ctx, costMessagesGet, func() (err error) {
		m, err = a.svc.Users.Messages.Get(a.user, nativeID(id)).
			Format("full").Context(ctx).Do()
		return err
	})
	if err != nil {
		return nil, err
	}

	attachID := findAttachmentID(m.Payload, partID)
	if attachID == "" {
		return nil, fmt.Errorf("gmail: %w: part %s of %s", mail.ErrNotFound, partID, id)
	}

	var att *gmail.MessagePartBody
	err = a.call(ctx, costAttachmentsGet, func() (err error) {
		att, err = a.svc.Users.Messages.Attachments.
			Get(a.user, nativeID(id), attachID).Context(ctx).Do()
		return err
	})
	if err != nil {
		return nil, err
	}
	raw, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(att.Data)
	if err != nil {
		return nil, fmt.Errorf("gmail: decode attachment: %w", err)
	}
	return io.NopCloser(strings.NewReader(string(raw))), nil
}

func findAttachmentID(p *gmail.MessagePart, partID string) string {
	if p == nil {
		return ""
	}
	if p.PartId == partID && p.Body != nil {
		return p.Body.AttachmentId
	}
	for _, child := range p.Parts {
		if id := findAttachmentID(child, partID); id != "" {
			return id
		}
	}
	return ""
}
