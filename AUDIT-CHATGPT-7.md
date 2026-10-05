# Lull Mail audit — post audit-6 (`main`)

Audit target: `lullmail/lullmail` at `02b1c8a98a12c6413a62b95210355126ec2525f4` (2026-10-04).

Scope:
- Read `AUDIT_OPEN.md` first.
- Treated audit-6 findings already fixed by `02b1c8a` as closed unless the fix itself introduced a regression.
- Did **not** re-report standing deferrals such as DATA-04, GRAPH-02/PROVIDER-01, WEB-02, WEB-03, OPS-05/06, OPS-10, OFF-02/DRAFT-02, etc.
- Focused especially on the code added or changed by audit-6: request-method inference, offline send behavior, Graph large-attachment upload sessions, health endpoints, and shared provider HTTP transport.

## Executive summary

Two new defects are high-confidence and both sit in the newly added Microsoft Graph large-attachment path.

1. **AUDIT7-F01 — High — Graph upload-session URL is not origin-validated and redirects are followed.**
   `uploadUrl` is a bearer-equivalent pre-authenticated URL returned by Graph. The implementation feeds it directly to the shared `providerHTTP` client. That client has the standard redirect policy and no upload-session host validation. The audit-6 fix correctly avoids adding the OAuth `Authorization` header, but it still accepts an arbitrary URL supplied by the provider response and will follow redirects. The opaque upload URL therefore needs a dedicated trust boundary.

2. **AUDIT7-F02 — High — ambiguous pre-send Graph transport failures are incorrectly marked `NotSubmitted`.**
   `graphDraftSend` and `graphReplySend` wrap several network errors in `mail.NotSubmittedError` merely because they occur before the explicit `/send` call. That is too broad. A transport error during PATCH/attachment upload can occur after the remote operation committed but before the response reached Lull Mail. The message itself has not been submitted to recipients yet, but the outbox's `NotSubmitted` classification means "provably safe outcome" and drives user-facing retry semantics. The code should distinguish deterministic provider refusal from an unknown transport outcome and, where possible, reconcile the draft state instead of claiming proof it did not happen.

No additional new defect in the audit-6 request-method, offline-send, health-split, or connection-pooling changes was strong enough to report after accounting for the existing register.

---

## AUDIT7-F01 — High — validate and pin Graph `uploadUrl`; do not follow redirects

### Evidence

`graphUploadAttachment` accepts the provider-returned URL verbatim:

```go
if session.UploadURL == "" {
    return fmt.Errorf("graph upload session for %q returned no upload url", att.Filename)
}

req, err := http.NewRequestWithContext(
    ctx,
    http.MethodPut,
    session.UploadURL,
    bytes.NewReader(att.Data[offset:end]),
)
...
res, err := providerHTTP.Do(req)
```

The shared client added by audit-6 is:

```go
var providerHTTP = &http.Client{Transport: &http.Transport{
    Proxy:                 http.ProxyFromEnvironment,
    MaxIdleConns:          100,
    MaxIdleConnsPerHost:   10,
    IdleConnTimeout:       90 * time.Second,
    TLSHandshakeTimeout:   10 * time.Second,
    ResponseHeaderTimeout: 30 * time.Second,
}}
```

There is no `CheckRedirect`, and there is no validation of the returned upload-session URL before the PUT.

Microsoft documents Outlook large-attachment `uploadUrl` as an opaque pre-authenticated URL for the `https://outlook.office.com` domain and explicitly says not to customize it. The URL itself contains authorization material, so it should be treated like a credential, not as an ordinary provider-controlled continuation.

### Why this matters

The audit-6 change correctly removed the OAuth bearer header from the PUT, preventing one obvious credential leak. But the opaque URL itself is authorization.

A compromised/broken upstream response, proxy, or unexpected continuation could return:

```text
https://attacker.example/upload?authtoken=...
```

or redirect a legitimate URL to another origin.

Because the client follows redirects by default, Lull Mail may send attachment bytes to a different destination. The query token embedded in the URL can also be exposed to the redirect destination through the redirected URL chain depending on the redirect target.

This is narrower than standing OPS-09. OPS-09 concerns configurable provider hosts and general egress policy. This finding is about a **fixed Microsoft Graph contract introduced in audit-6**: the Outlook attachment-session URL has a documented origin and can be validated without changing the product's private-mail-server policy.

### Fix

Use a dedicated upload-session client/request validator, not the generic provider client policy.

```go
var graphUploadHTTP = &http.Client{
    Transport: providerHTTP.Transport,
    CheckRedirect: func(req *http.Request, via []*http.Request) error {
        return http.ErrUseLastResponse
    },
}
```

Validate the URL before any request:

```go
func validateGraphUploadURL(raw string) (*url.URL, error) {
    u, err := url.Parse(raw)
    if err != nil {
        return nil, fmt.Errorf("invalid Graph upload URL: %w", err)
    }
    if u.Scheme != "https" {
        return nil, errors.New("Graph upload URL must use https")
    }
    if !strings.EqualFold(u.Hostname(), "outlook.office.com") {
        return nil, fmt.Errorf("unexpected Graph upload host %q", u.Hostname())
    }
    if u.User != nil {
        return nil, errors.New("Graph upload URL must not contain userinfo")
    }
    return u, nil
}
```

Then:

```go
uploadURL, err := validateGraphUploadURL(session.UploadURL)
if err != nil {
    return err
}

req, err := http.NewRequestWithContext(
    ctx,
    http.MethodPut,
    uploadURL.String(),
    bytes.NewReader(att.Data[offset:end]),
)
if err != nil {
    return err
}
req.Header.Set("Content-Type", "application/octet-stream")
req.Header.Set("Content-Range", ...)
req.ContentLength = int64(end - offset)

res, err := graphUploadHTTP.Do(req)
```

If Microsoft documents additional Outlook upload hostnames for supported clouds later, represent them as an explicit provider/cloud-specific allowlist. Do not accept arbitrary hosts.

### Regression tests

Add tests that prove:

- `https://outlook.office.com/...` succeeds.
- `http://outlook.office.com/...` is rejected before a request.
- `https://evil.example/...` is rejected before a request.
- userinfo URLs are rejected.
- a 302/307/308 response from the Outlook upload host is **not followed**.
- no `Authorization` header is sent on upload PUTs.
- the required `Content-Type: application/octet-stream`, `Content-Length`, and `Content-Range` headers are present.

---

## AUDIT7-F02 — High — only deterministic pre-acceptance failures may become `NotSubmitted`

### Evidence

The durable outbox treats `mail.NotSubmittedError` as a strong guarantee:

```go
if err = deliver(ctx, &payload.Outgoing); err != nil {
    if mail.IsNotSubmitted(err) {
        state, code = "failed", "not_submitted"
        return
    }
    state, code = "ambiguous", "submission_outcome_unknown"
    return
}
```

The audit-6 Graph implementation wraps broad classes of errors in `NotSubmittedError`:

```go
if err := graphCall(ctx, cred, http.MethodPost, "/me/messages", map[string]any{}, &draft); err != nil {
    return &mail.NotSubmittedError{Err: err}
}
...
if err := graphCall(ctx, cred, http.MethodPatch, draftPath, patch, nil); err != nil {
    return cleanup(&mail.NotSubmittedError{Err: err})
}
...
if err := graphCall(ctx, cred, http.MethodPost, draftPath+"/attachments", file, nil); err != nil {
    return cleanup(&mail.NotSubmittedError{Err: err})
}
for _, att := range oversize {
    if err := graphUploadAttachment(ctx, cred, draftPath, att); err != nil {
        return cleanup(&mail.NotSubmittedError{Err: err})
    }
}
```

`graphReplySend` similarly wraps upload failures as terminal and returns raw `graphCall` transport errors for earlier draft operations.

The important distinction is not simply "before `/send`". `graphCall` can fail because:
- the server explicitly returned a 4xx/5xx;
- DNS/TLS/connect failed before a request was sent;
- the connection dropped after the request body was transmitted;
- the server committed the PATCH/attachment write but the response was lost.

Only some of those are provably non-effects.

### Why this matters

For the final recipient submission, the draft has not been sent until `/send`, so these failures do not mean "recipient may already have the message."

But Lull Mail's durable outbox uses `NotSubmittedError` as a stronger semantic: it records a terminal `failed/not_submitted` outcome and presents it as safe rather than uncertain.

That can be wrong for Graph draft mutations.

Example:

1. Lull Mail successfully creates draft `D`.
2. It PUTs the final chunk of a large attachment.
3. Outlook stores the attachment.
4. The response is lost.
5. `providerHTTP.Do` returns a transport error.
6. `graphUploadAttachment` returns the error.
7. `graphDraftSend` wraps it in `NotSubmittedError`.
8. cleanup tries to DELETE the draft; that DELETE can also fail or be ambiguous.
9. outbox records `failed/not_submitted`.

The user is now told a deterministic story even though the remote mailbox may contain a modified leftover draft and cleanup may not have succeeded.

The same category exists for PATCH and small-attachment POST calls.

### Fix

Separate these concepts:

- **recipient submission certainty** — whether `/send` may have been accepted;
- **draft mutation certainty** — whether a pre-send remote draft operation may have committed;
- **local retry safety** — whether the outbox may automatically retry.

For the current outbox, the safest contained fix is:

1. Only wrap **deterministic local failures** and **explicit provider refusals** in `NotSubmittedError`.
2. Leave transport failures unwrapped unless the transport can prove no request bytes were accepted.
3. Attempt cleanup, but never let cleanup failure convert an uncertain operation into a deterministic one.
4. Persist a specific pre-send ambiguity code if desired, e.g. `draft_state_unknown`, while still preventing automatic resend.

A small typed Graph error layer would make this precise:

```go
type graphHTTPError struct {
    Status int
    Err    error
}

func graphCall(...) error {
    ...
    res, err := providerHTTP.Do(req)
    if err != nil {
        return err // transport uncertainty
    }
    if res.StatusCode < 200 || res.StatusCode >= 300 {
        return &graphHTTPError{
            Status: res.StatusCode,
            Err: fmt.Errorf(...),
        }
    }
    ...
}
```

Then:

```go
func graphDefinitelyRejected(err error) bool {
    var ge *graphHTTPError
    if !errors.As(err, &ge) {
        return false
    }
    // Explicit response proves the particular HTTP operation was refused.
    return ge.Status >= 400 && ge.Status < 500
}
```

For draft shaping:

```go
if err := graphCall(...); err != nil {
    cleanupErr := cleanupDraft(...)
    if graphDefinitelyRejected(err) {
        return &mail.NotSubmittedError{Err: errors.Join(err, cleanupErr)}
    }
    return errors.Join(err, cleanupErr)
}
```

For upload chunks, explicit 4xx can be treated as a deterministic refusal of that chunk. A lost response remains uncertain.

For the final `/send`, keep current ambiguity semantics: transport errors and 5xx remain ambiguous; explicit client refusal can be `NotSubmitted`.

### Stronger improvement

After an ambiguous draft mutation, reconcile before classifying:

- GET the draft;
- inspect attachment metadata / size;
- use the upload session's `nextExpectedRanges` where available;
- retry or resume only when the server state proves what bytes are missing;
- if draft state cannot be established, retain an explicit recoverable ambiguous state rather than claiming `not_submitted`.

This is especially appropriate because Graph upload sessions are designed to support resumption.

### Regression tests

Add table-driven tests for PATCH, small attachment POST, createUploadSession, chunk PUT, cleanup DELETE, and final send:

- explicit 400 -> deterministic `NotSubmitted` where appropriate;
- explicit 429 -> deterministic refusal of that operation;
- explicit 500 -> not `NotSubmitted`;
- connection failure before dial -> may be classified local-not-submitted only if the transport exposes proof;
- connection loss after request write -> **must not** be `NotSubmitted`;
- final chunk stored but response lost -> must not produce `failed/not_submitted`;
- cleanup DELETE failure must be retained/logged and must not overwrite the primary uncertainty classification;
- final `/send` response loss stays `ambiguous`.

---

## Additional improvement — Medium — honor upload-session protocol headers and resumability

This is best fixed alongside F01/F02 rather than tracked separately.

Microsoft's Outlook large-attachment protocol requires `Content-Length`, `Content-Range`, and `Content-Type: application/octet-stream` for each PUT, and exposes `nextExpectedRanges` so interrupted uploads can resume.

The current request sets only `Content-Range` explicitly. Go will normally infer `Content-Length` from the `bytes.Reader`, but relying on implicit behavior makes the protocol contract less clear and the code does not set `Content-Type`.

Set both explicitly and parse upload responses enough to validate/resume the expected range.

Recommended response model:

```go
type graphUploadSession struct {
    UploadURL          string   `json:"uploadUrl"`
    ExpirationDateTime string   `json:"expirationDateTime"`
    NextExpectedRanges []string `json:"nextExpectedRanges"`
}
```

For intermediate success, verify the returned next range advances monotonically. If a response is lost, query/resume the session instead of assuming the last chunk failed.

This also makes F02 easier to solve correctly.

---

## Patch sketch

A compact implementation shape:

```go
var graphUploadHTTP = &http.Client{
    Transport: providerHTTP.Transport,
    CheckRedirect: func(req *http.Request, via []*http.Request) error {
        return http.ErrUseLastResponse
    },
}

func validateGraphUploadURL(raw string) (*url.URL, error) {
    u, err := url.Parse(raw)
    if err != nil {
        return nil, err
    }
    if u.Scheme != "https" ||
        !strings.EqualFold(u.Hostname(), "outlook.office.com") ||
        u.User != nil {
        return nil, errors.New("untrusted Graph upload URL")
    }
    return u, nil
}

type graphHTTPError struct {
    Status int
    Err error
}
func (e *graphHTTPError) Error() string { return e.Err.Error() }
func (e *graphHTTPError) Unwrap() error { return e.Err }

func graphDefinitelyRejected(err error) bool {
    var ge *graphHTTPError
    return errors.As(err, &ge) && ge.Status >= 400 && ge.Status < 500
}
```

Then make pre-send callers use `graphDefinitelyRejected(err)` rather than wrapping every error in `NotSubmittedError`.

---

## Verification notes

I inspected the audit-6 commit delta from `c0a5f70` to `02b1c8a`, including:
- `dashboard/src/app/lib/api.ts` and its new tests;
- `sendqueue.go` audit-6 offline-send change;
- `cmd_serve.go` health split and tests;
- `oauth.go` / `oauth_test.go` provider client and Graph upload-session implementation;
- `teploy.yml`;
- the standing findings in `AUDIT_OPEN.md`.

I did not re-report:
- unbounded thread API (`DATA-04`);
- Graph immutable-ID migration (`GRAPH-02/PROVIDER-01`);
- raw-HTML preprocessing (`WEB-02`);
- `cid:` pipeline (`WEB-03`);
- public-port/trusted-proxy deployment policy (`OPS-05/06`);
- key rotation / agent-token policy (`OPS-10`);
- native-browser offline/draft validation (`OFF-02/DRAFT-02`).

Those remain standing decisions in the repository and were explicitly excluded by the requested audit scope.

## Recommended fix order

1. **F01 first:** lock down `uploadUrl` origin and redirects; this is a small security boundary patch.
2. **F02 next:** correct Graph error typing so the durable outbox never asserts more certainty than the transport can prove.
3. Add upload protocol/resume tests while touching the same code.
4. Run `go test -race -count=1 ./...` plus the PostgreSQL integration suite and add a loopback HTTP fault server that can drop connections after reading request bodies to exercise the ambiguous Graph cases.
