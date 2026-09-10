package main

// Password credentials: argon2id, PHC-encoded, with parameters carried in the
// stored string so a future parameter change verifies old hashes without a
// migration. Failed sign-ins lock the account (not the host) — five failures
// in a row lock it for fifteen minutes.

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

var (
	dummyPHC     string
	dummyPHCOnce sync.Once
)

// burnPasswordVerify runs argon2 against a dummy hash so a miss costs the
// same as a hit. Without this, "no such user" returns in milliseconds and
// "wrong password" pays 64MiB.
func burnPasswordVerify(password string) {
	dummyPHCOnce.Do(func() {
		dummyPHC, _ = hashPassword("not-a-user-password")
	})
	if dummyPHC != "" {
		_, _ = verifyPassword(dummyPHC, password)
	}
}

// OWASP-recommended argon2id profile for interactive login (m=64MiB, t=3,
// p=4). Memory is in KiB, as the argon2 package takes it.
const (
	argonMemory  uint32 = 64 * 1024
	argonTime    uint32 = 3
	argonThreads uint8  = 4
	argonSaltLen        = 16
	argonKeyLen  uint32 = 32
)

// Upper bounds applied to parameters read BACK out of a stored string. The
// database is trusted, but "trusted" has been wrong before, and a tampered
// hash with m=2^30 would turn every sign-in into a memory-exhaustion vector.
const (
	maxArgonMemory        uint32 = 1 << 20 // 1 GiB in KiB
	maxArgonTime          uint32 = 1 << 12
	maxArgonThreads       uint8  = 64
	passwordMinLen               = 8
	passwordMaxLen               = 1024
	passwordLockThreshold        = 5
	passwordLockDuration         = 15 * time.Minute
)

func validPasswordLength(pw string) bool {
	return len(pw) >= passwordMinLen && len(pw) <= passwordMaxLen
}

func hashPassword(password string) (string, error) {
	salt, err := randomBytes(argonSaltLen)
	if err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errors.New("password hash is not an argon2id PHC string")
	}
	if parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return false, fmt.Errorf("password hash has unsupported version %q", parts[2])
	}
	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false, fmt.Errorf("password hash has unreadable parameters %q", parts[3])
	}
	if memory == 0 || memory > maxArgonMemory || iterations == 0 || iterations > maxArgonTime ||
		threads == 0 || threads > maxArgonThreads {
		return false, fmt.Errorf("password hash parameters out of range (m=%d t=%d p=%d)", memory, iterations, threads)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("password hash salt is not base64: %w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("password hash key is not base64: %w", err)
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// passwordFails is failed-attempt state. Keyed by the ident the client
// typed (lowercased email or name) so a 429 cannot reveal that a user or
// password exists. The per-host limiter still runs alongside it.
type passwordFails struct {
	Count       int
	LockedUntil time.Time
}

// passwordLockRemaining reports how much longer the account is locked. An
// expired lock is deleted on read, so the next failure starts a fresh window
// instead of stacking onto the count that already caused one lockout.
func (a *App) passwordLockRemaining(uid string) time.Duration {
	a.authMu.Lock()
	defer a.authMu.Unlock()
	if a.pwFails == nil {
		return 0
	}
	f, ok := a.pwFails[uid]
	if !ok || f.LockedUntil.IsZero() {
		return 0
	}
	remaining := time.Until(f.LockedUntil)
	if remaining > 0 {
		return remaining
	}
	delete(a.pwFails, uid)
	return 0
}

// recordPasswordFailure counts one failed attempt and locks the account at
// the threshold. Returns the lock duration when this failure caused a lock.
func (a *App) recordPasswordFailure(uid string) time.Duration {
	a.authMu.Lock()
	defer a.authMu.Unlock()
	if a.pwFails == nil {
		a.pwFails = map[string]passwordFails{}
	}
	f := a.pwFails[uid]
	f.Count++
	if f.Count >= passwordLockThreshold {
		a.pwFails[uid] = passwordFails{LockedUntil: time.Now().Add(passwordLockDuration)}
		return passwordLockDuration
	}
	a.pwFails[uid] = f
	return 0
}

func (a *App) clearPasswordFailures(uid string) {
	a.authMu.Lock()
	delete(a.pwFails, uid)
	a.authMu.Unlock()
}
