package main

// Resolver for our accounts: identical to the engine's dialer except that
// loopback IMAP hosts dial plaintext. imap.Config already refuses plaintext
// off-loopback, so this cannot weaken a real account; it exists so local dev
// and self-hosted test servers (GreenMail, Stalwart on :143) work without
// TLS ceremony. Non-loopback delegates to the stock dialer untouched.

import (
	"context"
	"net"
	"time"

	"github.com/neutron-build/neutron/mail"
	"github.com/neutron-build/neutron/mail/dialer"
	"github.com/neutron-build/neutron/mail/imap"
)

func newResolver() mail.Resolver {
	stock := dialer.New()
	return func(ctx context.Context, acct mail.AccountID, cred mail.Credential) (mail.Adapter, func(), error) {
		if cred.Provider == mail.ProviderIMAP && isLoopbackHost(cred.Host) {
			conn, err := imap.Dial(ctx, imap.Config{
				Host:      cred.Host,
				Port:      cred.Port,
				Username:  cred.Email,
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

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
