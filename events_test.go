package main

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSyncEventsOwnerIsolation(t *testing.T) {
	broker := newSyncEvents()
	aCh, cancelA := broker.subscribe("owner-a")
	defer cancelA()
	bCh, cancelB := broker.subscribe("owner-b")
	defer cancelB()

	broker.publish("owner-a", syncEvent{Type: "sync-finished", AccountID: "acct-a", Changed: true})

	select {
	case ev := <-aCh:
		if ev.AccountID != "acct-a" || !ev.Changed {
			t.Fatalf("owner A got wrong event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("owner A never received its event")
	}
	select {
	case ev := <-bCh:
		t.Fatalf("owner B received owner A's event: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSyncEventsSlowSubscriberDoesNotBlock(t *testing.T) {
	broker := newSyncEvents()
	ch, cancel := broker.subscribe("owner")
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < syncEventBuffer*4; i++ {
			broker.publish("owner", syncEvent{Type: "sync-finished", AccountID: "acct", Changed: true})
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publish blocked on a subscriber that never reads")
	}

	// The buffer keeps the newest hints without loss beyond its capacity.
	seen := 0
	for {
		select {
		case <-ch:
			seen++
		default:
			if seen == 0 {
				t.Fatal("a coalesced hint should have survived the burst")
			}
			return
		}
	}
}

func TestSyncEventsCancelStopsDelivery(t *testing.T) {
	broker := newSyncEvents()
	ch, cancel := broker.subscribe("owner")
	cancel()
	if got := broker.count(); got != 0 {
		t.Fatalf("subscriber not removed after cancel, count=%d", got)
	}
	broker.publish("owner", syncEvent{Type: "sync-finished"})
	select {
	case ev := <-ch:
		t.Fatalf("canceled subscriber received event: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

// The handler must release the subscriber when the request goes away, or a
// browser tab close would leak a channel per visit.
func TestHandleEventsUnsubscribesOnDisconnect(t *testing.T) {
	app := &App{events: newSyncEvents()}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The session middleware normally injects the owner; stand in for it
		// so the handler exercises streaming, not the database lookup.
		ctx := context.WithValue(r.Context(), authContextKey{}, "owner-1")
		app.handleEvents(w, r.WithContext(ctx))
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	reader := bufio.NewReader(res.Body)
	line, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != ": connected" {
		t.Fatalf("expected initial comment, got %q err=%v", line, err)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if got := app.events.count(); got != 1 {
		t.Fatalf("subscriber count after connect = %d, want 1", got)
	}

	cancel()
	deadline := time.After(2 * time.Second)
	for app.events.count() != 0 {
		select {
		case <-deadline:
			t.Fatalf("subscriber leaked after disconnect, count=%d", app.events.count())
		case <-time.After(10 * time.Millisecond):
		}
	}
}
