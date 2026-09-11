package main

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestServeHTMLVersionsStylesheet(t *testing.T) {
	files := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(`<html><head><link rel="stylesheet" href="/styles.css"></head></html>`)},
		"styles.css": &fstest.MapFile{Data: []byte(`body { color: red; }`)},
	}
	w := httptest.NewRecorder()
	serveHTML(w, httptest.NewRequest("GET", "/", nil), files, "index.html")
	response := w.Result()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 {
		t.Fatalf("status = %d", response.StatusCode)
	}
	got := string(body)
	if strings.Contains(got, `href="/styles.css"`) || !strings.Contains(got, `href="/styles.css?v=`) {
		t.Fatalf("stylesheet was not content-versioned: %s", got)
	}
}

func workerVersion(t *testing.T, files fstest.MapFS) string {
	t.Helper()
	w := httptest.NewRecorder()
	serveServiceWorker(w, httptest.NewRequest("GET", "/service-worker.js", nil), files)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	const marker = `const VERSION = "lull-shell-`
	idx := strings.Index(w.Body.String(), marker)
	if idx < 0 {
		t.Fatalf("worker has no stamped version: %s", w.Body.String())
	}
	rest := w.Body.String()[idx+len(marker):]
	return rest[:strings.Index(rest, `"`)]
}

// The cache version must change when any precached asset changes, not only
// index.html: the worker serves icons and the manifest cache-first, so an
// asset-only deploy has to produce a new version or those files stay stale.
func TestServiceWorkerVersionCoversAllShellAssets(t *testing.T) {
	shell := func(icon string, manifest string) fstest.MapFS {
		return fstest.MapFS{
			"index.html":           &fstest.MapFile{Data: []byte(`<html>unchanged shell</html>`)},
			"service-worker.js":    &fstest.MapFile{Data: []byte(`const VERSION = "lull-shell-v1";` + "\n")},
			"icon.svg":             &fstest.MapFile{Data: []byte(icon)},
			"manifest.webmanifest": &fstest.MapFile{Data: []byte(manifest)},
		}
	}
	before := workerVersion(t, shell("<svg>one</svg>", `{}`))
	afterIcon := workerVersion(t, shell("<svg>two</svg>", `{}`))
	afterManifest := workerVersion(t, shell("<svg>one</svg>", `{"v":2}`))
	if before == "" || afterIcon == "" || afterManifest == "" {
		t.Fatal("version stamp missing")
	}
	if afterIcon == before {
		t.Fatal("icon-only deploy left the worker version unchanged")
	}
	if afterManifest == before {
		t.Fatal("manifest-only deploy left the worker version unchanged")
	}
	if afterManifest == afterIcon {
		t.Fatal("distinct asset changes produced the same version")
	}
}
