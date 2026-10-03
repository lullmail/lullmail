package main

// Outbound with an undo window (SPEC §6.1): sends sit in-process for a few
// seconds before provider submission; DELETE cancels. Timers and bounded
// completion receipts are process-local; durable recovery needs an outbox.

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	netmail "net/mail"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/neutron-build/neutron/mail"
)

const undoWindow = 5 * time.Second

type pendingSend struct {
	cancel context.CancelFunc
	done   <-chan error
	state  sendState
	// Idempotent-acceptance bookkeeping (audit 5 SEND-02): the client's
	// Idempotency-Key and the request hash it was answered for. The key
	// is owner-scoped and indexes pending work; a bounded receipt retains the
	// outcome afterward without retaining message content.
	key       string
	reqHash   string
	undoUntil time.Time
}

type sendState uint8

const (
	sendPending sendState = iota
	sendDelivering
)

type sendQueue struct {
	mu    sync.Mutex
	sends map[string]*pendingSend
	// keyed indexes live work; receipts retain completed/ambiguous/cancelled
	// outcomes after the payload and its budget slot have been released.
	keyed    map[string]string
	receipts map[string]sendReceipt
	// Aggregate admission budget: per-request caps alone do not bound how
	// many accepted compositions (decoded attachments included) can sit in
	// the process at once (audit 3 SEND-03).
	budget sendBudget
}

// Receipts are deliberately bounded and process-local. They cover ordinary
// lost-response retries after transport completion, but do not replace the
// durable outbox: expiry, capacity eviction, or restart can forget a key.
const (
	sendReceiptTTL   = 24 * time.Hour
	sendReceiptLimit = 4096
)

type sendOutcome string

const (
	sendSubmitted sendOutcome = "submitted"
	sendAmbiguous sendOutcome = "ambiguous"
	sendCancelled sendOutcome = "cancelled"
)

type sendReceipt struct {
	id        string
	reqHash   string
	outcome   sendOutcome
	expiresAt time.Time
}

// sendBudget bounds concurrently accepted sends by job count and estimated
// retained bytes. Zero value is ready; acquire/release are the whole API.
type sendBudget struct {
	mu    sync.Mutex
	jobs  int
	bytes int64
}

const (
	sendMaxJobs        = 8
	sendMaxBytes int64 = 128 << 20
	sendOverhead int64 = 1 << 10
)

var errSendCapacity = errors.New("send capacity exhausted")

func (b *sendBudget) acquire(n int64) (func(), error) {
	if n < 0 {
		n = 0
	}
	b.mu.Lock()
	if b.jobs >= sendMaxJobs || b.bytes+n > sendMaxBytes {
		b.mu.Unlock()
		return nil, errSendCapacity
	}
	b.jobs++
	b.bytes += n
	b.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			b.jobs--
			b.bytes -= n
			b.mu.Unlock()
		})
	}, nil
}

// outgoingWeight estimates what one accepted composition retains until its
// worker finishes. Every field the job keeps for the delivery call counts,
// not just the bodies: a large subject or a long recipient list consumes
// real memory while the job waits, and omitting them let compositions slip
// past the admission budget untouched (audit 4 F14).
func outgoingWeight(out *mail.Outgoing) int64 {
	n := sendOverhead + int64(len(out.Subject)+len(out.Text)+len(out.HTML)+len(out.InReplyTo))
	for _, addresses := range [][]mail.Address{{out.From}, out.To, out.Cc, out.Bcc} {
		for _, address := range addresses {
			n += int64(len(address.Name)+len(address.Email)) + 64
		}
	}
	for _, ref := range out.References {
		n += int64(len(ref)) + 16
	}
	for _, att := range out.Attachments {
		n += int64(len(att.Data)+len(att.Filename)+len(att.ContentType)) + 64
	}
	return n
}

type deliverFunc func(context.Context, *mail.Outgoing) error

// decodeSem bounds how many large request bodies decode at once (audit
// OPS-01): the slot is acquired BEFORE the JSON reader allocates, so a
// burst of multi-megabyte sends queues at admission instead of
// multiplying in-flight allocations. 2 slots at the 34 MiB send cap
// bound peak decode memory to ~68 MiB.
var decodeSem = make(chan struct{}, 2)

// acquireDecodeSlot waits for a decode slot with the request's context;
// a request that gives up waiting answers 503, not a timeout pile-up.
func acquireDecodeSlot(ctx context.Context) (func(), bool) {
	select {
	case decodeSem <- struct{}{}:
		return func() { <-decodeSem }, true
	case <-ctx.Done():
		return nil, false
	}
}

// sendBodyReadBudget bounds how long one send request may take to UPLOAD
// its body (audit 5 OPS-01): the decode slot and its 34 MiB allocation
// used to be holdable by an arbitrarily slow client, and two of those
// wedged every other send. The header timeout never sees body bytes.
const sendBodyReadBudget = 60 * time.Second

// withBodyDeadline gives one body-bearing handler a hard upload deadline:
// a context bound for handler work AND a transport read deadline, so a
// client that trickles bytes (or never finishes) is cut at the budget
// instead of holding admission indefinitely. Test recorders and exotic
// proxies may not support transport deadlines; there the context bound
// still applies and the deadline degrades with a warning rather than
// failing every request (audit 5 OPS-01).
func withBodyDeadline(limit time.Duration, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), limit)
		defer cancel()
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(time.Now().Add(limit)); err != nil {
			slog.Default().Warn("request transport cannot enforce upload deadlines", "err", err)
		} else {
			defer controller.SetReadDeadline(time.Time{})
		}
		next(w, r.WithContext(ctx))
	}
}

func newSendQueue() *sendQueue {
	return &sendQueue{sends: map[string]*pendingSend{}, keyed: map[string]string{}, receipts: map[string]sendReceipt{}}
}

func (a *App) handleSend(w http.ResponseWriter, r *http.Request) {
	// Idempotent acceptance (audit 5 SEND-02): the send route is excluded
	// from the generic api_mutations wrapper (a 34 MiB body cannot ride
	// the 1 MiB ledger buffer), but a lost acceptance response still needs
	// the same one-submission contract. The raw body is hashed BEFORE
	// decoding so the retry comparison is exact; the key is honored for
	// the active entry and bounded process-local completion receipt.
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if len(idempotencyKey) > idempotencyKeyLimit {
		writeProblem(w, http.StatusRequestEntityTooLarge, "Key Too Long",
			fmt.Sprintf("Idempotency-Key must be at most %d characters", idempotencyKeyLimit))
		return
	}
	var req struct {
		AccountID string `json:"account_id"` // email_accounts.id; empty = first
		To        string `json:"to"`
		Cc        string `json:"cc"`
		Bcc       string `json:"bcc"`
		Subject   string `json:"subject"`
		Text      string `json:"text"`
		HTML      string `json:"html"` // optional rich body; sent as multipart/alternative with Text
		ReplyToID string `json:"reply_to_message_id"`

		Attachments []sendAttachmentRequest `json:"attachments"`
	}
	// Attachments ride inside the JSON body, so the read cap has to cover
	// them at their wire size: base64 grows the decoded bytes by 4/3, so
	// the advertised 25 MiB total alone arrives as ~33.4 MiB of payload.
	// 34 MiB covers that plus the envelope; past it the request is refused
	// as too large, not reported as malformed JSON. The decode slot is
	// taken BEFORE the reader allocates (audit OPS-01): concurrent fat
	// sends wait at admission rather than decoding in parallel.
	releaseDecode, ok := acquireDecodeSlot(r.Context())
	if !ok {
		writeProblem(w, http.StatusServiceUnavailable, "Busy",
			"too many large requests are decoding — retry shortly")
		return
	}
	defer releaseDecode()
	rawBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 34<<20))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeProblem(w, http.StatusRequestEntityTooLarge, "Request Too Large",
				fmt.Sprintf("request exceeds the %d MiB cap; attachments are capped at 25 MiB decoded in total", maxErr.Limit>>20))
			return
		}
		writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if idempotencyKey != "" {
		// Receipts can outlive a session or owner deletion. Keep retry
		// identities isolated even if a later owner chooses the same key.
		uid, err := a.userID(r.Context())
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
			return
		}
		if a.replayOutbox(w, r, uid, idempotencyKey, sendRequestHash(r, rawBody)) {
			return
		}

	}
	if err := json.Unmarshal(rawBody, &req); err != nil {
		writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if req.To == "" || req.Subject == "" || (req.Text == "" && strings.TrimSpace(req.HTML) == "") {
		writeProblem(w, http.StatusUnprocessableEntity, "Missing Fields", "to, subject and a body (text or html) are required")
		return
	}
	toAddrs, ok := outboundRecipients(req.To)
	if !ok || len(toAddrs) == 0 || strings.ContainsAny(req.Subject, "\r\n") {
		writeProblem(w, http.StatusUnprocessableEntity, "Invalid Headers", "to must be a comma-separated address list and headers cannot contain newlines")
		return
	}
	ccAddrs, ok := outboundRecipients(req.Cc)
	if !ok {
		writeProblem(w, http.StatusUnprocessableEntity, "Invalid Headers", "cc must be a comma-separated address list")
		return
	}
	bccAddrs, ok := outboundRecipients(req.Bcc)
	if !ok {
		writeProblem(w, http.StatusUnprocessableEntity, "Invalid Headers", "bcc must be a comma-separated address list")
		return
	}
	attachments, problem := decodeAttachments(req.Attachments, 25<<20)
	if problem != "" {
		writeProblem(w, http.StatusUnprocessableEntity, "Attachment Rejected", problem)
		return
	}
	if req.ReplyToID != "" && req.AccountID == "" {
		writeProblem(w, http.StatusUnprocessableEntity, "Missing Account", "account_id is required when replying")
		return
	}
	if strings.ContainsAny(strings.TrimSpace(req.HTML), "\r") {
		req.HTML = strings.ReplaceAll(strings.ReplaceAll(req.HTML, "\r\n", "\n"), "\r", "\n")
	}
	if req.Text == "" {
		req.Text = htmlFallbackText(req.HTML)
	}
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	var mirror, provider string
	q := `SELECT mirror_account_id, provider FROM email_accounts WHERE user_id = $1`
	args := []any{uid}
	if req.AccountID != "" {
		q += ` AND (id::text = $2 OR mirror_account_id = $2)`
		args = append(args, req.AccountID)
	}
	q += ` ORDER BY created_at LIMIT 1`
	if err := a.db.QueryRowContext(r.Context(), q, args...).Scan(&mirror, &provider); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, http.StatusPreconditionFailed, "No Account", "connect an account first")
			return
		}
		// A database failure is not "no account connected": masking it as
		// 412 sends the user chasing a setup problem that does not exist
		// and teaches offline replay to drop the draft (audit 4 F23).
		a.log.Error("send account lookup failed", "err", err)
		writeLookupProblem(w, err, "account")
		return
	}
	// Transport limits are enforced before the job is accepted, not inside
	// delivery: a request the chosen transport is guaranteed to reject
	// locally must fail synchronously and leave the draft alone
	// (audit SEND-06).
	if problem := validateTransportAttachments(provider, attachments); problem != "" {
		writeProblem(w, http.StatusUnprocessableEntity, "Attachment Rejected", problem)
		return
	}
	// A JMAP account's API token authorizes JMAP reads; it is not a
	// verified SMTP submission credential, and deliveryFor would otherwise
	// fall through to SMTPFor and dial the API host on port 587 with the
	// token as a password. The send would be accepted and then fail
	// asynchronously after the undo window — the draft silently lost.
	// Fail synchronously until a real JMAP submission transport exists
	// (audit 4 F07).
	if provider == string(mail.ProviderJMAP) {
		writeProblem(w, http.StatusUnprocessableEntity, "Sending Not Configured",
			"this JMAP account has no verified send transport — the draft was not queued")
		return
	}

	// Resolve the reply parent from the mirror so threading headers are
	// built from the stored message, never trusted from the client. The
	// parent may live on any of the user's accounts — a reply must go out
	// through the account the thread belongs to, not whichever connected
	// first.
	var outgoing *mail.Outgoing
	if req.ReplyToID != "" {
		var parentAcct, parentProvider string
		err := a.db.QueryRowContext(r.Context(), `
	SELECT m.account_id, ea.provider FROM mail_messages m
	JOIN email_accounts ea ON ea.mirror_account_id = m.account_id AND ea.user_id = $1
	WHERE m.id = $2 AND (ea.id::text = $3 OR m.account_id = $3)`,
			uid, req.ReplyToID, req.AccountID).Scan(&parentAcct, &parentProvider)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				a.log.Error("reply parent lookup failed", "err", err)
			}
			writeLookupProblem(w, err, "reply parent")
			return
		}
		if problem := validateTransportAttachments(parentProvider, attachments); problem != "" {
			writeProblem(w, http.StatusUnprocessableEntity, "Attachment Rejected", problem)
			return
		}
		// Same submission-transport rule as the fresh-send path: a reply
		// must go out through the account the thread belongs to, and a
		// JMAP-only account has no verified submission path (audit 4 F07).
		if parentProvider == string(mail.ProviderJMAP) {
			writeProblem(w, http.StatusUnprocessableEntity, "Sending Not Configured",
				"this JMAP account has no verified send transport — the draft was not queued")
			return
		}
		parent, err := a.store.Envelope(r.Context(), mail.AccountID(parentAcct), mail.MessageID(req.ReplyToID))
		if err != nil {
			writeProblem(w, http.StatusNotFound, "Parent Not Found", "reply_to_message_id does not resolve")
			return
		}
		_, from, ok := a.deliveryFor(r.Context(), mail.AccountID(parentAcct), req.ReplyToID)
		if !ok {
			writeProblem(w, http.StatusPreconditionFailed, "No Send Credential", "cannot send for this account")
			return
		}
		outgoing = mail.ReplyTo(parent, from, req.Text)
		outgoing.HTML = req.HTML
		outgoing.Cc = ccAddrs
		outgoing.Bcc = bccAddrs
		outgoing.Attachments = attachments
		if len(toAddrs) > 0 {
			outgoing.To = toAddrs
		}
		if strings.TrimSpace(req.Subject) != "" {
			outgoing.Subject = req.Subject
		}
		a.acceptOutbox(w, r, uid, parentAcct, req.ReplyToID, outgoing, rawBody)
		return
	}

	_, from, ok := a.deliveryFor(r.Context(), mail.AccountID(mirror), "")
	if !ok {
		writeProblem(w, http.StatusPreconditionFailed, "No Send Credential", "cannot send for this account")
		return
	}
	outgoing = &mail.Outgoing{
		From:        from,
		To:          toAddrs,
		Cc:          ccAddrs,
		Bcc:         bccAddrs,
		Subject:     req.Subject,
		Text:        req.Text,
		HTML:        req.HTML,
		Attachments: attachments,
	}
	a.acceptOutbox(w, r, uid, mirror, "", outgoing, rawBody)
}

// sendRequestHash binds one send acceptance to its exact wire body. The
// hash is only meaningful together with a client Idempotency-Key.
func sendRequestHash(r *http.Request, rawBody []byte) string {
	if r.Header.Get("Idempotency-Key") == "" {
		return ""
	}
	return mutationRequestHash(r.Method, r.URL.Path, r.URL.RawQuery, rawBody)
}

type sendAttachmentRequest struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	// DataB64 is a POINTER so a present-but-empty string — the valid
	// base64 of a zero-byte file — is distinguishable from an omitted
	// field (audit 5 SEND-03).
	DataB64 *string `json:"data_base64"`
}

// validateTransportAttachments runs the chosen transport's attachment
// validator ahead of queue acceptance, reusing the exact delivery-time
// check so the two limits can never drift (audit SEND-06).
func validateTransportAttachments(provider string, attachments []mail.Attachment) string {
	if provider != "graph" || len(attachments) == 0 {
		return ""
	}
	if _, err := graphAttachments(attachments); err != nil {
		return err.Error()
	}
	return ""
}

// decodeAttachments turns JSON-carried base64 attachments into engine
// attachments with hard caps: one file at most 15 MiB decoded, the set at
// most totalMiB. A PRESENT empty data_base64 is a valid zero-byte
// attachment and is kept; an OMITTED one is rejected (audit 5 SEND-03).
// The engine sanitizes header values; these caps exist so a fat request
// cannot balloon server memory before composition.
func decodeAttachments(reqs []sendAttachmentRequest, totalBytes int) ([]mail.Attachment, string) {
	if len(reqs) == 0 {
		return nil, ""
	}
	if len(reqs) > 20 {
		return nil, "at most 20 attachments per message"
	}
	out := make([]mail.Attachment, 0, len(reqs))
	var total int
	for i, ra := range reqs {
		if ra.DataB64 == nil {
			return nil, fmt.Sprintf("attachment %d has no data", i+1)
		}
		if len(*ra.DataB64) > base64.StdEncoding.EncodedLen(15<<20) {
			return nil, fmt.Sprintf("attachment %d exceeds 15 MiB (encoded size limit)", i+1)
		}
		data, err := base64.StdEncoding.DecodeString(*ra.DataB64)
		if err != nil {
			return nil, fmt.Sprintf("attachment %d is not valid base64", i+1)
		}
		if len(data) > 15<<20 {
			return nil, fmt.Sprintf("attachment %d exceeds 15 MiB", i+1)
		}
		total += len(data)
		if total > totalBytes {
			return nil, "attachments exceed the 25 MiB total"
		}
		out = append(out, mail.Attachment{Filename: ra.Filename, ContentType: ra.ContentType, Data: data})
	}
	return out, ""
}

func outboundRecipients(list string) ([]mail.Address, bool) {
	if strings.ContainsAny(list, "\r\n") {
		return nil, false
	}
	list = strings.TrimSpace(list)
	if list == "" {
		return nil, true
	}
	parsed, err := netmail.ParseAddressList(list)
	if err != nil {
		return nil, false
	}
	out := make([]mail.Address, 0, len(parsed))
	for _, p := range parsed {
		if p.Address == "" {
			return nil, false
		}
		out = append(out, mail.Address{Name: p.Name, Email: p.Address})
	}
	return out, true
}

// htmlFallbackText derives the plain-text alternative for an HTML-only
// composition. It is a readability fallback, not a faithful conversion:
// block-level tags become line breaks, list items keep a bullet, entities
// decode, and the rest of the markup drops away so plain-text clients never
// see tags.
var htmlBreakRe = regexp.MustCompile(`(?i)<br\s*/?>|</?p[^>]*>|</div>|</tr>|</li>|<li>`)
var htmlCollapseRe = regexp.MustCompile(`\n{3,}`)

func htmlFallbackText(htmlBody string) string {
	text := htmlBreakRe.ReplaceAllStringFunc(htmlBody, func(tag string) string {
		if strings.EqualFold(tag, "<li>") {
			return "- "
		}
		return "\n"
	})
	text = stripTags(text)
	text = html.UnescapeString(text)
	text = htmlCollapseRe.ReplaceAllString(text, "\n\n")
	lines := strings.Split(text, "\n")
	var out []string
	for _, line := range lines {
		out = append(out, strings.TrimRight(line, " \t"))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func stripTags(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// deliveryFor resolves an account to its outbound sender: same host as
// IMAP, port 587 STARTTLS, unless the account overrides it. replyParent is
// the mirror message id being answered ("" for fresh sends); OAuth
// providers that cannot set threading headers on a flat send need it.
//
// Credentials are resolved at DELIVERY time, not enqueue time: the undo
// timer can outlive an account deletion, and a captured sender holding the
// old password must not submit mail for an account that no longer exists
// (audit SEND-04). The whole delivery additionally holds an account-use
// lease from before credential resolution until filing completes, so a
// concurrent deletion cannot commit between the credential lookup and the
// network submission (audit 3 SEND-02).
func (a *App) deliveryFor(ctx context.Context, account mail.AccountID, replyParent string) (deliverFunc, mail.Address, bool) {
	var provider, address string
	if err := a.db.QueryRowContext(ctx, `SELECT provider,address FROM email_accounts WHERE mirror_account_id=$1`, string(account)).Scan(&provider, &address); err != nil {
		return nil, mail.Address{}, false
	}
	if provider == "gmail" || provider == "graph" {
		deliver := func(ctx context.Context, outgoing *mail.Outgoing) error {
			return a.sendOAuth(ctx, provider, string(account), outgoing, replyParent)
		}
		return a.guardDelivery(account, deliver), mail.Address{Email: address}, true
	}
	from, ok := a.accountSendAddress(ctx, account)
	if !ok {
		return nil, mail.Address{}, false
	}
	deliver := func(ctx context.Context, outgoing *mail.Outgoing) error {
		sender, _, ok := a.SMTPFor(ctx, account)
		if !ok {
			return fmt.Errorf("account %s is no longer connected or has no send credential", account)
		}
		_, raw, err := sender.Send(ctx, outgoing)
		if err != nil {
			return err
		}
		if job, ok := ctx.Value(outboxContextKey{}).(outboxAttempt); ok {
			// Persist transport acceptance before attempting a separate Sent append.
			return a.recordOutboxAccepted(job, raw)
		}
		a.fileSent(ctx, account, raw)
		return nil
	}
	return a.guardDelivery(account, deliver), from, true
}

// guardDelivery holds the account-use lease across the entire outbound
// transport: admission, credential resolution, submission, and Sent-copy
// filing. Deletion therefore waits for an admitted send, and a send whose
// account has started deleting fails admission instead of submitting for a
// disconnected mailbox. The qualified gate context preserves cancellation
// through nested resolver calls without taking another account lease.
func (a *App) guardDelivery(account mail.AccountID, next deliverFunc) deliverFunc {
	return func(ctx context.Context, outgoing *mail.Outgoing) error {
		ctx, release, ok := a.beginAccountWorkCtx(ctx, account)
		if !ok {
			return fmt.Errorf("account %s is being deleted", account)
		}
		defer release()
		if err := ctx.Err(); err != nil {
			return err
		}
		return next(ctx, outgoing)
	}
}

// accountSendAddress reads the sender identity without the credential.
func (a *App) accountSendAddress(ctx context.Context, account mail.AccountID) (mail.Address, bool) {
	var address, displayName string
	err := a.db.QueryRowContext(ctx,
		`SELECT ea.address, u.display_name FROM email_accounts ea JOIN users u ON u.id = ea.user_id
		 WHERE ea.mirror_account_id = $1`, string(account)).Scan(&address, &displayName)
	if err != nil {
		return mail.Address{}, false
	}
	return mail.Address{Email: address, Name: displayName}, true
}

// fileSent appends the exact submitted bytes to the provider's Sent mailbox.
// Without it, mail sent from here never reaches the mirror, so anyone the
// owner writes to first stays an unknown sender and keeps hitting the
// Screener. Gmail and Graph never come down this path — their send APIs keep
// the sent copy themselves. Filing is best-effort: the message is already
// delivered, so a failure here is logged, never surfaced as a send failure.
func (a *App) fileSent(ctx context.Context, account mail.AccountID, raw []byte) {
	var box string
	err := a.db.QueryRowContext(ctx,
		`SELECT id FROM mail_mailboxes WHERE account_id=$1 AND role='sent' LIMIT 1`,
		string(account)).Scan(&box)
	if err != nil {
		a.log.Warn("sent copy not filed: no sent mailbox known for account", "account", account, "err", err)
		return
	}
	cred, err := a.Token(ctx, account)
	if err != nil {
		a.log.Warn("sent copy not filed: credential unavailable", "account", account, "err", err)
		return
	}
	adapter, release, err := a.accountResolver()(ctx, account, cred)
	if err != nil {
		a.log.Warn("sent copy not filed: connect failed", "account", account, "err", err)
		return
	}
	defer release()
	appender, ok := adapter.(mail.Appender)
	if !ok {
		return
	}
	if err := appender.Append(ctx, mail.MailboxID(box), raw); err != nil {
		a.log.Warn("sent copy not filed", "account", account, "mailbox", box, "err", err)
		return
	}
	// The mirror only sees the appended copy when the mailbox next syncs;
	// wake the scheduler so the thread completes within seconds.
	a.sched.Wake(account)
}

func (a *App) enqueue(w http.ResponseWriter, deliver deliverFunc, outgoing *mail.Outgoing, idempotencyKey, reqHash string) {
	a.enqueueAfter(w, deliver, outgoing, idempotencyKey, reqHash, undoWindow)
}

// The delay is explicit so concurrency/outcome tests need no wall-clock wait.
// Production always enters through enqueue with the standard undo window.
func (a *App) enqueueAfter(w http.ResponseWriter, deliver deliverFunc, outgoing *mail.Outgoing, idempotencyKey, reqHash string, delay time.Duration) {
	// Claim/check and publication are one critical section. Checking only
	// in handleSend lets concurrent requests both pass validation and send.
	// Replay before budget admission: a full queue must still replay a
	// previously accepted request without reserving a second job.
	a.sendq.mu.Lock()
	if replay := a.sendq.sendReplay(idempotencyKey, reqHash); replay != nil {
		a.sendq.mu.Unlock()
		replay(w)
		return
	}
	release, err := a.sendq.budget.acquire(outgoingWeight(outgoing))
	if err != nil {
		a.sendq.mu.Unlock()
		w.Header().Set("Retry-After", "5")
		writeProblem(w, http.StatusTooManyRequests, "Too Many Sends",
			"too many sends are already in flight — try again in a few seconds")
		return
	}
	id := time.Now().Format("150405.000") + "-" + newID()[:6]
	ctx, cancel := context.WithTimeout(a.tasksGroup().ctx, 60*time.Second)
	done := make(chan error, 1)
	a.sendq.sends[id] = &pendingSend{
		cancel:    cancel,
		done:      done,
		key:       idempotencyKey,
		reqHash:   reqHash,
		undoUntil: time.Now().Add(delay),
	}
	if idempotencyKey != "" {
		a.sendq.keyed[idempotencyKey] = id
	}

	worker := func(context.Context) {
		outcome := sendCancelled // no transport call has started
		defer func() {
			a.sendq.mu.Lock()
			a.sendq.finishSend(id, outcome)
			a.sendq.mu.Unlock()
			cancel()
			release()
		}()
		select {
		case <-ctx.Done():
			done <- ctx.Err()
			return
		case <-time.After(delay):
		}
		a.sendq.mu.Lock()
		p, ok := a.sendq.sends[id]
		if ok && p.state == sendPending {
			p.state = sendDelivering
		}
		a.sendq.mu.Unlock()
		if !ok {
			return
		}
		// A transport error (or panic) can follow provider acceptance.
		// Preserve ambiguity and NEVER turn a replay into another attempt.
		outcome = sendAmbiguous
		err := deliver(ctx, outgoing)
		if err != nil {
			a.log.Error("send failed", "err", err)
		} else {
			outcome = sendSubmitted
		}
		done <- err
	}
	// The admission result is load-bearing: a group already draining
	// refuses the launch, and the accepted send would otherwise be
	// acknowledged with no worker ever running — while holding budget and
	// a queue slot until restart (audit 5 LIFE-03). Refused work unrolls
	// its reservations and answers 503.
	if !a.launch("send-delivery", worker) {
		cancel()
		a.sendq.forgetSend(id)
		a.sendq.mu.Unlock()
		release()
		writeProblem(w, http.StatusServiceUnavailable, "Shutting Down",
			"the server is shutting down — the message was NOT queued; try again after it restarts")
		return
	}
	// Do not expose an accepted key until worker admission succeeded.
	a.sendq.mu.Unlock()

	writeJSON(w, map[string]any{"queued": id, "undo_seconds": int(delay.Seconds())})
}

// sendReplay snapshots a live acceptance or retained outcome while q.mu is
// held. Its response writes outside the mutex and reads no mutable state.
func (q *sendQueue) sendReplay(key, reqHash string) func(http.ResponseWriter) {
	if key == "" {
		return nil
	}
	q.pruneReceipts(time.Now())
	if id, hit := q.keyed[key]; hit {
		if p := q.sends[id]; p != nil {
			if p.reqHash != reqHash {
				return sendKeyConflict
			}
			remaining := int(time.Until(p.undoUntil).Seconds())
			if remaining < 0 {
				remaining = 0
			}
			return func(w http.ResponseWriter) {
				w.Header().Set("X-Idempotent-Replay", "true")
				writeJSON(w, map[string]any{"queued": id, "undo_seconds": remaining})
			}
		}
	}
	receipt, ok := q.receipts[key]
	if !ok {
		return nil
	}
	if receipt.reqHash != reqHash {
		return sendKeyConflict
	}
	return func(w http.ResponseWriter) {
		w.Header().Set("X-Idempotent-Replay", "true")
		switch receipt.outcome {
		case sendSubmitted:
			writeJSON(w, map[string]any{"queued": receipt.id, "undo_seconds": 0, "status": "submitted"})
		case sendCancelled:
			writeProblem(w, http.StatusConflict, "Send Cancelled",
				"the original send was cancelled before submission; create a new submission if you want to send it")
		default:
			writeProblem(w, http.StatusConflict, "Send Outcome Unknown",
				"the original send may have reached the recipient; check Sent before creating a new submission — retrying this key will not send again")
		}
	}
}

func sendKeyConflict(w http.ResponseWriter) {
	writeProblem(w, http.StatusConflict, "Key Reused",
		"this Idempotency-Key was already used for a different send")
}

// finishSend releases payload ownership into a small receipt. Both worker
// completion and undo call this under q.mu; the first terminal state wins.
func (q *sendQueue) finishSend(id string, outcome sendOutcome) {
	if p := q.sends[id]; p != nil && p.key != "" {
		q.receipts[p.key] = sendReceipt{id: id, reqHash: p.reqHash, outcome: outcome, expiresAt: time.Now().Add(sendReceiptTTL)}
		q.pruneReceipts(time.Now())
	}
	q.forgetSend(id)
}

// Expiration is not refreshed by retries, so a hot key cannot live forever.
// At capacity, evict the oldest outcome. The hard bound includes every
// failed, ambiguous, and cancelled keyed submission as well as successes.
func (q *sendQueue) pruneReceipts(now time.Time) {
	for key, receipt := range q.receipts {
		if !receipt.expiresAt.After(now) {
			delete(q.receipts, key)
		}
	}
	for len(q.receipts) > sendReceiptLimit {
		var oldestKey string
		var oldest time.Time
		for key, receipt := range q.receipts {
			if oldest.IsZero() || receipt.expiresAt.Before(oldest) {
				oldestKey, oldest = key, receipt.expiresAt
			}
		}
		delete(q.receipts, oldestKey)
	}
}

// forgetSend drops a queue entry and its idempotency-key index. Callers
// hold sendq.mu.
func (q *sendQueue) forgetSend(id string) {
	if p, ok := q.sends[id]; ok && p.key != "" && q.keyed[p.key] == id {
		delete(q.keyed, p.key)
	}
	delete(q.sends, id)
}

func (a *App) handleUndoSend(w http.ResponseWriter, r *http.Request) {
	if a.db != nil {
		a.cancelOutbox(w, r)
		return
	}
	id := r.PathValue("id")
	a.sendq.mu.Lock()
	p, ok := a.sendq.sends[id]
	if ok && p.state == sendPending {
		a.sendq.finishSend(id, sendCancelled)
	} else {
		ok = false
	}
	a.sendq.mu.Unlock()
	if !ok {
		writeProblem(w, http.StatusGone, "Too Late", "send already left the building")
		return
	}
	p.cancel()
	writeJSON(w, map[string]any{"cancelled": id})
}
