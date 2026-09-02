package main

// Zero-config first run, on the Jenkins/GitLab model: the operator proves
// host access once by copying a one-time token from the container logs,
// then finishes everything else in the web setup flow. SECRET_KEY and the
// setup token are generated locally when their env vars are absent; the
// browser origin is detected from the first setup request and pinned at
// completion. Env vars always win for operators who want explicit config.

import (
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
	if err := writePrivateFile(path, key+"\n"); err != nil {
		return err
	}
	log.Printf("setup: generated SECRET_KEY at %s (set the env var to pin your own)", path)
	cfg.SecretKey = key
	return nil
}

type setupTokenFile struct {
	Token   string    `json:"token"`
	Created time.Time `json:"created"`
}

// loadOrCreateSetupToken returns the current first-run token, reusing a
// still-fresh file so restarts do not invalidate a token the operator has
// not used yet, and regenerating once the 24h window lapses.
func loadOrCreateSetupToken(dir string) (setupTokenFile, error) {
	path := filepath.Join(dir, "setup-token.json")
	if raw, err := os.ReadFile(path); err == nil {
		var file setupTokenFile
		if json.Unmarshal(raw, &file) == nil && file.Token != "" {
			if setupNow().Sub(file.Created) < setupTokenLifetime {
				return file, nil
			}
			log.Println("setup: previous setup token expired — generating a new one")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return setupTokenFile{}, err
	}
	token, err := opaqueToken(24)
	if err != nil {
		return setupTokenFile{}, err
	}
	file := setupTokenFile{Token: token, Created: setupNow().UTC()}
	data, err := json.Marshal(file)
	if err != nil {
		return setupTokenFile{}, err
	}
	if err := writePrivateFile(path, string(data)+"\n"); err != nil {
		return setupTokenFile{}, err
	}
	return file, nil
}

func deleteSetupToken(dir string) {
	_ = os.Remove(filepath.Join(dir, "setup-token.json"))
}

func writePrivateFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

// The stored browser origin survives restarts so passkeys (bound to the
// WebAuthn RP ID) keep working without PUBLIC_URL being set.
func storeSetting(db *sql.DB, key, value string) error {
	_, err := db.Exec(`INSERT INTO app_settings (key, value) VALUES ($1,$2)
		ON CONFLICT (key) DO UPDATE SET value=excluded.value`, key, value)
	return err
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
// WebAuthn instance it implies. Before the first passkey exists nothing is
// secret, so re-detecting per ceremony start is safe; the origin that
// completes setup is the one that gets stored.
func (a *App) setOriginForSetup(origin string) bool {
	a.waMu.Lock()
	defer a.waMu.Unlock()
	if origin == "" || !applyOrigin(a.cfg, origin) {
		return false
	}
	wa, err := newWebAuthn(a.cfg)
	if err != nil {
		return false
	}
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

// constantTimeBearer matches "Bearer <token>" against the configured value.
func constantTimeBearer(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte("Bearer "+want)) == 1
}

// prepareSetup runs at boot after the database is reachable: restore a
// stored origin when PUBLIC_URL was not pinned, and surface or mint the
// first-run token while no passkey exists.
func (a *App) prepareSetup() {
	if !a.cfg.PublicURLSet {
		if stored, err := loadSetting(a.db, "public_url"); err == nil && stored != "" {
			if applyOrigin(a.cfg, stored) {
				log.Printf("setup: using origin %s (set PUBLIC_URL to override)", stored)
			}
		}
	}
	var credentials int
	if err := a.db.QueryRow(`SELECT count(*) FROM auth_credentials`).Scan(&credentials); err != nil {
		return
	}
	if credentials > 0 {
		if a.cfg.APIToken != "" && a.cfg.DataDir != "" && !a.tokenFromEnv {
			deleteSetupToken(a.cfg.DataDir)
		}
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
