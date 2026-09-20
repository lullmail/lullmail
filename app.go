package main

// App is the running product: config, the product database pool, and the
// neutron-mail engine pieces. Everything the handlers need hangs off it.

import (
	"context"
	"database/sql"
	"errors"
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
	tasksMu           sync.Mutex
	tasks             *backgroundTasks
	instIDMu          sync.Mutex
	instID            string
	exportMu          sync.Mutex
	export            *exportSlots

	// dial builds adapters from stored credentials. connectApp leaves it
	// nil (the production resolver is constructed per invocation); tests
	// substitute their own to drive sync paths deterministically.
	dial mail.Resolver
}

// accountLifecycle is one account's admission gate (audit OPS-05): an
// active-operation counter sealed by deletion, plus a context derived from
// the background root that deletion cancels so in-flight provider I/O
// aborts instead of running to its own timeout. The owner lock is NOT held
// here — one account's long sync can no longer delay another account's
// deletion.
type accountLifecycle struct {
	mu       sync.Mutex
	deleting bool
	active   int
	// drained is closed exactly once when the last active operation
	// releases after a seal; nil whenever no seal is outstanding.
	drained chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
	parent  *App
}

func (a *App) accountState(acct mail.AccountID) *accountLifecycle {
	a.accountStatesMu.Lock()
	defer a.accountStatesMu.Unlock()
	if a.accountStates == nil {
		a.accountStates = make(map[mail.AccountID]*accountLifecycle)
	}
	state := a.accountStates[acct]
	if state == nil {
		ctx, cancel := context.WithCancel(a.bgRoot())
		state = &accountLifecycle{ctx: ctx, cancel: cancel, parent: a}
		a.accountStates[acct] = state
	}
	return state
}

// beginAccountUse admits one operation under the account's gate, failing
// only while the account is being deleted. The returned release must always
// be called.
func (a *App) beginAccountUse(acct mail.AccountID) (func(), bool) {
	_, release, ok := a.beginAccountWork(acct)
	return release, ok
}

// beginAccountWork is beginAccountUse for operations that drive provider
// I/O: the returned context carries the account's own cancellation, so a
// deletion (or the shutdown drain) aborts the dial, fetch, or submission
// instead of waiting it out. The context is captured under the state
// mutex — an unseal replaces it, and reading it after unlock could hand a
// caller the canceled context of a deleted-and-resurrected account
// (audit 5 LIFE-02).
func (a *App) beginAccountWork(acct mail.AccountID) (context.Context, func(), bool) {
	state := a.accountState(acct)
	state.mu.Lock()
	if state.deleting {
		state.mu.Unlock()
		return nil, nil, false
	}
	state.active++
	accountCtx := state.ctx
	state.mu.Unlock()
	return accountCtx, func() {
		state.mu.Lock()
		state.active--
		if state.deleting && state.active == 0 && state.drained != nil {
			close(state.drained)
			state.drained = nil
		}
		state.mu.Unlock()
	}, true
}

// joinAccountContext derives a context that observes BOTH parents: the
// caller's request/job deadline and values, and the account gate's
// cancellation (audit 5 LIFE-02). Substituting the gate context only when
// it is already canceled — the old pattern — missed every cancellation
// that arrived later, so a deletion could wait out provider I/O it had
// every right to interrupt.
func joinAccountContext(parent, account context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(account, cancel)
	if account.Err() != nil {
		cancel()
	}
	return ctx, func() {
		stop()
		cancel()
	}
}

// beginAccountDeletion seals the account, cancels its context so in-flight
// provider I/O aborts, and waits for the active count to drain — bounded by
// the caller's context plus a hard cap, so a wedged operation fails the
// deletion retryably instead of holding it forever. A failed or uncommitted
// deletion resurrects the account context; the interrupted operations
// report their cancellation honestly.
func (a *App) beginAccountDeletion(ctx context.Context, acct mail.AccountID) (func(bool), bool) {
	state := a.accountState(acct)
	if !state.seal() {
		return nil, false
	}
	waitCtx, cancelWait := context.WithTimeout(ctx, accountDeletionDrainTimeout)
	defer cancelWait()
	select {
	case <-state.currentDrain():
	case <-waitCtx.Done():
		state.unseal()
		return nil, false
	}
	return func(committed bool) {
		if !committed {
			state.unseal()
		}
		// Committed: the account row is gone and the id is never reused;
		// the entry stays sealed forever.
	}, true
}

const accountDeletionDrainTimeout = 2 * time.Minute

// seal marks the gate deleting and cancels the account context. The caller
// owns waiting on currentDrain and either unsealing or leaving the gate
// retired.
func (s *accountLifecycle) seal() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleting {
		return false
	}
	s.deleting = true
	s.drained = make(chan struct{})
	if s.active == 0 {
		close(s.drained)
		s.drained = nil
	}
	s.cancel()
	return true
}

// unseal reverses an uncommitted seal: fresh context, admissions open.
func (s *accountLifecycle) unseal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.deleting {
		return
	}
	s.deleting = false
	s.drained = nil
	ctx, cancel := context.WithCancel(s.parent.bgRoot())
	s.ctx = ctx
	s.cancel = cancel
}

// currentDrain snapshots the seal's completion channel. Nil means the count
// already drained at seal time; the closed-channel read satisfies the wait.
func (s *accountLifecycle) currentDrain() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.drained == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return s.drained
}

// accountGateKey carries the mirror account id whose gate the request
// already holds via accountWorkLifecycle (audit 5 LIFE-01): the value is
// the EXACT leased account — a boolean marker claimed a lease that was
// never acquired whenever the route carried no ?account=, an unresolvable
// one, or one whose admission deletion had sealed, and nested code then
// trusted it for ANY account.
type accountGateKey struct{}

// accountLeaseHeld reports whether ctx already holds the admission gate
// for exactly this account.
func accountLeaseHeld(ctx context.Context, id mail.AccountID) bool {
	held, ok := ctx.Value(accountGateKey{}).(mail.AccountID)
	return ok && held != "" && held == id
}

func (a *App) accountWorkLifecycle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The owner read lock fences full-owner deletion only: it is held
		// for the request, and single-account deletion never takes the
		// write side, so one account's stream can not delay another
		// account's deletion (audit OPS-05).
		a.accountOwnerMu.RLock()
		defer a.accountOwnerMu.RUnlock()
		release, leased, ok := a.holdRequestAccount(r)
		defer release()
		if !ok {
			// The route names an account that is being deleted right now:
			// proceeding WITHOUT a gate (the old silent no-op) is exactly
			// the barrier defeat audit 5 LIFE-01 describes. Fail closed.
			writeProblem(w, http.StatusConflict, "Account Closing",
				"this account is being deleted — retry once it finishes")
			return
		}
		ctx := r.Context()
		if leased != "" {
			ctx = context.WithValue(ctx, accountGateKey{}, leased)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// holdRequestAccount takes the request's account gate when the route
// carries an ?account= parameter that resolves to a connected mailbox, so
// a deletion of that account waits for the stream. Unresolvable or absent
// parameters take no gate — the handler's own ownership lookup answers
// those — and a sealed account fails the request closed instead of
// pretending the gate is held.
func (a *App) holdRequestAccount(r *http.Request) (func(), mail.AccountID, bool) {
	uid, err := a.userID(r.Context())
	if err != nil {
		return func() {}, "", true
	}
	account := r.URL.Query().Get("account")
	if account == "" {
		return func() {}, "", true
	}
	var mirror string
	err = a.db.QueryRowContext(r.Context(),
		`SELECT mirror_account_id FROM email_accounts WHERE user_id=$1 AND (id::text=$2 OR mirror_account_id=$2)`,
		uid, account).Scan(&mirror)
	if err != nil {
		return func() {}, "", true
	}
	release, ok := a.beginAccountUse(mail.AccountID(mirror))
	if !ok {
		return func() {}, "", false
	}
	return release, mail.AccountID(mirror), true
}

// beginAccountUseCtx is beginAccountUse for call chains that may already run
// inside accountWorkLifecycle. The request-level gate excludes deletion
// outright for the SAME account, so a nested use for that account needs no
// second count; a nested use for a DIFFERENT account still takes its own
// gate — the middleware's lease is account-specific, never owner-wide
// (audit 5 LIFE-01).
func (a *App) beginAccountUseCtx(ctx context.Context, acct mail.AccountID) (func(), bool) {
	if accountLeaseHeld(ctx, acct) {
		return func() {}, true
	}
	return a.beginAccountUse(acct)
}

// sealOwnerAccounts seals every account of one owner and waits for their
// in-flight work while holding the owner write lock: account creation and
// gated requests cannot start meanwhile. Callers pass the same mirrors to
// finish when the deletion's outcome is known.
type ownerSeal struct {
	app    *App
	states []*accountLifecycle
}

func (a *App) sealOwnerAccounts(ctx context.Context, uid string) (*ownerSeal, error) {
	a.accountOwnerMu.Lock()
	rows, err := a.db.QueryContext(ctx,
		`SELECT mirror_account_id FROM email_accounts WHERE user_id=$1`, uid)
	if err != nil {
		a.accountOwnerMu.Unlock()
		return nil, err
	}
	var mirrors []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			a.accountOwnerMu.Unlock()
			return nil, err
		}
		mirrors = append(mirrors, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		a.accountOwnerMu.Unlock()
		return nil, err
	}
	rows.Close()

	seal := &ownerSeal{app: a}
	waitCtx, cancelWait := context.WithTimeout(ctx, accountDeletionDrainTimeout)
	defer cancelWait()
	for _, mirror := range mirrors {
		state := a.accountState(mail.AccountID(mirror))
		if !state.seal() {
			continue // already retired by a concurrent deletion
		}
		state.cancel()
		seal.states = append(seal.states, state)
		select {
		case <-state.currentDrain():
		case <-waitCtx.Done():
			seal.finish(false)
			return nil, waitCtx.Err()
		}
	}
	return seal, nil
}

// finish releases the owner lock; an uncommitted deletion resurrects every
// sealed account, a committed one leaves them retired.
func (s *ownerSeal) finish(committed bool) {
	if !committed {
		for _, state := range s.states {
			state.unseal()
		}
	}
	s.app.accountOwnerMu.Unlock()
}

// installationID is the durable identity of THIS deployment, minted once
// into app_settings and never rotated. The offline-v2 namespace pairs it
// with the user id (audit WEB-07/WEB-01/R08) so browser storage is keyed
// by identities the owner cannot accidentally reuse: unlike the email
// address, an installation id + user id pair is not a mutable, shared,
// or re-registrable name.
func (a *App) installationID(ctx context.Context) (string, error) {
	a.instIDMu.Lock()
	cached := a.instID
	a.instIDMu.Unlock()
	if cached != "" {
		return cached, nil
	}
	var id string
	err := a.db.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key='installation_id'`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		id = newID()
		if _, err = a.db.ExecContext(ctx,
			`INSERT INTO app_settings (key, value) VALUES ('installation_id', $1) ON CONFLICT (key) DO NOTHING`, id); err == nil {
			err = a.db.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key='installation_id'`).Scan(&id)
		}
	}
	if err != nil {
		return "", err
	}
	a.instIDMu.Lock()
	a.instID = id
	a.instIDMu.Unlock()
	return id, nil
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
// The base dialer is read per invocation so a substituted test resolver
// takes effect immediately.
func (a *App) accountResolver() mail.Resolver {
	return func(ctx context.Context, acct mail.AccountID, cred mail.Credential) (mail.Adapter, func(), error) {
		base := a.dial
		if base == nil {
			base = newResolver()
		}
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
