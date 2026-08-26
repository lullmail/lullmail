package main

// App bootstrap: connect the product database and the neutron-mail engine,
// or run with the API gracefully degraded when the database is absent.

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/neutron-build/neutron/mail"
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// connectApp opens both pools (product database/sql + engine pgx) and runs
// both migrations, so a fresh deploy converges from zero. The mail_* mirror
// tables are neutron-mail's; our schema.sql never creates or alters them.
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
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		log.Printf("app: ping failed — API disabled: %v", err)
		return nil
	}
	for _, stmt := range splitStatements(schemaSQL) {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			db.Close()
			log.Printf("app: product migration failed — API disabled: %v", err)
			return nil
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

	app := &App{
		cfg:          cfg,
		db:           db,
		log:          slog.Default(),
		store:        store,
		eng:          mail.NewEngine(store, slog.Default()),
		sendq:        newSendQueue(),
		authAttempts: map[string]authAttempt{},
		tokenFromEnv: cfg.APIToken != "",
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
	app.svc.Resolve = newResolver()
	app.svc.Senders = func(acct mail.AccountID) (*mail.Sender, mail.Address, bool) {
		return app.SMTPFor(context.Background(), acct)
	}

	app.sched = mail.NewScheduler(store, app.eng, nil, slog.Default())
	app.sched.Tokens = app
	app.sched.Resolve = newResolver()

	if err := app.ensureUser(ctx); err != nil {
		log.Printf("app: user bootstrap failed (continuing): %v", err)
	}
	return app
}

// startBackground runs the sync scheduler and a classification pass on the
// app's own cadence. Classification is idempotent; a slightly stale bucket
// fixes itself on the next tick.
func (a *App) startBackground() {
	ctx := context.Background()
	go a.sched.Run(ctx)
	go func() {
		t := time.NewTicker(2 * time.Minute)
		defer t.Stop()
		for {
			uid, err := a.userID(ctx)
			if err == nil {
				if err := a.classifyUser(ctx, uid); err != nil {
					a.log.Error("classify failed", "err", err)
				}
				// Dated snoozes whose day has come return to the Imbox; the
				// board and briefing also sweep on demand so a just-arrived
				// return never waits on the tick.
				if err := a.sweepSnoozed(ctx, uid); err != nil {
					a.log.Error("sweep failed", "err", err)
				}
				if err := a.applyRetention(ctx, uid); err != nil {
					a.log.Error("retention sweep failed", "err", err)
				}
				a.sendPushForUser(ctx, uid)
			}
			<-t.C
		}
	}()
}

func (a *App) mountAPI(mux *http.ServeMux) {
	api := http.NewServeMux()
	public := http.NewServeMux()
	a.mountAuth(public)
	a.mountOAuthCallbacks(public)

	// neutron-mail surface, for the dashboard's use (bodies, raw search).
	api.Handle("/mail/", http.StripPrefix("/mail", a.svc.Handler()))

	api.HandleFunc("GET /accounts", a.handleAccounts)
	api.HandleFunc("POST /accounts", a.handleAccounts)
	api.HandleFunc("GET /accounts/{id}", a.handleAccountItem)
	api.HandleFunc("DELETE /accounts/{id}", a.handleAccountItem)
	api.HandleFunc("POST /accounts/{id}", a.handleAccountItem)
	api.HandleFunc("GET /accounts/{id}/export", a.handleAccountExport)
	api.HandleFunc("GET /security", a.handleSecurity)
	api.HandleFunc("POST /security/passkeys/begin", a.handlePasskeyRegisterBegin)
	api.HandleFunc("POST /security/passkeys/finish", a.handlePasskeyRegisterFinish)
	api.HandleFunc("DELETE /security/passkeys/{id}", a.handlePasskeyDelete)
	api.HandleFunc("POST /security/recovery/regenerate", a.handleRecoveryRegenerate)
	api.HandleFunc("POST /security/totp/begin", a.handleTOTPBegin)
	api.HandleFunc("POST /security/totp/confirm", a.handleTOTPConfirm)
	api.HandleFunc("DELETE /security/totp", a.handleTOTPDelete)
	api.HandleFunc("GET /security/sessions", a.handleSessions)
	api.HandleFunc("DELETE /security/sessions/{id}", a.handleSessions)
	api.HandleFunc("DELETE /account", a.handleFullAccountDelete)
	api.HandleFunc("GET /personal/export", a.handlePersonalExport)
	api.HandleFunc("GET /push", a.handlePush)
	api.HandleFunc("POST /push", a.handlePush)
	api.HandleFunc("DELETE /push", a.handlePush)
	api.HandleFunc("GET /oauth/status", a.handleOAuthStatus)
	api.HandleFunc("POST /oauth/{provider}/start", a.handleOAuthStart)

	api.HandleFunc("GET /screener", a.handleScreener)
	api.HandleFunc("GET /counts", a.handleCounts)
	api.HandleFunc("GET /search", a.handleSearch)
	api.HandleFunc("GET /briefing", a.handleBriefing)
	api.HandleFunc("GET /board", a.handleBoard)
	api.HandleFunc("POST /board/pin", a.handleBoardPin)
	api.HandleFunc("POST /board/cards", a.handleBoardCard)
	api.HandleFunc("POST /board/cards/{id}/done", a.handleBoardCardDone)
	api.HandleFunc("POST /board/unpin", a.handleBoardUnpin)

	api.HandleFunc("GET /notes", a.handleNotes)
	api.HandleFunc("POST /notes", a.handleNoteCreate)
	api.HandleFunc("POST /notes/{id}", a.handleNoteUpdate)
	api.HandleFunc("DELETE /notes/{id}", a.handleNoteDelete)
	api.HandleFunc("GET /people", a.handlePeople)
	api.HandleFunc("GET /recent", a.handleRecent)
	api.HandleFunc("GET /folder", a.handleFolder)
	api.HandleFunc("GET /mailboxes", a.handleMailboxList)
	api.HandleFunc("POST /screener/decide", a.handleDecide)
	api.HandleFunc("POST /screener/undecide", a.handleUndecide)
	api.HandleFunc("GET /buckets/{bucket}", a.handleBucket)
	api.HandleFunc("GET /threads/{thread}", a.handleThread)
	api.HandleFunc("POST /messages/{message}/action", a.handleMessageAction)
	api.HandleFunc("GET /messages/{message}/attachment/{part}", a.handleAttachment)
	api.HandleFunc("GET /messages/{message}/eml", a.handleMessageEML)
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
		go a.sendPushForUser(context.Background(), uid)
		writeJSON(w, map[string]any{"ok": true})
	})

	// Auth ceremony/status routes are public; all product data is session
	// protected. The bootstrap token stops working after the first passkey.
	public.Handle("/", a.requireAuth(api))
	mux.Handle("/api/", http.StripPrefix("/api", public))
}

// apiUnavailable keeps API paths JSON-shaped (RFC 7807) instead of letting
// them fall through to the dashboard's HTML fallback.
func apiUnavailable(mux *http.ServeMux, reason string) {
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, http.StatusServiceUnavailable, "API unavailable", reason)
	})
}
