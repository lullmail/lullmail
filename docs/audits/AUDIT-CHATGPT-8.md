# Lull Mail follow-up audit — audit-6 / audit-7 delta

Audit target: `lullmail/lullmail` at `main` = `7cbe476de63da5a3de50017b2b040fe8e8469af4`.

Scope:
- Read `AUDIT_OPEN.md` first and treated it as the exclusion register.
- Focused on the code newly landed in:
  - `02b1c8a98a12c6413a62b95210355126ec2525f4` (`audit 6`)
  - `7cbe476de63da5a3de50017b2b040fe8e8469af4` (`audit 7`)
- Did **not** re-report standing deferrals such as DATA-04, GRAPH-02/PROVIDER-01, WEB-02/03, OPS-05/06, OPS-10, OFF-02/DRAFT-02, etc.
- Did **not** re-report findings already fixed in audit-6 or audit-7.
- Reviewed the surrounding send/outbox lifecycle because the new Graph code's correctness depends on its error classification and cancellation contracts.

## Executive summary

The audit-7 changes correctly close the two reported High findings:
- Graph upload-session URLs are now origin-pinned and redirects are not followed.
- transport/5xx failures are no longer blindly converted to `NotSubmittedError`.

The audit-6 request-method, readiness, and shared HTTP client changes also look substantially correct and have useful regression coverage.

I found **3 new bugs** in the new Graph upload/draft path:

1. **High — non-final upload chunks can be accepted without a continuation acknowledgement.**
2. **Medium — draft cleanup reuses a cancelled/expired submission context, so cleanup can be skipped exactly on timeout/cancellation failures.**
3. **Medium — Graph path segments built from provider IDs are not URL-escaped; a provider ID containing reserved path characters can address the wrong resource or fail unpredictably.**

I also include one low-risk improvement that is not itself a correctness bug:

4. **Low — upload URL validation should reject a non-default port and fragments, tightening the “one documented origin” contract.**

---

# F01 — High — upload loop advances after a non-final 2xx with no `nextExpectedRanges`

## Where

`oauth.go`, `graphUploadAttachment`.

Current logic:

```go
body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
res.Body.Close()
if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
    var next struct {
        NextExpectedRanges []string `json:"nextExpectedRanges"`
    }
    if err := json.Unmarshal([]byte(trimmed), &next); err != nil {
        return fmt.Errorf("graph upload %q response undecodable: %w", att.Filename, err)
    }
    if len(next.NextExpectedRanges) > 0 {
        if start, ok := uploadRangeStart(next.NextExpectedRanges[0]); !ok || start != end {
            return fmt.Errorf(...)
        }
    }
}
offset = end
```

## Why this is a bug

For a non-final upload chunk, the client needs positive evidence that the service accepted exactly the bytes through `end-1` before advancing its local offset.

Today these responses all advance `offset = end`:

```json
{}
```

```json
{"foo":"bar"}
```

or even an empty 2xx body.

That means a malformed, truncated, proxy-rewritten, or unexpected Graph response can cause Lull Mail to continue from a byte offset that the server never acknowledged.

Audit-7 added a useful mismatch check when `nextExpectedRanges` is present, but absence is still treated as success.

The final chunk is different: Graph may return the created attachment resource rather than another `nextExpectedRanges`, so the fix must distinguish final from non-final chunks.

## Impact

- An attachment upload can silently become incomplete or corrupted at the provider.
- The client can proceed to `/send` despite never obtaining a valid continuation acknowledgement for an intermediate chunk.
- Depending on provider behavior, the failure may surface late or as an opaque Graph error, making recovery classification less precise.

## Fix

Require a continuation acknowledgement on every **non-final** successful chunk.

Suggested patch:

```go
final := end == len(att.Data)

body, readErr := io.ReadAll(io.LimitReader(res.Body, 4096))
closeErr := res.Body.Close()
if readErr != nil {
    return fmt.Errorf("graph upload %q response read failed: %w", att.Filename, readErr)
}
if closeErr != nil {
    return fmt.Errorf("graph upload %q response close failed: %w", att.Filename, closeErr)
}

trimmed := strings.TrimSpace(string(body))
if trimmed == "" {
    if !final {
        return fmt.Errorf(
            "graph upload %q returned no continuation acknowledgement after byte %d",
            att.Filename, end,
        )
    }
    offset = end
    continue
}

var next struct {
    NextExpectedRanges []string `json:"nextExpectedRanges"`
}
if err := json.Unmarshal([]byte(trimmed), &next); err != nil {
    return fmt.Errorf("graph upload %q response undecodable: %w", att.Filename, err)
}

if final {
    // Final success may be attachment metadata and need not include ranges.
    if len(next.NextExpectedRanges) > 0 {
        if start, ok := uploadRangeStart(next.NextExpectedRanges[0]); !ok || start != end {
            return fmt.Errorf(
                "graph upload %q final response unexpectedly expects %q after byte %d",
                att.Filename, next.NextExpectedRanges[0], end,
            )
        }
    }
} else {
    if len(next.NextExpectedRanges) == 0 {
        return fmt.Errorf(
            "graph upload %q returned no nextExpectedRanges after byte %d",
            att.Filename, end,
        )
    }
    start, ok := uploadRangeStart(next.NextExpectedRanges[0])
    if !ok || start != end {
        return fmt.Errorf(
            "graph upload %q session expects %q after byte %d; stored state diverged",
            att.Filename, next.NextExpectedRanges[0], end,
        )
    }
}

offset = end
```

I would also treat more than one disjoint continuation range as unsupported unless the implementation explicitly handles them:

```go
if !final && len(next.NextExpectedRanges) != 1 {
    return fmt.Errorf(
        "graph upload %q returned unsupported continuation ranges %q",
        att.Filename,
        next.NextExpectedRanges,
    )
}
```

## Regression tests

Add cases for:

- intermediate `202 {}` → error, no next chunk sent;
- intermediate `202` with empty body → error;
- intermediate valid `nextExpectedRanges` → continue;
- intermediate wrong range → existing divergence error;
- final 2xx attachment metadata without `nextExpectedRanges` → success;
- final response that unexpectedly asks for more bytes → error.

---

# F02 — Medium — draft cleanup uses the failed submission context and can be cancelled before DELETE is attempted

## Where

Both `graphReplySend` and `graphDraftSend` define:

```go
cleanup := func(err error) error {
    if derr := graphCall(ctx, cred, http.MethodDelete, draftPath, nil, nil); derr != nil {
        return errors.Join(err, derr)
    }
    return err
}
```

The same `ctx` is used for:
- PATCH,
- attachment upload,
- final `/send`,
- and cleanup DELETE.

## Why this is a bug

The durable outbox wraps provider submission in a 60-second context:

```go
ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
```

If Graph stalls until that deadline, or the account/request context is cancelled during a provider operation, control returns with `ctx.Err() != nil`.

The cleanup function then immediately calls:

```go
http.NewRequestWithContext(ctx, ...)
```

with an already-cancelled context.

So the cleanup DELETE can fail locally without making a network attempt.

This is exactly the failure class where best-effort cleanup matters most: a partial draft may already exist remotely.

## Impact

- Orphaned Graph drafts accumulate after timeout/cancellation paths.
- The error is joined with a cleanup failure, but no cleanup request was actually attempted.
- This makes the new “delete leftover draft on failure” behavior less reliable than the code/comments imply.

## Important lifecycle constraint

Do **not** simply use `context.Background()`.

Account deletion cancellation must still win. The project deliberately binds provider work to the account lifecycle so disconnecting an account cancels its work.

The desired semantics are:

- ignore the submission attempt's own deadline/cancellation for a short cleanup grace;
- still abort cleanup when the account is being deleted / its lifetime is cancelled;
- keep cleanup tightly bounded.

## Fix

Give cleanup its own short context derived from the account lifetime, not the exhausted per-attempt timeout.

One clean way is to carry the account-lifetime context separately into the Graph send functions.

For example, introduce a helper:

```go
const graphCleanupTimeout = 5 * time.Second

func graphCleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
    // Preserve values but detach the provider-attempt deadline/cancellation.
    // The caller must pass a context whose cancellation still represents the
    // account lifetime if account deletion must abort cleanup.
    return context.WithTimeout(context.WithoutCancel(parent), graphCleanupTimeout)
}
```

But because `context.WithoutCancel` also removes account cancellation if both are represented by the same context, the better structural fix is to keep two contexts:

```go
func (a *App) graphDraftSend(
    attemptCtx context.Context,
    accountCtx context.Context,
    ...
) error
```

Then:

```go
cleanup := func(primary error) error {
    cleanupCtx, cancel := context.WithTimeout(accountCtx, 5*time.Second)
    defer cancel()

    if derr := graphCall(
        cleanupCtx,
        cred,
        http.MethodDelete,
        draftPath,
        nil,
        nil,
    ); derr != nil {
        return errors.Join(primary, derr)
    }
    return primary
}
```

If changing signatures is too invasive, expose the account lifetime from `deliveryFor` alongside the delivery closure and use that for cleanup.

## Regression tests

Add tests proving:

1. Provider operation blocks until the submission deadline expires.
2. `DELETE /me/messages/{draft}` is still attempted afterward on a fresh cleanup deadline.
3. Cleanup remains bounded.
4. If the account lifecycle is cancelled (account disconnect), cleanup is cancelled and does not outlive the account.

---

# F03 — Medium — provider message/draft IDs are interpolated into Graph paths without URL escaping

## Where

Examples in `oauth.go`:

```go
"/me/messages/" + native + "/createReply"
```

```go
draftPath := "/me/messages/" + draft.ID
```

Then `graphCall` builds:

```go
graphAPIBase + endpoint
```

without escaping path segments.

## Why this is a bug

Provider IDs are opaque identifiers. They should not be assumed safe as literal URL path text.

If an ID contains a reserved path character such as `/`, `?`, or `#`, interpolation changes URL structure rather than addressing that exact message.

Even if Microsoft's current common message ID formats often look URL-safe, code that treats an opaque provider identifier as a path segment should encode it by construction.

This is particularly relevant because the repository already tracks a future migration to Graph immutable IDs. The path-building helper should be safe independently of the current ID format.

## Impact

- Reply creation or draft mutation can hit the wrong URL or fail on valid provider IDs.
- Query/fragment characters can truncate the intended resource path.
- The bug is hard to diagnose because it depends on provider-generated identifiers.

## Fix

Escape each Graph resource ID as a path segment before interpolation.

Do not use `url.QueryEscape`; use path-segment escaping.

For example:

```go
func graphPathSegment(s string) string {
    return url.PathEscape(s)
}
```

Then:

```go
nativePath := graphPathSegment(native)

if err := graphCall(
    ctx,
    cred,
    http.MethodPost,
    "/me/messages/"+nativePath+"/createReply",
    map[string]any{},
    &draft,
); err != nil {
    ...
}
```

and:

```go
draftPath := "/me/messages/" + graphPathSegment(draft.ID)
```

However, Go's URL construction deserves one extra caution: when concatenating escaped strings into a URL and reparsing, `%2F` handling can be subtle because `URL.Path` and `RawPath` interact.

The more robust improvement is to stop building Graph URLs by ad-hoc string concatenation and centralize URL construction.

For example, make `graphCall` accept path segments rather than a preassembled URL path, or introduce a helper that constructs the `url.URL` while preserving escaped segments correctly.

A helper based on `url.URL.JoinPath` may be appropriate, but its exact behavior for encoded slashes should be locked down with tests before adopting it.

## Regression tests

Use synthetic IDs containing:

```text
abc/def
abc?def
abc#def
abc%2Fdef
abc def
```

Assert the outgoing request still addresses one `messages/{id}` resource and does not alter query or fragment structure.

---

# F04 — Low — upload URL validation should pin the complete origin, not only scheme + hostname

## Where

`validateGraphUploadURL`.

Current checks:

```go
if u.Scheme != "https" {
    ...
}
if !strings.EqualFold(u.Hostname(), graphUploadHost) {
    ...
}
if u.User != nil {
    ...
}
```

## Why improve it

The comment says this enforces “the documented origin”, but a URL like:

```text
https://outlook.office.com:444/...
```

passes because `Hostname()` ignores the port.

A fragment is also accepted even though it is never sent to the server and has no useful place in a provider-issued pre-authenticated upload URL.

This is not the same severity as the audit-7 redirect bug, but the code can cheaply make its stated trust boundary exact.

## Fix

Require:

- scheme exactly `https`;
- hostname exactly `outlook.office.com` case-insensitively;
- port empty or `443`;
- no userinfo;
- no fragment.

Example:

```go
func validateGraphUploadURL(raw string) error {
    u, err := url.Parse(raw)
    if err != nil {
        return fmt.Errorf("invalid graph upload url: %w", err)
    }

    if u.Scheme != "https" {
        return fmt.Errorf("graph upload url must use https: %q", raw)
    }

    if !strings.EqualFold(u.Hostname(), graphUploadHost) {
        return fmt.Errorf(
            "graph upload url host %q is not %s",
            u.Hostname(),
            graphUploadHost,
        )
    }

    if port := u.Port(); port != "" && port != "443" {
        return fmt.Errorf("graph upload url uses unexpected port %q", port)
    }

    if u.User != nil {
        return errors.New("graph upload url must not carry userinfo")
    }

    if u.Fragment != "" {
        return errors.New("graph upload url must not carry a fragment")
    }

    return nil
}
```

## Regression tests

Add:

- `https://outlook.office.com/...` → accepted;
- `https://outlook.office.com:443/...` → accepted;
- `https://outlook.office.com:444/...` → rejected;
- URL with fragment → rejected.

---

# Notes on reviewed changes that do not need another finding

## Audit-6 F01 request method initialization

The new `api.ts` ordering and inference logic is a real improvement.

The added tests cover:

- GET/no body;
- explicit method/no body;
- null/false/zero/empty-string POST inference;
- owner-generation fencing;
- GET-only snapshot-generation fencing.

I did not find a new defect there worth reporting.

## Audit-6 F12 readiness split

`/health/live` and `/health/ready` are separated, and `teploy.yml` now uses `/health/ready`.

That closes the reported readiness issue without changing the standing deployment-policy deferral.

## Audit-6 F14 shared provider client

Centralizing Gmail/Graph calls onto `providerHTTP` gives pooling plus TLS/header timeouts and is directionally correct.

The code still relies on caller contexts for whole-operation deadlines, which the outbox path provides.

I did not find a new standalone F14 regression in the reviewed delta.

## Audit-7 F01 redirect/origin hardening

The main reported security problem is fixed:

- foreign upload hosts are rejected;
- plaintext is rejected;
- userinfo is rejected;
- upload PUTs carry no bearer token;
- redirects are not followed.

F04 above is tightening, not a reopening of AUDIT7-F01.

## Audit-7 F02 failure classification

The important contract is now correct:

- explicit 4xx refusals can become `NotSubmittedError`;
- transport errors and 5xx remain ambiguous;
- cleanup errors are retained with `errors.Join`.

F02 above concerns the cleanup **context**, not the classification rule itself.

---

# Recommended patch order

1. **F01** first: it is the strongest correctness bug and is local to `graphUploadAttachment`.
2. **F02** next: introduce an explicit cleanup context/lifetime contract before more Graph draft logic accumulates.
3. **F03**: centralize Graph URL/path construction rather than patching individual call sites.
4. **F04**: fold into the same Graph upload hardening change as F01.

A good single follow-up commit could be:

```text
fix(graph): require chunk acknowledgements, bound draft cleanup, escape resource IDs
```

with the upload-origin port/fragment tightening included in that commit.

---

# Suggested minimal test matrix

Run at minimum:

```bash
go test -race -count=1 ./...
go vet ./...
cd dashboard && npm test -- --run
cd dashboard && npm run typecheck
cd dashboard && npm run build
```

For the new Graph tests, specifically exercise:

- intermediate upload response with no ranges;
- final response containing attachment metadata;
- cancellation/deadline followed by a cleanup DELETE attempt;
- account-lifecycle cancellation aborting cleanup;
- reserved characters in message/draft IDs;
- upload URL non-default port / fragment.

No standing `AUDIT_OPEN.md` deferral needs to be changed merely to fix F01-F04.