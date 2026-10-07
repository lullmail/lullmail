package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/neutron-build/neutron/mail"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

var oauthRefreshLocks sync.Map

func oauthRefreshLock(account string) *contextLock {
	lock, _ := oauthRefreshLocks.LoadOrStore(account, newContextLock())
	return lock.(*contextLock)
}

func (a *App) mountOAuthCallbacks(mux *http.ServeMux) {
	mux.HandleFunc("GET /oauth/google/callback", func(w http.ResponseWriter, r *http.Request) { a.handleOAuthCallback(w, r, "gmail") })
	mux.HandleFunc("GET /oauth/microsoft/callback", func(w http.ResponseWriter, r *http.Request) { a.handleOAuthCallback(w, r, "graph") })
}

func (a *App) oauthConfig(provider string) (*oauth2.Config, error) {
	switch provider {
	case "gmail":
		if a.cfg.GoogleClientID == "" || a.cfg.GoogleClientSecret == "" {
			return nil, fmt.Errorf("Google OAuth is not configured")
		}
		return &oauth2.Config{ClientID: a.cfg.GoogleClientID, ClientSecret: a.cfg.GoogleClientSecret, RedirectURL: a.cfg.PublicURL + "/api/oauth/google/callback", Endpoint: google.Endpoint, Scopes: []string{"openid", "email", "https://www.googleapis.com/auth/gmail.modify", "https://www.googleapis.com/auth/gmail.send"}}, nil
	case "graph":
		if a.cfg.MicrosoftClientID == "" || a.cfg.MicrosoftClientSecret == "" {
			return nil, fmt.Errorf("Microsoft OAuth is not configured")
		}
		tenant := url.PathEscape(a.cfg.MicrosoftTenant)
		return &oauth2.Config{ClientID: a.cfg.MicrosoftClientID, ClientSecret: a.cfg.MicrosoftClientSecret, RedirectURL: a.cfg.PublicURL + "/api/oauth/microsoft/callback", Endpoint: oauth2.Endpoint{AuthURL: "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0/authorize", TokenURL: "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0/token"}, Scopes: []string{"openid", "email", "offline_access", "User.Read", "Mail.ReadWrite", "Mail.Send"}}, nil
	default:
		return nil, fmt.Errorf("unsupported OAuth provider")
	}
}

func (a *App) handleOAuthStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]bool{"google": a.cfg.GoogleClientID != "" && a.cfg.GoogleClientSecret != "", "microsoft": a.cfg.MicrosoftClientID != "" && a.cfg.MicrosoftClientSecret != ""})
}

func (a *App) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	if provider == "google" {
		provider = "gmail"
	}
	if provider == "microsoft" {
		provider = "graph"
	}
	if provider != "gmail" && provider != "graph" {
		writeProblem(w, http.StatusNotFound, "Unknown Provider", "provider must be google or microsoft")
		return
	}
	config, err := a.oauthConfig(provider)
	if err != nil {
		writeProblem(w, 503, "OAuth Not Configured", err.Error())
		return
	}
	uid, _ := a.userID(r.Context())
	state, err := opaqueToken(32)
	if err != nil {
		writeProblem(w, 500, "OAuth Failed", err.Error())
		return
	}
	verifier := oauth2.GenerateVerifier()
	_, err = a.db.ExecContext(r.Context(), `INSERT INTO oauth_states(state_hash,user_id,provider,verifier,expires_at) VALUES($1,$2,$3,$4,$5)`, tokenHash(state), uid, provider, verifier, time.Now().Add(10*time.Minute))
	if err != nil {
		writeProblem(w, 500, "OAuth Failed", err.Error())
		return
	}
	authURL := config.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("prompt", "consent"))
	writeJSON(w, map[string]string{"url": authURL})
}

func (a *App) handleOAuthCallback(w http.ResponseWriter, r *http.Request, provider string) {
	if oauthErr := r.URL.Query().Get("error"); oauthErr != "" {
		http.Redirect(w, r, "/settings/accounts?oauth_error="+url.QueryEscape(oauthErr), http.StatusSeeOther)
		return
	}
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		writeProblem(w, 400, "OAuth Failed", "provider returned no code or state")
		return
	}
	var uid, storedProvider, verifier string
	err := a.db.QueryRowContext(r.Context(), `DELETE FROM oauth_states WHERE state_hash=$1 AND expires_at>now() RETURNING user_id,provider,verifier`, tokenHash(state)).Scan(&uid, &storedProvider, &verifier)
	if err != nil || storedProvider != provider {
		writeProblem(w, 400, "OAuth Expired", "start the connection again")
		return
	}
	config, err := a.oauthConfig(provider)
	if err != nil {
		writeProblem(w, 503, "OAuth Not Configured", err.Error())
		return
	}
	token, err := config.Exchange(r.Context(), code, oauth2.VerifierOption(verifier))
	if err != nil {
		// A public route: the upstream exchange error is logged, not handed
		// to the browser.
		a.log.Error("oauth exchange failed", "provider", provider, "err", err)
		writeProblem(w, http.StatusBadGateway, "OAuth Exchange Failed", "the provider rejected the sign-in; start the connection again")
		return
	}
	email, label, err := oauthIdentity(r.Context(), provider, config.Client(r.Context(), token))
	if err != nil {
		a.log.Error("oauth identity failed", "provider", provider, "err", err)
		writeProblem(w, http.StatusBadGateway, "Identity Failed", "the provider did not return a usable identity; start the connection again")
		return
	}
	cred := mail.Credential{Provider: mail.Provider(provider), Email: email, AccessToken: token.AccessToken}
	adapter, release, err := newResolver()(r.Context(), "verify", cred)
	if err != nil {
		a.log.Error("oauth connect failed", "provider", provider, "err", err)
		writeProblem(w, http.StatusBadGateway, "Connect Failed", "could not reach the mailbox; start the connection again")
		return
	}
	boxes, err := adapter.Mailboxes(r.Context())
	release()
	if err != nil {
		a.log.Error("oauth mailbox listing failed", "provider", provider, "err", err)
		writeProblem(w, http.StatusBadGateway, "Mailboxes Failed", "the mailbox did not answer; start the connection again")
		return
	}
	raw, _ := json.Marshal(token)
	sealed, err := sealSecret(a.cfg, string(raw))
	if err != nil {
		writeProblem(w, 500, "Encrypt Failed", err.Error())
		return
	}
	// The callback runs on a public route, but account creation must join
	// the same owner lifecycle gate as the authenticated connect flow: a
	// full-owner deletion that enumerated its mirrors while this callback
	// was mid-flight must not come back to a freshly inserted mirror row
	// (audit 3 PROVIDER-03). Duplicate addresses answer the same 409 the
	// connect flow uses.
	var exists bool
	if err := a.db.QueryRowContext(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM email_accounts WHERE user_id=$1 AND lower(address)=lower($2))`,
		uid, email).Scan(&exists); err != nil {
		writeProblem(w, 500, "Connect Failed", err.Error())
		return
	}
	if exists {
		writeProblem(w, http.StatusConflict, "Already Connected", "that address is already connected")
		return
	}
	mirror := newID()
	a.accountOwnerMu.RLock()
	defer a.accountOwnerMu.RUnlock()
	// One transaction for both rows (audit 5 DATA-05): the mirror account
	// and its email_accounts owner commit together — the engine pool's
	// separate commit left an orphan mirror behind when the process died
	// between them, and the deferred cleanup could not run after that.
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, 500, "Connect Failed", err.Error())
		return
	}
	defer tx.Rollback()
	if err := putMirrorAccountTx(r.Context(), tx, mail.AccountID(mirror), mail.Provider(provider), email, label); err != nil {
		writeProblem(w, 500, "Mirror Failed", err.Error())
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO email_accounts(user_id,mirror_account_id,provider,address,label,username,host,port,smtp_host,smtp_port,cred_ciphertext,backfill_days) VALUES($1,$2,$3,$4,$5,$4,'',0,'',0,$6,90)`, uid, mirror, provider, email, label, sealed); err != nil {
		if isUniqueViolation(err) {
			writeProblem(w, http.StatusConflict, "Already Connected", "that address is already connected")
			return
		}
		writeProblem(w, 500, "Connect Failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		writeProblem(w, 500, "Connect Failed", err.Error())
		return
	}
	a.launchAccountSync(mail.AccountID(mirror))
	http.Redirect(w, r, "/settings/accounts?connected="+url.QueryEscape(provider)+"&mailboxes="+fmt.Sprint(len(boxes)), http.StatusSeeOther)
}

// providerJSONLimit bounds every provider JSON response the product
// decodes (audit OPS-01): identity and token payloads are small
// documents, and an unbounded decode lets a hostile or broken provider
// endpoint balloon server memory before validation runs.
const providerJSONLimit = 4 << 20

func oauthIdentity(ctx context.Context, provider string, client *http.Client) (string, string, error) {
	endpoint := "https://openidconnect.googleapis.com/v1/userinfo"
	if provider == "graph" {
		endpoint = "https://graph.microsoft.com/v1.0/me?$select=mail,userPrincipalName,displayName"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	res, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return "", "", fmt.Errorf("identity status %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	var data struct {
		Email             string `json:"email"`
		Mail              string `json:"mail"`
		UserPrincipalName string `json:"userPrincipalName"`
		Name              string `json:"name"`
		DisplayName       string `json:"displayName"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, providerJSONLimit)).Decode(&data); err != nil {
		return "", "", err
	}
	email := data.Email
	if email == "" {
		email = data.Mail
	}
	if email == "" {
		email = data.UserPrincipalName
	}
	name := data.Name
	if name == "" {
		name = data.DisplayName
	}
	if email == "" {
		return "", "", fmt.Errorf("provider returned no email address")
	}
	return email, name, nil
}

func (a *App) oauthToken(ctx context.Context, provider, account, address, sealed string) (mail.Credential, error) {
	return a.oauthTokenAfterRejection(ctx, provider, account, address, sealed, "")
}

// refreshGmailToken is only installed for stored-account read adapters. The
// application keeps refresh tokens and client secrets; the engine receives
// only a replacement short-lived bearer. A replaced credential is reloaded
// under the same refresh lock and compare-and-swap as ordinary token refresh.
func (a *App) refreshGmailToken(ctx context.Context, acct mail.AccountID, rejected mail.Credential) (mail.Credential, error) {
	var provider, address, sealed string
	if err := a.db.QueryRowContext(ctx, `SELECT provider, address, cred_ciphertext FROM email_accounts WHERE mirror_account_id=$1`, string(acct)).Scan(&provider, &address, &sealed); err != nil {
		return mail.Credential{}, err
	}
	if provider != "gmail" || rejected.Provider != mail.ProviderGmail {
		return mail.Credential{}, fmt.Errorf("account provider changed during Gmail read")
	}
	return a.oauthTokenAfterRejection(ctx, provider, string(acct), address, sealed, rejected.AccessToken)
}

func (a *App) oauthTokenAfterRejection(ctx context.Context, provider, account, address, sealed, rejected string) (mail.Credential, error) {
	unlock, err := oauthRefreshLock(account).Lock(ctx)
	if err != nil {
		return mail.Credential{}, err
	}
	defer unlock()

	// Token() reads the account before entering this lock. Reload it so a
	// concurrent refresh cannot continue from the ciphertext it saw earlier.
	if err := a.db.QueryRowContext(ctx, `SELECT cred_ciphertext FROM email_accounts WHERE mirror_account_id=$1`, account).Scan(&sealed); err != nil {
		return mail.Credential{}, err
	}
	plain, err := openSecret(a.cfg, sealed)
	if err != nil {
		return mail.Credential{}, err
	}
	var token oauth2.Token
	if err := json.Unmarshal([]byte(plain), &token); err != nil {
		return mail.Credential{}, err
	}
	config, err := a.oauthConfig(provider)
	if err != nil {
		return mail.Credential{}, err
	}
	if rejected != "" && token.AccessToken == rejected {
		if token.RefreshToken == "" {
			return mail.Credential{}, fmt.Errorf("Gmail access token rejected and no refresh token remains: %w", mail.ErrReauthRequired)
		}
		// A server can reject a token before its recorded expiry. Only invalidate
		// the exact rejected bearer; another request may already have replaced it.
		token.Expiry = time.Unix(1, 0)
	}
	fresh, err := config.TokenSource(ctx, &token).Token()
	if err != nil {
		var retrieve *oauth2.RetrieveError
		if errors.As(err, &retrieve) && retrieve.ErrorCode == "invalid_grant" {
			return mail.Credential{}, fmt.Errorf("OAuth grant rejected: %w", mail.ErrReauthRequired)
		}
		return mail.Credential{}, err
	}
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = token.RefreshToken
	}
	if fresh.AccessToken != token.AccessToken || fresh.RefreshToken != token.RefreshToken || !fresh.Expiry.Equal(token.Expiry) {
		raw, _ := json.Marshal(fresh)
		next, err := sealSecret(a.cfg, string(raw))
		if err != nil {
			return mail.Credential{}, err
		}
		result, err := a.db.ExecContext(ctx, `UPDATE email_accounts SET cred_ciphertext=$1 WHERE mirror_account_id=$2 AND cred_ciphertext=$3`, next, account, sealed)
		if err != nil {
			return mail.Credential{}, err
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return mail.Credential{}, err
		}
		if updated != 1 {
			return mail.Credential{}, fmt.Errorf("OAuth credential changed concurrently for account %s", account)
		}
	}
	return mail.Credential{Provider: mail.Provider(provider), Email: address, AccessToken: fresh.AccessToken}, nil
}

// Gmail's published message-size budget for the media upload route, from
// the API discovery document's users.messages.send.mediaUpload.maxSize
// (36,700,160 bytes at revision 20261005). The send path submits the same
// message bytes through the JSON raw field, so the rendered MIME — not
// the request JSON that carries it base64-encoded again — is what must
// fit. No live JSON-route rejection threshold is measured by this
// constant; it is the documented budget the preflight enforces.
const gmailMIMEBudget = 36_700_160

// The admission preflight renders the MIME once and the delivery path
// renders it again; the second render re-mints the Message-ID, boundary
// and Date bytes. Every one of those has a fixed length in the normal
// path (22-char base64 local part, "neutron-" + 22 chars, RFC1123Z), and
// only the random-source-failure fallback Message-ID can grow (a
// nanosecond timestamp). This allowance explicitly covers that variance
// so a composition admitted at the boundary cannot silently exceed the
// budget when it is actually submitted.
const gmailMIMEAllowance = 4 << 10

// renderGmailMIME produces the exact bytes a Gmail submission carries.
// Splitting render/size from submission would let two renders disagree;
// both admission preflight and delivery render through this one helper.
func renderGmailMIME(out *mail.Outgoing) ([]byte, error) {
	return out.RenderWithBcc()
}

// validateGmailComposition is the full-message Gmail preflight (LUL-D11):
// MIME base64 expansion and quoted-printable encoding grow attachments
// and text far past their input sizes, and neither the request-JSON cap
// nor the raw attachment caps bound that output. Rendering the complete
// composition with the production renderer proves the message fits the
// documented upload budget before it is durably accepted. The existing
// 34 MiB input and 15/25 MiB attachment guards stay independent.
func validateGmailComposition(out *mail.Outgoing) string {
	raw, err := renderGmailMIME(out)
	if err != nil {
		return err.Error()
	}
	if len(raw)+gmailMIMEAllowance > gmailMIMEBudget {
		return "the encoded message exceeds Gmail's message-size limit; remove some attachments or shorten the body"
	}
	return ""
}

// Microsoft documents a 4 MB limit on Graph write requests and answers
// larger ones with 413. Attachment bytes ride base64 inside JSON, so a
// composition whose decoded size passes every existing cap can still
// serialize past the write limit. The budget below is deliberately
// conservative (bytes, not a binary MiB constant): it is a planning
// bound under the documented cap, not a measured tenant limit.
const graphJSONWriteBudget = 4_000_000

// graphWriteBudgetError names one serialized Graph request that cannot be
// sent as-is. Preflight turns it into a synchronous refusal; delivery
// treats it as a request never made (nothing left this process).
func graphWriteBudgetError(what string, size int) error {
	return fmt.Errorf("%s is %d bytes, over the Microsoft 4 MB request limit", what, size)
}

// graphSendMailPayload builds the one-shot sendMail request body for a
// fresh composition. Preflight and delivery marshal the same structure so
// the budget decision is made on the actual wire bytes, never an
// estimate from raw attachment lengths.
func graphSendMailPayload(out *mail.Outgoing, inline []map[string]any) map[string]any {
	contentType, content := "Text", out.Text
	if out.HTML != "" {
		contentType, content = "HTML", out.HTML
	}
	message := map[string]any{"subject": out.Subject, "body": map[string]string{"contentType": contentType, "content": content}, "toRecipients": graphRecipients(out.To), "ccRecipients": graphRecipients(out.Cc), "bccRecipients": graphRecipients(out.Bcc)}
	if inline != nil {
		message["attachments"] = inline
	}
	return map[string]any{"message": message, "saveToSentItems": true}
}

// graphDraftPatch is the fixed shape every draft-based route PATCHes
// before attachments and sending: body, subject and recipients, with no
// attachment bytes. A composition whose PATCH alone cannot fit has no
// legal split — no attachment routing can repair an oversized body.
func graphDraftPatch(out *mail.Outgoing) map[string]any {
	contentType, content := "Text", out.Text
	if out.HTML != "" {
		contentType, content = "HTML", out.HTML
	}
	return map[string]any{
		"subject":      out.Subject,
		"body":         map[string]string{"contentType": contentType, "content": content},
		"toRecipients": graphRecipients(out.To),
		"ccRecipients": graphRecipients(out.Cc),
		"bccRecipients": graphRecipients(out.Bcc),
	}
}

// graphUploadSessionItem is the createUploadSession metadata body for one
// oversize attachment: name and size only, but a pathological filename is
// still a serialized request that must fit the write budget.
func graphUploadSessionItem(att mail.Attachment) map[string]any {
	return map[string]any{
		"AttachmentItem": map[string]any{
			"attachmentType": "file",
			"name":           att.Filename,
			"size":           len(att.Data),
			"contentType":    att.ContentType,
		},
	}
}

// validateGraphComposition is the complete serialized-request plan for a
// Graph composition (LUL-GRAPH-01), checked before durable acceptance:
//
//   - the fixed-shape draft PATCH (body/subject/recipients) must fit,
//     whatever route the attachments take — an oversized body has no
//     legal split and must be refused synchronously, never parked in the
//     outbox to fail at delivery;
//   - each inline attachment's own JSON object must fit a per-file
//     attachment POST. A file below the 3 MiB upload-session minimum
//     whose inline JSON is over budget is unsplittable and is refused;
//   - each upload-session metadata request must fit;
//   - a composition whose whole sendMail body fits keeps the fast route.
//     A multi-attachment aggregate over the budget still sends: the draft
//     route posts each file separately, and every piece of that route
//     was just proven to fit.
func validateGraphComposition(out *mail.Outgoing) string {
	inline, oversize, err := graphAttachments(out.Attachments)
	if err != nil {
		return err.Error()
	}
	if patch, merr := json.Marshal(graphDraftPatch(out)); merr != nil {
		return merr.Error()
	} else if len(patch) > graphJSONWriteBudget {
		return "message body is too large for Microsoft's request limit; shorten the body"
	}
	for _, file := range inline {
		encoded, merr := json.Marshal(file)
		if merr != nil {
			return merr.Error()
		}
		if len(encoded) > graphJSONWriteBudget {
			return fmt.Sprintf("attachment %q is too large for Microsoft's request format; files must fit inline below 3 MiB or ride an upload session at 3 MiB and above", file["name"])
		}
	}
	for _, att := range oversize {
		if encoded, merr := json.Marshal(graphUploadSessionItem(att)); merr != nil {
			return merr.Error()
		} else if len(encoded) > graphJSONWriteBudget {
			return fmt.Sprintf("attachment %q metadata exceeds Microsoft's request limit", att.Filename)
		}
	}
	if len(inline) == 0 && len(oversize) == 0 {
		if body, merr := json.Marshal(graphSendMailPayload(out, nil)); merr != nil {
			return merr.Error()
		} else if len(body) > graphJSONWriteBudget {
			return "message body is too large for Microsoft's request limit; shorten the body"
		}
	}
	return ""
}

// Errors wrapped in mail.NotSubmittedError are known to precede provider
// acceptance (no credential, local composition error, or an explicit 4xx
// refusal), so the durable outbox records them as failed rather than
// ambiguous. A 5xx or a transport error may follow acceptance and stays
// unwrapped.
func (a *App) sendOAuth(ctx context.Context, provider, account string, out *mail.Outgoing, replyParent, replyThreadID string) error {
	cred, err := a.Token(ctx, mail.AccountID(account))
	if err != nil {
		return &mail.NotSubmittedError{Err: err}
	}
	if provider == "gmail" {
		// Raw MIME via the engine renderer: identical multipart handling
		// to SMTP, including the HTML part when one is set. The raw
		// upload has no envelope, so Bcc must ride in the headers to be
		// delivered at all; Gmail strips the header for recipients but
		// keeps it on the saved Sent copy.
		raw, err := renderGmailMIME(out)
		if err != nil {
			return &mail.NotSubmittedError{Err: err}
		}
		// Preflight already bounded this at admission; the delivery-time
		// recheck holds the line on the exact bytes being submitted
		// (re-minted Message-ID/boundary/Date included) and refuses
		// before anything reaches the provider.
		if len(raw) > gmailMIMEBudget {
			return &mail.NotSubmittedError{Err: fmt.Errorf("encoded Gmail message is %d bytes, over the 35 MiB message-size limit", len(raw))}
		}
		// A reply carries the parent's native thread id beside the raw
		// MIME: Google's threading contract requires the message to name
		// the thread it belongs to, which the raw bytes alone do not do.
		// The id is captured at admission from the ownership-checked
		// parent envelope (LUL-D12); a legacy pending payload that
		// predates the field recovers it from the mirror while the
		// parent is still retained, and sends without it otherwise —
		// the RFC References/In-Reply-To headers still ride in the MIME.
		// A sufficiently reworded subject is Google's call per its
		// documented threading conditions; sending the id makes no claim
		// of forcing such a reply into the old thread.
		threadID := replyThreadID
		if threadID == "" && replyParent != "" {
			if parent, perr := a.store.Envelope(ctx, mail.AccountID(account), mail.MessageID(replyParent)); perr == nil {
				threadID = string(parent.ThreadID)
			}
		}
		bodyMap := map[string]string{"raw": base64.RawURLEncoding.EncodeToString(raw)}
		if threadID != "" {
			bodyMap["threadId"] = threadID
		}
		body, _ := json.Marshal(bodyMap)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://gmail.googleapis.com/gmail/v1/users/me/messages/send", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
		res, err := providerHTTP.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			data, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
			return oauthStatusError(res.StatusCode, fmt.Errorf("gmail send status %d: %s", res.StatusCode, strings.TrimSpace(string(data))))
		}
		return nil
	}
	// Graph documents that internetMessageHeaders on sendMail accepts only
	// x- prefixed custom headers, so a reply cannot carry In-Reply-To or
	// References that way (and renaming them to x- would strip their
	// standard meaning). Replies go through createReply, which builds the
	// threading headers server-side from the parent message.
	if out.InReplyTo != "" && replyParent != "" {
		return a.graphReplySend(ctx, cred, out, replyParent)
	}
	inline, oversize, err := graphAttachments(out.Attachments)
	if err != nil {
		return &mail.NotSubmittedError{Err: err}
	}
	if len(oversize) > 0 {
		return a.graphDraftSend(ctx, cred, out, inline, oversize)
	}
	payload := graphSendMailPayload(out, inline)
	body, merr := json.Marshal(payload)
	if merr != nil {
		return &mail.NotSubmittedError{Err: merr}
	}
	// The aggregate JSON can exceed the write budget even when every file
	// is individually small — attachment bytes grow under base64. A
	// multi-file composition over the budget still sends: the draft route
	// posts each attachment separately and each piece was proven to fit
	// at admission. With no attachments there is no split, so an
	// oversized body is refused here without contacting the provider.
	if len(body) > graphJSONWriteBudget {
		if len(inline) > 0 {
			return a.graphDraftSend(ctx, cred, out, inline, oversize)
		}
		return &mail.NotSubmittedError{Err: graphWriteBudgetError("message body", len(body))}
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, graphAPIBase+"/me/sendMail", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	res, err := providerHTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return oauthStatusError(res.StatusCode, fmt.Errorf("graph send status %d: %s", res.StatusCode, strings.TrimSpace(string(data))))
	}
	return nil
}

// oauthStatusError marks an explicit client-error refusal as not submitted.
// Request-timeout and rate-limit answers are also refusals before any send.
func oauthStatusError(status int, err error) error {
	if status >= 400 && status < 500 {
		return &mail.NotSubmittedError{Err: err}
	}
	return err
}

// graphHTTPError is an answer the service explicitly returned, as opposed
// to a transport failure, where the request's server-side fate is unknown.
type graphHTTPError struct {
	Status int
	Err    error
}

func (e *graphHTTPError) Error() string { return e.Err.Error() }
func (e *graphHTTPError) Unwrap() error { return e.Err }

// graphDefinitelyRejected reports whether err is an explicit 4xx refusal of
// one operation, which proves that operation never took effect. Transport
// failures and 5xx answers may follow committed server-side work, so they
// prove nothing either way.
func graphDefinitelyRejected(err error) bool {
	var ge *graphHTTPError
	return errors.As(err, &ge) && ge.Status >= 400 && ge.Status < 500
}

// graphPathSegment escapes a provider-supplied identifier into exactly one
// Graph URL path segment: an opaque id carrying reserved path, query or
// fragment characters must address that one resource, not rewrite the URL's
// structure. url.PathEscape is the path-segment escaper; QueryEscape would
// also encode it, but as form data.
func graphPathSegment(id string) string {
	return url.PathEscape(id)
}

// graphCleanupTimeout bounds the best-effort draft cleanup DELETE.
const graphCleanupTimeout = 5 * time.Second

// graphCleanupContext derives the cleanup DELETE's context from the account
// lifetime the submission ran under (accountLifetimeKey), not from the
// failed attempt's own deadline: a Graph send whose 60-second submission
// context expired mid-draft must still attempt to delete its partial draft,
// while an account being deleted or drained still aborts cleanup. A caller
// with no account lease detaches only the attempt's cancellation.
func graphCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	parent := context.WithoutCancel(ctx)
	if lifetime, ok := ctx.Value(accountLifetimeKey{}).(context.Context); ok && lifetime != nil {
		parent = lifetime
	}
	return context.WithTimeout(parent, graphCleanupTimeout)
}

// providerHTTP is the one client for the fixed-origin provider APIs (Gmail,
// Graph). Centralizing it makes transport hygiene explicit — pooling,
// handshake and header timeouts as an outer ceiling — where http.DefaultClient
// carried none of it; every request still supplies its own context deadline,
// which remains the primary timeout. The variable mirrors graphAPIBase so
// tests can point the transport at a fake service. Redirect and egress
// policy for configurable hosts is deliberately not encoded here (open item
// OPS-09: user-selected private mail servers are a feature, so a blanket
// ban would be wrong).
var providerHTTP = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	MaxIdleConns:          100,
	MaxIdleConnsPerHost:   10,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: 30 * time.Second,
}}

// graphAPIBase is a variable only so tests can point the Graph client at a
// fake service; production always talks to the real endpoint.
var graphAPIBase = "https://graph.microsoft.com/v1.0"

// graphReplySend threads a reply the documented way: createReply drafts a
// message with In-Reply-To/References set by the service, the draft is
// shaped with the composer's body and recipients, then sent. A failure
// after drafting removes the leftover draft rather than littering Drafts;
// only deterministic refusals are terminal (NotSubmittedError) — a draft
// mutation whose transport outcome is unknown stays ambiguous.
func (a *App) graphReplySend(ctx context.Context, cred mail.Credential, out *mail.Outgoing, replyParent string) error {
	native := mail.NativeID(mail.MessageID(replyParent))
	if native == "" {
		return &mail.NotSubmittedError{Err: fmt.Errorf("graph reply: parent %q has no native provider id", replyParent)}
	}
	var draft struct {
		ID string `json:"id"`
	}
	if err := graphCall(ctx, cred, http.MethodPost, "/me/messages/"+graphPathSegment(native)+"/createReply", map[string]any{}, &draft); err != nil {
		if graphDefinitelyRejected(err) {
			return &mail.NotSubmittedError{Err: err}
		}
		return err
	}
	if draft.ID == "" {
		return &mail.NotSubmittedError{Err: errors.New("graph reply: createReply returned no draft id")}
	}
	draftPath := "/me/messages/" + graphPathSegment(draft.ID)
	cleanup := func(primary error) error {
		cleanupCtx, cancel := graphCleanupContext(ctx)
		defer cancel()
		if derr := graphCall(cleanupCtx, cred, http.MethodDelete, draftPath, nil, nil); derr != nil {
			return errors.Join(primary, derr)
		}
		return primary
	}
	contentType, content := "Text", out.Text
	if out.HTML != "" {
		contentType, content = "HTML", out.HTML
	}
	patch := map[string]any{
		"subject":       out.Subject,
		"body":          map[string]string{"contentType": contentType, "content": content},
		"toRecipients":  graphRecipients(out.To),
		"ccRecipients":  graphRecipients(out.Cc),
		"bccRecipients": graphRecipients(out.Bcc),
	}
	if encoded, merr := json.Marshal(patch); merr != nil {
		return cleanup(&mail.NotSubmittedError{Err: merr})
	} else if len(encoded) > graphJSONWriteBudget {
		// Same write budget as every Graph request: a reply body that
		// cannot fit one PATCH has no legal split and is refused before
		// anything reaches the provider.
		return cleanup(&mail.NotSubmittedError{Err: graphWriteBudgetError("reply body", len(encoded))})
	}
	if err := graphCall(ctx, cred, http.MethodPatch, draftPath, patch, nil); err != nil {
		if graphDefinitelyRejected(err) {
			return cleanup(&mail.NotSubmittedError{Err: err})
		}
		return cleanup(err)
	}
	inline, oversize, err := graphAttachments(out.Attachments)
	if err != nil {
		return cleanup(&mail.NotSubmittedError{Err: err})
	}
	for _, file := range inline {
		if encoded, merr := json.Marshal(file); merr != nil {
			return cleanup(&mail.NotSubmittedError{Err: merr})
		} else if len(encoded) > graphJSONWriteBudget {
			return cleanup(&mail.NotSubmittedError{Err: graphWriteBudgetError(fmt.Sprintf("reply attachment %q", file["name"]), len(encoded))})
		}
		if err := graphCall(ctx, cred, http.MethodPost, draftPath+"/attachments", file, nil); err != nil {
			if graphDefinitelyRejected(err) {
				return cleanup(&mail.NotSubmittedError{Err: err})
			}
			return cleanup(err)
		}
	}
	for _, att := range oversize {
		if err := graphUploadAttachment(ctx, cred, draftPath, att); err != nil {
			if graphDefinitelyRejected(err) {
				return cleanup(&mail.NotSubmittedError{Err: err})
			}
			return cleanup(err)
		}
	}
	if err := graphCall(ctx, cred, http.MethodPost, draftPath+"/send", nil, nil); err != nil {
		if graphDefinitelyRejected(err) {
			return cleanup(&mail.NotSubmittedError{Err: err})
		}
		return cleanup(err)
	}
	return nil
}

// graphRecipients serializes engine addresses into Graph recipient values.
func graphRecipients(items []mail.Address) []map[string]any {
	rows := make([]map[string]any, 0, len(items))
	for _, item := range items {
		rows = append(rows, map[string]any{"emailAddress": map[string]string{"address": item.Email, "name": item.Name}})
	}
	return rows
}

// graphCall performs one authorized JSON call against Graph and reports
// non-2xx answers with the service's own error body.
func graphCall(ctx context.Context, cred mail.Credential, method, endpoint string, body map[string]any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, _ := json.Marshal(body)
		reader = bytes.NewReader(encoded)
	}
	req, _ := http.NewRequestWithContext(ctx, method, graphAPIBase+endpoint, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	res, err := providerHTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return &graphHTTPError{Status: res.StatusCode, Err: fmt.Errorf("graph %s %s status %d: %s", method, endpoint, res.StatusCode, strings.TrimSpace(string(data)))}
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(res.Body, providerJSONLimit)).Decode(out)
	}
	return nil
}

// Graph's regular attachments cap at 3 MB per file; beyond that the whole
// request is rejected with a 413. Files at or above that cap (the
// product's send admission allows up to 15 MiB) ride upload sessions on a
// draft message instead of being refused, so they come back separately
// here; the 25 MB total cap spans both kinds. The boundary is inclusive:
// a file of exactly 3 MiB is at the provider's documented upload-session
// minimum, and its base64 inline JSON object would not fit under the
// 4 MB write budget — at and above the cap is the session route.
const (
	graphAttachmentMax    = 3 << 20
	graphAttachmentMaxSum = 25 << 20
	// Upload-session chunks must be a multiple of 320 KiB except the last.
	graphUploadChunk = 5 * 320 << 10
)

// graphAttachments serializes engine attachments into Graph fileAttachment
// entries, mirroring what the SMTP renderer carries. Entries at or above
// the inline cap are returned in the second value for upload sessions.
func graphAttachments(atts []mail.Attachment) (inline []map[string]any, sessions []mail.Attachment, err error) {
	if len(atts) == 0 {
		return nil, nil, nil
	}
	inline = make([]map[string]any, 0, len(atts))
	var total int
	for _, att := range atts {
		total += len(att.Data)
		if total > graphAttachmentMaxSum {
			return nil, nil, fmt.Errorf("attachments exceed the 25 MB total limit for Microsoft accounts")
		}
		contentType := att.ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		if len(att.Data) >= graphAttachmentMax {
			sessions = append(sessions, mail.Attachment{Filename: att.Filename, ContentType: contentType, Data: att.Data})
			continue
		}
		inline = append(inline, map[string]any{
			"@odata.type":  "#microsoft.graph.fileAttachment",
			"name":         att.Filename,
			"contentType":  contentType,
			"contentBytes": base64.StdEncoding.EncodeToString(att.Data),
		})
	}
	return inline, sessions, nil
}

// graphUploadHost is the one origin Microsoft documents for Outlook
// attachment-session upload URLs. Additional clouds, if ever supported,
// belong here as an explicit allowlist — never an open one.
const graphUploadHost = "outlook.office.com"

// validateGraphUploadURL enforces the documented origin of the provider's
// pre-authenticated upload URL: its query token IS the credential, so a
// compromised or unexpected continuation must be refused before any bytes
// leave. This is a fixed Microsoft contract, distinct from OPS-09's
// user-configured provider hosts.
func validateGraphUploadURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid graph upload url: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("graph upload url must use https: %q", raw)
	}
	if !strings.EqualFold(u.Hostname(), graphUploadHost) {
		return fmt.Errorf("graph upload url host %q is not %s", u.Hostname(), graphUploadHost)
	}
	if port := u.Port(); port != "" && port != "443" {
		return fmt.Errorf("graph upload url uses unexpected port %q", port)
	}
	if u.User != nil {
		return errors.New("graph upload url must not carry userinfo")
	}
	if u.Fragment != "" {
		return errors.New("graph upload url must not carry a fragment")
	}
	return nil
}

// graphUploadClient is the shared provider transport with upload-session
// policy: a redirect on the pre-authenticated upload URL would carry both
// the attachment bytes and the URL's token to an unvetted destination, so
// redirects are never followed. The per-call copy reads providerHTTP's
// transport live, keeping the tests that re-point it working.
func graphUploadClient() *http.Client {
	client := *providerHTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

// uploadRangeStart reads the leading offset of a Graph "start-" range entry.
func uploadRangeStart(entry string) (int, bool) {
	dash := strings.IndexByte(entry, '-')
	if dash <= 0 {
		return 0, false
	}
	n, err := strconv.Atoi(entry[:dash])
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// graphUploadAttachment streams one oversize attachment into a draft
// through a Graph upload session. The uploadUrl is pre-authenticated, so
// the bearer token must not ride along on the chunk PUTs, the URL's origin
// is validated before the first PUT, and redirects are never followed.
func graphUploadAttachment(ctx context.Context, cred mail.Credential, draftPath string, att mail.Attachment) error {
	item := graphUploadSessionItem(att)
	if encoded, err := json.Marshal(item); err != nil {
		return err
	} else if len(encoded) > graphJSONWriteBudget {
		// Preflight already refused metadata this large at admission;
		// this holds the same line on the actual bytes at delivery.
		return &mail.NotSubmittedError{Err: graphWriteBudgetError(fmt.Sprintf("upload-session metadata for %q", att.Filename), len(encoded))}
	}
	var session struct {
		UploadURL string `json:"uploadUrl"`
	}
	if err := graphCall(ctx, cred, http.MethodPost, draftPath+"/attachments/createUploadSession", item, &session); err != nil {
		return err
	}
	if session.UploadURL == "" {
		// A fully-received answer: nothing was uploaded, so the refusal is
		// deterministic.
		return &mail.NotSubmittedError{Err: fmt.Errorf("graph upload session for %q returned no upload url", att.Filename)}
	}
	if err := validateGraphUploadURL(session.UploadURL); err != nil {
		return &mail.NotSubmittedError{Err: err}
	}
	client := graphUploadClient()
	for offset := 0; offset < len(att.Data); {
		end := min(offset+graphUploadChunk, len(att.Data))
		final := end == len(att.Data)
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, session.UploadURL, bytes.NewReader(att.Data[offset:end]))
		if err != nil {
			return err
		}
		req.ContentLength = int64(end - offset)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, end-1, len(att.Data)))
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			data, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
			res.Body.Close()
			return &graphHTTPError{Status: res.StatusCode, Err: fmt.Errorf("graph upload %q status %d: %s", att.Filename, res.StatusCode, strings.TrimSpace(string(data)))}
		}
		body, readErr := io.ReadAll(io.LimitReader(res.Body, 4096))
		closeErr := res.Body.Close()
		if readErr != nil {
			return fmt.Errorf("graph upload %q response read failed: %w", att.Filename, readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("graph upload %q response close failed: %w", att.Filename, closeErr)
		}
		trimmed := strings.TrimSpace(string(body))
		if trimmed == "" {
			// Only the final chunk may answer without a continuation:
			// Graph returns the created attachment there instead. An
			// intermediate 2xx with no acknowledgement is not evidence
			// the bytes through end-1 were accepted.
			if !final {
				return fmt.Errorf("graph upload %q returned no continuation acknowledgement after byte %d", att.Filename, end)
			}
			offset = end
			continue
		}
		var next struct {
			NextExpectedRanges []string `json:"nextExpectedRanges"`
		}
		if err := json.Unmarshal([]byte(trimmed), &next); err != nil {
			return fmt.Errorf("graph upload %q response undecodable: %w", att.Filename, err)
		}
		if final {
			// The final answer may be attachment metadata and need not
			// include ranges, but one that still expects bytes the client
			// never sends means the stored session state diverged.
			if len(next.NextExpectedRanges) > 0 {
				if start, ok := uploadRangeStart(next.NextExpectedRanges[0]); !ok || start != end {
					return fmt.Errorf("graph upload %q final response unexpectedly expects %q after byte %d", att.Filename, next.NextExpectedRanges[0], end)
				}
			}
		} else {
			if len(next.NextExpectedRanges) == 0 {
				return fmt.Errorf("graph upload %q returned no nextExpectedRanges after byte %d", att.Filename, end)
			}
			if len(next.NextExpectedRanges) != 1 {
				return fmt.Errorf("graph upload %q returned unsupported continuation ranges %q", att.Filename, next.NextExpectedRanges)
			}
			start, ok := uploadRangeStart(next.NextExpectedRanges[0])
			if !ok || start != end {
				return fmt.Errorf("graph upload %q session expects %q after byte %d; stored state diverged", att.Filename, next.NextExpectedRanges[0], end)
			}
		}
		offset = end
	}
	return nil
}

// graphDraftSend carries a fresh (non-reply) send whose attachments include
// files above the inline cap: a draft is created and shaped, oversize files
// ride upload sessions and small ones the regular attachments endpoint, and
// the draft is sent. Deterministic refusals before the final send are
// terminal (NotSubmittedError) after deleting the leftover draft; a draft
// mutation whose transport outcome is unknown stays ambiguous, because the
// remote draft may hold committed work and the cleanup DELETE may itself
// fail. The send itself keeps sendMail's ambiguity semantics.
func (a *App) graphDraftSend(ctx context.Context, cred mail.Credential, out *mail.Outgoing, inline []map[string]any, oversize []mail.Attachment) error {
	var draft struct {
		ID string `json:"id"`
	}
	if err := graphCall(ctx, cred, http.MethodPost, "/me/messages", map[string]any{}, &draft); err != nil {
		if graphDefinitelyRejected(err) {
			return &mail.NotSubmittedError{Err: err}
		}
		return err
	}
	if draft.ID == "" {
		return &mail.NotSubmittedError{Err: errors.New("graph send: draft creation returned no id")}
	}
	draftPath := "/me/messages/" + graphPathSegment(draft.ID)
	cleanup := func(primary error) error {
		cleanupCtx, cancel := graphCleanupContext(ctx)
		defer cancel()
		if derr := graphCall(cleanupCtx, cred, http.MethodDelete, draftPath, nil, nil); derr != nil {
			return errors.Join(primary, derr)
		}
		return primary
	}
	contentType, content := "Text", out.Text
	if out.HTML != "" {
		contentType, content = "HTML", out.HTML
	}
	patch := map[string]any{
		"subject":       out.Subject,
		"body":          map[string]string{"contentType": contentType, "content": content},
		"toRecipients":  graphRecipients(out.To),
		"ccRecipients":  graphRecipients(out.Cc),
		"bccRecipients": graphRecipients(out.Bcc),
	}
	if encoded, merr := json.Marshal(patch); merr != nil {
		return cleanup(&mail.NotSubmittedError{Err: merr})
	} else if len(encoded) > graphJSONWriteBudget {
		// An impossible body must not be attempted even on the draft
		// route: the request is refused before it is made, and the
		// leftover draft is removed.
		return cleanup(&mail.NotSubmittedError{Err: graphWriteBudgetError("message body", len(encoded))})
	}
	if err := graphCall(ctx, cred, http.MethodPatch, draftPath, patch, nil); err != nil {
		if graphDefinitelyRejected(err) {
			return cleanup(&mail.NotSubmittedError{Err: err})
		}
		return cleanup(err)
	}
	for _, file := range inline {
		if encoded, merr := json.Marshal(file); merr != nil {
			return cleanup(&mail.NotSubmittedError{Err: merr})
		} else if len(encoded) > graphJSONWriteBudget {
			return cleanup(&mail.NotSubmittedError{Err: graphWriteBudgetError(fmt.Sprintf("attachment %q", file["name"]), len(encoded))})
		}
		if err := graphCall(ctx, cred, http.MethodPost, draftPath+"/attachments", file, nil); err != nil {
			if graphDefinitelyRejected(err) {
				return cleanup(&mail.NotSubmittedError{Err: err})
			}
			return cleanup(err)
		}
	}
	for _, att := range oversize {
		if err := graphUploadAttachment(ctx, cred, draftPath, att); err != nil {
			if graphDefinitelyRejected(err) {
				return cleanup(&mail.NotSubmittedError{Err: err})
			}
			return cleanup(err)
		}
	}
	if err := graphCall(ctx, cred, http.MethodPost, draftPath+"/send", nil, nil); err != nil {
		if graphDefinitelyRejected(err) {
			return cleanup(&mail.NotSubmittedError{Err: err})
		}
		return cleanup(err)
	}
	return nil
}
