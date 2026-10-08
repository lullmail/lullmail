package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPushEgressRequiresPublicHTTPS(t *testing.T) {
	for _, raw := range []string{"http://push.example/path", "https://user:secret@push.example/path", "https://push.example:8443/path", "https://push.example/path#fragment", "https://127.0.0.1/path", "https://[::1]/path"} {
		if publicPushEndpoint(raw) {
			t.Fatalf("accepted %s", raw)
		}
	}
	if !publicPushEndpoint("https://push.example/path?token=test") {
		t.Fatal("public subscription refused")
	}
	for _, raw := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "::1", "fc00::1", "0.0.0.0", "0.1.2.3", "::ffff:0.1.2.3", "2002::1", "2001::1", "224.0.0.1", "100.100.100.200", "64:ff9b::7f00:1"} {
		if publicPushAddress(net.ParseIP(raw)) {
			t.Fatalf("accepted %s", raw)
		}
	}
	if !publicPushAddress(net.ParseIP("8.8.8.8")) {
		t.Fatal("public address refused")
	}
}

func TestPushCheckedDialRejectsEveryMixedDNSAnswer(t *testing.T) {
	for _, bad := range []string{"0.1.2.3", "10.1.2.3", "::ffff:0.1.2.3", "2002:0808:0808::1", "2001:0:1234::1", "169.254.169.254"} {
		for _, reversed := range []bool{false, true} {
			ips := []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP(bad)}}
			if reversed {
				ips[0], ips[1] = ips[1], ips[0]
			}
			calls := 0
			client := pushHTTPClientWithNetwork(func(context.Context, string) ([]net.IPAddr, error) { return ips, nil }, func(context.Context, string, string) (net.Conn, error) {
				calls++
				return nil, errors.New("must not dial")
			})
			transport := client.Transport.(pushEndpointTransport).Transport
			if _, err := transport.DialContext(context.Background(), "tcp", "push.example:443"); err == nil || calls != 0 {
				t.Fatalf("mixed answer %s reversed=%v dial=%d err=%v", bad, reversed, calls, err)
			}
		}
	}
}

func TestPushProductionTransportPinsIPIgnoresProxyAndRefusesRedirect(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:1")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "push.example" {
			t.Errorf("Host lost: %s", r.Host)
		}
		w.Header().Set("Location", "https://127.0.0.1/private")
		w.WriteHeader(307)
	}))
	defer server.Close()
	var addresses []string
	lookups := 0
	client := pushHTTPClientWithNetwork(func(ctx context.Context, host string) ([]net.IPAddr, error) {
		lookups++
		if host != "push.example" {
			t.Errorf("unexpected DNS %s", host)
		}
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}, func(ctx context.Context, network, address string) (net.Conn, error) {
		addresses = append(addresses, address)
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String()) // local fixture only
	})
	transport := client.Transport.(pushEndpointTransport).Transport
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // fixture certificate; no external connection
	defer transport.CloseIdleConnections()
	if transport.Proxy != nil {
		t.Fatal("environment proxy enabled")
	}
	response, err := client.Get("https://push.example/subscription")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 307 || lookups != 1 || len(addresses) != 1 || addresses[0] != "8.8.8.8:443" {
		t.Fatalf("status=%d DNS=%d dial=%v", response.StatusCode, lookups, addresses)
	}
	for _, raw := range []string{"http://push.example/x", "https://0.1.2.3/x", "https://[2002::1]/x", "https://push.example:444/x"} {
		if _, err := client.Get(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if lookups != 1 || len(addresses) != 1 {
		t.Fatal("rejected request reached DNS/dial")
	}
}
