// Package gmail implements the mail.Adapter interface over the Gmail API.
//
// The Gmail API rather than IMAP, because it is the only path with a real
// change feed: historyId gives incremental sync, message IDs are stable
// across labels, and threadId means threading needs no reconstruction. IMAP
// against Gmail has none of that.
//
// Scope note: every useful Gmail scope is restricted, which means the OAuth
// client needs verification and an annual CASA assessment before serving
// users beyond the testing cohort.
package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/neutron-build/neutron/mail"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// Adapter is a Gmail client bound to one account.
type Adapter struct {
	svc    *gmail.Service
	budget *budget

	// user is always "me" in practice; the API keys off the token.
	user string
}

// New wraps an authenticated Gmail service. account keys the quota budget
// and must be stable for the mailbox across runs: Google meters the user,
// not the connection.
//
// The caller supplies the token source, so refresh, storage, and revocation
// stay outside this package — x/oauth2 already handles refresh correctly and
// reimplementing it here would only add a second thing to get wrong.
func New(ctx context.Context, account string, opts ...option.ClientOption) (*Adapter, error) {
	svc, err := gmail.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("gmail: new service: %w", err)
	}
	return &Adapter{svc: svc, budget: budgetFor(account), user: "me"}, nil
}

func (a *Adapter) Provider() mail.Provider { return mail.ProviderGmail }
func (a *Adapter) Close() error            { return nil }

// classify maps Google API errors onto the engine's typed errors.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		switch gerr.Code {
		case 401, 403:
			// 403 is ambiguous at Google — it covers both a revoked grant
			// and a quota exhaustion. The reason field disambiguates, and
			// getting this wrong means either retrying forever or asking a
			// user to reconnect an account that was merely throttled.
			for _, e := range gerr.Errors {
				switch e.Reason {
				case "rateLimitExceeded", "userRateLimitExceeded", "quotaExceeded":
					return fmt.Errorf("gmail: %s: %w", e.Reason, mail.ErrRateLimited)
				}
			}
			return fmt.Errorf("gmail: %s: %w", gerr.Message, mail.ErrReauthRequired)
		case 404:
			return fmt.Errorf("gmail: %s: %w", gerr.Message, mail.ErrNotFound)
		case 429:
			return fmt.Errorf("gmail: %s: %w", gerr.Message, mail.ErrRateLimited)
		}
	}
	return err
}

// Mailboxes lists labels. Gmail models folders as labels, and a message can
// carry several at once.
func (a *Adapter) Mailboxes(ctx context.Context) ([]mail.Mailbox, error) {
	var res *gmail.ListLabelsResponse
	err := a.call(ctx, costLabelsList, func() (err error) {
		res, err = a.svc.Users.Labels.List(a.user).Context(ctx).Do()
		return err
	})
	if err != nil {
		return nil, err
	}

	boxes := make([]mail.Mailbox, 0, len(res.Labels))
	for _, l := range res.Labels {
		boxes = append(boxes, mail.Mailbox{
			ID:     mail.MailboxID(l.Id),
			Name:   l.Name,
			Role:   roleFrom(l.Id),
			Native: l.Id,
		})
	}
	return boxes, nil
}

// roleFrom maps Gmail's reserved label IDs onto the canonical role. The IDs
// are stable and locale-independent; the display names are neither.
func roleFrom(labelID string) mail.Role {
	switch labelID {
	case "INBOX":
		return mail.RoleInbox
	case "SENT":
		return mail.RoleSent
	case "DRAFT":
		return mail.RoleDrafts
	case "TRASH":
		return mail.RoleTrash
	case "SPAM":
		return mail.RoleJunk
	default:
		return mail.RoleNone
	}
}

// Sync returns changes since cur.
//
// The cursor is a historyId. Google retains history for a limited window, so
// a cursor older than that window is rejected with 404 — reported here as a
// reset, which is the same recovery path an IMAP UIDVALIDITY change takes.
func (a *Adapter) Sync(ctx context.Context, box mail.MailboxID, cur mail.Cursor) (*mail.Changes, error) {
	if cur == "" {
		var profile *gmail.Profile
		err := a.call(ctx, costGetProfile, func() (err error) {
			profile, err = a.svc.Users.GetProfile(a.user).Context(ctx).Do()
			return err
		})
		if err != nil {
			return nil, err
		}
		return a.initialSync(ctx, box, "", profile.HistoryId)
	}
	if strings.HasPrefix(string(cur), "gmail-initial:") {
		pageToken, historyID, err := decodeInitialCursor(cur)
		if err != nil {
			return &mail.Changes{Reset: true}, nil
		}
		return a.initialSync(ctx, box, pageToken, historyID)
	}

	var pageToken string
	start, err := strconv.ParseUint(string(cur), 10, 64)
	if strings.HasPrefix(string(cur), "gmail-history:") {
		pageToken, start, err = decodeHistoryCursor(cur)
	}
	if err != nil {
		return &mail.Changes{Reset: true}, nil
	}

	call := a.svc.Users.History.List(a.user).
		StartHistoryId(start).
		LabelId(string(box)).
		MaxResults(500)
	if pageToken != "" {
		call = call.PageToken(pageToken)
	}

	var res *gmail.ListHistoryResponse
	err = a.call(ctx, costHistoryList, func() (err error) {
		res, err = call.Context(ctx).Do()
		return err
	})
	if errors.Is(err, mail.ErrNotFound) {
		// The history window has moved past this cursor.
		return &mail.Changes{Reset: true}, nil
	}
	if err != nil {
		return nil, err
	}

	changes := &mail.Changes{More: res.NextPageToken != ""}
	if changes.More {
		changes.Next = encodeHistoryCursor(res.NextPageToken, start)
	} else {
		changes.Next = mail.Cursor(strconv.FormatUint(res.HistoryId, 10))
	}

	// History records carry message IDs, not envelopes. Deduplicating here
	// matters: one message touched several times in a window appears in
	// every record, and refetching it once per appearance burns quota that
	// Gmail counts per user per second.
	added := map[string]bool{}
	removed := map[string]bool{}

	for _, h := range res.History {
		for _, m := range h.MessagesAdded {
			added[m.Message.Id] = true
			delete(removed, m.Message.Id)
		}
		for _, m := range h.MessagesDeleted {
			removed[m.Message.Id] = true
			delete(added, m.Message.Id)
		}
		for _, l := range h.LabelsAdded {
			if !removed[l.Message.Id] {
				added[l.Message.Id] = true
			}
		}
		for _, l := range h.LabelsRemoved {
			if !removed[l.Message.Id] {
				added[l.Message.Id] = true
			}
		}
	}

	for id := range added {
		changes.Changes = append(changes.Changes, mail.Change{
			Kind: mail.ChangeUpdated,
			ID:   mail.NativeMessageID(mail.ProviderGmail, id),
		})
	}
	for id := range removed {
		changes.Changes = append(changes.Changes, mail.Change{
			Kind: mail.ChangeDestroyed,
			ID:   mail.NativeMessageID(mail.ProviderGmail, id),
		})
	}
	return changes, nil
}

// initialSync enumerates one label page. historyID is captured before the
// first list request, so changes arriving while a large import paginates are
// replayed rather than skipped when the final page becomes incremental.
//
// The page carries bare IDs and leaves the envelope fetch to the engine,
// which skips messages the mirror already holds. That matters for Gmail
// specifically: a message appears under every label it carries, and one
// metadata fetch returns all of them, so fetching here would pay for each
// message once per label.
func (a *Adapter) initialSync(ctx context.Context, box mail.MailboxID, pageToken string, historyID uint64) (*mail.Changes, error) {
	call := a.svc.Users.Messages.List(a.user).
		LabelIds(string(box)).
		MaxResults(500)
	if pageToken != "" {
		call = call.PageToken(pageToken)
	}
	var res *gmail.ListMessagesResponse
	err := a.call(ctx, costMessagesList, func() (err error) {
		res, err = call.Context(ctx).Do()
		return err
	})
	if err != nil {
		return nil, err
	}

	changes := &mail.Changes{
		More:             res.NextPageToken != "",
		EnumerationStart: pageToken == "",
		Complete:         res.NextPageToken == "",
	}
	if changes.More {
		changes.Next = encodeInitialCursor(res.NextPageToken, historyID)
	} else {
		changes.Next = mail.Cursor(strconv.FormatUint(historyID, 10))
	}
	for _, m := range res.Messages {
		changes.Changes = append(changes.Changes, mail.Change{
			Kind: mail.ChangeCreated, ID: mail.NativeMessageID(mail.ProviderGmail, m.Id),
		})
	}
	return changes, nil
}

func encodeInitialCursor(pageToken string, historyID uint64) mail.Cursor {
	raw, _ := json.Marshal(struct {
		Page    string `json:"page"`
		History uint64 `json:"history"`
	}{pageToken, historyID})
	return mail.Cursor("gmail-initial:" + base64.RawURLEncoding.EncodeToString(raw))
}

func decodeInitialCursor(cur mail.Cursor) (string, uint64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(string(cur), "gmail-initial:"))
	if err != nil {
		return "", 0, err
	}
	var state struct {
		Page    string `json:"page"`
		History uint64 `json:"history"`
	}
	if err := json.Unmarshal(raw, &state); err != nil || state.Page == "" || state.History == 0 {
		if err == nil {
			err = fmt.Errorf("missing initial cursor fields")
		}
		return "", 0, err
	}
	return state.Page, state.History, nil
}

func encodeHistoryCursor(pageToken string, start uint64) mail.Cursor {
	raw, _ := json.Marshal(struct {
		Page  string `json:"page"`
		Start uint64 `json:"start"`
	}{pageToken, start})
	return mail.Cursor("gmail-history:" + base64.RawURLEncoding.EncodeToString(raw))
}

func decodeHistoryCursor(cur mail.Cursor) (string, uint64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(string(cur), "gmail-history:"))
	if err != nil {
		return "", 0, err
	}
	var state struct {
		Page  string `json:"page"`
		Start uint64 `json:"start"`
	}
	if err := json.Unmarshal(raw, &state); err != nil || state.Page == "" || state.Start == 0 {
		if err == nil {
			err = fmt.Errorf("missing history cursor fields")
		}
		return "", 0, err
	}
	return state.Page, state.Start, nil
}

// Envelopes fetches message metadata, one call per message: the Gmail API
// has no bulk metadata read, and each call costs the same 20 units whatever
// format it asks for. format=metadata is chosen for payload size, not quota.
func (a *Adapter) Envelopes(ctx context.Context, ids []mail.MessageID) ([]mail.Envelope, error) {
	out := make([]mail.Envelope, 0, len(ids))
	for _, id := range ids {
		var m *gmail.Message
		err := a.call(ctx, costMessagesGet, func() (err error) {
			m, err = a.svc.Users.Messages.Get(a.user, nativeID(id)).
				Format("metadata").
				MetadataHeaders("From", "To", "Cc", "Bcc", "Reply-To",
					"Subject", "Date", "Message-ID", "In-Reply-To", "References").
				Context(ctx).Do()
			return err
		})
		if err != nil {
			if errors.Is(err, mail.ErrNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, toEnvelope(m))
	}
	return out, nil
}

func nativeID(id mail.MessageID) string {
	return strings.TrimPrefix(string(id), "n:"+string(mail.ProviderGmail)+":")
}

func toEnvelope(m *gmail.Message) mail.Envelope {
	env := mail.Envelope{
		ID:                 mail.NativeMessageID(mail.ProviderGmail, m.Id),
		ThreadID:           mail.ThreadID(m.ThreadId),
		Size:               m.SizeEstimate,
		Preview:            m.Snippet,
		MailboxIDsComplete: true,
	}

	// InternalDate is milliseconds since the epoch.
	if m.InternalDate > 0 {
		env.ReceivedAt = time.UnixMilli(m.InternalDate).UTC()
	}

	for _, l := range m.LabelIds {
		env.MailboxIDs = append(env.MailboxIDs, mail.MailboxID(l))
		switch l {
		case "UNREAD":
			// Gmail models read state as the absence of a label, so Seen
			// is the inverse and is set after the loop.
		case "STARRED":
			env.Keywords.Flagged = true
		case "DRAFT":
			env.Keywords.Draft = true
		}
	}
	env.Keywords.Seen = !hasLabel(m.LabelIds, "UNREAD")

	if m.Payload != nil {
		for _, h := range m.Payload.Headers {
			switch strings.ToLower(h.Name) {
			case "subject":
				env.Subject = h.Value
			case "from":
				env.From = parseAddrs(h.Value)
			case "to":
				env.To = parseAddrs(h.Value)
			case "cc":
				env.Cc = parseAddrs(h.Value)
			case "bcc":
				env.Bcc = parseAddrs(h.Value)
			case "reply-to":
				env.ReplyTo = parseAddrs(h.Value)
			case "message-id":
				env.MessageIDHeader = h.Value
			case "in-reply-to":
				env.InReplyTo = mail.ParseReferences(h.Value)
			case "references":
				env.References = mail.ParseReferences(h.Value)
			case "date":
				if t, err := time.Parse(time.RFC1123Z, h.Value); err == nil {
					env.SentAt = t
				}
			}
		}
		env.HasAttachment = payloadHasAttachment(m.Payload)
	}

	env.Fingerprint = mail.ComputeFingerprint(&env)
	return env
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

func payloadHasAttachment(p *gmail.MessagePart) bool {
	if p.Filename != "" {
		return true
	}
	for _, part := range p.Parts {
		if payloadHasAttachment(part) {
			return true
		}
	}
	return false
}

func parseAddrs(header string) []mail.Address {
	var out []mail.Address
	for _, raw := range strings.Split(header, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if open := strings.LastIndex(raw, "<"); open >= 0 {
			if close := strings.Index(raw[open:], ">"); close >= 0 {
				name := strings.Trim(strings.TrimSpace(raw[:open]), `"`)
				out = append(out, mail.Address{
					Name:  name,
					Email: raw[open+1 : open+close],
				})
				continue
			}
		}
		out = append(out, mail.Address{Email: raw})
	}
	return out
}

// Apply pushes a mutation by modifying labels.
func (a *Adapter) Apply(ctx context.Context, op mail.Operation) error {
	ids := make([]string, 0, len(op.IDs))
	for _, id := range op.IDs {
		ids = append(ids, nativeID(id))
	}
	if len(ids) == 0 {
		return nil
	}

	var req gmail.BatchModifyMessagesRequest
	req.Ids = ids

	switch op.Kind {
	case mail.OpAddKeyword:
		// Read state is the absence of UNREAD, so marking seen removes a
		// label rather than adding one.
		if strings.EqualFold(op.Keyword, "seen") {
			req.RemoveLabelIds = []string{"UNREAD"}
		} else {
			req.AddLabelIds = []string{gmailLabel(op.Keyword)}
		}
	case mail.OpRemoveKeyword:
		if strings.EqualFold(op.Keyword, "seen") {
			req.AddLabelIds = []string{"UNREAD"}
		} else {
			req.RemoveLabelIds = []string{gmailLabel(op.Keyword)}
		}
	case mail.OpMove:
		req.AddLabelIds = []string{string(op.Target)}
		req.RemoveLabelIds = []string{"INBOX"}
	case mail.OpDelete:
		req.AddLabelIds = []string{"TRASH"}
		req.RemoveLabelIds = []string{"INBOX"}
	default:
		return fmt.Errorf("gmail: unsupported operation %d", op.Kind)
	}

	return a.call(ctx, costMessagesBatchMod, func() error {
		return a.svc.Users.Messages.BatchModify(a.user, &req).Context(ctx).Do()
	})
}

func gmailLabel(keyword string) string {
	switch strings.ToLower(keyword) {
	case "flagged":
		return "STARRED"
	case "draft":
		return "DRAFT"
	default:
		return strings.ToUpper(keyword)
	}
}

var _ mail.Adapter = (*Adapter)(nil)
