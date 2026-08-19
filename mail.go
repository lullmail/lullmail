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
		cfg:   cfg,
		db:    db,
		log:   slog.Default(),
		store: store,
		eng:   mail.NewEngine(store, slog.Default()),
		sendq: newSendQueue(),
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
			}
			<-t.C
		}
	}()
}

func (a *App) mountAPI(mux *http.ServeMux) {
	api := http.NewServeMux()

	// neutron-mail surface, for the dashboard's use (bodies, raw search).
	api.Handle("/mail/", http.StripPrefix("/mail", a.svc.Handler()))

	api.HandleFunc("GET /accounts", a.handleAccounts)
	api.HandleFunc("POST /accounts", a.handleAccounts)
	api.HandleFunc("GET /accounts/{id}", a.handleAccountItem)
	api.HandleFunc("DELETE /accounts/{id}", a.handleAccountItem)
	api.HandleFunc("POST /accounts/{id}", a.handleAccountItem)

	api.HandleFunc("GET /screener", a.handleScreener)
	api.HandleFunc("GET /counts", a.handleCounts)
	api.HandleFunc("GET /search", a.handleSearch)
	api.HandleFunc("GET /briefing", a.handleBriefing)
	api.HandleFunc("GET /people", a.handlePeople)
	api.HandleFunc("GET /recent", a.handleRecent)
	api.HandleFunc("GET /folder", a.handleFolder)
	api.HandleFunc("GET /mailboxes", a.handleMailboxList)
	api.HandleFunc("POST /screener/decide", a.handleDecide)
	api.HandleFunc("GET /buckets/{bucket}", a.handleBucket)
	api.HandleFunc("GET /threads/{thread}", a.handleThread)
	api.HandleFunc("POST /messages/{message}/action", a.handleMessageAction)
	api.HandleFunc("GET /messages/{message}/attachment/{part}", a.handleAttachment)
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
		writeJSON(w, map[string]any{"ok": true})
	})

	// One wrapper, every API route behind it.
	mux.Handle("/api/", http.StripPrefix("/api", a.requireToken(api)))
}

// apiUnavailable keeps API paths JSON-shaped (RFC 7807) instead of letting
// them fall through to the dashboard's HTML fallback.
func apiUnavailable(mux *http.ServeMux, reason string) {
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, http.StatusServiceUnavailable, "API unavailable", reason)
	})
}
