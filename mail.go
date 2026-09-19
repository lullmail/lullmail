package main

// App bootstrap: connect the product database and the neutron-mail engine,
// or run with the API gracefully degraded when the database is absent.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/neutron-build/neutron/mail"
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// connectApp opens both pools (product database/sql + engine pgx) and
// runs both versioned migrations, so a fresh deploy converges from zero
// and an existing one converges through the ledger (audit OPS-02). The
// mail_* mirror tables are neutron-mail's; our migrations never create
// or alter them. Engine first: the product's account-scope migration
// reads mail_messages.
func connectApp(cfg *Config) *App {
	if cfg.DatabaseURL == "" {
		log.Println("app: DATABASE_URL not set — API disabled")
		return nil
	}
	if err := resolveSecretKey(cfg); err != nil {
		log.Printf("app: SECRET_KEY setup failed — API disabled: %v", err)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		log.Printf("app: open failed — API disabled: %v", err)
		return nil
	}
	// The product pool needs a budget: unbounded, a burst of concurrent
	// requests could consume the deployment's entire connection allowance
	// (the engine keeps a separate pool). Single-operator scale leaves
	// generous headroom for migrations and admin sessions (audit 3
	// OPS-02).
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)
	for {
		if err := db.PingContext(ctx); err == nil {
			break
		} else if ctx.Err() != nil {
			db.Close()
			log.Printf("app: ping failed — API disabled: %v", err)
			return nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
	}

	store, err := mail.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		db.Close()
		log.Printf("app: mail open failed — API disabled: %v", err)
		return nil
	}
	if err := store.Migrate(ctx); err != nil {
		store.Close()
		db.Close()
		log.Printf("app: mail migration failed — API disabled: %v", err)
		return nil
	}
	if err := runProductMigrations(ctx, db); err != nil {
		store.Close()
		db.Close()
		log.Printf("app: product migration failed — API disabled: %v", err)
		return nil
	}

	app := &App{
		cfg:           cfg,
		db:            db,
		log:           slog.Default(),
		store:         store,
		eng:           newSyncEngine(store, cfg),
		events:        newSyncEvents(),
		sendq:         newSendQueue(),
		authAttempts:  map[string]authAttempt{},
		pwFails:       map[string]passwordFails{},
		accountStates: map[mail.AccountID]*accountLifecycle{},
		tokenFromEnv:  cfg.APIToken != "",
	}
	// Restore a setup-pinned origin and surface the first-run token before
	// WebAuthn is constructed: both decide whether this boot runs configured
	// or in first-run setup mode.
	app.prepareSetup()
	if app.cfg.RPID != "" {
		app.wa, err = newWebAuthn(cfg)
		if err != nil {
			store.Close()
			db.Close()
			log.Printf("app: webauthn setup failed — API disabled: %v", err)
			return nil
		}
	} else {
		log.Println("app: no origin pinned — first-run setup will detect it from the browser")
	}
	app.svc = mail.NewService(store, app.eng)
	app.svc.Resolve = app.accountResolver()
	app.svc.Senders = func(acct mail.AccountID) (*mail.Sender, mail.Address, bool) {
		return app.SMTPFor(context.Background(), acct)
	}

	app.sched = mail.NewScheduler(store, app.eng, nil, slog.Default())
	app.sched.Tokens = app
	app.sched.Resolve = app.accountResolver()
	app.sched.Interval = time.Minute
	app.sched.AfterSync = func(ctx context.Context, account mail.Account, reports []mail.SyncReport, err error) {
		// finishSync now returns the finalization outcome; a scheduled
		// reconciliation failure must be visible in the log, not silently
		// recorded on the account row (audit 4 F16).
		if finishErr := app.finishSync(ctx, account.ID, reports, err); finishErr != nil {
			app.log.Error("sync finalization failed", "account", account.ID, "err", finishErr)
		}
	}
	// email_accounts.sync_enabled is the product-level pause switch; the
	// engine only knows the mirror, so the decision is supplied from here.
	app.sched.Include = func(acct mail.Account) bool {
		var enabled bool
		if err := db.QueryRow(`SELECT sync_enabled FROM email_accounts WHERE mirror_account_id=$1`, string(acct.ID)).Scan(&enabled); err != nil {
			// Unknown to the product layer: not ours to sync.
			return false
		}
		return enabled
	}

	if err := app.ensureUser(ctx, ""); err != nil {
		log.Printf("app: user bootstrap failed (continuing): %v", err)
	}
	return app
}

// migrateAccountScopedState now lives in migrate.go as product migration
// version 2 (audit OPS-02).

// startBackground runs the sync scheduler and a classification pass on the
// app's own cadence. Classification is idempotent; a slightly stale bucket
// fixes itself on the next tick. The context is the server's shutdown
// context and roots the app's task group: both loops stop when the process
// drains, and stopBackground joins them before the pools close (audit
// OPS-04).
func (a *App) startBackground(ctx context.Context) {
	a.startBackgroundTasks(ctx)
	// A crashed export build can leave its temp archive behind; the
	// handler removes its own file on every return path, so anything
	// matching the pattern before the server listens is garbage. Run
	// synchronously ahead of ListenAndServe so the sweep can never race
	// a live build from this process (audit OPS-01).
	sweepStaleExportTemps()
	a.launch("sync-scheduler", func(ctx context.Context) {
		_ = a.sched.Run(ctx)
	})
	a.launch("housekeeping", func(ctx context.Context) {
		t := time.NewTicker(2 * time.Minute)
		defer t.Stop()
		a.purgeExpired()
		// A reconcile job left 'running' belongs to a previous process;
		// it goes back to pending so this process's passes retry it.
		if err := a.resetStaleReconcileJobs(ctx); err != nil {
			a.log.Error("reconcile job reset failed", "err", err)
		}
		for {
			uid, err := a.userID(ctx)
			if err == nil {
				if err := a.classifyUser(ctx, uid); err != nil {
					a.log.Error("classify failed", "err", err)
				}
				// Dated snoozes whose day has come return to the Imbox; the
				// board and briefing also sweep on demand so a just-arrived
				// return never waits on the tick.
				if err := a.sweepSnoozed(ctx, uid, ""); err != nil {
					a.log.Error("sweep failed", "err", err)
				}
				if err := a.applyRetention(ctx, uid); err != nil {
					a.log.Error("retention sweep failed", "err", err)
				}
				if err := a.processReconcileJobs(ctx, uid); err != nil {
					a.log.Error("reconcile pass failed", "err", err)
				}
				a.sendPushForUser(ctx, uid)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			a.purgeExpired()
		}
	})
}

// purgeExpired keeps auth tables bounded: ceremonies and OAuth states are
// consumed-or-deleted today, so anything expired is garbage; sessions are
// filtered on read but would otherwise live in the table forever. It also
// reclaims expired push-claim leases whose dispatch died mid-send.
func (a *App) purgeExpired() {
	for _, q := range []string{
		`DELETE FROM auth_challenges WHERE expires_at < now()`,
		`DELETE FROM oauth_states WHERE expires_at < now()`,
		`DELETE FROM auth_sessions WHERE expires_at < now()`,
		`DELETE FROM push_deliveries WHERE delivered_at IS NULL AND claimed_at < now() - interval '10 minutes'`,
		// Idempotency answers outlive the request by design (a parked
		// offline queue can replay weeks later) but not forever: past the
		// retention window the row goes and a retried mutation would
		// re-apply — the window is the documented contract.
		fmt.Sprintf(`DELETE FROM api_mutations WHERE created_at < now() - interval '%d days'`, idempotencyRetentionDays),
		// Spent factor-budget windows are garbage the moment they roll
		// over; a day of slack covers clock skew and investigation.
		`DELETE FROM auth_factor_windows WHERE window_start < now() - interval '1 day'`,
	} {
		if _, err := a.db.Exec(q); err != nil {
			a.log.Error("purge failed", "err", err, "query", q)
		}
	}
}

// readOnlyEngine denies every non-read method on the wrapped handler: the
// raw engine surface must not offer an alternate mutation/send pipeline
// beside the product's own semantics.
func readOnlyEngine(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeProblem(w, http.StatusMethodNotAllowed, "Use Product API",
				"mail mutations and sends must use the owner-scoped product endpoints")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ownedMirror fences the engine's account-keyed surface behind
// email_accounts ownership. The bare account list answers directly (it has
// no {account} segment to check) with only the caller's accounts.
func (a *App) ownedMirror(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, err := a.userID(r.Context())
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
			return
		}
		if r.URL.Path == "/v1/accounts" {
			rows, err := a.db.QueryContext(r.Context(), `
				SELECT ma.id, ma.provider, ma.email, ma.name, ma.needs_reauth
				FROM mail_accounts ma
				JOIN email_accounts ea ON ea.mirror_account_id = ma.id AND ea.user_id = $1
				ORDER BY ma.id`, uid)
			if err != nil {
				writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
				return
			}
			defer rows.Close()
			accounts := []map[string]any{}
			for rows.Next() {
				var id, provider, email, name string
				var reauth bool
				if err := rows.Scan(&id, &provider, &email, &name, &reauth); err != nil {
					writeProblem(w, http.StatusInternalServerError, "Scan Failed", err.Error())
					return
				}
				accounts = append(accounts, map[string]any{
					"id": id, "provider": provider, "email": email, "name": name, "needs_reauth": reauth,
				})
			}
			if err := rows.Err(); err != nil {
				writeProblem(w, http.StatusInternalServerError, "Query Failed", err.Error())
				return
			}
			writeJSON(w, map[string]any{"accounts": accounts})
			return
		}
		// /v1/accounts/{account}/... — every downstream handler is keyed by
		// this segment, so one check covers reads, sends, and mutations.
		rest := strings.TrimPrefix(r.URL.Path, "/v1/accounts/")
		account, _, _ := strings.Cut(rest, "/")
		if account != "" {
			var owned bool
			if err := a.db.QueryRowContext(r.Context(),
				`SELECT EXISTS(SELECT 1 FROM email_accounts WHERE mirror_account_id=$1 AND user_id=$2)`,
				account, uid).Scan(&owned); err == nil && owned {
				next.ServeHTTP(w, r)
				return
			}
		}
		writeProblem(w, http.StatusNotFound, "No Such Account", "that mailbox is not connected for this user")
	})
}

func (a *App) mountAPI(mux *http.ServeMux) {
	api := http.NewServeMux()
	public := http.NewServeMux()
	a.mountAuth(public)
	a.mountOAuthCallbacks(public)

	// neutron-mail surface. Owned-envelope middleware first: the engine is
	// account-id keyed with no user concept of its own, so ownership is
	// enforced here before any handler sees a request.
	//
	// The mount is read-only (audit SEND-07): the raw engine API is a
	// second send/sync/mutation pipeline that bypasses the product's undo
	// window, Sent-copy filing, and post-sync classification. Reads
	// (accounts, mailboxes, messages, bodies, attachments) remain available
	// for tooling; mutations and sends belong to the owner-scoped product
	// endpoints.
	api.Handle("/mail/", http.StripPrefix("/mail", a.ownedMirror(readOnlyEngine(a.svc.Handler()))))

	api.HandleFunc("GET /accounts", a.handleAccounts)
	api.HandleFunc("POST /accounts", a.handleAccounts)
	api.HandleFunc("GET /accounts/{id}", a.handleAccountItem)
	api.HandleFunc("DELETE /accounts/{id}", a.handleAccountItem)
	api.HandleFunc("POST /accounts/{id}", a.handleAccountItem)
	api.Handle("GET /accounts/{id}/export", a.accountExportLifecycle(http.HandlerFunc(a.handleAccountExport)))
	api.HandleFunc("GET /events", a.handleEvents)
	api.HandleFunc("GET /security", a.handleSecurity)
	api.HandleFunc("POST /security/reauthenticate", a.handleReauthenticate)
	api.HandleFunc("POST /security/passkeys/begin", a.handlePasskeyRegisterBegin)
	api.HandleFunc("POST /security/passkeys/finish", a.handlePasskeyRegisterFinish)
	api.HandleFunc("DELETE /security/passkeys/{id}", a.handlePasskeyDelete)
	api.HandleFunc("POST /security/recovery/regenerate", a.handleRecoveryRegenerate)
	api.HandleFunc("POST /security/totp/begin", a.handleTOTPBegin)
	api.HandleFunc("POST /security/totp/confirm", a.handleTOTPConfirm)
	api.HandleFunc("DELETE /security/totp", a.handleTOTPDelete)
	api.HandleFunc("POST /security/password", a.handlePasswordSet)
	api.HandleFunc("DELETE /security/password", a.handlePasswordDelete)
	api.HandleFunc("GET /security/sessions", a.handleSessions)
	api.HandleFunc("DELETE /security/sessions/{id}", a.handleSessions)
	a.mountAgentTokens(api)
	api.HandleFunc("DELETE /account", a.handleFullAccountDelete)
	api.HandleFunc("GET /personal/export", a.handlePersonalExport)
	api.HandleFunc("GET /push", a.handlePush)
	api.HandleFunc("POST /push", a.handlePush)
	api.HandleFunc("DELETE /push", a.handlePush)
	api.HandleFunc("GET /oauth/status", a.handleOAuthStatus)
	api.HandleFunc("POST /oauth/{provider}/start", a.handleOAuthStart)

	api.HandleFunc("GET /screener", a.handleScreener)
	api.HandleFunc("GET /counts", a.handleCounts)
	api.HandleFunc("GET /prefs", a.handlePrefs)
	api.HandleFunc("POST /prefs", a.handlePrefs)
	api.HandleFunc("GET /search", a.handleSearch)
	api.HandleFunc("GET /briefing", a.handleBriefing)
	api.HandleFunc("GET /board", a.handleBoard)
	api.HandleFunc("POST /board/pin", a.withIdempotency(a.handleBoardPin))
	api.HandleFunc("POST /board/cards", a.withIdempotency(a.handleBoardCard))
	api.HandleFunc("POST /board/cards/{id}/done", a.withIdempotency(a.handleBoardCardDone))
	api.HandleFunc("POST /board/unpin", a.withIdempotency(a.handleBoardUnpin))

	api.HandleFunc("GET /notes", a.handleNotes)
	api.HandleFunc("POST /notes", a.withIdempotency(a.handleNoteCreate))
	api.HandleFunc("POST /notes/{id}", a.withIdempotency(a.handleNoteUpdate))
	api.HandleFunc("DELETE /notes/{id}", a.withIdempotency(a.handleNoteDelete))
	api.HandleFunc("GET /people", a.handlePeople)
	api.HandleFunc("GET /recent", a.handleRecent)
	api.HandleFunc("GET /folder", a.handleFolder)
	api.HandleFunc("GET /mailboxes", a.handleMailboxList)
	api.HandleFunc("POST /screener/decide", a.withIdempotency(a.handleDecide))
	api.HandleFunc("POST /screener/undecide", a.withIdempotency(a.handleUndecide))
	api.HandleFunc("GET /buckets/{bucket}", a.handleBucket)
	// GitHub-derived thread ids contain "/" (repo/check-suites/...@github.com),
	// so the segment is a suffix wildcard, not a single path element.
	api.Handle("GET /threads/{thread...}", a.accountWorkLifecycle(http.HandlerFunc(a.handleThread)))
	api.HandleFunc("POST /messages/{message}/action", a.withIdempotency(a.handleMessageAction))
	api.Handle("GET /messages/{message}/attachment/{part}", a.accountWorkLifecycle(http.HandlerFunc(a.handleAttachment)))
	api.Handle("GET /messages/{message}/eml", a.accountWorkLifecycle(http.HandlerFunc(a.handleMessageEML)))
	api.HandleFunc("POST /send", a.handleSend)
	api.HandleFunc("DELETE /outbox/{id}", a.handleUndoSend)
	api.HandleFunc("POST /classify", func(w http.ResponseWriter, r *http.Request) {
		uid, err := a.userID(r.Context())
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
			return
		}
		if err := a.classifyUser(r.Context(), uid); err != nil {
			writeProblem(w, http.StatusInternalServerError, "Classify Failed", err.Error())
			return
		}
		pushUID := uid
		a.launch("push-dispatch", func(ctx context.Context) {
			a.sendPushForUser(ctx, pushUID)
		})
		writeJSON(w, map[string]any{"ok": true})
	})

	// Unknown API paths answer RFC 7807 like every other API error, rather
	// than falling through to the dashboard's HTML 404.
	api.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, http.StatusNotFound, "Not Found", "no such API route")
	})

	// Auth ceremony/status routes are public; all product data is session
	// protected. The bootstrap token stops working after the first credential.
	// Agent Bearer tokens enter through requireAgent, which additionally
	// fences them away from the auth/security surface.
	//
	// Everything under /api carries a no-store policy: authenticated mail,
	// session, and problem responses have no business living in shared or
	// persisted caches (audit 3 OPS-08). Individual download handlers set
	// the same header themselves; this is the backstop for everything else.
	public.Handle("/", a.requireAgent(api))
	mux.Handle("/api/", http.StripPrefix("/api", noStoreAPI(public)))
}

// noStoreAPI marks every API response uncacheable by intermediaries.
func noStoreAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// apiUnavailable keeps API paths JSON-shaped (RFC 7807) instead of letting
// them fall through to the dashboard's HTML fallback.
func apiUnavailable(mux *http.ServeMux, reason string) {
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, http.StatusServiceUnavailable, "API unavailable", reason)
	})
}

// newSyncEngine builds the sync engine with body prefetch ON.
//
// Without it, mail_bodies is filled only when someone opens a message, so the
// FIRST open of every message is a live round trip to the provider — connect,
// authenticate, select, fetch — and "first open" is every message a person has
// not read yet. That is the slow path they actually notice, because the ones
// they have already read are the ones already cached.
//
// The engine's own default is off, and correctly so: it is a library, and
// bodies are what make a mirror unbounded. This is the server, where the mirror
// is one operator's own accounts and the whole store is measured in hundreds of
// messages. PREFETCH_BODIES=0 turns it back off for an install where that stops
// being true.
func newSyncEngine(store mail.Store, cfg *Config) *mail.Engine {
	eng := mail.NewEngine(store, slog.Default())
	eng.FetchBodies = envOr("PREFETCH_BODIES", "1") != "0"
	return eng
}
