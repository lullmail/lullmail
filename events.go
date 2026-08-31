package main

// Server-Sent Events: a process-local hint channel from finished syncs to
// any open dashboard tabs. An event never carries mail state — it tells the
// browser to re-read the authoritative API. SSE over a WebSocket because
// communication is server-to-browser only, reconnection is native, and the
// same-site session cookie authenticates without a token in the URL.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// syncEvent is the payload published after one account's sync run finishes.
// AccountID is the product email_accounts.id because the dashboard's mailbox
// lens stores that, not the mirror id.
type syncEvent struct {
	Type      string `json:"type"`
	RunID     string `json:"run_id,omitempty"`
	AccountID string `json:"account_id"`
	Changed   bool   `json:"changed"`
	Error     string `json:"error,omitempty"`
}

const syncEventBuffer = 8

// syncEvents is a broker keyed by owner user ID. Publishing never blocks the
// sync path: a slow tab drops an older hint (they coalesce — each one says
// the same "re-read state") and recovers on the next event or the poll.
type syncEvents struct {
	mu   sync.Mutex
	subs map[string]map[chan syncEvent]struct{}
}

func newSyncEvents() *syncEvents {
	return &syncEvents{subs: map[string]map[chan syncEvent]struct{}{}}
}

func (e *syncEvents) subscribe(uid string) (chan syncEvent, func()) {
	ch := make(chan syncEvent, syncEventBuffer)
	e.mu.Lock()
	if e.subs[uid] == nil {
		e.subs[uid] = map[chan syncEvent]struct{}{}
	}
	e.subs[uid][ch] = struct{}{}
	e.mu.Unlock()
	return ch, func() {
		e.mu.Lock()
		if set, ok := e.subs[uid]; ok {
			delete(set, ch)
			if len(set) == 0 {
				delete(e.subs, uid)
			}
		}
		e.mu.Unlock()
	}
}

func (e *syncEvents) publish(uid string, ev syncEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for ch := range e.subs[uid] {
		select {
		case ch <- ev:
		default:
			// Full buffer: discard the oldest buffered hint and retry once.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- ev:
			default:
			}
		}
	}
}

func (e *syncEvents) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, set := range e.subs {
		n += len(set)
	}
	return n
}

// handleEvents streams hints to an authenticated owner. A comment every 20
// seconds keeps proxies from idling out; request cancellation unsubscribes.
func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	uid, err := a.userID(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "Lookup Failed", err.Error())
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeProblem(w, http.StatusInternalServerError, "Streaming Unsupported", "this connection cannot stream events")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ch, cancel := a.events.subscribe(uid)
	defer cancel()

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case ev := <-ch:
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: sync\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}
