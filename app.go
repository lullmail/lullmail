package main

// App is the running product: config, the product database pool, and the
// neutron-mail engine pieces. Everything the handlers need hangs off it.

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/neutron-build/neutron/mail"
)

type App struct {
	cfg               *Config
	db                *sql.DB
	log               *slog.Logger
	store             *mail.PgStore
	eng               *mail.Engine
	svc               *mail.Service
	sched             *mail.Scheduler
	events            *syncEvents
	sendq             *sendQueue
	wa                *webauthn.WebAuthn
	waMu              sync.Mutex
	setupTokenCreated time.Time
	tokenFromEnv      bool
	authMu            sync.Mutex
	authAttempts      map[string]authAttempt
	pwFails           map[string]passwordFails
	accountOwnerMu    sync.RWMutex
	accountStatesMu   sync.Mutex
	accountStates     map[mail.AccountID]*accountLifecycle
}

type accountLifecycle struct {
	mu       sync.RWMutex
	deleting bool
}

func (a *App) accountState(acct mail.AccountID) *accountLifecycle {
	a.accountStatesMu.Lock()
	defer a.accountStatesMu.Unlock()
	if a.accountStates == nil {
		a.accountStates = make(map[mail.AccountID]*accountLifecycle)
	}
	state := a.accountStates[acct]
	if state == nil {
		state = &accountLifecycle{}
		a.accountStates[acct] = state
	}
	return state
}

// beginAccountUse prevents mailbox deletion while an operation can write to
// or stream from the mirror. The returned release must always be called.
func (a *App) beginAccountUse(acct mail.AccountID) (func(), bool) {
	a.accountOwnerMu.RLock()
	state := a.accountState(acct)
	state.mu.RLock()
	if state.deleting {
		state.mu.RUnlock()
		a.accountOwnerMu.RUnlock()
		return nil, false
	}
	return func() {
		state.mu.RUnlock()
		a.accountOwnerMu.RUnlock()
	}, true
}

// beginAccountDeletion waits for in-flight account work and tombstones the
// mirror so stale scheduler/manual requests cannot restart after deletion.
func (a *App) beginAccountDeletion(acct mail.AccountID) (func(bool), bool) {
	a.accountOwnerMu.Lock()
	state := a.accountState(acct)
	state.mu.Lock()
	if state.deleting {
		state.mu.Unlock()
		a.accountOwnerMu.Unlock()
		return nil, false
	}
	state.deleting = true
	return func(committed bool) {
		if !committed {
			state.deleting = false
		}
		state.mu.Unlock()
		a.accountOwnerMu.Unlock()
	}, true
}

// accountGateKey marks a request that already holds the account owner read
// lock via accountWorkLifecycle. Go's RWMutex blocks new read locks while a
// writer is waiting, so a nested beginAccountUse under the gate would
// deadlock the request against a concurrent deletion. Carrying the gate in
// the context lets the inner acquisition see it and become a no-op.
type accountGateKey struct{}

func (a *App) accountWorkLifecycle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.accountOwnerMu.RLock()
		defer a.accountOwnerMu.RUnlock()
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), accountGateKey{}, true)))
	})
}

// beginAccountUseCtx is beginAccountUse for call chains that may already run
// inside accountWorkLifecycle. The request-level gate excludes deletion
// outright, so a nested use needs no second lock pair.
func (a *App) beginAccountUseCtx(ctx context.Context, acct mail.AccountID) (func(), bool) {
	if ctx.Value(accountGateKey{}) != nil {
		return func() {}, true
	}
	return a.beginAccountUse(acct)
}

// beginFullAccountDeletion blocks account creation and waits for every account
// operation before the handler lists mirror rows. A committed deletion retires
// those mirrors before releasing the gate, so stale work cannot restart.
func (a *App) beginFullAccountDeletion() func([]mail.AccountID, bool) {
	a.accountOwnerMu.Lock()
	return func(accounts []mail.AccountID, committed bool) {
		if committed {
			for _, acct := range accounts {
				state := a.accountState(acct)
				state.mu.Lock()
				state.deleting = true
				state.mu.Unlock()
			}
		}
		a.accountOwnerMu.Unlock()
	}
}

func (a *App) accountExportLifecycle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, err := a.userID(r.Context())
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
			return
		}
		var mirror string
		err = a.db.QueryRowContext(r.Context(),
			`SELECT mirror_account_id FROM email_accounts WHERE id=$1 AND user_id=$2`,
			r.PathValue("id"), uid).Scan(&mirror)
		if err == sql.ErrNoRows {
			writeProblem(w, http.StatusNotFound, "Not Found", "no such account")
			return
		}
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
			return
		}
		release, ok := a.beginAccountUse(mail.AccountID(mirror))
		if !ok {
			writeProblem(w, http.StatusConflict, "Delete In Progress", "this account is being deleted")
			return
		}
		defer release()
		next.ServeHTTP(w, r)
	})
}

func (a *App) Token(ctx context.Context, acct mail.AccountID) (mail.Credential, error) {
	var provider, address, username, host, ciphertext string
	var port int
	err := a.db.QueryRowContext(ctx,
		`SELECT provider, address, username, host, port, cred_ciphertext
		 FROM email_accounts WHERE mirror_account_id = $1`, string(acct),
	).Scan(&provider, &address, &username, &host, &port, &ciphertext)
	if err != nil {
		return mail.Credential{}, err
	}
	if provider == "gmail" || provider == "graph" {
		return a.oauthToken(ctx, provider, string(acct), address, ciphertext)
	}
	password, err := openSecret(a.cfg, ciphertext)
	if err != nil {
		return mail.Credential{}, fmt.Errorf("stored credential could not be unsealed (was SECRET_KEY changed? reconnect the account): %w", err)
	}
	return storedCredential(mail.Provider(provider), address, username, password, host, port), nil
}

func storedCredential(provider mail.Provider, address, username, secret, host string, port int) mail.Credential {
	if username == "" {
		username = address
	}
	cred := mail.Credential{Provider: provider, Email: address, Username: username, Host: host, Port: port}
	if provider == mail.ProviderJMAP {
		cred.AccessToken = secret
	} else {
		cred.Password = secret
	}
	return cred
}

// accountResolver gives scheduler and engine HTTP operations stored
// credentials and keeps their adapter lifetime inside the deletion gate.
func (a *App) accountResolver() mail.Resolver {
	base := newResolver()
	return func(ctx context.Context, acct mail.AccountID, cred mail.Credential) (mail.Adapter, func(), error) {
		releaseUse, ok := a.beginAccountUseCtx(ctx, acct)
		if !ok {
			return nil, nil, fmt.Errorf("account %s is being deleted", acct)
		}
		var owned bool
		if err := a.db.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM email_accounts WHERE mirror_account_id=$1)`,
			string(acct)).Scan(&owned); err != nil || !owned {
			releaseUse()
			if err != nil {
				return nil, nil, err
			}
			return nil, nil, fmt.Errorf("account %s is no longer connected", acct)
		}
		if cred.Zero() {
			var err error
			cred, err = a.Token(ctx, acct)
			if err != nil {
				releaseUse()
				return nil, nil, err
			}
		}
		adapter, release, err := base(ctx, acct, cred)
		if err != nil {
			releaseUse()
			return nil, nil, err
		}
		return adapter, func() {
			release()
			releaseUse()
		}, nil
	}
}

// SMTPFor resolves an account to its outbound sender: same host as IMAP,
// port 587 STARTTLS, unless the account overrides it.
func (a *App) SMTPFor(ctx context.Context, acct mail.AccountID) (*mail.Sender, mail.Address, bool) {
	var address, username, host, ciphertext, smtpHost, displayName string
	var smtpPort int
	err := a.db.QueryRowContext(ctx,
		`SELECT ea.address, ea.username, ea.host, ea.cred_ciphertext, ea.smtp_host, ea.smtp_port, u.display_name
		 FROM email_accounts ea JOIN users u ON u.id = ea.user_id
		 WHERE ea.mirror_account_id = $1`, string(acct),
	).Scan(&address, &username, &host, &ciphertext, &smtpHost, &smtpPort, &displayName)
	if err != nil {
		return nil, mail.Address{}, false
	}
	password, err := openSecret(a.cfg, ciphertext)
	if err != nil || password == "" {
		return nil, mail.Address{}, false
	}
	if smtpHost == "" {
		smtpHost = host
	}
	if smtpPort == 0 {
		smtpPort = 587
	}
	// The display name is the owner's, never the login: an IMAP username is
	// a credential, and with Username now distinct from address it can be
	// anything the server handed out.
	from := mail.Address{Email: address, Name: displayName}
	return mail.NewSender(mail.SMTPConfig{
		Host:     smtpHost,
		Port:     smtpPort,
		Username: username,
		Password: password,
	}), from, true
}
