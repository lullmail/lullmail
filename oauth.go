package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/neutron-build/neutron/mail"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

var oauthRefreshLocks sync.Map

func oauthRefreshLock(account string) *sync.Mutex {
	lock, _ := oauthRefreshLocks.LoadOrStore(account, &sync.Mutex{})
	return lock.(*sync.Mutex)
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
		writeProblem(w, 502, "OAuth Exchange Failed", err.Error())
		return
	}
	email, label, err := oauthIdentity(r.Context(), provider, config.Client(r.Context(), token))
	if err != nil {
		writeProblem(w, 502, "Identity Failed", err.Error())
		return
	}
	cred := mail.Credential{Provider: mail.Provider(provider), Email: email, AccessToken: token.AccessToken}
	adapter, release, err := newResolver()(r.Context(), "verify", cred)
	if err != nil {
		writeProblem(w, 502, "Connect Failed", err.Error())
		return
	}
	boxes, err := adapter.Mailboxes(r.Context())
	release()
	if err != nil {
		writeProblem(w, 502, "Mailboxes Failed", err.Error())
		return
	}
	raw, _ := json.Marshal(token)
	sealed, err := sealSecret(a.cfg, string(raw))
	if err != nil {
		writeProblem(w, 500, "Encrypt Failed", err.Error())
		return
	}
	mirror := newID()
	if err := a.store.PutAccount(r.Context(), &mail.Account{ID: mail.AccountID(mirror), Provider: mail.Provider(provider), Email: email, Name: label}); err != nil {
		writeProblem(w, 500, "Mirror Failed", err.Error())
		return
	}
	_, err = a.db.ExecContext(r.Context(), `INSERT INTO email_accounts(user_id,mirror_account_id,provider,address,label,username,host,port,smtp_host,smtp_port,cred_ciphertext,backfill_days) VALUES($1,$2,$3,$4,$5,$4,'',0,'',0,$6,90)`, uid, mirror, provider, email, label, sealed)
	if err != nil {
		_, _ = a.db.ExecContext(r.Context(), `DELETE FROM mail_accounts WHERE id=$1`, mirror)
		writeProblem(w, 500, "Connect Failed", err.Error())
		return
	}
	go func() {
		ctx := context.Background()
		_ = a.syncAccount(ctx, mail.AccountID(mirror))
	}()
	http.Redirect(w, r, "/settings/accounts?connected="+url.QueryEscape(provider)+"&mailboxes="+fmt.Sprint(len(boxes)), http.StatusSeeOther)
}

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
	if err := json.NewDecoder(res.Body).Decode(&data); err != nil {
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
	lock := oauthRefreshLock(account)
	lock.Lock()
	defer lock.Unlock()

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
	fresh, err := config.TokenSource(ctx, &token).Token()
	if err != nil {
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

func safeHeader(value string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(value) }

func (a *App) sendOAuth(ctx context.Context, provider, account string, out *mail.Outgoing) error {
	cred, err := a.Token(ctx, mail.AccountID(account))
	if err != nil {
		return err
	}
	if provider == "gmail" {
		// Raw MIME via the engine renderer: identical multipart handling
		// to SMTP, including the HTML part when one is set.
		raw, err := out.Render()
		if err != nil {
			return err
		}
		body, _ := json.Marshal(map[string]string{"raw": base64.RawURLEncoding.EncodeToString(raw)})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://gmail.googleapis.com/gmail/v1/users/me/messages/send", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			data, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
			return fmt.Errorf("gmail send status %d: %s", res.StatusCode, strings.TrimSpace(string(data)))
		}
		return nil
	}
	recipients := func(items []mail.Address) []map[string]any {
		rows := make([]map[string]any, 0, len(items))
		for _, item := range items {
			rows = append(rows, map[string]any{"emailAddress": map[string]string{"address": item.Email, "name": item.Name}})
		}
		return rows
	}
	headers := []map[string]string{}
	if out.InReplyTo != "" {
		headers = append(headers, map[string]string{"name": "In-Reply-To", "value": out.InReplyTo})
	}
	if len(out.References) > 0 {
		headers = append(headers, map[string]string{"name": "References", "value": strings.Join(out.References, " ")})
	}
	// Graph has no multipart submission: HTML messages send as HTML and
	// plain messages as plain, with the alternative part already carried
	// inside the SMTP-rendered copy for IMAP providers.
	contentType, content := "Text", out.Text
	if out.HTML != "" {
		contentType, content = "HTML", out.HTML
	}
	payload := map[string]any{"message": map[string]any{"subject": out.Subject, "body": map[string]string{"contentType": contentType, "content": content}, "toRecipients": recipients(out.To), "ccRecipients": recipients(out.Cc), "bccRecipients": recipients(out.Bcc), "internetMessageHeaders": headers}, "saveToSentItems": true}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://graph.microsoft.com/v1.0/me/sendMail", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return fmt.Errorf("graph send status %d: %s", res.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}
