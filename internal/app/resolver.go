package app

// Resolver for our accounts: identical to the engine's dialer except that
// loopback IMAP hosts dial plaintext. imap.Config already refuses plaintext
// off-loopback, so this cannot weaken a real account; it exists so local dev
// and self-hosted test servers (GreenMail, Stalwart on :143) work without
// TLS ceremony. Non-loopback delegates to the stock dialer untouched.

import (
	"context"
	"net"
	"strings"
	"time"

	"github.com/neutron-build/neutron/mail"
	"github.com/neutron-build/neutron/mail/dialer"
	"github.com/neutron-build/neutron/mail/imap"
)

func newResolver() mail.Resolver { return newResolverWithRefresh(nil) }

// newResolverWithRefresh lets Gmail and Graph reads refresh a rejected token
// once and continue, so a long import outlives its hour-long access token.
func newResolverWithRefresh(refresh dialer.Refresh) mail.Resolver {
	stock := dialer.NewWithPolicy(refresh, dialer.Policy{JMAPAllowedOrigins: jmapAllowedOrigins, GraphRefresh: refresh})
	return func(ctx context.Context, acct mail.AccountID, cred mail.Credential) (mail.Adapter, func(), error) {
		if cred.Provider == mail.ProviderIMAP && isLoopbackHost(cred.Host) {
			user := strings.TrimSpace(cred.Username)
			if user == "" {
				user = cred.Email
			}
			conn, err := imap.Dial(ctx, imap.Config{
				Host:      cred.Host,
				Port:      cred.Port,
				Username:  user,
				Password:  cred.Password,
				Timeout:   30 * time.Second,
				Plaintext: true,
			})
			if err != nil {
				return nil, nil, err
			}
			ad := imap.New(conn)
			return ad, func() { _ = ad.Close() }, nil
		}
		return stock(ctx, acct, cred)
	}
}

// jmapExtraOrigins lists, per JMAP discovery host, the provider's credential
// destinations outside that origin. The engine no longer lets a session
// advertise them itself; a provider absent here works if it keeps its API
// and downloads on the discovery origin.
var jmapExtraOrigins = map[string][]string{
	"api.fastmail.com": {"https://www.fastmailusercontent.com"},
}

func jmapAllowedOrigins(_ context.Context, _ mail.AccountID, cred mail.Credential) ([]string, error) {
	return jmapExtraOrigins[strings.ToLower(cred.Host)], nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
