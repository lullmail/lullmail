// Command outbox-sink runs the loopback SMTP and IMAP fakes behind a small
// control API, so a browser or script can drive lullmail's durable outbox
// against a transport that fails on demand. It touches no real mail system.
//
//	go run ./tools/outbox-sink -control 127.0.0.1:18125
//
// It prints one JSON line with its ports, then serves:
//
//	GET  /state                         kept messages and connection counts
//	POST /fault?server=smtp|imap&step=S&action=drop|reject|continue
//	            [&times=N][&after=K][&hold_ms=M]
//	                                    apply action at occurrences K+1..K+N of
//	                                    step S (defaults after=0, times=1),
//	                                    pausing hold_ms first
//	POST /reset                         clear messages, counters and faults
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"lullmail/internal/faketransport"
)

type scripted struct {
	mu    sync.Mutex
	rules []*rule
}

type rule struct {
	step         string
	skip, remain int
	action       faketransport.Action
	hold         time.Duration
}

func (s *scripted) hook(step string) faketransport.Action {
	s.mu.Lock()
	var hit *rule
	for _, r := range s.rules {
		if r.step != step {
			continue
		}
		if r.skip > 0 {
			r.skip--
			continue
		}
		if r.remain > 0 {
			r.remain--
			hit = r
			break
		}
	}
	s.mu.Unlock()
	if hit == nil {
		return faketransport.Continue
	}
	time.Sleep(hit.hold)
	return hit.action
}

func main() {
	control := flag.String("control", "127.0.0.1:18125", "control API address (loopback)")
	smtpPort := flag.Int("smtp-port", 0, "ignored; ports are chosen by the OS and printed")
	_ = smtpPort
	flag.Parse()

	smtp, err := faketransport.NewSMTP()
	if err != nil {
		log.Fatal(err)
	}
	imap, err := faketransport.NewIMAP()
	if err != nil {
		log.Fatal(err)
	}
	scripts := map[string]*scripted{"smtp": {}, "imap": {}}
	smtp.SetHook(scripts["smtp"].hook)
	imap.SetHook(scripts["imap"].hook)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"smtp": map[string]any{"connections": smtp.Connections(), "delivered": smtp.Delivered(), "steps": smtp.Steps()},
			"imap": map[string]any{"connections": imap.Connections(), "stored": imap.Stored(), "steps": imap.Steps()},
		})
	})
	mux.HandleFunc("POST /fault", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		sc := scripts[q.Get("server")]
		if sc == nil {
			http.Error(w, "server must be smtp or imap", 400)
			return
		}
		actions := map[string]faketransport.Action{"drop": faketransport.Drop, "reject": faketransport.Reject, "continue": faketransport.Continue}
		action, ok := actions[q.Get("action")]
		if !ok || q.Get("step") == "" {
			http.Error(w, "step and action (drop|reject|continue) are required", 400)
			return
		}
		num := func(key string, def int) int {
			if v, err := strconv.Atoi(q.Get(key)); err == nil {
				return v
			}
			return def
		}
		sc.mu.Lock()
		sc.rules = append(sc.rules, &rule{step: q.Get("step"), skip: num("after", 0), remain: num("times", 1), action: action, hold: time.Duration(num("hold_ms", 0)) * time.Millisecond})
		sc.mu.Unlock()
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("POST /reset", func(w http.ResponseWriter, r *http.Request) {
		for _, sc := range scripts {
			sc.mu.Lock()
			sc.rules = nil
			sc.mu.Unlock()
		}
		smtp.Reset()
		imap.Reset()
		smtp.SetHook(scripts["smtp"].hook)
		imap.SetHook(scripts["imap"].hook)
		fmt.Fprintln(w, "ok")
	})
	ports, _ := json.Marshal(map[string]int{"smtp": smtp.Port(), "imap": imap.Port()})
	fmt.Println(string(ports))
	log.Fatal(http.ListenAndServe(*control, mux))
}
