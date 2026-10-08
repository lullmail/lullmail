package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

func publicPushEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	if err == nil {
		if ip := net.ParseIP(u.Hostname()); ip != nil && !publicPushAddress(ip) {
			return false
		}
	}
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == "" && (u.Port() == "" || u.Port() == "443")
}
func publicPushAddress(ip net.IP) bool {
	for _, raw := range []string{"0.0.0.0/8", "2002::/16", "2001::/32", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "64:ff9b::/96", "64:ff9b:1::/48", "2001:db8::/32"} {
		_, blocked, _ := net.ParseCIDR(raw)
		if blocked.Contains(ip) {
			return false
		}
	}
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
}

type pushLookup func(context.Context, string) ([]net.IPAddr, error)
type pushDial func(context.Context, string, string) (net.Conn, error)

// These seams drive the production DNS validation and checked-IP dial path.
func pushHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return pushHTTPClientWithNetwork(net.DefaultResolver.LookupIPAddr, dialer.DialContext)
}
func pushHTTPClientWithNetwork(lookup pushLookup, dial pushDial) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if port != "443" {
			return nil, fmt.Errorf("push endpoint must use port 443")
		}
		ips, err := lookup(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("push endpoint has no addresses")
		}
		for _, ip := range ips {
			if !publicPushAddress(ip.IP) {
				return nil, fmt.Errorf("push endpoint resolves to a non-public address")
			}
		}
		var last error
		for _, ip := range ips {
			conn, err := dial(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		return nil, last
	}
	return &http.Client{Transport: pushEndpointTransport{transport}, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (a *App) pushHTTPClient() *http.Client {
	if a.pushClient != nil {
		return a.pushClient
	}
	return pushHTTPClient()
}

// Validate every request before DNS/TLS, including callers bypassing registration.
type pushEndpointTransport struct{ *http.Transport }

func (t pushEndpointTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !publicPushEndpoint(req.URL.String()) {
		return nil, fmt.Errorf("push destination is not permitted")
	}
	return t.Transport.RoundTrip(req)
}
