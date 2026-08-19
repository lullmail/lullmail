package main

import (
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

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	app := connectApp(cfg)
	if app != nil {
		app.mountAPI(mux)
		app.startBackground()
	} else {
		apiUnavailable(mux, "no database configured")
	}

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
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("email-soft listening on %s", addr)
	log.Fatal(srv.ListenAndServe())
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
	const link = `<link rel="stylesheet" href="/styles.css">`
	if !strings.Contains(s, link) {
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
