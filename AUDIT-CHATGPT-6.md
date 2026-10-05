# Lull Mail code audit — current `main`

Audit target: `lullmail/lullmail` at commit `c0a5f705142987b69656e0fe4099065aa7acd4f4` (2026-10-03).

## Executive summary

This audit reviewed the current repository rather than copying the older audit documents already committed in the tree. Several previously serious findings are now obsolete: in particular, outbound sends now use a durable PostgreSQL outbox with idempotent submission keys, per-account serialization, recovery records, retention, shutdown fencing, and ambiguity handling.

The highest-confidence current defect I found is a frontend request-layer temporal-dead-zone hazard that should be cleaned up before more refactoring. I also confirmed a set of still-open correctness, privacy, deployment, and provider risks that are either explicitly deferred in the repository or remain structurally present.

Priorities:

1. **P0/P1 — clean up immediately:** `dashboard/src/app/lib/api.ts` defines `assertCurrent()` before the `method` value it depends on. Current call ordering avoids an actual TDZ crash, but the code is fragile and regression-prone.
2. **P1 — API scalability/correctness:** thread reads serialize every cached body in the thread with no response pagination.
3. **P1 — Microsoft correctness:** Graph message IDs are not made immutable, so folder moves can invalidate stored identity.
4. **P1 — privacy:** untrusted email HTML is parsed with `DOMParser` before remote-resource stripping is guaranteed.
5. **P1/P2 — deployment:** Docker/Teploy examples permit raw plaintext exposure and forwarded-origin trust still needs an explicit trusted-proxy policy.
6. **P2 — reader feature/correctness:** `cid:` images are preserved by sanitization but are not resolved to authenticated inline parts.
7. **P2 — browser validation gap:** offline owner fencing/draft logic is elaborate and unit-tested, but browser crash/quota/upgrade/cross-tab behavior needs real-browser regression coverage.
8. **P2 — secret/token lifecycle:** key rotation/versioning and scoped/expiring agent tokens remain hardening gaps.

---

## Finding 1 — P1 — request method initialization is fragile and easy to regress into a TDZ crash

### Evidence

In `dashboard/src/app/lib/api.ts`, `assertCurrent` closes over `method` and is defined before:

```ts
const assertCurrent = () => {
  if (!protectedRoute) return;
  assertOwner(owner, gen);
  if (method === "GET" && snapshotGeneration() !== snapshots) {
    throw new StaleOwnerError();
  }
};

let body: string | undefined;
// ...
const method = opts.method || (body ? "POST" : "GET");
assertCurrent();
```

JavaScript lexical bindings created by `const` are in the temporal dead zone until initialization. The closure itself is legal, and **on the current commit the first call happens after `method` initialization**, so this is not presently a live crash. But the dependency is hidden and brittle: moving `assertCurrent()` earlier in a future edit would instantly make every protected request throw.

### Fix

Initialize the request method before creating any closure that depends on it, and derive it independently of serialized body:

```ts
const method = opts.method ?? (opts.body !== undefined ? "POST" : "GET");

let body: string | undefined;
if (opts.body !== undefined) {
  headers["Content-Type"] = "application/json";
  body = JSON.stringify(opts.body);
}

const assertCurrent = () => {
  if (!protectedRoute) return;
  assertOwner(owner, gen);
  if (method === "GET" && snapshotGeneration() !== snapshots) {
    throw new StaleOwnerError();
  }
};
```

This also removes the subtle distinction where serialized-body truthiness influences method inference.

### Regression tests

Add tests covering:

- GET with no body.
- explicit POST with no body.
- POST with `null`, `false`, `0`, and empty string payloads.
- owner generation changes before fetch and after fetch.
- snapshot generation changes invalidate GET only, not mutations.

---

## Finding 2 — P1 — thread endpoint can return unbounded cached message bodies

### Evidence

The repository's existing open-audit register still records this as `DATA-04`: the thread surface returns all messages and any cached bodies in one response. The eager-fetch limit bounds newly fetched provider bodies, but it does not bound already-cached bodies serialized into the API response.

A long-running mailing-list thread, automated notification thread, or imported historical thread can therefore create a very large JSON response, high server memory pressure, slow browser parsing, large offline-cache writes, and poor mobile behavior.

### Fix

Split the contract:

```text
GET /api/threads/:thread_id?cursor=...&limit=50
  -> message envelopes + body_status + has_more + next_cursor

GET /api/messages/:message_id/body
  -> text/html body only

GET /api/messages/:message_id/attachments
  -> attachment metadata only
```

Recommended implementation details:

- Keyset pagination by a stable tuple such as `(received_at, id)`.
- Default 50 messages, hard cap 100.
- Never inline large bodies into list/envelope pages.
- Reader lazily loads visible/expanded message bodies.
- MCP `read_thread` should expose pagination rather than silently concatenating an unbounded thread.
- Offline cache should cache body records per message, not one monolithic thread response.

### Regression tests

- 10,000-message synthetic thread does not produce an unbounded response.
- Page boundaries do not duplicate or skip equal-timestamp messages.
- A cached 10 MiB body does not inflate envelope responses.
- Reader can progressively load old messages and bodies.

---

## Finding 3 — P1 — Microsoft Graph IDs can change when messages move folders

### Evidence

The current code uses Graph REST endpoints and stored provider IDs but does not consistently request Graph immutable IDs with:

```http
Prefer: IdType="ImmutableId"
```

Microsoft Graph's default message IDs can change when an item is moved between folders. Lull Mail performs filing/moves and stores provider identities in its mirror, so a move can make a previously stored provider ID stale. This can affect subsequent body fetches, replies, reconciliation, push matching, or actions that address the message by provider ID.

The repo's earlier audit explicitly tracks this as unresolved because switching ID formats requires translating existing persisted IDs.

### Fix

Do this as a migration, not a one-line header change.

1. Add an account-level capability/version marker, e.g. `graph_id_format = 'default' | 'immutable'`.
2. For new Graph accounts, use immutable IDs from first sync.
3. Add `Prefer: IdType="ImmutableId"` to every Graph request that returns or consumes IDs.
4. For existing accounts, enumerate messages and translate stored IDs before flipping the account marker.
5. Update:
   - message envelopes,
   - folder membership references,
   - body/attachment references,
   - push correlation state,
   - filing/reconciliation references,
   - any reply-parent/provider ID references.
6. Make migration resumable and idempotent.

### Regression tests

- Sync a message in Inbox, move it to Archive, then fetch/open/reply using the same local row.
- Push event after a move maps back to the existing message.
- Migration interrupted halfway resumes without duplicate rows.
- Mixed-format accounts are fenced until migration completes.

---

## Finding 4 — P1 privacy — raw email HTML is parsed before remote resources are certainly neutralized

### Evidence

`dashboard/src/app/reader/Body.tsx` does:

```ts
const doc = new DOMParser().parseFromString(html, "text/html");
const blocked = sanitizeImageResources(doc, false);
```

and `cleanLinks()` also parses the original HTML before removing risky elements/resources.

The eventual iframe has a restrictive CSP and the sanitizer removes many network-backed sources, which is good. The remaining concern is **pre-render parsing**: browser HTML parsing behavior around inert documents and resource fetching can vary by element/browser. If a resource is initiated during parse, removing the node afterward is too late for a strict "remote images blocked means zero sender request" privacy guarantee.

### Fix

Best fix: sanitize server-side with a parser that never performs network I/O.

Pipeline:

```text
provider MIME -> server parser -> allowlist sanitizer ->
store sanitized HTML + original/plain body separately ->
browser receives already-network-neutral HTML
```

Server sanitizer policy:

- Remove: `script`, `iframe`, `frame`, `object`, `embed`, `form`, `link`, media, inputs, meta refresh, base.
- Remove all `on*` attributes.
- Remove URL-bearing attributes unless they are permitted links.
- Replace remote image/background/style URLs with explicit placeholders/tokens.
- Preserve a separate list of remote image URLs if "Load images" is desired.
- Resolve `cid:` references server-side to authenticated part IDs or blob tokens.
- Sanitize CSS URLs and `@import`.
- Browser should never parse unsanitized provider HTML.

Short-term containment: default to stored plain text when remote images are blocked, and only parse sanitized HTML after an explicit opt-in.

### Regression tests

Use Playwright with a local HTTP trap server and messages containing every network vector:

- `img src`
- `srcset`
- SVG `image href`
- CSS `background-image`
- `@import`
- `image-set()`
- `video poster`
- `source srcset`
- `link rel=stylesheet`
- malformed HTML edge cases.

Assert **zero requests** hit the trap before explicit image permission.

---

## Finding 5 — P1/P2 deployment — default examples allow accidental plaintext network exposure

### Evidence

`compose.yaml` publishes:

```yaml
ports:
  - "8080:8080"
```

which binds on all host interfaces by default.

`teploy.yml` documents:

```yaml
ingress: host
port: 8080
```

and describes Caddy/HTTPS as optional.

The app warns when it detects public exposure, but credentials, session cookies, mail contents, setup token interactions, and OAuth callbacks should not be intentionally exposed over plaintext internet transport.

### Fix

For Docker quickstart, bind loopback by default:

```yaml
ports:
  - "127.0.0.1:8080:8080"
```

Document two explicit production modes:

1. reverse proxy on same host/network terminating HTTPS;
2. private VPN/tailnet access.

For Teploy, make HTTPS/domain ingress the recommended/default production template. If raw host ingress is retained, label it development/private-network only.

Also add a startup warning or optional enforcement:

- If effective `PUBLIC_URL` is non-loopback and `http://`, log a high-severity warning.
- Optional `REQUIRE_HTTPS=1` should refuse startup for non-loopback HTTP.

---

## Finding 6 — P1/P2 — forwarded-origin trust needs an explicit trusted-proxy boundary

### Evidence

The project auto-detects and pins the browser origin during setup and uses forwarded headers behind reverse proxies. Existing audit notes indicate forwarded origin parsing is not tied to a configured trusted proxy peer.

If an app port is directly reachable and forwarded headers are accepted from any client, a requester can influence origin detection during first-run setup. Because WebAuthn RP/origin and mutation-origin checks derive from the pinned origin, first-run origin trust is security-sensitive.

### Fix

Add explicit proxy configuration:

```text
TRUST_PROXY=off
TRUST_PROXY=loopback
TRUST_PROXY=10.0.0.0/8,172.16.0.0/12,...
```

Rules:

- Only honor `Forwarded`, `X-Forwarded-Proto`, `X-Forwarded-Host` when `RemoteAddr` is trusted.
- Otherwise derive scheme/host from the direct request.
- Prefer RFC 7239 `Forwarded`; support X-forwarded headers as fallback.
- Reject malformed/multi-host ambiguity rather than guessing.
- In production docs, recommend explicit `PUBLIC_URL`; auto-detection is convenience for first-run local setups.

### Regression tests

- Direct untrusted client cannot spoof forwarded host/proto.
- Trusted proxy can set the public HTTPS origin.
- Multiple forwarded hops follow a documented trusted-hop algorithm.
- WebAuthn RP ID derives from the same canonical origin.

---

## Finding 7 — P2 — `cid:` inline images are kept but not resolvable

### Evidence

The reader explicitly treats `cid:` as local/safe:

```ts
if (/^(?:data:|cid:|#)/i.test(url)) return true;
```

and the iframe CSP allows:

```text
img-src data: cid:
```

But browser `cid:` URLs inside `srcdoc` are not automatically connected to the MIME parts stored by the backend. The API does not expose a complete Content-ID -> authenticated inline-part mapping, so legitimate embedded images can remain broken.

### Fix

Expose inline MIME parts explicitly:

```json
{
  "inline_parts": [
    {
      "content_id": "logo@example",
      "content_type": "image/png",
      "part_id": "...",
      "size": 12345
    }
  ]
}
```

Browser path:

1. fetch authenticated part bytes;
2. create Blob URL;
3. replace matching `cid:logo@example` references in already-sanitized HTML;
4. revoke Blob URLs on message unmount/change.

Do not expose long-lived bearer tokens in image URLs. Use same-origin authenticated fetches.

### Regression tests

- HTML message with one `cid:` image renders.
- duplicate/angle-bracketed Content-IDs normalize correctly.
- Blob URLs are revoked.
- missing inline part becomes a placeholder, not a broken network request.

---

## Finding 8 — P2 — offline ownership/draft correctness needs native browser fault testing

### Evidence

The offline layer is unusually careful: owner+generation namespaces, IndexedDB metadata authority, tombstones, revision checks, replay keys, and cross-tab invalidation are all present. Existing comments and audit records, however, explicitly note that synthetic/fake-IndexedDB tests do not prove behavior under browser quota errors, real upgrade transactions, renderer crashes, abrupt tab/process death, or cross-tab races.

For a mail client, offline privacy boundaries are security-sensitive because stale owner data must never become visible to a later user of the same browser profile.

### Fix

Add Playwright browser suites using real IndexedDB/service workers:

- two browser contexts/tabs representing owner A and owner B;
- force logout/login owner switch while reads and writes are in flight;
- service-worker update during offline mode;
- IndexedDB schema upgrade from previous production version;
- quota/write failure injection where browser permits;
- page crash/close immediately after transaction request but before UI acknowledgment;
- delayed storage/BroadcastChannel events.

Assertions:

- no owner A content appears after owner B admission;
- stale mutations never replay under B;
- drafts do not resurrect after committed retirement tombstone;
- failed purge suspends offline access rather than using uncertain data.

---

## Finding 9 — P2 hardening — encryption key lifecycle has no rotation/version migration

### Evidence

`SECRET_KEY` seals credentials and other sensitive records. The README explicitly states changing it invalidates sealed data. That means there is no online rotation or key version metadata.

A long-lived self-hosted mailbox should have a path to rotate a compromised/aged key without reconnecting every account or losing encrypted local state.

### Fix

Introduce envelope versioning:

```text
ciphertext format:
v2:<key-id>:<nonce>:<ciphertext>
```

Configuration:

```text
SECRET_KEYS=current:k2,...,old:k1,...
```

Decrypt by key ID; encrypt only with current key. Add a background/CLI migration that re-seals rows from old keys to current, with per-row authenticated additional data including table/column/owner/record identity.

Do not silently fall back through arbitrary keys without key IDs.

---

## Finding 10 — P2 hardening — agent tokens are broad and effectively long-lived

### Evidence

The project correctly fences agent tokens away from authentication/security routes, but the repository's open audit notes that tokens are broad and do not have first-class scopes/expiry.

For MCP/automation use, compromise of one token should not grant every mail mutation indefinitely.

### Fix

Add token fields:

```text
expires_at
last_used_at
scopes[]
name/device label
created_at
revoked_at
```

Suggested scopes:

- `mail.read`
- `mail.send`
- `mail.move`
- `screener.write`
- `notes.read/write`
- `board.read/write`
- `accounts.read`

Default new tokens to least privilege, short expiry for temporary integrations, and make "never expires" an explicit advanced choice.

---

## Finding 11 — P2 — Graph attachment support rejects files above 3 MiB instead of using upload sessions

### Evidence

The send path hard-rejects Microsoft attachments larger than 3 MiB per file:

```go
const graphAttachmentMax = 3 << 20
```

This is consistent with Graph's simple attachment endpoint, but Graph supports upload sessions for larger attachments. The product advertises a general 15 MiB per-file send limit, so Microsoft accounts receive a materially different capability.

This is not data corruption because the server rejects synchronously before acceptance, but it is a provider behavior gap.

### Fix

Implement Graph large attachment upload sessions:

- <= 3 MiB: existing direct attachment endpoint.
- > 3 MiB and <= product cap: create upload session and upload chunks.
- bind the upload to the draft message already created.
- use provider request deadlines and cancellation.
- if attachment upload fails before send, delete draft and report synchronous/terminal failure.
- keep outbox ambiguity semantics only around the final provider send boundary.

---

## Finding 12 — P2 — health endpoint reports HTTP 200 when the database is down

### Evidence

`handleHealth` intentionally returns:

```json
{"status":"ok","database":"down"}
```

with status 200 even if PostgreSQL is unavailable.

The comment says this is a liveness probe choice. That is fine for **liveness**, but using the same endpoint as deployment readiness means a new container can be marked healthy before it can actually serve mail.

### Fix

Split probes:

```text
/health/live   -> 200 if process/server is alive
/health/ready  -> 200 only if DB/migrations/critical dependencies are usable
```

Configure Teploy/container readiness against `/health/ready`, while any process-supervisor liveness probe uses `/health/live`.

This prevents traffic cutover to an instance whose database is down without creating restart loops for transient DB outages.

---

## Finding 13 — P3 — HTML sanitizer duplicates policy in multiple passes

### Evidence

`cleanLinks()`, `sanitizeImageResources()`, `stripRemoteImages()`, and `frameDoc()` each enforce pieces of the HTML/network policy. This creates policy drift risk: one pass can preserve a construct another pass forgot to forbid.

For example, `cleanLinks` sanitizes executable elements and allows remote image URLs, then `stripRemoteImages` parses that HTML again and removes remote images. The CSP is a third line of defense.

### Fix

Move to one canonical sanitizer result type:

```ts
type SanitizedMessage = {
  html: string;
  blockedRemoteResources: RemoteResource[];
  inlineCids: string[];
};
```

Prefer server-side implementation; if browser-side remains temporarily, make one parser traversal responsible for all:

- active-content removal,
- link normalization,
- CSS policy,
- image blocking/permission,
- CID collection.

Then generate iframe HTML from this already-sanitized result only.

---

## Finding 14 — P3 — provider HTTP client behavior should be centralized

### Evidence

Provider calls such as `graphCall` use `http.DefaultClient`. Context deadlines cover many call sites, but a single shared provider client would make redirect policy, connection limits, TLS behavior, tracing, and egress controls explicit.

### Fix

Give `App` / provider adapters a configured client:

```go
&http.Client{
    Transport: &http.Transport{
        Proxy: http.ProxyFromEnvironment,
        MaxIdleConns: 100,
        MaxIdleConnsPerHost: 10,
        IdleConnTimeout: 90 * time.Second,
        TLSHandshakeTimeout: 10 * time.Second,
        ResponseHeaderTimeout: 30 * time.Second,
    },
    CheckRedirect: providerRedirectPolicy,
}
```

Every request must still carry a context deadline; client timeout should be a final outer ceiling, not the only timeout.

For Graph specifically, reject redirects to origins outside the configured Microsoft Graph origin before forwarding Authorization.

---

# Recommended implementation order

## Phase 1 — correctness/security blockers

1. Refactor API request method initialization and add request-layer tests.
2. Add trusted-proxy configuration and secure deployment defaults.
3. Move HTML sanitization to a network-free server-side stage or temporarily render plain text until opt-in.
4. Design and implement Graph immutable-ID migration.

## Phase 2 — response and reader architecture

5. Paginate thread envelopes and split message-body retrieval.
6. Add authenticated inline-part API and CID resolution.
7. Add real-browser offline/privacy fault suites.

## Phase 3 — operational/provider hardening

8. Split liveness/readiness endpoints.
9. Add key IDs and secret-key rotation.
10. Add scoped/expiring agent tokens.
11. Add Graph upload sessions for >3 MiB attachments.
12. Centralize provider HTTP clients and redirect/egress policy.

---

# Tests I would require before calling the audit closed

```bash
go test -race ./...
go vet ./...

cd mail-engine
go test -race ./...
go vet ./...

cd ../mcp
go test -race ./...
go vet ./...

cd ../dashboard
npm ci
npm test
npm run typecheck
npm run build
```

Plus PostgreSQL-backed integration suites with their database environment variables enabled, and Playwright/browser tests for:

- owner switch/offline cache isolation;
- service worker upgrade while offline;
- remote-resource trap test for HTML mail;
- Graph move/immutable-ID behavior against a real or high-fidelity Graph test account;
- send acceptance lost-response/restart/deletion/shutdown paths;
- thread pagination with pathological large threads.

---

# Notes on older audit findings

Several older reports in the repository are valuable historical context but should not be implemented literally against current `main`.

Most importantly, the old "accepted sends live only in process memory" finding is superseded by the current durable `outbox_jobs` implementation. Current code persists encrypted compositions before acknowledgment, stores submission keys/request hashes, distinguishes pending/submitting/submitted/failed/ambiguous/cancelled, serializes per account, sweeps interrupted claims to ambiguous instead of resending, and retains bounded receipts.

When applying fixes from any older audit, re-check the exact current path first; this codebase has changed heavily and some former bugs have already been replaced by newer mechanisms.
