package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func serve() {
	cfg := loadConfig()
	addr := cfg.Addr

	mux := http.NewServeMux()

	app := connectApp(cfg)
	if app != nil {
		app.mountAPI(mux)
	} else {
		apiUnavailable(mux, "no database configured")
	}

	// Registered after connectApp so it can report database state.
	mux.HandleFunc("/health", app.handleHealth)

	dist, err := fs.Sub(assets, "dashboard/dist")
	if err != nil {
		log.Fatal(err)
	}
	var handler http.Handler
	if _, err := fs.Stat(dist, "index.html"); err != nil {
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Write([]byte("dashboard not built — run: cd dashboard && npm i && npm run build\n"))
		})
	} else {
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := strings.Trim(r.URL.Path, "/")
			if name == "" {
				name = "index.html"
			}
			if fi, err := fs.Stat(dist, name); err == nil && fi.IsDir() {
				name += "/index.html"
			}
			if _, err := fs.Stat(dist, name); err == nil {
				if name == "service-worker.js" {
					w.Header().Set("Cache-Control", "no-cache")
					w.Header().Set("Service-Worker-Allowed", "/")
					serveServiceWorker(w, r, dist)
					return
				}
				if strings.HasSuffix(name, ".html") {
					serveHTML(w, r, dist, name)
					return
				}
				http.ServeFileFS(w, r, dist, name)
				return
			}
			// A dot normally means a real file. Folder routes are the
			// exception: IMAP servers use '.' as a hierarchy separator, so
			// /folder/Archive.2024 is a client route, not a missing asset.
			if strings.Contains(name, ".") && !strings.HasPrefix(name, "folder/") {
				http.NotFound(w, r)
				return
			}
			// Extensionless fallback = client route: serve the root page.
			serveHTML(w, r, dist, "index.html")
		})
	}
	mux.Handle("/", handler)

	srv := &http.Server{
		Addr:              addr,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// SIGTERM/SIGINT drain in-flight requests and stop the background
	// workers before the process exits, so a deploy or container stop never
	// cuts a request or a sync mid-flight. The drain is bounded: requests
	// that outlive it are dropped rather than blocking shutdown forever.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if app != nil {
		app.startBackground(ctx)
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-ctx.Done()
		log.Printf("lullmail shutting down (%v), draining HTTP", ctx.Err())
		drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(drainCtx); err != nil {
			log.Printf("shutdown: HTTP drain incomplete: %v", err)
		}
	}()

	log.Printf("lullmail listening on %s", addr)
	err = srv.ListenAndServe()
	if !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	// The listener is closed and the drain goroutine owns the remaining
	// in-flight budget; wait it out, then release the pools.
	<-drained
	if app != nil {
		app.db.Close()
		app.store.Close()
	}
}

// handleHealth is the deploy liveness probe. Always 200 on purpose: health
// checks should restart on process death, not on a database blip — the API
// degrades to 503 instead and the dashboard says why. The database state in
// the body carries its own short deadline so a stalled probe cannot hang
// liveness until the caller disconnects.
func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	db := "down"
	if a != nil && a.db != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := a.db.PingContext(ctx); err == nil {
			db = "up"
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok","database":"` + db + `"}`))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; base-uri 'self'; object-src 'none'; frame-ancestors 'none'; "+
				"script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data: blob: https:; connect-src 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

// serveServiceWorker stamps the worker with a fingerprint of the built
// shell. The browser only reinstalls a service worker when its bytes
// change; a static file would keep serving the deploy-time shell offline
// forever. Hashing every build artifact — not only index.html — ties the
// version to whatever the build shipped, so a deploy that changes only an
// icon or the manifest still re-installs and re-caches the shell: no
// manual VERSION bumps to forget.
func serveServiceWorker(w http.ResponseWriter, r *http.Request, fsys fs.FS) {
	data, err := fs.ReadFile(fsys, "service-worker.js")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := sha256.New()
	fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path == "service-worker.js" {
			return nil
		}
		file, err := fs.ReadFile(fsys, path)
		if err != nil {
			return nil
		}
		h.Write([]byte(path))
		h.Write(file)
		return nil
	})
	sum := h.Sum(nil)
	body := strings.Replace(string(data),
		`const VERSION = "lull-shell-v1";`,
		`const VERSION = "lull-shell-`+hex.EncodeToString(sum[:6])+`";`, 1)
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	io.WriteString(w, body)
}

// serveHTML writes an HTML file with the stylesheet injected. The Neutron
// static preset emits bare pages (title only); the CSS link is added here at
// serve time — the same convention akiroo's static.go uses — so routes never
// each have to remember it.
func serveHTML(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s := string(data)
	const plainHref = `href="/styles.css"`
	href := "/styles.css"
	if stylesheet, err := fs.ReadFile(fsys, "styles.css"); err == nil {
		sum := sha256.Sum256(stylesheet)
		href += fmt.Sprintf("?v=%x", sum[:6])
	}
	link := `<link rel="stylesheet" href="` + href + `">`
	if strings.Contains(s, plainHref) {
		s = strings.Replace(s, plainHref, `href="`+href+`"`, 1)
	} else if !strings.Contains(s, link) {
		s = strings.Replace(s, "</head>", link+"</head>", 1)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(s))
}

func envOr(key, fallback string) string {
	if v := osGetenv(key); v != "" {
		return v
	}
	return fallback
}
