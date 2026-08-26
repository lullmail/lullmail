package main

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"
)

func serve() {
	cfg := loadConfig()
	addr := cfg.Addr

	mux := http.NewServeMux()

	app := connectApp(cfg)
	if app != nil {
		app.mountAPI(mux)
		app.startBackground()
	} else {
		apiUnavailable(mux, "no database configured")
	}

	// Always 200 on purpose: deploy health checks should restart on process
	// death, not on a database blip — the API degrades to 503 instead and
	// the dashboard says why. Registered after connectApp so it can report
	// database state.
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		db := "down"
		if app != nil {
			if err := app.db.PingContext(r.Context()); err == nil {
				db = "up"
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","database":"` + db + `"}`))
	})

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
				}
				if strings.HasSuffix(name, ".html") {
					serveHTML(w, r, dist, name)
					return
				}
				http.ServeFileFS(w, r, dist, name)
				return
			}
			if strings.Contains(name, ".") {
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
	log.Printf("email-soft listening on %s", addr)
	log.Fatal(srv.ListenAndServe())
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
