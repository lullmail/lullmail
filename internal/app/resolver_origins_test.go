package app

import (
	"context"
	"testing"

	"github.com/neutron-build/neutron/mail"
)

func TestJMAPAllowedOriginsNameFastmailDownloads(t *testing.T) {
	for host, want := range map[string]int{"api.fastmail.com": 1, "API.Fastmail.com": 1, "jmap.example.com": 0} {
		got, err := jmapAllowedOrigins(context.Background(), "acct", mail.Credential{Provider: mail.ProviderJMAP, Host: host})
		if err != nil || len(got) != want {
			t.Errorf("%s: origins=%v err=%v, want %d", host, got, err, want)
		}
	}
}
