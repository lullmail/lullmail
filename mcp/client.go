package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// client is a thin authenticated wrapper around email-soft's HTTP API.
// It exists only here: email-soft knows nothing about MCP, and this binary
// knows nothing about any specific agent.
type client struct {
	base  *url.URL
	token string
	http  *http.Client
}

func newClient(baseURL, token string) (*client, error) {
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("EMAILSOFT_URL must be an absolute origin like https://mail.example.com")
	}
	if token == "" {
		return nil, fmt.Errorf("EMAILSOFT_AGENT_TOKEN is required (create one in email-soft under Settings -> Security -> Agent tokens)")
	}
	return &client{base: base, token: token, http: &http.Client{Timeout: 60 * time.Second}}, nil
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
	data, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 400 {
		apiErr := &apiError{Status: res.StatusCode, Title: res.Status}
		var problem struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
		}
		if json.Unmarshal(data, &problem) == nil {
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

func (c *client) del(ctx context.Context, path string) ([]byte, error) {
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}
