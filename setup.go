package main

// Zero-config first run, on the Jenkins/GitLab model: the operator proves
// host access once by copying a one-time token from the container logs,
// then finishes everything else in the web setup flow. SECRET_KEY and the
// setup token are generated locally when their env vars are absent; the
// browser origin is detected from the first setup request and pinned at
// completion. Env vars always win for operators who want explicit config.

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

const setupTokenLifetime = 24 * time.Hour

// setupNow is swappable so expiry can be tested without sleeping.
var setupNow = time.Now

// resolveSecretKey returns the effective SECRET_KEY: the env value if set
// (operators pinning it), otherwise a locally generated key persisted to
// DATA_DIR/secret.key with 0600. The keyfile lives outside the database on
// purpose: a leaked SQL dump must not carry the means to unseal itself.
func resolveSecretKey(cfg *Config) error {
	if cfg.SecretKey != "" {
		return nil
	}
	if cfg.DataDir == "" {
		return nil
	}
	path := filepath.Join(cfg.DataDir, "secret.key")
	if raw, err := os.ReadFile(path); err == nil {
		key := strings.TrimSpace(string(raw))
		if _, err := hex.DecodeString(key); err != nil || len(key) != 64 {
			return errors.New("secret.key is not a 64-char hex key — delete it or fix permissions")
		}
		cfg.SecretKey = key
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	b, err := randomBytes(32)
	if err != nil {
		return err
	}
	key := hex.EncodeToString(b)
	// The key is generated-then-published exactly once: os.WriteFile would
	// let two concurrent starts (or a crash mid-write) overwrite or
	// truncate the only persisted key, and losing it loses every stored
	// provider credential (audit AUTH-08).
	stored, err := publishPrivateOnce(path, []byte(key+"\n"))
	if err != nil {
		return err
	}
	winner := strings.TrimSpace(string(stored))
	if _, err := hex.DecodeString(winner); err != nil || len(winner) != 64 {
		return errors.New("secret.key is not a 64-char hex key — delete it or fix permissions")
	}
	if winner != key {
		log.Printf("setup: concurrent start won SECRET_KEY publication at %s — using the existing key", path)
	} else {
		log.Printf("setup: generated SECRET_KEY at %s (set the env var to pin your own)", path)
	}
	cfg.SecretKey = winner
	return nil
}

type setupTokenFile struct {
	Token   string    `json:"token"`
	Created time.Time `json:"created"`
}

// loadOrCreateSetupToken returns the current first-run token, reusing a
// still-fresh file so restarts do not invalidate a token the operator has
// not used yet, and regenerating once the 24h window lapses.
//
// The whole read/expiry-check/generate/publish sequence runs under a
// cross-process file lock, so two concurrent starts cannot each publish
// their own token and each answer with a value the other process (or the
// final file) disagrees with (audit 3 AUTH-07).
func loadOrCreateSetupToken(dir string) (setupTokenFile, error) {
	var file setupTokenFile
	err := withPrivateDirLock(dir, func() error {
		path := filepath.Join(dir, "setup-token.json")
		if raw, err := os.ReadFile(path); err == nil {
			var existing setupTokenFile
			if json.Unmarshal(raw, &existing) == nil && existing.Token != "" {
				if setupNow().Sub(existing.Created) < setupTokenLifetime {
					file = existing
					return nil
				}
				log.Println("setup: previous setup token expired — generating a new one")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		token, err := opaqueToken(24)
		if err != nil {
			return err
		}
		file = setupTokenFile{Token: token, Created: setupNow().UTC()}
		data, err := json.Marshal(file)
		if err != nil {
			return err
		}
		return writePrivateFile(path, string(data)+"\n")
	})
	if err != nil {
		return setupTokenFile{}, err
	}
	return file, nil
}

// withPrivateDirLock holds an exclusive advisory lock on a marker file in
// dir for the duration of fn. The lock is per-directory (not per-process),
// so concurrent starts sharing a data volume serialize; a crash releases it
// via the closed descriptor.
func withPrivateDirLock(dir string, fn func() error) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".private.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := flockExclusive(f); err != nil {
		return err
	}
	defer funlock(f)
	return fn()
}

// syncDirectory makes a rename durable: without the directory fsync, a
// crash can leave the OLD name in place even though the rename returned.
func syncDirectory(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func deleteSetupToken(dir string) {
	_ = os.Remove(filepath.Join(dir, "setup-token.json"))
}

// writePrivateFile writes mutable private state atomically: a same-directory
// temp file, fsynced, then renamed over the target. A crash mid-write leaves
// the previous contents intact instead of a truncated file (audit AUTH-08).
func writePrivateFile(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".lullmail-private-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return syncDirectory(dir)
}

// publishPrivateOnce publishes an immutable private file so that exactly one
// concurrent writer wins and every loser reads the winner's bytes: a temp
// file is fsynced and hard-linked into place, and link(2) fails with EEXIST
// if any other process got there first (audit AUTH-08). The directory is
// fsynced so the new name survives a crash. The returned bytes are whatever
// actually holds the path — the caller's candidate or the winner's.
func publishPrivateOnce(path string, data []byte) ([]byte, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, ".lullmail-secret-*")
	if err != nil {
		return nil, err
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return nil, err
	}
	if _, err := f.Write(data); err != nil {
		return nil, err
	}
	if err := f.Sync(); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := os.Link(name, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return os.ReadFile(path)
		}
		return nil, err
	}
	d, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return nil, err
	}
	return append([]byte(nil), data...), nil
}

func loadSetting(db *sql.DB, key string) (string, error) {
	var value string
	err := db.QueryRow(`SELECT value FROM app_settings WHERE key=$1`, key).Scan(&value)
	return value, err
}

// detectOrigin reconstructs the browser-visible origin: reverse proxies
// (Caddy, teploy ingress) front the app with HTTPS, so the forwarded proto
// wins over the socket's.
func detectOrigin(r *http.Request) string {
	proto := "http"
	if r.TLS != nil {
		proto = "https"
	}
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded == "http" || forwarded == "https" {
		proto = forwarded
	}
	host := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0])
	if host == "" {
		host = r.Host
	}
	if host == "" {
		return ""
	}
	return proto + "://" + host
}

// applyOrigin pins an origin into the config the same way PUBLIC_URL does.
func applyOrigin(cfg *Config, origin string) bool {
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		return false
	}
	cfg.PublicURL = origin
	cfg.RPID = parsed.Hostname()
	cfg.SecureAuth = parsed.Scheme == "https"
	return true
}

// setOriginForSetup pins the first setup request's origin and builds the
// WebAuthn instance it implies. Before the first credential exists nothing is
// secret, so re-detecting per ceremony start is safe; the origin that
// completes setup is the one that gets stored.
//
// The candidate is validated on a COPY of the config and published only
// after WebAuthn construction succeeds: applying the origin to the live
// config first left a half-updated configuration (new RP ID, stale
// instance) behind when construction failed (audit 3 AUTH-04).
func (a *App) setOriginForSetup(origin string) bool {
	a.waMu.Lock()
	defer a.waMu.Unlock()
	if origin == "" {
		return false
	}
	candidate := *a.cfg
	if !applyOrigin(&candidate, origin) {
		return false
	}
	wa, err := newWebAuthn(&candidate)
	if err != nil {
		return false
	}
	*a.cfg = candidate
	a.wa = wa
	return true
}

// webAuthn returns the instance under the same lock setOriginForSetup
// writes with, so a setup-time swap can never race a ceremony.
func (a *App) webAuthn() *webauthn.WebAuthn {
	a.waMu.Lock()
	defer a.waMu.Unlock()
	return a.wa
}

// setupTokenValid reports whether the auto-generated first-run token is
// still inside its lifetime. A zero Created means the token came from the
// environment (explicit config, no expiry) or does not exist.
func (a *App) setupTokenValid() bool {
	if a.cfg.APIToken == "" {
		return false
	}
	if a.setupTokenCreated.IsZero() {
		return true
	}
	return setupNow().Sub(a.setupTokenCreated) < setupTokenLifetime
}

// retireSetupToken drops the generated first-run token from disk and from
// the running process. An env-supplied LULL_TOKEN is left in memory but is
// inert once ownerConfigured is true.
func (a *App) retireSetupToken() {
	if a.cfg.DataDir != "" && !a.tokenFromEnv {
		deleteSetupToken(a.cfg.DataDir)
	}
	a.setupTokenCreated = time.Time{}
	if !a.tokenFromEnv {
		a.cfg.APIToken = ""
	}
}

// constantTimeBearer matches "Bearer <token>" against the configured value.
func constantTimeBearer(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte("Bearer "+want)) == 1
}

// prepareSetup runs at boot after the database is reachable: restore a
// stored origin when PUBLIC_URL was not pinned, and surface or mint the
// first-run token while no sign-in credential exists.
func (a *App) prepareSetup() {
	if !a.cfg.PublicURLSet {
		if stored, err := loadSetting(a.db, "public_url"); err == nil && stored != "" {
			if applyOrigin(a.cfg, stored) {
				log.Printf("setup: using origin %s (set PUBLIC_URL to override)", stored)
			}
		}
	}
	configured, err := a.ownerConfigured(context.Background())
	if err != nil {
		return
	}
	if configured {
		a.retireSetupToken()
		return
	}
	if a.cfg.APIToken == "" && a.cfg.DataDir != "" {
		file, err := loadOrCreateSetupToken(a.cfg.DataDir)
		if err != nil {
			log.Printf("setup: could not create the first-run token: %v", err)
			return
		}
		a.cfg.APIToken = file.Token
		a.setupTokenCreated = file.Created
	}
	if a.cfg.APIToken != "" && a.setupTokenValid() {
		expires := "24 hours"
		if !a.setupTokenCreated.IsZero() {
			remaining := setupTokenLifetime - setupNow().Sub(a.setupTokenCreated)
			expires = remaining.Round(time.Minute).String()
		}
		log.Printf("\n=======================================================================\n"+
			"  Lull Mail first-run setup\n"+
			"  Open the site in your browser and paste this one-time token:\n\n"+
			"      %s\n\n"+
			"  It expires in %s. Find it again later with:\n"+
			"      docker compose logs app   (or your platform's log viewer)\n"+
			"=======================================================================",
			a.cfg.APIToken, expires)
	}
}
