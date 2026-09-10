package main

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

func TestPasswordHashVerifyRoundtrip(t *testing.T) {
	encoded, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=") {
		t.Fatalf("hash is not PHC-encoded argon2id: %q", encoded[:20])
	}
	ok, err := verifyPassword(encoded, "correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("roundtrip failed: ok=%v err=%v", ok, err)
	}
	ok, err = verifyPassword(encoded, "Correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("wrong password accepted")
	}
	// Fresh salt every time: two hashes of one password must differ.
	again, _ := hashPassword("correct horse battery staple")
	if again == encoded {
		t.Fatal("salt was reused across hashes")
	}
}

func TestVerifyPasswordParsesParamsFromStoredString(t *testing.T) {
	// A hash written with different parameters must still verify: the
	// parameters travel in the string, not in today's constants.
	salt := make([]byte, argonSaltLen)
	key := argon2.IDKey([]byte("secret"), salt, 2, 32*1024, 2, argonKeyLen)
	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, 32*1024, 2, 2,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key))
	ok, err := verifyPassword(encoded, "secret")
	if err != nil || !ok {
		t.Fatalf("hash with non-default params failed verify: ok=%v err=%v", ok, err)
	}
}

func TestVerifyPasswordRejectsMalformedAndHostileStrings(t *testing.T) {
	bad := []string{
		"",
		"not-a-hash",
		"$argon2i$v=19$m=65536,t=3,p=4$ c2FsdA $a2V5", // wrong variant + spaces
		"$argon2id$v=18$m=65536,t=3,p=4$c2FsdA$a2V5",  // unsupported version
		"$argon2id$v=19$m=abc,t=3,p=4$c2FsdA$a2V5",    // unparsable params
		// Hostile parameters read back from the string: verify must refuse,
		// not allocate.
		"$argon2id$v=19$m=99999999,t=3,p=4$" + base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef")) + "$a2V5",
		"$argon2id$v=19$m=65536,t=99999,p=4$" + base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef")) + "$a2V5",
		"$argon2id$v=19$m=65536,t=3,p=999$" + base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef")) + "$a2V5",
		"$argon2id$v=19$m=65536,t=3,p=4$!!not-base64!!$a2V5",
	}
	for _, s := range bad {
		ok, err := verifyPassword(s, "anything")
		if ok || err == nil {
			t.Fatalf("verifyPassword(%q) = ok=%v err=%v, want ok=false err!=nil", s, ok, err)
		}
	}
}

func TestValidPasswordLengthBounds(t *testing.T) {
	if validPasswordLength("short7") {
		t.Fatal("7-byte password accepted")
	}
	if !validPasswordLength("12345678") {
		t.Fatal("8-byte password rejected")
	}
	if validPasswordLength(strings.Repeat("x", passwordMaxLen+1)) {
		t.Fatal("over-length password accepted")
	}
}

func TestPublicExposureClassification(t *testing.T) {
	private := []string{
		"",
		"http://localhost:18081",
		"http://127.0.0.1:8080",
		"http://[::1]:8080",
		"http://192.168.1.20",
		"http://10.0.0.5:8080",
		"http://172.16.4.4",
		"https://mail.tail1234.ts.net",
		"http://100.108.123.49:18081",
	}
	for _, origin := range private {
		if publicExposure(origin) {
			t.Fatalf("publicExposure(%q) = true, want false", origin)
		}
	}
	public := []string{
		"https://mail.example.com",
		"http://203.0.113.9:8080",
		"https://mail.lullmail.com",
	}
	for _, origin := range public {
		if !publicExposure(origin) {
			t.Fatalf("publicExposure(%q) = false, want true", origin)
		}
	}
}

func TestPasswordLockoutLocksAtThresholdAndExpires(t *testing.T) {
	a := &App{pwFails: map[string]passwordFails{}}
	const uid = "u1"
	for i := 1; i < passwordLockThreshold; i++ {
		if started := a.recordPasswordFailure(uid); started != 0 {
			t.Fatalf("failure %d started a lock early", i)
		}
		if rem := a.passwordLockRemaining(uid); rem != 0 {
			t.Fatalf("failure %d reported a lock", i)
		}
	}
	if started := a.recordPasswordFailure(uid); started != passwordLockDuration {
		t.Fatalf("threshold failure returned %v, want %v", started, passwordLockDuration)
	}
	remaining := a.passwordLockRemaining(uid)
	if remaining <= 0 || remaining > passwordLockDuration {
		t.Fatalf("lock remaining = %v", remaining)
	}
	// A successful sign-in clears the window entirely.
	a.clearPasswordFailures(uid)
	if rem := a.passwordLockRemaining(uid); rem != 0 {
		t.Fatalf("cleared account still locked for %v", rem)
	}
	// An expired lock is dropped on read, so the next failure starts a fresh
	// window rather than stacking toward a second lockout.
	a.pwFails[uid] = passwordFails{Count: passwordLockThreshold, LockedUntil: time.Now().Add(-time.Second)}
	if rem := a.passwordLockRemaining(uid); rem != 0 {
		t.Fatalf("expired lock still reported %v", rem)
	}
	if started := a.recordPasswordFailure(uid); started != 0 {
		t.Fatal("failure after an expired lock immediately locked again")
	}
}
