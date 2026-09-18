package main

// Offline coverage for the OPS-01 body bounds: the budgeted export
// writer, the decode-slot admission, and one handler-level proof that an
// oversized body answers 413 rather than a decode error.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBudgetedWriterEnforcesCap(t *testing.T) {
	w := &budgetedWriter{w: &strings.Builder{}, max: 10}
	if _, err := w.Write([]byte("0123456789")); err != nil {
		t.Fatalf("write within budget: %v", err)
	}
	if _, err := w.Write([]byte("x")); err == nil {
		t.Fatal("write past the budget succeeded")
	}
}

func TestDecodeSlotAdmission(t *testing.T) {
	release1, ok := acquireDecodeSlot(context.Background())
	if !ok {
		t.Fatal("first slot denied with an idle semaphore")
	}
	release2, ok := acquireDecodeSlot(context.Background())
	if !ok {
		t.Fatal("second slot denied with an idle semaphore")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := acquireDecodeSlot(ctx); ok {
		t.Fatal("third slot granted past capacity")
	}
	release2()
	release3, ok := acquireDecodeSlot(context.Background())
	if !ok {
		t.Fatal("slot not returned by release")
	}
	release1()
	release3()
}

// TestRouteBodyBoundAnswers413: a mutation route with an oversized body
// must refuse before decoding — the bound is the response, not a JSON
// error from a reader that already consumed the bytes.
func TestRouteBodyBoundAnswers413(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Sender string `json:"sender"`
		}
		if err := decodeJSON(w, r, &req); err != nil {
			writeDecodeProblem(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	fat := strings.Repeat("a", 128<<10)
	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest("POST", "/screener/decide", strings.NewReader(`{"sender":"`+fat+`"}`)))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: status %d body %s", w.Code, w.Body.String())
	}
	small := httptest.NewRecorder()
	handler(small, httptest.NewRequest("POST", "/screener/decide", strings.NewReader(`{"sender":"x@example.com"}`)))
	if small.Code != http.StatusOK {
		t.Fatalf("normal body: status %d", small.Code)
	}
	var errMax *http.MaxBytesError
	// A syntactically incomplete document forces the decoder to keep
	// reading past the cap (an immediate syntax error would stop it first).
	if !errors.As(decodeJSONLimit(httptest.NewRecorder(),
		httptest.NewRequest("POST", "/", strings.NewReader(`{"sender":"`+fat+`"}`)),
		&struct{}{}, 4<<10), &errMax) {
		t.Fatal("bounded decode did not surface *http.MaxBytesError")
	}
}
