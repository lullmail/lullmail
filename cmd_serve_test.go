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
