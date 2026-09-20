package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// client is a thin authenticated wrapper around Lull Mail's HTTP API.
// It exists only here: Lull Mail knows nothing about MCP, and this binary
// knows nothing about any specific agent.
type client struct {
	base  *url.URL
	token string
	http  *http.Client
}

const maxResponseBytes = 8 << 20

func newClient(baseURL, token string) (*client, error) {
	base, err := validateOrigin(baseURL)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, fmt.Errorf("LULL_AGENT_TOKEN is required (create one in Lull Mail under Settings -> Security -> Agent tokens)")
	}
	httpClient := &http.Client{Timeout: 60 * time.Second}
	// Every request carries the agent bearer token; redirects stay inside
	// the configured origin — a proxy redirect to some other host or a
	// scheme downgrade must not silently forward it (audit 5 MCP-01).
	httpClient.CheckRedirect = sameOriginRedirect(base)
	return &client{base: base, token: token, http: httpClient}, nil
}

// validateOrigin enforces the credential-transport policy for the
// configured LULL_URL (audit 5 MCP-01): a remote origin must be HTTPS —
// a mistaken http:// configuration would send the agent token in
// cleartext — with no userinfo, query, or fragment. Plain HTTP is
// allowed only on literal loopback, where local development servers and
// test fakes live.
func validateOrigin(raw string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || base.Scheme == "" || base.Host == "" || base.User != nil ||
		base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("LULL_URL must be a clean absolute origin like https://lullmail.com")
	}
	if base.Scheme != "https" {
		host := base.Hostname()
		ip := net.ParseIP(host)
		loopback := strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
		if !(base.Scheme == "http" && loopback) {
			return nil, fmt.Errorf("remote LULL_URL must use HTTPS (plain HTTP is allowed only on loopback)")
		}
	}
	return base, nil
}

// sameOriginRedirect rejects cross-origin or downgrading redirects before
// any credential-bearing request reaches the new destination.
func sameOriginRedirect(base *url.URL) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many API redirects")
		}
		if !strings.EqualFold(req.URL.Scheme, base.Scheme) || !strings.EqualFold(req.URL.Host, base.Host) {
			return errors.New("cross-origin API redirect rejected")
		}
		return nil
	}
}

// apiError carries the server's problem+json detail, so tool failures read
// as instructions ("address, password and host are required"), not statuses.
type apiError struct {
	Status int
	Title  string
	Detail string
}

func (e *apiError) Error() string {
	if e.Detail != "" {
		return e.Title + ": " + e.Detail
	}
	return fmt.Sprintf("%s (HTTP %d)", e.Title, e.Status)
}

// safeSegment rejects path metacharacters before interpolation. Identifiers
// in this API are UUIDs and slugs; JoinPath would split anything else on "/"
// or "%" long before escaping could help, so refuse instead of rewriting.
func safeSegment(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("empty identifier")
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == '~':
		default:
			return "", fmt.Errorf("invalid identifier %q", s)
		}
	}
	return s, nil
}

func (c *client) do(ctx context.Context, method, path string, body any, query url.Values) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	u := c.base.JoinPath("/api", path)
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxResponseBytes {
		return nil, fmt.Errorf("response exceeds %d MiB limit", maxResponseBytes>>20)
	}
	if res.StatusCode >= 400 {
		apiErr := &apiError{Status: res.StatusCode, Title: res.Status}
		var problem struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
		}
		// A body may be valid JSON without being problem+json; only adopt
		// its fields when the title is actually there, or the status line
		// fallback gets overwritten with emptiness.
		if json.Unmarshal(data, &problem) == nil && problem.Title != "" {
			apiErr.Title, apiErr.Detail = problem.Title, problem.Detail
		}
		return nil, apiErr
	}
	return data, nil
}

func (c *client) get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	return c.do(ctx, http.MethodGet, path, nil, query)
}

func (c *client) post(ctx context.Context, path string, body any) ([]byte, error) {
	return c.do(ctx, http.MethodPost, path, body, nil)
}

func (c *client) postQuery(ctx context.Context, path string, body any, query url.Values) ([]byte, error) {
	return c.do(ctx, http.MethodPost, path, body, query)
}

func (c *client) del(ctx context.Context, path string) ([]byte, error) {
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}
