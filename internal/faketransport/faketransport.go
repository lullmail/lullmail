// Package faketransport provides loopback SMTP and IMAP servers that speak
// just enough of each protocol for lullmail's real client code (the engine's
// SMTP submit and IMAP APPEND) to run against them, and that can misbehave at
// every protocol step. They exist for crash-safety tests and local browser
// checks of the durable outbox; nothing here is used by the product.
package faketransport

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Action is what a server does at a protocol step.
type Action int

const (
	// Continue proceeds normally.
	Continue Action = iota
	// Drop closes the connection at once, as a dying peer or network would.
	Drop
	// Reject answers with a permanent protocol error and keeps the session.
	Reject
)

// Hook observes a protocol step and decides what the server does there. It
// runs on the connection's goroutine, so it may block (to hold a session open
// while a test kills the other side) but must not call back into the server.
type Hook func(step string) Action

type server struct {
	ln   net.Listener
	mu   sync.Mutex
	hook Hook
	wg   sync.WaitGroup
	// conns counts accepted connections.
	conns int
	log   []string
}

func (s *server) start(handle func(net.Conn)) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	s.ln = ln
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns++
			s.mu.Unlock()
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
				handle(conn)
			}()
		}
	}()
	return nil
}

// Close stops accepting and drops every open session.
func (s *server) Close() { s.ln.Close() }

// Port is the loopback TCP port.
func (s *server) Port() int {
	_, p, _ := net.SplitHostPort(s.ln.Addr().String())
	n, _ := strconv.Atoi(p)
	return n
}

// SetHook installs the behavior hook; nil restores normal behavior.
func (s *server) SetHook(h Hook) { s.mu.Lock(); s.hook = h; s.mu.Unlock() }

// Connections is the number of sessions accepted so far.
func (s *server) Connections() int { s.mu.Lock(); defer s.mu.Unlock(); return s.conns }

func (s *server) step(name string) Action {
	s.mu.Lock()
	h := s.hook
	s.log = append(s.log, name)
	s.mu.Unlock()
	if h == nil {
		return Continue
	}
	return h(name)
}

// Reset forgets recorded steps and connections and removes the hook. Kept
// messages are cleared by the owning server's own Reset.
func (s *server) resetBase() {
	s.mu.Lock()
	s.hook, s.conns, s.log = nil, 0, nil
	s.mu.Unlock()
}

// Steps is the ordered record of every protocol step reached.
func (s *server) Steps() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.log...)
}

// Script returns a hook that applies action at the n-th occurrence (1-based)
// of step, across all sessions, and continues everywhere else.
func Script(step string, n int, action Action) Hook {
	var mu sync.Mutex
	seen := 0
	return func(s string) Action {
		if s != step {
			return Continue
		}
		mu.Lock()
		defer mu.Unlock()
		seen++
		if seen == n {
			return action
		}
		return Continue
	}
}

// ---- SMTP ----

// SMTP is a submission server. Steps, in order: greeting, ehlo, mail, rcpt,
// data (DATA received, before 354), body (first body line read), end (whole
// message read, not yet kept), committed (message kept, 250 not yet sent),
// done (250 sent). Drop at "committed" is the dangerous one: the message is
// delivered and the client never learns it.
type SMTP struct {
	server
	delivered []string
}

func NewSMTP() (*SMTP, error) {
	s := &SMTP{}
	return s, s.start(s.handle)
}

// Delivered returns the raw messages the server kept.
func (s *SMTP) Delivered() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.delivered...)
}

// CountContaining counts kept messages containing marker.
func (s *SMTP) CountContaining(marker string) int {
	n := 0
	for _, m := range s.Delivered() {
		if strings.Contains(m, marker) {
			n++
		}
	}
	return n
}

// Reset clears kept messages, the step record and any hook.
func (s *SMTP) Reset() {
	s.resetBase()
	s.mu.Lock()
	s.delivered = nil
	s.mu.Unlock()
}

func (s *SMTP) handle(conn net.Conn) {
	r := bufio.NewReader(conn)
	say := func(line string) { fmt.Fprintf(conn, "%s\r\n", line) }
	if s.step("greeting") == Drop {
		return
	}
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		verb := strings.Fields(cmd + " x")[0]
		switch verb {
		case "EHLO", "HELO":
			switch s.step("ehlo") {
			case Drop:
				return
			case Reject:
				say("554 refused")
				continue
			}
			say("250-fake")
			say("250-AUTH PLAIN")
			say("250 8BITMIME")
		case "AUTH":
			switch s.step("auth") {
			case Drop:
				return
			case Reject:
				say("535 authentication failed")
				continue
			}
			say("235 authenticated")
		case "MAIL":
			if s.step("mail") == Drop {
				return
			}
			say("250 ok")
		case "RCPT":
			switch s.step("rcpt") {
			case Drop:
				return
			case Reject:
				say("550 no such user")
				continue
			}
			say("250 ok")
		case "DATA":
			switch s.step("data") {
			case Drop:
				return
			case Reject:
				say("554 transaction failed")
				continue
			}
			say("354 go")
			var body strings.Builder
			first := true
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if first {
					first = false
					if s.step("body") == Drop {
						return
					}
				}
				if strings.TrimRight(l, "\r\n") == "." {
					break
				}
				body.WriteString(l)
			}
			switch s.step("end") {
			case Drop:
				return
			case Reject:
				say("554 rejected after data")
				continue
			}
			s.mu.Lock()
			s.delivered = append(s.delivered, body.String())
			s.mu.Unlock()
			if s.step("committed") == Drop {
				return
			}
			say("250 accepted")
			s.step("done")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

// ---- IMAP ----

// IMAP serves greeting, CAPABILITY, LOGIN and a synchronising-literal APPEND.
// Steps: greeting, capability, login, append (APPEND received, before the
// continuation), literal (first literal bytes read), end (literal complete,
// not yet kept), committed (kept, OK not yet sent), done (OK sent).
type IMAP struct {
	server
	stored []string
}

func NewIMAP() (*IMAP, error) {
	s := &IMAP{}
	return s, s.start(s.handle)
}

// Stored returns the messages the server kept.
func (s *IMAP) Stored() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.stored...)
}

func (s *IMAP) CountContaining(marker string) int {
	n := 0
	for _, m := range s.Stored() {
		if strings.Contains(m, marker) {
			n++
		}
	}
	return n
}

// Reset clears kept messages, the step record and any hook.
func (s *IMAP) Reset() {
	s.resetBase()
	s.mu.Lock()
	s.stored = nil
	s.mu.Unlock()
}

func (s *IMAP) handle(conn net.Conn) {
	r := bufio.NewReader(conn)
	send := func(l string) { _, _ = io.WriteString(conn, l+"\r\n") }
	if s.step("greeting") == Drop {
		return
	}
	send("* OK fake ready")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return
		}
		tag, cmd := fields[0], strings.ToUpper(fields[1])
		switch cmd {
		case "CAPABILITY":
			if s.step("capability") == Drop {
				return
			}
			send("* CAPABILITY IMAP4rev1")
			send(tag + " OK done")
		case "LOGIN":
			switch s.step("login") {
			case Drop:
				return
			case Reject:
				send(tag + " NO authentication failed")
				continue
			}
			send(tag + " OK logged in")
		case "LOGOUT":
			send("* BYE")
			send(tag + " OK logged out")
			return
		case "APPEND":
			switch s.step("append") {
			case Drop:
				return
			case Reject:
				send(tag + " NO [TRYCREATE] no such mailbox")
				continue
			}
			var n int
			if _, err := fmt.Sscanf(line[strings.LastIndex(line, "{"):], "{%d}", &n); err != nil {
				return
			}
			send("+ ready for literal")
			buf := make([]byte, n+2)
			head := min(n, 16)
			if _, err := io.ReadFull(r, buf[:head]); err != nil {
				return
			}
			if s.step("literal") == Drop {
				return
			}
			if _, err := io.ReadFull(r, buf[head:]); err != nil {
				return
			}
			switch s.step("end") {
			case Drop:
				return
			case Reject:
				send(tag + " NO rejected after literal")
				continue
			}
			s.mu.Lock()
			s.stored = append(s.stored, string(buf[:n]))
			s.mu.Unlock()
			if s.step("committed") == Drop {
				return
			}
			send(tag + " OK APPEND completed")
			s.step("done")
		default:
			send(tag + " BAD unsupported")
		}
	}
}
