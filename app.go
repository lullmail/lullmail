package main

// App is the running product: config, the product database pool, and the
// neutron-mail engine pieces. Everything the handlers need hangs off it.

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/neutron-build/neutron/mail"
)

type App struct {
	cfg          *Config
	db           *sql.DB
	log          *slog.Logger
	store        *mail.PgStore
	eng          *mail.Engine
	svc          *mail.Service
	sched        *mail.Scheduler
	sendq        *sendQueue
	wa           *webauthn.WebAuthn
	authMu       sync.Mutex
	authAttempts map[string]authAttempt
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
		return mail.Credential{}, err
	}
	return mail.Credential{
		Provider: mail.Provider(provider),
		Email:    address,
		Password: password,
		Host:     host,
		Port:     port,
	}, nil
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
