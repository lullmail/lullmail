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

func (a *App) accountWorkLifecycle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.accountOwnerMu.RLock()
		defer a.accountOwnerMu.RUnlock()
		next.ServeHTTP(w, r)
	})
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
	return storedCredential(mail.Provider(provider), address, password, host, port), nil
}

func storedCredential(provider mail.Provider, address, secret, host string, port int) mail.Credential {
	cred := mail.Credential{Provider: provider, Email: address, Host: host, Port: port}
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
		releaseUse, ok := a.beginAccountUse(acct)
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
	var address, username, host, ciphertext, smtpHost string
	var smtpPort int
	err := a.db.QueryRowContext(ctx,
		`SELECT address, username, host, cred_ciphertext, smtp_host, smtp_port
		 FROM email_accounts WHERE mirror_account_id = $1`, string(acct),
	).Scan(&address, &username, &host, &ciphertext, &smtpHost, &smtpPort)
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
	from := mail.Address{Email: address, Name: username}
	return mail.NewSender(mail.SMTPConfig{
		Host:     smtpHost,
		Port:     smtpPort,
		Username: username,
		Password: password,
	}), from, true
}
