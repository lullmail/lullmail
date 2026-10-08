package main

import (
	"sync"
	"testing"
)

func TestSetupConfigPublicationIsRaceFree(t *testing.T) {
	a := &App{cfg: &Config{SecretKey: "test", APIToken: "test"}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				a.config()
				a.setupTokenValid()
				a.webAuthn()
			}
		}()
	}
	for n := 0; n < 100; n++ {
		if !a.setOriginForSetup("https://mail.example.test") {
			t.Fatal("origin refused")
		}
		a.setUserEmail("owner@example.test")
	}
	a.retireSetupToken()
	wg.Wait()
}

func TestEmptyBootstrapTokenNeverAuthenticates(t *testing.T) {
	if constantTimeBearer("Bearer ", "") {
		t.Fatal("empty token authenticated")
	}
}
