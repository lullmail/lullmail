package main

import (
	"bytes"
	"context"
	"crypto/elliptic"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"
)

// Registration needs the pool for ceremony consumption and credential
// loading, then one transaction for installation writes. A real
// database/sql pool of ONE connection proves those phases never overlap.
// The driver records transaction ordering without requiring a SQL server.
type registrationDriver struct {
	mu          sync.Mutex
	session     string
	challenge   bool
	credentials int
	commits     int
	events      []string
	failInsert  bool
}
type registrationConn struct{ d *registrationDriver }
type registrationTx struct{ d *registrationDriver }

func (d *registrationDriver) Open(string) (driver.Conn, error)  { return &registrationConn{d}, nil }
func (c *registrationConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c *registrationConn) Close() error                        { return nil }
func (c *registrationConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *registrationConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	c.d.events = append(c.d.events, "begin")
	return &registrationTx{c.d}, nil
}
func (tx *registrationTx) Commit() error {
	tx.d.mu.Lock()
	defer tx.d.mu.Unlock()
	tx.d.commits++
	tx.d.events = append(tx.d.events, "commit")
	return nil
}
func (tx *registrationTx) Rollback() error {
	tx.d.mu.Lock()
	defer tx.d.mu.Unlock()
	tx.d.events = append(tx.d.events, "rollback")
	return nil
}
func (c *registrationConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	c.d.events = append(c.d.events, q)
	if strings.Contains(q, "INSERT INTO auth_credentials") {
		if c.d.failInsert {
			return nil, errors.New("synthetic insert failure")
		}
		c.d.credentials++
	}
	return driver.RowsAffected(1), nil
}
func (c *registrationConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	c.d.mu.Lock()
	defer c.d.mu.Unlock()
	c.d.events = append(c.d.events, q)
	switch {
	case strings.Contains(q, "GREATEST("):
		return &testRows{columns: []string{"fresh"}, values: [][]driver.Value{{true}}}, nil
	case strings.Contains(q, "DELETE FROM auth_challenges"):
		if !c.d.challenge {
			return emptyRows("user_id", "session_json"), nil
		}
		c.d.challenge = false
		return &testRows{columns: []string{"user_id", "session_json"}, values: [][]driver.Value{{"owner-1", c.d.session}}}, nil
	case strings.Contains(q, "SELECT id,webauthn_handle"):
		return &testRows{columns: []string{"id", "webauthn_handle", "email", "display_name", "auth_epoch"}, values: [][]driver.Value{{"owner-1", []byte("test-user-id"), "owner@example.org", "Owner", int64(0)}}}, nil
	case strings.Contains(q, "SELECT credential_ciphertext"):
		return emptyRows("credential_ciphertext"), nil
	case strings.Contains(q, "SELECT auth_epoch"):
		return &testRows{columns: []string{"auth_epoch"}, values: [][]driver.Value{{int64(0)}}}, nil
	case strings.Contains(q, "EXISTS(SELECT 1 FROM auth_credentials)"):
		return &testRows{columns: []string{"configured"}, values: [][]driver.Value{{c.d.credentials > 0}}}, nil
	default:
		return nil, fmt.Errorf("unexpected registration query: %s", q)
	}
}

var registrationDriverSequence atomic.Uint64

func registrationFixture(t *testing.T) (*App, *registrationDriver, []byte) {
	t.Helper()
	challenge := base64.RawURLEncoding.EncodeToString([]byte("synthetic-registration-challenge"))
	session := webauthn.SessionData{Challenge: challenge, UserID: []byte("test-user-id"), UserVerification: protocol.VerificationRequired, CredParams: []protocol.CredentialParameter{{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgES256}}}
	rawSession, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	d := &registrationDriver{session: string(rawSession), challenge: true}
	name := fmt.Sprintf("registration-pool-%d", registrationDriverSequence.Add(1))
	sql.Register(name, d)
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	cfg := &Config{PublicURL: "https://example.org", RPID: "example.org", SecretKey: "0123456789abcdef0123456789abcdef", APIToken: "synthetic-setup-token"}
	wa, err := newWebAuthn(cfg)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{db: db, cfg: cfg, wa: wa, log: discardLogger(), tokenFromEnv: true, authAttempts: map[string]authAttempt{}}

	// An attestation-format "none" authenticator response with a real P-256
	// COSE public key. It still verifies challenge, origin, RP hash and UV.
	x, y := elliptic.P256().ScalarBaseMult([]byte{1})
	key, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x.FillBytes(make([]byte, 32)), -3: y.FillBytes(make([]byte, 32))})
	if err != nil {
		t.Fatal(err)
	}
	id := []byte("synthetic-credential")
	rpHash := sha256.Sum256([]byte("example.org"))
	authData := append([]byte{}, rpHash[:]...)
	authData = append(authData, 0x45) // user present, user verified, attested data
	authData = append(authData, make([]byte, 4+16)...)
	authData = binary.BigEndian.AppendUint16(authData, uint16(len(id)))
	authData = append(authData, id...)
	authData = append(authData, key...)
	attestation, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": authData})
	if err != nil {
		t.Fatal(err)
	}
	clientData, _ := json.Marshal(map[string]any{"type": "webauthn.create", "challenge": challenge, "origin": "https://example.org", "crossOrigin": false})
	encode := base64.RawURLEncoding.EncodeToString
	response, err := json.Marshal(map[string]any{"id": encode(id), "rawId": encode(id), "type": "public-key", "response": map[string]string{"clientDataJSON": encode(clientData), "attestationObject": encode(attestation)}})
	if err != nil {
		t.Fatal(err)
	}
	return app, d, response
}

func registrationRequest(ctx context.Context, body []byte) *http.Request {
	ctx = context.WithValue(ctx, authContextKey{}, "owner-1")
	ctx = context.WithValue(ctx, sessionContextKey{}, "fresh-session")
	r := httptest.NewRequest(http.MethodPost, "/security/passkeys/finish", bytes.NewReader(body)).WithContext(ctx)
	r.AddCookie(&http.Cookie{Name: ceremonyCookie, Value: "synthetic-ceremony"})
	r.Header.Set("Authorization", "Bearer synthetic-setup-token")
	return r
}

func TestPasskeyRegistrationCompletesWithOneConnection(t *testing.T) {
	for _, kind := range []string{"register", "bootstrap"} {
		t.Run(kind, func(t *testing.T) {
			app, d, body := registrationFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			w := httptest.NewRecorder()
			if kind == "bootstrap" {
				app.handleBootstrapFinish(w, registrationRequest(ctx, body))
			} else {
				app.handlePasskeyRegisterFinish(w, registrationRequest(ctx, body))
			}
			if w.Code != http.StatusOK {
				t.Fatalf("finish=%d: %s; events=%q", w.Code, w.Body.String(), d.events)
			}
			if d.credentials != 1 || d.commits != 2 || d.challenge {
				t.Fatalf("credentials=%d commits=%d challenge=%v", d.credentials, d.commits, d.challenge)
			}
			if waits := app.db.Stats().WaitCount; waits != 0 {
				t.Fatalf("registration waited for its own connection %d times", waits)
			}
			if kind == "bootstrap" {
				lock, owner, insert := -1, -1, -1
				for i, event := range d.events {
					if strings.Contains(event, "pg_advisory_xact_lock") {
						lock = i
					}
					if owner < 0 && strings.Contains(event, "SELECT auth_epoch") {
						owner = i
					}
					if strings.Contains(event, "INSERT INTO auth_credentials") {
						insert = i
					}
				}
				if lock < 0 || owner <= lock || insert <= owner {
					t.Fatalf("bootstrap lock order changed: lock=%d owner=%d insert=%d", lock, owner, insert)
				}
			}
		})
	}
}

func TestPasskeyRegistrationConsumesRejectedAndRolledBackCeremonies(t *testing.T) {
	for _, failure := range []string{"bad-proof", "insert"} {
		t.Run(failure, func(t *testing.T) {
			app, d, body := registrationFixture(t)
			if failure == "bad-proof" {
				body = []byte(`{"type":"public-key"}`)
			} else {
				d.failInsert = true
			}
			first := httptest.NewRecorder()
			app.handlePasskeyRegisterFinish(first, registrationRequest(context.Background(), body))
			if first.Code < 400 {
				t.Fatalf("failure was accepted: %d", first.Code)
			}
			if d.challenge {
				t.Fatal("failed registration restored one-use ceremony")
			}
			if d.credentials != 0 {
				t.Fatal("failed registration installed a credential")
			}
			second := httptest.NewRecorder()
			app.handlePasskeyRegisterFinish(second, registrationRequest(context.Background(), body))
			if second.Code != 400 || !strings.Contains(second.Body.String(), "Passkey Expired") {
				t.Fatalf("replay=%d: %s", second.Code, second.Body.String())
			}
		})
	}
}

// Public assertion finish and both attestation finish routes share the
// published 16 KiB ceiling, including bytes after a valid JSON document.
func TestWebAuthnFinishBodyLimit(t *testing.T) {
	for _, route := range []string{"login", "register", "bootstrap"} {
		for _, extra := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s/extra=%d", route, extra), func(t *testing.T) {
				app, d, body := registrationFixture(t)
				// Two connections isolate body admission from the separate pool
				// regression, and let this test fail for the right reason pre-fix.
				app.db.SetMaxOpenConns(2)
				size := (16 << 10) + extra
				body = append(body, bytes.Repeat([]byte(" "), size-len(body))...)
				request := registrationRequest(context.Background(), body)
				reader := &countedPasskeyReader{Reader: bytes.NewReader(body)}
				request.Body = reader
				w := httptest.NewRecorder()
				switch route {
				case "login":
					app.handleLoginFinish(w, request)
				case "register":
					app.handlePasskeyRegisterFinish(w, request)
				case "bootstrap":
					app.handleBootstrapFinish(w, request)
				}
				want := http.StatusOK
				if route == "login" {
					want = http.StatusUnauthorized
				} // creation proof is not an assertion
				if extra > 0 {
					want = http.StatusRequestEntityTooLarge
				}
				if w.Code != want {
					t.Fatalf("status=%d want=%d: %s", w.Code, want, w.Body.String())
				}
				if d.challenge {
					t.Fatal("finish did not consume the one-use ceremony")
				}
				if reader.read > 16<<10+1 {
					t.Fatalf("read past bound: %d", reader.read)
				}
				if !reader.closed {
					t.Fatal("finish did not close original request body")
				}
			})
		}
	}
}

type countedPasskeyReader struct {
	*bytes.Reader
	read   int
	closed bool
}

func (r *countedPasskeyReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}
func (r *countedPasskeyReader) Close() error { r.closed = true; return nil }
