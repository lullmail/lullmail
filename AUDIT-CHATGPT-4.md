# Lullmail repository audit and implementation guide

**Repository:** [lullmail/lullmail](https://github.com/lullmail/lullmail)  
**Reviewed commit:** [`52fe475a712348b744b36eea875c8817dacd376e`](https://github.com/lullmail/lullmail/commit/52fe475a712348b744b36eea875c8817dacd376e)  
**Review date:** September 17, 2026  
**Deliverable:** Findings, concrete fixes, implementation examples, existing-risk register, and verification plan in this one file.

## 1. Scope, evidence, and limits

This is a source-level audit of the actual commit-pinned GitHub files, not a review of the README alone. It follows authentication, account lifecycle, provider connection, composition, sending, classification, export, browser persistence, and release workflows. It also compares the implementation with the repository's existing `AUDIT_OPEN.md` so that accepted risks and already-fixed findings are not misrepresented as new discoveries.

**This is not a claim that every possible defect has been found.** The coverage ledger at the end distinguishes files inspected from areas not exhaustively reviewed. There was no attack on a running installation, no production-data access, and no repository modification.

The most important newly verified issue is the Gmail authorization-host mismatch. The engine permits bearer credentials on `www.googleapis.com`, while the exact pinned Google API dependency constructs requests to `gmail.googleapis.com`. The report also identifies concrete draft-loss, wrong-recipient, offline replay, privacy, connection-pool, download-integrity, and CI-release problems.

### Evidence labels

- **Confirmed from source:** The stated control flow or data transformation is present in the reviewed files. This does not claim a live-provider reproduction.
- **Mechanism reproduced:** A small local Go or JavaScript test exercised the relevant expression, transport decision, or standard-library behavior. These are not substitutes for repository integration tests.
- **Existing / carried forward:** The repository already acknowledges the risk. Implementation guidance is supplied here, but discovery credit is not claimed.
- **Integration-dependent:** A proposed fix changes a schema, interface, state machine, or several modules and requires coordinated implementation and migration tests.

Severity is assessed for the documented single-owner deployment. **High** means a primary workflow can be broken, private data or user work can be lost/exposed, or an important release safeguard is bypassed. **Medium** means a significant correctness, reliability, or conditional security problem. **Low** means limited-impact correctness or usability. None of the new findings is presented as a verified unauthenticated remote-code-execution vulnerability.

### What was actually executed

The environment had Go 1.23.2 and Node.js 22.16.0. The product requests Go 1.26. The repository could be read through GitHub, but a local clone/dependency installation was not available; PostgreSQL was not available either. Consequently, **the full Go suites, race tests, dashboard build, browser tests, migration tests, and real-provider tests were not run**.

Fifteen isolated checks passed: nine Go checks and six JavaScript checks. They demonstrate the Gmail allowlist failure and correction, database-pool hold-and-wait, invalid UTF-8 byte slicing, successful-looking truncated HTTP downloads, premature attachment readiness, unconditional retirement autosave, replay reordering, secret trimming, and send-weight undercounting. Source and outputs are included in Appendix A. Patch examples below have not been compiled as a complete modified application.

### How to use the implementations

Small replacements are marked **local patch**. Longer examples are marked **integration implementation** or **reference implementation** and name the required call-site or schema changes. They are intended to be implemented together with their stated tests, not concatenated blindly into the repository. Migration examples are forward designs, not instructions to execute destructive SQL against an existing installation without a backup and rehearsed upgrade.

## 2. Priority order

First fix F01, F03, F06, F08, and F02: Gmail connection, attachment preservation, reply targeting, connection-pool deadlock, and release gating. Next fix the offline ordering/isolation/purge family F09–F12 and retirement/durability F04–F05. Implement the durable outbox and session-epoch work before relying on send acceptance or credential revocation as strong guarantees. Then complete the remaining correctness fixes and the staged synchronization/migration work.

## 3. Confirmed findings

| ID | Severity | Finding |
|---|---|---|
| [F01](#f01) | High | Gmail requests lose their bearer token because the allowlist names the wrong host |
| [F02](#f02) | High | Images may publish despite a failed PostgreSQL integration/race job |
| [F03](#f03) | High | Attachment restoration can be overwritten and sending can omit files still loading |
| [F04](#f04) | Medium | Unmount autosave recreates a draft after send or discard |
| [F05](#f05) | Medium | Draft persistence silently fails on large undo seeds and drops attachment-only drafts |
| [F06](#f06) | High | Reply defaults ignore Reply-To and can address a reply to the owner |
| [F07](#f07) | Medium | JMAP accounts are sent through SMTP without a verified SMTP capability |
| [F08](#f08) | High | Classification holds an open result set while waiting for another connection from the same capped pool |
| [F09](#f09) | High | Backoff lets newer offline mutations overtake older mutations |
| [F10](#f10) | Medium | A replay network exception keeps work queued but never schedules its retry |
| [F11](#f11) | High | Failure to prepare the offline owner does not actually disable offline storage |
| [F12](#f12) | High | Mailbox disconnection and retention do not purge the browser’s persistent mail cache |
| [F13](#f13) | Medium | Logout removes the only retry token when revocation fails, and the UI suppresses that failure |
| [F14](#f14) | Medium | Send admission undercounts retained data and happens after expensive request allocation |
| [F15](#f15) | Medium | A failed attachment stream can look like a successful truncated download |
| [F16](#f16) | Medium | Synchronous sync reports success even when product finalization fails |
| [F17](#f17) | Low | Byte slicing can produce invalid UTF-8 names and filenames |
| [F18](#f18) | Medium | The mailbox connection form trims significant password whitespace |
| [F19](#f19) | Medium | The reader ignores body_status and mislabels empty or failed bodies as sync-in-progress |
| [F20](#f20) | Medium | Undoing a pin can delete a pin that existed before the operation |
| [F21](#f21) | Medium | HTML sanitization drops body-level presentation before deciding whether to apply the dark theme |
| [F22](#f22) | Low | Push notification UI uses account-wide subscription state for a device-local action |
| [F23](#f23) | Medium | Several operational database failures are reported as missing resources or invalid authentication |

<a id="f01"></a>

### F01 — Gmail requests lose their bearer token because the allowlist names the wrong host

**Severity:** High  
**Status:** Confirmed; mechanism reproduced  
**Sources:** [`mail-engine/dialer/dialer.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/mail-engine/dialer/dialer.go), [`mail-engine/gmail/adapter.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/mail-engine/gmail/adapter.go), [`mail-engine/go.mod`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/mail-engine/go.mod), [`go.mod`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/go.mod)

**Evidence.** `dialGmail` constructs the SDK with `bearerClient(cred.AccessToken, "www.googleapis.com")`. `bearerTransport.RoundTrip` removes `Authorization` on any other hostname. `gmail.New` calls `gmail.NewService` without overriding its endpoint. Both module files pin `google.golang.org/api v0.291.0`; [that version's generated Gmail client](https://github.com/googleapis/google-api-go-client/blob/v0.291.0/gmail/v1/gmail-gen.go#L98-L104) sets `basePath` to `https://gmail.googleapis.com/`. Google's [service reference](https://developers.google.com/workspace/gmail/api/reference/rest) independently names the same endpoint.

**Impact.** The ordinary Gmail engine connection/sync path makes unauthenticated API requests and cannot work with an otherwise valid token. The app's separate Gmail send request manually sets authorization, so a working send path would not disprove this connection defect.

**Local patch — `mail-engine/dialer/dialer.go`:**

```go
func dialGmail(ctx context.Context, cred mail.Credential) (mail.Adapter, func(), error) {
    const endpoint = "https://gmail.googleapis.com/"
    ad, err := gmail.New(ctx,
        option.WithEndpoint(endpoint),
        option.WithHTTPClient(bearerClient(cred.AccessToken, "gmail.googleapis.com")),
    )
    if err != nil {
        return nil, nil, err
    }
    return ad, func() { _ = ad.Close() }, nil
}
```

Do not fix this by attaching the bearer token to every redirect or every `*.googleapis.com` host. Keep origin restrictions; ideally bind scheme, hostname, and effective port together. Explicitly configuring the endpoint makes the client/allowlist contract testable.

**Regression tests.** Construct the real pinned SDK with the production transport and capture a labels/profile request. Assert the requested endpoint and bearer header. Also assert that a different hostname and a downgrade to HTTP receive no credential. When adding the stronger scheme/host/port origin check, add an unauthorized-port case; the small host correction above does not itself introduce port restrictions. The isolated test here verifies the transport decision, not live Google authorization.


<a id="f02"></a>

### F02 — Images may publish despite a failed PostgreSQL integration/race job

**Severity:** High  
**Status:** Confirmed  
**Sources:** [`.github/workflows/ci.yml`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/.github/workflows/ci.yml)

**Evidence.** `postgres-integration` is a separate job. `publish` has `needs: verify`, not a dependency on that integration job. The push condition permits publication as soon as `verify` finishes, even if database/race checks fail or are still running.

**Impact.** The newly added real-database checks are not a release gate. A broken image can be published under the commit tag and, on main, `latest`.

**Local patch:**

```yaml
publish:
  needs: [verify, postgres-integration]
  if: github.event_name == 'push'
  permissions:
    contents: read
    packages: write
  # Keep the existing runs-on and steps below this point.
```

Add a release-workflow test or policy check that requires all verification jobs in the dependency set. Exercise an intentional integration failure on a non-production branch/fixture and confirm no publishing step runs. Existing protections against a historical version tag moving `latest` backward should remain intact. Pin third-party actions and base images to reviewed immutable revisions as a separate supply-chain hardening task; no specific vulnerable action version was established in this review.


<a id="f03"></a>

### F03 — Attachment restoration can be overwritten and sending can omit files still loading

**Severity:** High  
**Status:** Confirmed; readiness/guard mechanism reproduced  
**Sources:** [`dashboard/src/app/ui/Compose.tsx`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/ui/Compose.tsx)

**Evidence.** `attachmentsReady` initializes as `!seed.attachments`. An ordinary parked/restored draft has no in-memory attachment seed, so it starts **ready**, with an empty array, before `loadDraftAttachments` resolves. The persistence effect can write that empty array over the stored set. The `send` function checks only the recipient and `busy`; the keyboard path therefore has no independent attachment-readiness guard. `addFiles` has no pending-import guard either. The load-error path marks the set ready, allowing an empty save after a failed restore.

**Impact.** A restored or newly attaching draft can be sent without its attachments, and the saved attachment set can be lost. This is a residual defect in code whose comment explicitly promises the opposite.

**Integration implementation — replace the readiness boolean with a state machine in `DraftForm`:**

```tsx
type FilePhase = "loading" | "ready" | "error";
const [filePhase, setFilePhase] = useState<FilePhase>("loading");
const [pendingImports, setPendingImports] = useState(0);
const [fileError, setFileError] = useState<string | null>(null);
const mounted = useRef(true);
const imports = useRef(0);
const sendInFlight = useRef(false);

useEffect(() => {
  mounted.current = true;
  let active = true;
  (async () => {
    try {
      const restored = seed.attachments !== undefined
        ? [...seed.attachments]
        : (await loadDraftAttachments<SendAttachment>(seed.id)) ?? [];
      if (!active) return;
      setAttachments(restored);
      setFilePhase("ready");
    } catch (error) {
      if (!active) return;
      setFileError(error instanceof Error ? error.message : "Attachment restore failed");
      setFilePhase("error"); // Never persist an empty replacement on restore failure.
    }
  })();
  return () => { active = false; mounted.current = false; };
}, [seed.id]);

const addFiles = async (list: FileList | null) => {
  if (!list || filePhase !== "ready" || sendInFlight.current) return;
  const files = Array.from(list); // Snapshot before the input is reset.
  imports.current++;
  setPendingImports(imports.current);
  try {
    const added: SendAttachment[] = [];
    for (const file of files) {
      const item = await fileToAttachment(file);
      if (item) added.push(item);
    }
    if (mounted.current) setAttachments(old => [...old, ...added]);
  } catch (error) {
    if (mounted.current) {
      setFileError(error instanceof Error ? error.message : "Attachment import failed");
      setFilePhase("error");
    }
  } finally {
    imports.current--;
    if (mounted.current) setPendingImports(imports.current);
  }
};

// Put this guard INSIDE send(), not only on the button.
// Use sendInFlight to close the same-tick double-activation window.
const maySend = filePhase === "ready" && pendingImports === 0 && !busy;
// At the start of send():
// if (!to.trim() || filePhase !== "ready" || imports.current > 0 ||
//     sendInFlight.current) return;
// sendInFlight.current = true;
// try { ...existing sendMail call... }
// finally { sendInFlight.current = false; setBusy(false); }
```

Disable file additions/removals during restore and submission. Persist only a ready, settled attachment snapshot, and catch persistence failures. Present an explicit retry/acknowledged-discard choice after a restore error; do not interpret the error as an authoritative empty set. Serialize saves and deletion as described in F04 so an older write cannot resurrect a retired draft. The ref guards above must be used by both button and keyboard submission.

**Regression tests.** Delay and reject the IndexedDB read; send immediately using both activation paths; import a large file and submit before conversion finishes; perform two selections; switch drafts during a read. Assert no send omits a known pending file and no empty write occurs before authoritative restoration.


<a id="f04"></a>

### F04 — Unmount autosave recreates a draft after send or discard

**Severity:** Medium  
**Status:** Confirmed; mechanism reproduced  
**Sources:** [`dashboard/src/app/ui/Compose.tsx`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/ui/Compose.tsx), [`dashboard/src/app/lib/store.ts`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/lib/store.ts)

**Evidence.** The autosave effect cleanup always calls `write()`. The successful-send and discard paths remove `es-draft-<id>` and retire/unmount the draft. Cleanup then writes the private text and recipients back into the supposedly deleted slot. Attachment saves are also launched without serialization against deletion.

**Impact.** Sent/discarded text can remain in browser storage. An old attachment save may race deletion. The ring may no longer display the draft, making the retained data less visible rather than erased.

**Local patch for the synchronous text race:**

```tsx
const retired = useRef(false);
useEffect(() => {
  const write = () => {
    if (retired.current) return;
    try {
      localStorage.setItem(draftKey, JSON.stringify(latest.current));
    } catch {
      showToast("This draft is not saved on this device");
    }
  };
  const timer = setTimeout(write, 250);
  return () => { clearTimeout(timer); write(); };
}, [draftKey, to, cc, bcc, subject, body, htmlMode, accountId]);

// Both confirmed send acceptance and explicit discard must use this path.
async function retireLocalDraft(): Promise<void> {
  retired.current = true; // Must precede removal and unmount.
  try {
    localStorage.removeItem(draftKey);
    await clearDraftAttachments(seed.id);
  } finally {
    // Unmount even if cleanup fails, so a sent message is not offered for re-send.
    retireDraft(seed.id);
  }
}
```

Catch and display deletion failures outside `retireLocalDraft`; a failed local cleanup is not a failed mail submission and must not invite a duplicate send. The full fix puts draft metadata, files, and a retirement tombstone in IndexedDB, with one per-draft operation queue and owner generation. All previously started writes must finish or observe the tombstone before deletion is declared complete. This is distinct from the durable send/outcome problem in R01.

**Regression tests.** Send and discard with an outstanding autosave timer, an outstanding attachment write, and a quota/storage failure. After unmount, verify both stores and the ring; no private slot may be recreated.


<a id="f05"></a>

### F05 — Draft persistence silently fails on large undo seeds and drops attachment-only drafts

**Severity:** Medium  
**Status:** Confirmed  
**Sources:** [`dashboard/src/app/lib/store.ts`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/lib/store.ts), [`dashboard/src/app/ui/Compose.tsx`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/ui/Compose.tsx), [`dashboard/src/app/lib/actions.ts`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/lib/actions.ts)

**Evidence.** `persistDraftsNow` serializes the complete `draftStack`, including `ComposeState.attachments` containing base64 data, into localStorage and suppresses write errors. Undo-send seeds contain the full attachment set. This defeats the stated separation of large attachments into IndexedDB. `restoreDrafts` considers only `to`, `subject`, and `body`, so a draft containing only attachments or copied recipients can be treated as blank and omitted.

**Impact.** A large undo-restored attachment can cause the entire ring save to fail silently. A reload then restores stale or missing drafts. Attachment-only drafts are not reliably restorable even though their files may remain on disk.

**Integration implementation.** Store each complete draft as one IndexedDB record, including files, and maintain the ordered ring in the same transaction. Use a stable random ID rather than a clock/per-tab counter. Do not claim a successful save before transaction completion. A transitional localStorage shape must strip binary data and retain an attachment-presence marker:

```ts
type DraftMetadata = Omit<ComposeState, "attachments"> & { hasAttachments: boolean };
function draftMetadata(d: ComposeState, attachmentCount: number): DraftMetadata {
  const { attachments: _files, ...metadata } = d;
  return { ...metadata, hasAttachments: attachmentCount > 0 };
}
function worthRestoring(d: DraftMetadata): boolean {
  return d.hasAttachments || [d.to, d.cc, d.bcc, d.subject, d.body]
    .some(value => typeof value === "string" && value.trim().length > 0);
}
function newDraftId(): string { return crypto.randomUUID(); }
```

Update the attachment-count metadata whenever the file set changes. The transitional metadata patch alone does not make two different storage engines atomic; use the single-IDB-record design for the durable fix. On quota errors keep the in-memory draft and show a persistent unsaved indicator, not a swallowed exception. Restore and owner changes must follow R08/F11.

**Regression tests.** Undo a multi-megabyte send, force localStorage quota failure, reload an attachment-only draft, and open two tabs at the same mocked clock time. No saved draft should silently disappear or collide.


<a id="f06"></a>

### F06 — Reply defaults ignore Reply-To and can address a reply to the owner

**Severity:** High  
**Status:** Confirmed  
**Sources:** [`dashboard/src/app/reader/Thread.tsx`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/reader/Thread.tsx), [`classify.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/classify.go), [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/sendqueue.go), [`mail-engine/send.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/mail-engine/send.go)

**Evidence.** `Thread.tsx` uses the newest message's rendered `from` address as the composer's `to`. The thread JSON does not expose a computed reply target or full `Reply-To`. The server's `mail.ReplyTo` initially selects the correct `Reply-To`/`From`, but `handleSend` then replaces it with the nonempty `to` supplied by the browser. If the last message was sent by the owner, the UI defaults to replying to that owner.

**Impact.** A reply can go to the wrong mailbox rather than the destination specified by the original message. This also breaks the common workflow of following up on one's own sent message.

**Integration implementation.** Compute the default recipient server-side from the stored envelope and the owner's verified addresses, return it in thread JSON, and populate the composer from that result. Preserve explicit user edits; do not forcibly rewrite them at submission.

```go
// Uses the existing mail.Address and mail.Envelope types.
// own must contain normalized addresses of all connected accounts and verified aliases.
func defaultReplyRecipients(parent *mail.Envelope, own map[string]bool) []mail.Address {
    normalize := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
    fromOwn := false
    for _, address := range parent.From {
        if own[normalize(address.Email)] { fromOwn = true; break }
    }
    candidates := parent.ReplyTo
    if fromOwn {
        // A follow-up on one's own sent message is addressed to its recipients.
        candidates = parent.To
    } else if len(candidates) == 0 {
        candidates = parent.From
    }
    result := make([]mail.Address, 0, len(candidates))
    seen := make(map[string]bool)
    for _, address := range candidates {
        key := normalize(address.Email)
        if key == "" || own[key] || seen[key] { continue }
        seen[key] = true
        result = append(result, address)
    }
    return result
}
```

Add `reply_to` as an array of address objects, or a server-formatted address-list string, to each thread message. In the reader use that field instead of `splitFrom(last.from)`. An empty result must prompt for a recipient; do not silently add all Cc/Bcc addresses or fabricate a reply-all policy. For aliases not recorded locally, provide explicit verified-alias configuration. Apply the same logic to keyboard reply shortcuts.

**Regression tests.** `From != Reply-To`; latest message is in Sent; multiple owner addresses; an alias; empty recipients; explicit user recipient override; and two accounts sharing an RFC Message-ID. Ensure the parent/account authorization check remains intact.


<a id="f07"></a>

### F07 — JMAP accounts are sent through SMTP without a verified SMTP capability

**Severity:** Medium  
**Status:** Confirmed; provider-dependent impact  
**Sources:** [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/sendqueue.go), [`accounts.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/accounts.go), [`mail-engine/dialer/dialer.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/mail-engine/dialer/dialer.go), [`dashboard/src/app/views/AccountsView.tsx`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/views/AccountsView.tsx)

**Evidence.** The connection form offers JMAP. The read adapter uses the JMAP bearer token. `deliveryFor` has special send paths for Gmail and Graph, but otherwise calls `SMTPFor`. A JMAP token and discovery endpoint do not establish a working SMTP submission credential. The account model/UI does not distinguish a validated JMAP submission capability from a separately validated SMTP transport.

**Impact.** A valid JMAP-only mailbox can appear send-capable, have its composition accepted into the volatile queue, and fail later when used as an SMTP account. This does not mean every JMAP provider rejects SMTP; the defect is assuming support without validating it.

**Immediate local mitigation — before `deliveryFor`/queue acceptance:**

```go
if provider == "jmap" {
    writeProblem(w, http.StatusUnprocessableEntity, "Sending Not Configured",
        "This JMAP account has no verified submission transport; the draft was not queued")
    return
}
```

Apply the check to the effective reply-parent provider as well. Return `can_send` and a reason in the accounts API and disable selection for unsupported accounts. This intentionally disables currently unverified JMAP sending rather than pretending to implement it.

**Full implementation.** Add a transport discriminator (`smtp`, `gmail`, `graph`, `jmap_submission`, `none`) and separately encrypted SMTP credentials when configured. Validate SMTP authentication before enabling that transport. Alternatively implement JMAP EmailSubmission using the discovered advertised capability and an explicit recipient envelope, retaining the outbound idempotency/outcome protections in R01. Do not send JMAP bearer credentials to an inferred SMTP endpoint.

**Regression tests.** A JMAP-only mock account must fail synchronously without retiring the draft; a separately verified SMTP submission account must work; read-only accounts remain readable.


<a id="f08"></a>

### F08 — Classification holds an open result set while waiting for another connection from the same capped pool

**Severity:** High  
**Status:** Confirmed; database/sql mechanism reproduced  
**Sources:** [`classify.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/classify.go), [`mail.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/mail.go)

**Evidence.** `classifyUser` opens the unclassified-message `rows` and defers `Close`, then calls several `collect` queries on the same `*sql.DB` before consuming the first result set. The product pool is limited to 16 open connections. Concurrent calls can each hold one result set and wait for another pool connection.

**Impact.** Sixteen overlapping classifiers can exhaust the pool with hold-and-wait. A one-connection pool reproduces the same mechanism with one call. Cancellation may eventually release it, but some callers use background contexts, and unrelated API work shares the pool.

**Local patch.** Move the existing `pending` type, `batch` scan loop, `rows.Err()` check, and explicit `rows.Close()` immediately after the first query and **before any `collect` invocation**. Do not begin another query or transaction while that result set is open. Preserve the historical timestamp calculation and the owner-row lock used later.

```go
// Immediately after the initial QueryContext in classifyUser:
var batch []pending // Move the existing local pending type above this line.
for rows.Next() {
    var acct, id, fromJSON string
    var received, connected sql.NullTime
    if err := rows.Scan(&acct, &id, &fromJSON, &received, &connected); err != nil {
        rows.Close()
        return err
    }
    batch = append(batch, pending{
        acct: acct, id: id, sender: firstSenderEmail(fromJSON),
        historical: received.Valid && connected.Valid && received.Time.Before(connected.Time),
    })
}
scanErr := rows.Err()
closeErr := rows.Close()
if err := errors.Join(scanErr, closeErr); err != nil { return err }
// Only now execute the correspondent collection queries and the existing transaction.
```

A better scaling follow-up replaces unbounded classification with batches and coalesces concurrent requests per owner. Do not simply increase the pool: that only changes the concurrency needed to reproduce the deadlock. Keep ownership/decision locking and error propagation.

**Regression tests.** Run classification with `SetMaxOpenConns(1)` and with a barrier-started burst at the configured cap against PostgreSQL. Bound the test context and require successful completion. Appendix A includes a standard-library fake-driver reproduction of the connection-pool mechanism, not the product SQL.


<a id="f09"></a>

### F09 — Backoff lets newer offline mutations overtake older mutations

**Severity:** High  
**Status:** Confirmed; ordering mechanism reproduced  
**Sources:** [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/lib/offline.ts)

**Evidence.** `replayMutations` sorts by `queuedAt`, but when an older item has a future `nextAttemptAt`, the loop uses `continue`. A newer mutation can therefore reach the server first. After the delay the older write runs and can overwrite the newer result. This directly contradicts the retry branch's comment about retaining the rest “behind this one, in order.”

**Impact.** Sequential note edits, moves, read/unread changes, and undo operations can be applied in the wrong order. A successful HTTP response does not protect against semantic loss of the latest edit.

**Local patch:**

```ts
if (item.nextAttemptAt && item.nextAttemptAt > Date.now()) {
  retryAt = item.nextAttemptAt;
  break; // Do not overtake the oldest unresolved mutation.
}
```

Use a durable monotonically increasing sequence allocated in the queue transaction; timestamps alone do not order two entries created in the same millisecond. A more concurrent implementation may serialize per entity, but only after defining dependencies between operations. Failed/conflicting edits need a user-visible resolution path, not only a console message. This patch does not solve cross-tab duplicate replay; see R07.

**Regression tests.** Queue edit A then edit B for one note, make A receive 503, and invoke replay while A is backed off. B must not run until A completes or the user explicitly resolves/skips A. Repeat with equal timestamps and read/unread pairs.


<a id="f10"></a>

### F10 — A replay network exception keeps work queued but never schedules its retry

**Severity:** Medium  
**Status:** Confirmed  
**Sources:** [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/lib/offline.ts)

**Evidence.** A thrown `fetch` is caught with `break` before any `retryAt` is set or attempts persisted. `startOfflineData` schedules a timer only when that field is returned. A network request can fail while `navigator.onLine` remains true, so a subsequent `online` event is not guaranteed.

**Impact.** Offline changes can stay stranded until a later navigation or connectivity event. A second issue is that an in-flight replay can schedule a new timer after the returned cleanup function has run.

**Local patch — share the retry path between HTTP failures and network exceptions:**

```ts
async function deferReplay(item: Queued, retryAfter: string | null): Promise<number> {
  const attempts = (item.attempts ?? 0) + 1;
  const at = Date.now() + retryDelay(attempts, retryAfter);
  await transaction(QUEUE, "readwrite", store =>
    store.put({ ...item, attempts, nextAttemptAt: at }));
  return at;
}

// In replayMutations, replace `catch { break; }`:
// catch {
//   retryAt = await deferReplay(item, null);
//   break;
// }
// In the HTTP retry branch:
// retryAt = await deferReplay(item, response.headers.get("Retry-After"));
// break;
```

In `startOfflineData`, add a `stopped` flag, check it before replay and after every awaited result, and set it before clearing the timer in cleanup. Coalesce concurrent replay invocations. Clamp timer delays to the platform's supported range; recheck the persisted deadline when the timer fires rather than treating a long `Retry-After` as permission to run early. Abort replay on auth/owner changes using F11/R08.

**Regression tests.** Keep `navigator.onLine=true`, reject the first fetch, and resolve the second without emitting an online event. The second request must occur on the scheduled retry. Unmount before the first replay resolves and assert that no timer is recreated.


<a id="f11"></a>

### F11 — Failure to prepare the offline owner does not actually disable offline storage

**Severity:** High  
**Status:** Confirmed; residual of acknowledged owner-isolation risk  
**Sources:** [`dashboard/src/app/lib/api.ts`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/lib/api.ts), [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/lib/offline.ts), [`dashboard/src/app/App.tsx`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/App.tsx)

**Evidence.** `refreshAuth` logs that offline storage is disabled if `prepareOfflineOwner` fails, but no shared disabled state gates `cacheResponse`, `cachedResponse`, or replay. Those functions keep deriving their namespace from the old localStorage marker. An owner-change wipe failure deliberately preserves that old marker. Replay is started independently in the application lifecycle.

**Impact.** After an owner transition and storage failure, a current session can read/write an old namespace or replay commands before the identity transition is safe. The documented single-owner design does not remove reinstall/reset/reconfiguration transitions.

**Integration implementation — explicit, fail-closed scope state:**

```ts
// Add to offline.ts and require it for every cache, queue, attachment, and replay operation.
export interface OfflineScope { readonly owner: string; readonly generation: number }
let generation = 0;
let activeScope: OfflineScope | null = null;

export function suspendOffline(): void {
  generation++;
  activeScope = null;
}
export function captureOfflineScope(): OfflineScope | null { return activeScope; }
export function isCurrentScope(scope: OfflineScope): boolean {
  return activeScope === scope;
}
export async function activateOffline(owner: string): Promise<boolean> {
  const mine = ++generation;
  activeScope = null; // Old localStorage markers are not authority.
  if (!owner) return false;
  try {
    await prepareOfflineOwner(owner);
    if (mine !== generation) return false;
    activeScope = Object.freeze({ owner, generation: mine });
    return true;
  } catch (error) {
    console.error("Offline storage unavailable", error);
    return false; // Remains disabled.
  }
}
```

In `api.ts`, capture the scope **when a request starts** and pass it into cache writes; do not select a new owner after the response arrives. Check that scope before opening a transaction and after asynchronous reads. Cache reads must return no fallback when the scope is inactive. Queueing must fail visibly rather than pretend a mutation was saved. Start replay only after successful activation, and suspend it on logout, reauthentication failure, reset, and account-wide deletion.

This in-memory guard is the immediate correction. The durable design in R08 additionally uses an immutable installation/user ID, a persisted generation checked inside the IndexedDB write transaction, and cross-tab invalidation. It is needed to fence writes already opened before a destructive transition.

**Regression tests.** Seed owner A, authenticate owner B, abort the wipe transaction, then attempt fallback reads, response writes, and replay. None may use A's namespace. Resolve a request from the previous generation after the switch and confirm it cannot update B's cache or UI.


<a id="f12"></a>

### F12 — Mailbox disconnection and retention do not purge the browser’s persistent mail cache

**Severity:** High  
**Status:** Confirmed; privacy boundary differs from server deletion  
**Sources:** [`dashboard/src/app/views/AccountsView.tsx`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/views/AccountsView.tsx), [`dashboard/src/app/lib/api.ts`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/lib/api.ts), [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/lib/offline.ts), [`accounts.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/accounts.go)

**Evidence.** The disconnect handler deletes the account on the server, reloads accounts, and refreshes counts. The retention handler similarly refreshes the view. Neither purges persistent response snapshots. The API layer intentionally preserves those snapshots on ordinary mutations. Cached thread bodies therefore survive these destructive operations on the browser side.

**Impact.** Previously viewed private mail can remain in IndexedDB after a user disconnects the mailbox or removes older local mail. The server's deletion transaction is not the problem; the browser's separate copy is outside that guarantee. User-facing wording about permanently removing local mail is incomplete.

**Immediate mitigation.** After confirmed server deletion/retention, suspend late cache writes through the generation fence, clear the response cache, invalidate in-memory responses, and close any reader displaying removed material. Do not clear the mutation queue for unrelated mailboxes just to erase response snapshots. If cleanup fails, display that server deletion succeeded but device cleanup did not.

```ts
// Use after server success, coordinated with F11's generation transition.
async function purgeDeviceMailSnapshots(resetMemory: () => void): Promise<void> {
  resetMemory();
  closeReader();
  await clearResponseCache();
}
```

**Complete implementation.** Add `owner_id`, `mailbox_ids`, and retention metadata to persisted response records. In one IndexedDB transaction, delete affected thread/list snapshots, remove or mark obsolete mutations for the deleted mailbox, and remove its local draft/file records according to an explicit user choice. Preserve other mailboxes' work. Increment a persistent cache generation and reject writes from older generations. Broadcast the transition to other tabs. For mixed-account aggregate responses, evict the whole response rather than trying to leave removed records inside it.

The small helper is a conservative single-device mitigation, not a complete cross-tab solution. Also distinguish “disconnect from server” from “forget this device” in the UI; deleting a server mirror does not automatically erase browser storage on other devices.

**Regression tests.** Cache a thread, disconnect its account online, go offline, and inspect both UI and IndexedDB. Repeat after retention, during an in-flight body response, and with a second tab open. No removed private content should reappear in the active namespace.


<a id="f13"></a>

### F13 — Logout removes the only retry token when revocation fails, and the UI suppresses that failure

**Severity:** Medium  
**Status:** Confirmed  
**Sources:** [`auth.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/auth.go), [`dashboard/src/app/views/SecurityView.tsx`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/views/SecurityView.tsx)

**Evidence.** On a failed `DELETE FROM auth_sessions`, `handleLogout` calls `clearCookie` and then tells the user to try again. That browser no longer has the token needed for a retry, while another copy of it can remain valid. `SecurityView.logout` catches and ignores the failed request, then updates authentication state.

**Local patch — replace the handler:**

```go
func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
    if cookie, err := r.Cookie(sessionCookie); err == nil {
        if _, err := a.db.ExecContext(r.Context(),
            `DELETE FROM auth_sessions WHERE id_hash=$1`, tokenHash(cookie.Value)); err != nil {
            a.log.Error("logout revocation failed", "err", err)
            w.Header().Set("Retry-After", "2")
            // Preserve the cookie so this browser can retry revocation.
            writeProblem(w, http.StatusServiceUnavailable, "Logout Failed",
                "The session could not be revoked. You are not signed out; retry.")
            return
        }
    }
    a.clearCookie(w, sessionCookie)
    writeJSON(w, map[string]any{"ok": true})
}
```

**Client patch:**

```ts
const logout = async () => {
  setBusy("logout");
  try {
    await authApi("/auth/logout", { method: "POST" });
    suspendOffline(); // F11; also broadcast the confirmed logout to other tabs.
    authed.value = false;
    await refreshAuth();
    navigate("/today");
  } catch (error) {
    report(error, "Sign-out failed; the session may still be active");
  } finally {
    setBusy("");
  }
};
```

Whether sign-out retains encrypted/offline drafts or performs “forget this device” is a product policy choice. Expose that choice explicitly; do not silently discard unsent work. A server revocation failure must never be presented as a completed sign-out.

**Regression tests.** Fail the deletion once, assert no cookie-expiration header is returned, retry with the same token, and then confirm the session is invalid and the cookie is cleared. Ensure the client shows the first failure.


<a id="f14"></a>

### F14 — Send admission undercounts retained data and happens after expensive request allocation

**Severity:** Medium  
**Status:** Confirmed; undercount mechanism reproduced  
**Sources:** [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/sendqueue.go)

**Evidence.** `outgoingWeight` counts only a fixed 1 KiB overhead, body strings, and decoded file bytes. It omits subject, address/display-name strings, attachment metadata, and threading references. A large subject can consume most of the 34 MiB request allowance but contribute nothing to the queue's byte accounting. JSON and base64 decoding occur before budget acquisition.

**Impact.** The 128 MiB estimate is not a bound on accepted composition memory, and concurrent requests can allocate substantial memory before any admission decision. This is authenticated resource exhaustion, not a demonstrated unauthenticated exploit.

**Local patch — include retained fields:**

```go
func outgoingWeight(out *mail.Outgoing) int64 {
    n := sendOverhead + int64(len(out.Subject)+len(out.Text)+len(out.HTML)+len(out.InReplyTo))
    for _, addresses := range [][]mail.Address{{out.From}, out.To, out.Cc, out.Bcc} {
        for _, address := range addresses {
            n += int64(len(address.Name) + len(address.Email)) + 64
        }
    }
    for _, ref := range out.References { n += int64(len(ref)) + 16 }
    for _, att := range out.Attachments {
        n += int64(len(att.Data)+len(att.Filename)+len(att.ContentType)) + 64
    }
    return n
}
```

Add a small global decode semaphore **before** JSON allocation and release it on every handler exit. Limit recipient count and individual header sizes before parsing/formatting them. These are product policy limits, not substitutes for correctly folding MIME headers.

```go
var sendDecodeSlots = make(chan struct{}, 2)

// At the beginning of handleSend, after authentication middleware:
select {
case sendDecodeSlots <- struct{}{}:
    defer func() { <-sendDecodeSlots }()
case <-r.Context().Done():
    return
 default:
    w.Header().Set("Retry-After", "2")
    writeProblem(w, http.StatusServiceUnavailable, "Send Busy", "Retry without discarding the draft")
    return
}
```

The queue estimate also needs headroom for MIME/base64 expansion and temporary copies during rendering. Measure peak memory in tests; do not claim `outgoingWeight` alone is a strict process-memory ceiling. The existing wire cap and Graph attachment preflight are useful and should remain.

**Regression tests.** Submit large subjects/display names with tiny bodies and concurrent near-limit requests. Assert bounded accepted jobs, correct byte rejection, early decode admission, and no leaked permits after every failure/cancellation path.


<a id="f15"></a>

### F15 — A failed attachment stream can look like a successful truncated download

**Severity:** Medium  
**Status:** Confirmed; HTTP behavior reproduced  
**Sources:** [`classify.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/classify.go), [`dashboard/src/app/lib/api.ts`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/lib/api.ts)

**Evidence.** `handleAttachment` performs `io.Copy`, logs an error, and returns normally. It does not set a reliable expected content length or abort the response. When the provider fails after some bytes, the HTTP server can terminate the response cleanly; a client reading a blob then sees a successful partial file.

**Impact.** The user can save a corrupt attachment without an error. This is distinct from account ZIP export, which already stages a complete archive and checks writes before sending headers.

**Local patch:**

```go
if _, err := io.Copy(w, rc); err != nil {
    a.log.Warn("attachment stream interrupted", "account", acct,
        "message", msgID, "part", partID, "err", err)
    // A normal return could terminate a partial 200 response successfully.
    // The HTTP server recognizes ErrAbortHandler and aborts the stream.
    panic(http.ErrAbortHandler)
}
```

Confirm that any recovery middleware does not swallow `http.ErrAbortHandler` and convert it into a normal response; re-panic it. Alternatively spool the attachment to a bounded temporary file, verify successful provider EOF and any trustworthy expected size, and only then send it with `Content-Length`. Never trust an unverified provider size to allocate unbounded memory.

**Regression tests.** Use a reader that emits a prefix and then an error. With the current behavior the local test receives a 200 and no read error. With an aborted response, the client must receive an error rather than a successful file. Also test failure before the first byte and a disconnected client.


<a id="f16"></a>

### F16 — Synchronous sync reports success even when product finalization fails

**Severity:** Medium  
**Status:** Confirmed  
**Sources:** [`accounts.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/accounts.go), [`mail.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/mail.go)

**Evidence.** `triggerSync?wait=1` trusts the error returned by `syncAccount`. `syncAccount` calls `finishSync` but returns only the provider error. `finishSync` already records reconciliation failures, yet returns `void`. A successful provider fetch followed by failed cleanup, retention, or classification therefore returns `synced` to the waiting caller.

**Impact.** The synchronous API result can disagree with persisted status. This is a residual observability bug, not a claim that reconciliation errors are still always cleared in the database.

**Integration implementation — replace the two functions in `accounts.go`:**

```go
func (a *App) syncAccount(ctx context.Context, acct mail.AccountID) error {
    releaseUse, ok := a.beginAccountUse(acct)
    if !ok { return fmt.Errorf("account %s is being deleted", acct) }
    defer releaseUse()
    cred, err := a.Token(ctx, acct)
    if err != nil { return a.finishSync(ctx, acct, nil, err) }
    adapter, release, err := newResolver()(ctx, acct, cred)
    if err != nil { return a.finishSync(ctx, acct, nil, err) }
    defer release()
    reports, syncErr := a.eng.SyncAccount(ctx, acct, adapter)
    return a.finishSync(ctx, acct, reports, syncErr)
}

func (a *App) finishSync(ctx context.Context, acct mail.AccountID,
    reports []mail.SyncReport, syncErr error) error {
    if ctx.Err() != nil { ctx = context.Background() }
    ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
    defer cancel()
    changed := false
    for _, report := range reports {
        changed = changed || report.Created+report.Updated+report.Deleted > 0
    }
    var accountID, uid string
    if syncErr != nil {
        err := a.db.QueryRowContext(ctx, `
            UPDATE email_accounts SET last_error=$1 WHERE mirror_account_id=$2
            RETURNING id,user_id`, syncErr.Error(), string(acct)).Scan(&accountID, &uid)
        if err != nil { return errors.Join(syncErr, fmt.Errorf("record sync error: %w", err)) }
        a.events.publish(uid, syncEvent{Type:"sync-finished", AccountID:accountID,
            Changed:changed, Error:syncErr.Error()})
        return syncErr
    }
    if err := a.db.QueryRowContext(ctx, `
        UPDATE email_accounts SET last_sync_at=now() WHERE mirror_account_id=$1
        RETURNING id,user_id`, string(acct)).Scan(&accountID, &uid); err != nil {
        return fmt.Errorf("record provider sync: %w", err)
    }
    var failures []error
    if err := a.cleanupMirrorOrphans(ctx, uid, acct); err != nil {
        failures = append(failures, fmt.Errorf("orphan cleanup: %w", err))
    }
    var days int
    if err := a.db.QueryRowContext(ctx,
        `SELECT retention_days FROM email_accounts WHERE mirror_account_id=$1`,
        string(acct)).Scan(&days); err != nil {
        failures = append(failures, fmt.Errorf("retention policy: %w", err))
    } else if err := a.applyAccountRetention(ctx, uid, acct, days); err != nil {
        failures = append(failures, fmt.Errorf("retention: %w", err))
    }
    if err := a.classifyUser(ctx, uid); err != nil {
        failures = append(failures, fmt.Errorf("classification: %w", err))
    }
    reconcileErr := errors.Join(failures...)
    var lastError any // SQL NULL on success.
    if reconcileErr != nil { lastError = "reconcile failed after sync: " + reconcileErr.Error() }
    _, recordErr := a.db.ExecContext(ctx,
        `UPDATE email_accounts SET last_error=$1 WHERE mirror_account_id=$2`,
        lastError, string(acct))
    if recordErr != nil {
        return errors.Join(reconcileErr, fmt.Errorf("record final status: %w", recordErr))
    }
    event := syncEvent{Type:"sync-finished", AccountID:accountID, Changed:changed}
    if reconcileErr != nil {
        event.Error = "reconcile failed after sync"
    } else if changed {
        a.sendPushForUser(ctx, uid)
    }
    // Also notify when an earlier error was cleared but no mail changed.
    a.events.publish(uid, event)
    return reconcileErr
}
```

The scheduler's existing void callback must be wrapped to log the returned error:

```go
// Use this closure where the existing AfterSync callback is assigned.
func(ctx context.Context, acct mail.AccountID, reports []mail.SyncReport, err error) {
    if finishErr := a.finishSync(ctx, acct, reports, err); finishErr != nil {
        a.log.Error("sync finalization failed", "account", acct, "err", finishErr)
    }
}
```

Keep finalization in the owned task lifecycle from I08. An interrupted client's detached finalization must still be tracked until completion. Provider success and product readiness may also be exposed separately in structured API status. R12 separately addresses policy mutations that commit before their effects.

**Regression tests.** Make provider sync succeed, then fail classification, retention, and the final status write individually. `wait=1` must not return a clean `synced` result. Verify successful recovery clears the persisted error and emits an event even if no messages changed. Test the scheduler wrapper and canceled contexts.


<a id="f17"></a>

### F17 — Byte slicing can produce invalid UTF-8 names and filenames

**Severity:** Low  
**Status:** Confirmed; mechanism reproduced  
**Sources:** [`auth.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/auth.go), [`agent.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/agent.go), [`mail-engine/send.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/mail-engine/send.go)

**Evidence.** Some user-facing names/metadata are bounded using byte slices such as `[:80]`; attachment filename sanitization retains the last 200 bytes. A cut can split a multibyte character. Unicode-aware truncation already exists in `safeExportName`, but it is not consistently used elsewhere.

**Impact.** Valid non-ASCII input can become invalid UTF-8 before storage or MIME formatting, producing a failed request or malformed filename. This is not a proven credential bypass.

**Local helper implementation:**

```go
func utf8Prefix(s string, maxBytes int) string {
    if maxBytes <= 0 { return "" }
    s = strings.ToValidUTF8(s, "\uFFFD")
    if len(s) <= maxBytes { return s }
    end := maxBytes
    for end > 0 && !utf8.RuneStart(s[end]) { end-- }
    return s[:end]
}
```

Use it for display names and user-agent metadata with explicit byte limits; reject invalid request text where replacement is inappropriate. For filenames, retain a bounded UTF-8 prefix and preserve a short sanitized extension separately rather than slicing arbitrary trailing bytes. Import `unicode/utf8`. Do not apply character truncation to opaque tokens, hashes, or raw attachment content.

**Regression tests.** Cut at every byte position through accented text, emoji, and CJK filenames. Assert valid UTF-8, the stated byte limit, and a stable nonempty fallback filename.


<a id="f18"></a>

### F18 — The mailbox connection form trims significant password whitespace

**Severity:** Medium  
**Status:** Confirmed; mechanism reproduced  
**Sources:** [`dashboard/src/app/views/AccountsView.tsx`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/views/AccountsView.tsx)

**Evidence.** `ConnectForm.submit` calls `String(v).trim()` for every form value, including `password`. The resulting secret is not the value the user entered.

**Impact.** A valid password containing leading/trailing spaces cannot be submitted faithfully. The problem is client-side; changing provider credentials or server authentication is unnecessary.

**Local patch:**

```ts
data.forEach((value, key) => {
  const raw = String(value);
  const text = key === "password" ? raw : raw.trim();
  if (text === "" || (key === "backfill_days" && text === "default")) return;
  body[key] = ["port", "smtp_port", "backfill_days"].includes(key)
    ? Number.parseInt(text, 10)
    : text;
});
```

Validate numeric fields explicitly and keep whitespace preservation limited to secret values; normalizing addresses/hosts remains appropriate. Test a password with leading spaces, trailing spaces, and embedded spaces, and assert exact equality at the API boundary.


<a id="f19"></a>

### F19 — The reader ignores body_status and mislabels empty or failed bodies as sync-in-progress

**Severity:** Medium  
**Status:** Confirmed  
**Sources:** [`classify.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/classify.go), [`dashboard/src/app/reader/Thread.tsx`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/reader/Thread.tsx), [`dashboard/src/app/reader/Body.tsx`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/reader/Body.tsx)

**Evidence.** The server now returns `body_status` with `ready`, `missing`, or `failed`, but `ThreadMessage` passes only body/html to `MessageBody`. `MessageBody` displays the same “not fetched yet — sync in progress” fallback whenever plain text is empty. A genuinely empty ready body and a failed fetch are indistinguishable. Some credential/dial failures also leave the server status at `missing` rather than `failed`.

**Impact.** Users receive a false progress promise and no useful retry/error state. The eight-body eager-fetch limit is sensible, but the remaining bodies need an explicit route to availability.

**Integration implementation:**

```tsx
type BodyStatus = "ready" | "missing" | "failed";

function BodyState({ status, html, text, retry }: {
  status: BodyStatus; html?: string; text: string; retry: () => void;
}) {
  if (status === "failed") return (
    <div role="alert">Message content could not be fetched.
      <button type="button" onClick={retry}>Retry</button>
    </div>
  );
  if (status === "missing") return (
    <div>Message content has not been loaded.
      <button type="button" onClick={retry}>Load message</button>
    </div>
  );
  if (!html && !text) return <div>This message has an empty body.</div>;
  return null; // The caller renders the existing sanitized MessageBody here.
}
```

Extend `Message` and the reader props to carry the status; render the existing sanitized HTML/text component only after these states are handled. The retry must bypass stale in-memory response caching and fetch the specific missing body, rather than repeatedly reopening the first eight messages of a long thread. Set a failed status on credential/dial failures as well. Do not treat an authoritative empty body as an error or re-fetch it forever.

**Regression tests.** Empty-ready body, failed token, failed body read, a thread longer than the eager cap, offline fallback, and a successful explicit retry.


<a id="f20"></a>

### F20 — Undoing a pin can delete a pin that existed before the operation

**Severity:** Medium  
**Status:** Confirmed  
**Sources:** [`board.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/board.go), [`dashboard/src/app/lib/actions.ts`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/lib/actions.ts)

**Evidence.** `handleBoardPin` uses an upsert that returns the existing card ID and resets its done state. `pinThreads` records returned IDs and its undo removes those cards. It does not distinguish newly created pins from pre-existing pins. Restoring a removed thread pin by account/thread alone also cannot restore all prior card metadata.

**Impact.** Pinning an already pinned thread and undoing can erase the original pin, including its note/state. A bulk operation can partially apply without retaining an exact reversible result.

**Integration implementation.** Return a mutation result containing `created`, the prior card state, and a revision; undo only changes caused by that operation. The server should obtain this under a transaction/owner lock and use compare-and-swap revisions for undo.

```sql
-- Within the board-pin transaction, after acquiring the owner lock:
SELECT id, title, note, done_at
FROM board_cards
WHERE user_id = $1 AND account_id = $2 AND thread_key = $3
FOR UPDATE;

-- When no row exists, insert and report created=true.
-- When a row exists, report created=false and its prior values before updating.
-- Add a revision column, increment it on mutation, and return the new revision.
```

```ts
interface PinResult {
  card_id: string;
  created: boolean;
  revision: number;
  before?: { title: string; note: string; done_at: string | null };
}
// Undo protocol:
// created=true: delete only WHERE id/user/revision still match.
// created=false: restore before only WHERE id/user/revision still match.
// A mismatch is an explicit conflict, not permission to erase a later edit.
```

Do not simply skip undo for pre-existing cards when the upsert changed their done state; restore that change. For bulk actions return per-item success/preimage or make the whole batch atomic. Pins surviving account disconnection are explicitly intentional in the source and are not reported as a bug here.

**Regression tests.** Existing pin with a note, completed pin, new pin, partial bulk failure, and an intervening note edit before undo.


<a id="f21"></a>

### F21 — HTML sanitization drops body-level presentation before deciding whether to apply the dark theme

**Severity:** Medium  
**Status:** Confirmed from serialization order  
**Sources:** [`dashboard/src/app/reader/Body.tsx`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/reader/Body.tsx)

**Evidence.** `cleanLinks` serializes `doc.body.innerHTML`, dropping attributes on the body element itself. `emailHasOwnColors` is then called on that sanitized serialization. A body-level `bgcolor` or `style="background:..."` is no longer visible to the decision, even though the helper's comment says body colors should be detected.

**Impact.** Legitimate mail authored with a white body canvas and dark text can lose that canvas, receive the application dark theme, and become unreadable. This is a rendering/accessibility bug; CSP and the iframe sandbox still provide meaningful execution/network protections.

**Integration implementation.** Preserve only vetted presentation on a wrapper inside the sanitized document before serializing. Do not restore event handlers, arbitrary body attributes, or network-backed backgrounds.

```ts
// Insert near the end of cleanLinks, after existing resource sanitization.
const wrapper = doc.createElement("div");
const bodyStyle = doc.body.getAttribute("style");
if (bodyStyle) wrapper.setAttribute("style", bodyStyle);
const bgcolor = doc.body.getAttribute("bgcolor");
if (bgcolor && typeof CSS !== "undefined" && CSS.supports("color", bgcolor)) {
  wrapper.style.backgroundColor = bgcolor;
}
while (doc.body.firstChild) wrapper.appendChild(doc.body.firstChild);
doc.body.appendChild(wrapper);
return doc.body.innerHTML;
```

Only copy the **already-sanitized** style; keep the current iframe CSP and remote-image stripping. Re-run the resource sanitizer on the final document and test both remote-images-off/on. A more precise implementation returns explicit sanitized canvas metadata to `frameDoc` rather than relying on a wrapper. Do not weaken network policy to preserve an email's presentation.

**Regression tests.** Body `bgcolor`, body inline background, head styles, inline black text on white canvas under dark mode, CSS backgrounds containing external URLs, and malformed markup. Use real browser rendering for visual confirmation; no such browser test was run here.


<a id="f22"></a>

### F22 — Push notification UI uses account-wide subscription state for a device-local action

**Severity:** Low  
**Status:** Confirmed  
**Sources:** [`push.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/push.go), [`dashboard/src/app/views/SecurityView.tsx`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/views/SecurityView.tsx)

**Evidence.** GET `/push` returns `subscribed` from the count of all subscriptions for the user. The button label uses that value. The toggle handler instead asks the current browser's `pushManager.getSubscription()`. When another device is subscribed but this one is not, the UI says “Disable” while clicking it subscribes the current device.

**Local patch:**

```ts
const [deviceSubscribed, setDeviceSubscribed] = useState(false);
async function refreshDevicePushState(): Promise<void> {
  if (!("serviceWorker" in navigator) || !("PushManager" in window)) {
    setDeviceSubscribed(false);
    return;
  }
  const registration = await navigator.serviceWorker.getRegistration();
  const subscription = await registration?.pushManager.getSubscription();
  setDeviceSubscribed(Boolean(subscription));
}
// Use deviceSubscribed for the Enable/Disable label, and refresh it after toggle.
// Keep the server count as a separate “devices registered” indicator.
```

Handle the partial-success case where browser subscription succeeds but API registration fails: offer to finish registering that existing subscription rather than treating the next click as a request to disable it. Likewise surface an `unsubscribe()` result that is false. Avoid waiting indefinitely on `serviceWorker.ready` when no worker is registered.

**Regression tests.** Two browser contexts with only one subscribed; failed server registration; an existing browser subscription with no matching server row; and absent service-worker support.


<a id="f23"></a>

### F23 — Several operational database failures are reported as missing resources or invalid authentication

**Severity:** Medium  
**Status:** Confirmed in inspected call sites  
**Sources:** [`accounts.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/accounts.go), [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/sendqueue.go), [`agent.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/agent.go), [`push.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/push.go), [`auth.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/auth.go)

**Evidence.** Examples include `triggerSync` treating every account lookup error as 404, `updateBackfill` treating every `UPDATE ... RETURNING` error as 404, `handleSend` treating every account query failure as “connect an account first,” and push status discarding its count-query error. Agent/session lookup boundaries also need to preserve the distinction between an invalid credential and an unavailable database. This is not a claim that all authentication handlers are wrong; several already fail closed correctly.

**Impact.** Transient infrastructure failures produce misleading user advice, hide failures from monitoring, and can cause the offline queue to classify a retryable failure as a permanent rejection. Reporting 404/401 is not harmless when clients attach retry semantics to status codes.

**Local pattern — classify lookup results explicitly:**

```go
func writeLookupProblem(w http.ResponseWriter, err error, noun string) {
    if errors.Is(err, sql.ErrNoRows) {
        writeProblem(w, http.StatusNotFound, "Not Found", "no such "+noun)
        return
    }
    w.Header().Set("Retry-After", "2")
    writeProblem(w, http.StatusServiceUnavailable, "Temporarily Unavailable",
        "The operation could not be completed; retry without discarding your changes")
}
```

Log the internal error at the call site, with a request ID but without tokens or provider secrets. Authentication helpers should return `(identity, valid, error)` or an equivalent typed result: missing/expired credentials yield 401, infrastructure errors yield 503, and neither path authorizes access. A push count failure should return an error, not `subscribed:false`. Preserve a genuine 404 for nonexistent owned resources without revealing another owner's resources.

**Regression tests.** For each lookup test `sql.ErrNoRows`, connection failure, deadline, permission error, and a real successful row. Assert retry semantics and that no infrastructure failure changes user state or is silently converted to an empty list.

## 4. Existing risks that remain open

These are **26 carried-forward implementation work items**, consolidated from the repository's [open audit register](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/AUDIT_OPEN.md). They are not 26 additional newly discovered vulnerabilities. Some overlap the more specific residual defects above. “Code-confirmed” means the relevant implementation was inspected in this review; “register-backed” means the prior register is the basis and the affected engine path was not fully re-audited here.

| ID | Severity | Remaining problem and suggested correction | Evidence basis / implementation |
|---|---|---|---|
| R01 | High | The undo-send queue is in memory; accepted work, delivery errors, and Sent-copy repair do not survive restart. Add an encrypted durable outbox with explicit pending/submitting/accepted/uncertain/failed states and observable status. Never blindly retry an ambiguous SMTP outcome. | Code-confirmed: `sendqueue.go`, `mail-engine/send.go`; I01. |
| R02 | High | Credential changes can race an already-verified login, which creates a new session after revocation. Add a user auth epoch and bind session creation to the verified epoch under the owner row lock. | Code-confirmed: `auth.go`; I02. |
| R03 | Medium | A standing session authorizes adding factors, recovery replacement, and agent-token creation without fresh proof. This is an accepted policy, not automatically an auth bypass. Add short-lived, action/session-bound reauthentication grants for sensitive operations. | Code-confirmed: `auth.go`, `agent.go`; I02. |
| R04 | High | TOTP guessing limits are not account-wide across multiple source peers. Add shared per-account attempt budgets plus existing per-peer limits, preserving a recoverable sign-in path. TOTP is deliberately an alternative login factor, not an implemented second factor. | Code-confirmed: `auth.go`; I02. |
| R05 | Medium | Single-owner identity creation/configuration is not enforced as one installation-wide transaction and stable identity. Bind the owner through an explicit singleton record, not a mutable email lookup; reconcile existing extra rows before enforcing it. | Code-confirmed: bootstrap/user helpers; I03. |
| R06 | Medium | Setup/origin/token changes mutate a shared `Config` that is read outside the WebAuthn mutex. Publish immutable runtime snapshots and remove all in-place writes. Run concurrent setup/status/auth tests under the race detector. | Code-confirmed: `app.go`, `setup.go`, `config.go`, `auth.go`; I03. |
| R07 | High | Multiple tabs or a lost mutation response can replay the same operation. A tab lock helps but does not provide server-side idempotency. Add request IDs, body-hash validation, and stored results in the same SQL transaction as each queueable mutation. | Code-confirmed: `offline.ts`, mutation handlers; I04. |
| R08 | High | Offline identity uses a mutable email, legacy global draft keys, and no comprehensive async generation fence. Introduce immutable installation/user scope, transactional generations, cross-tab invalidation, and migration/cleanup of legacy private keys. | Code-confirmed: `offline.ts`, `api.ts`, `store.ts`, `App.tsx`; I04 and F11–F12. |
| R09 | Medium | Bucket/search endpoints cap results without cursor pagination, making older results inaccessible. Use stable keyset cursors with an account-qualified tie-breaker, and expose `next_cursor`. | Code-confirmed: `classify.go`; I05. |
| R10 | Medium | Relative `until_days` replay/undo recalculates the intended snooze date later. Capture an absolute return instant at user intent time, retain timezone semantics, and replay that value unchanged. | Code-confirmed: actions/handler; I05. |
| R11 | Medium | Bulk/thread undo does not preserve every message's prior filing/read/snooze state. Use transactionally captured preimages and revision-checked undo; do not flatten a mixed thread to one guessed prior bucket. | Code-confirmed: actions/handler; I05 and F20. |
| R12 | High | Retention/backfill settings are persisted before all resulting mutations finish. Either transact a bounded transition or persist requested/applied generations and show pending/failed state until reconciliation completes. | Code-confirmed: `accounts.go`; I06. |
| R13 | High | Cursor reset can destroy live mirror state before replacement is complete; enumeration completeness is held in memory and not durable across page limits/restarts. Stage scans and commit absence/deletion decisions only after complete durable enumeration. | Code-confirmed reset and in-memory scan tracking in `mail-engine/sync.go`; detailed provider/store behavior register-backed; I06. |
| R14 | High | Retention and body/sync writers lack a shared transactional lifecycle boundary and comprehensive referential constraints. A writer can recreate local data after cleanup. Share account-generation checks and add validated foreign keys/cascades where appropriate. | Code-confirmed application retention/gates; engine-wide race register-backed; I06/I08. |
| R15 | Medium | Widening retention does not guarantee re-fetching old unchanged mail once incremental cursors have advanced. Persist a reconciliation request until a complete staged rescan finishes. | Register-backed; I06. |
| R16 | Medium | Identity promotion now writes the replacement envelope first, but product filing/receipts/body relationships are not migrated atomically to the new identity. Introduce a transactional identity map and migrate dependent rows together. | Register-backed, ordering protection acknowledged; I06. |
| R17 | Low | Gmail attachment presence is inferred from MIME structure that metadata-only responses omit. Represent unknown/present/absent explicitly and resolve unknown lazily rather than falsely claiming no attachment. | Register-backed; I06. |
| R18 | Medium | Graph default message IDs change on moves; no coordinated immutable-ID rollout exists. Translate existing IDs and their references before enabling the preference throughout requests. A header-only patch is unsafe for existing mirrors. | Register-backed; current Microsoft primary documentation checked; I06. |
| R19 | Medium | JSON, provider-response, attachment, thread, and export memory/disk budgets are incomplete. Add route-specific input limits, bounded readers, admission limits, deadlines, and a private staged-export budget. | Code-confirmed several routes/export; provider-wide scope register-backed; I07/F14–F15. |
| R20 | High | Startup reapplies schema statements without a version/checksum ledger or installation-wide migration lock. Introduce one migration owner and rehearsed baseline/upgrade paths covering product and engine tables. Pool caps already exist. | Code-confirmed startup schema application; I07. |
| R21 | Medium | Shutdown does not join every application-owned goroutine before closing pools. Track work, stop admission, cancel, and join; durable jobs make interrupted sends recoverable/observable. | Code-confirmed `cmd_serve.go`, `accounts.go`, queue/loops; I08. |
| R22 | Medium | A global lifecycle lock makes unrelated account deletion wait behind long-running work; some account/refresh locks ignore cancellation. Use per-account cancellable admission and coalesce redundant sync requests. | Code-confirmed `app.go`, `oauth.go`, `mail-engine/sync.go`; I08. |
| R23 | Medium | Default host publication exposes plaintext HTTP; public-origin detection trusts forwarded headers without verifying proxy peers. Default to loopback, require explicit HTTPS/proxy configuration, and validate the MCP target origin too. | Code-confirmed `compose.yaml`, `setup.go`, `config.go`, `mcp/client.go`; I07. |
| R24 | Medium | CI now runs engine PostgreSQL integration/race checks, but product SQL/upgrade/rollback coverage remains incomplete. Add a real product database harness and migration fixtures; ensure publication requires it. | Code-confirmed workflow; test-coverage extent register-backed, not an exhaustive census of tests; I09/F02. |
| R25 | High | Untrusted push endpoints and provider-advertised URLs can reach unintended network destinations without comprehensive egress policy. Enforce HTTPS/origin policy, validate resolved addresses at dial time, and recheck redirects. | Code-confirmed `push.go`; broader JMAP behavior register-backed; I07. |
| R26 | Medium | Operator-supplied secret-key entropy, ciphertext versioning/context binding/rotation, and agent expiry/scopes are incomplete. Add a versioned keyring and authenticated context, scoped expiring tokens, and migration/rotation tests. | Code-confirmed `secretbox.go`, `agent.go`, `setup.go`; I10. |

## 5. Cross-cutting implementations for the existing risks

The following implementations are deliberately coordinated designs. They include executable SQL/helper logic but require the named application wiring, migration handling, and tests. They do not claim that a few isolated snippets complete an outbox, provider protocol, or schema migration.

<a id="i01"></a>

### I01 — Durable outbox and uncertain-delivery state (R01)

The server must distinguish accepting a composition from a provider accepting a message. Persist the complete composition and a stable RFC Message-ID before returning queue acceptance. Store attachment bytes in encrypted durable storage or the encrypted payload, not only in a closure. Bind jobs to the authenticated owner and public account row.

```sql
CREATE TABLE outbox_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    account_id uuid NOT NULL REFERENCES email_accounts(id) ON DELETE CASCADE,
    message_id_header text NOT NULL,
    payload_ciphertext text NOT NULL,
    state text NOT NULL DEFAULT 'queued'
        CHECK (state IN ('queued','submitting','accepted','failed','cancelled','uncertain')),
    not_before timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz,
    lease_version bigint NOT NULL DEFAULT 0,
    accepted_at timestamptz,
    sent_copy_pending boolean NOT NULL DEFAULT false,
    error_code text,
    UNIQUE (user_id, message_id_header)
);
CREATE INDEX outbox_ready ON outbox_jobs(not_before, id) WHERE state='queued';

-- Claim one due job, atomically; execute under a short transaction.
WITH candidate AS (
    SELECT id FROM outbox_jobs
    WHERE state='queued' AND not_before <= now()
    ORDER BY not_before, id
    FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE outbox_jobs j
SET state='submitting', lease_until=now()+interval '2 minutes',
    lease_version=lease_version+1, updated_at=now()
FROM candidate c WHERE j.id=c.id
RETURNING j.*;

-- Undo: only a still-queued job in the published undo window can be cancelled.
UPDATE outbox_jobs SET state='cancelled', updated_at=now()
WHERE id=$1 AND user_id=$2 AND state='queued' AND not_before > now()
RETURNING id;

-- Completion is fenced against an obsolete worker.
UPDATE outbox_jobs
SET state='accepted', accepted_at=now(), lease_until=NULL,
    sent_copy_pending=$4, updated_at=now()
WHERE id=$1 AND state='submitting' AND lease_version=$2 AND user_id=$3;

-- A worker crash is NOT evidence that the provider did not accept the mail.
UPDATE outbox_jobs
SET state='uncertain', lease_until=NULL, error_code='worker_outcome_unknown', updated_at=now()
WHERE state='submitting' AND lease_until < now();
```

Return `202` with a job ID and an authenticated status endpoint. Stream/poll state changes to the browser. Keep a recoverable composition until a definitive accepted/cancelled outcome; on definitive failure offer edit/retry. Cancel/account-delete races must use the same lifecycle gate and transaction checks. Never put raw provider errors containing credentials into the public status record.

Extend `mail.Outgoing` with a validated optional fixed Message-ID, use it in MIME rendering, and persist it with the job. A stable Message-ID aids reconciliation but **does not guarantee SMTP deduplication**. A timeout after SMTP DATA or a lost HTTP response is `uncertain`, not automatically retryable. Only failures known to precede submission may return to queued automatically. Reconcile provider Sent state where supported; otherwise require explicit user review. Track SMTP delivery and Sent-folder APPEND repair separately so an APPEND failure never causes a second delivery.

Tests must kill the process before claim, during submission, after provider acceptance but before the DB update, during Sent-copy repair, and while undo/delete races with claim. The expected result must be recoverable state, not an unsupported exactly-once promise.

<a id="i02"></a>

### I02 — Auth epochs, fresh proof, and distributed attempt budgets (R02–R04)

```sql
ALTER TABLE users ADD COLUMN auth_epoch bigint NOT NULL DEFAULT 0;
ALTER TABLE auth_sessions ADD COLUMN auth_epoch bigint NOT NULL DEFAULT 0;

CREATE TABLE reauth_grants (
    token_hash text PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_hash text NOT NULL,
    purpose text NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at timestamptz
);

-- Consume a grant in the SAME transaction as the sensitive mutation.
UPDATE reauth_grants SET used_at=now()
WHERE token_hash=$1 AND user_id=$2 AND session_hash=$3 AND purpose=$4
  AND used_at IS NULL AND expires_at > now()
RETURNING token_hash;

CREATE TABLE auth_attempt_windows (
    scope_hash text NOT NULL,
    window_start timestamptz NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    PRIMARY KEY (scope_hash, window_start)
);

-- Atomic shared budget. Parameter $2 is the same normalized window for all workers.
INSERT INTO auth_attempt_windows(scope_hash, window_start, attempts)
VALUES ($1, $2, 1)
ON CONFLICT (scope_hash, window_start)
DO UPDATE SET attempts=auth_attempt_windows.attempts+1
RETURNING attempts;
```

The account key should be a normalized non-secret identifier/hash; do not record attempted TOTP values. Apply account and peer budgets before verification, clean up expired windows, and avoid permanent account lockout. The exact thresholds are deployment policy and need abuse/load tests. Keep the existing KDF concurrency cap and one-time recovery/TOTP-consumption protections.

Capture the owner's epoch with the credential used for verification. After verification, create the session only while holding the same user lock used by credential changes:

```go
var errStaleAuthentication = errors.New("credentials changed during sign-in")

func createSessionAtEpoch(ctx context.Context, db *sql.DB, uid string,
    verifiedEpoch int64, hash, userAgent string, expires time.Time) error {
    tx, err := db.BeginTx(ctx, nil)
    if err != nil { return err }
    defer tx.Rollback()
    var current int64
    if err := tx.QueryRowContext(ctx,
        `SELECT auth_epoch FROM users WHERE id=$1 FOR UPDATE`, uid).Scan(&current); err != nil {
        return err
    }
    if current != verifiedEpoch { return errStaleAuthentication }
    _, err = tx.ExecContext(ctx, `
        INSERT INTO auth_sessions(id_hash,user_id,expires_at,user_agent,auth_epoch)
        VALUES ($1,$2,$3,$4,$5)`, hash, uid, expires, userAgent, current)
    if err != nil { return err }
    return tx.Commit()
}
```

Increment `users.auth_epoch` in the transaction that removes/replaces credentials or otherwise revokes access. If the current session is intentionally retained, update its epoch in that transaction; delete other sessions. Every session authentication query must join the owner and require equal epochs in addition to expiry/revocation checks. Each login factor must capture and recheck the epoch; adding it only to password login leaves the other races open. Credential existence/one-time consumption must be checked in the appropriate transaction too.

For fresh proof, re-use the existing factor-verification machinery to issue a random, hashed, short-lived grant bound to current session and action (e.g. `create-agent-token` or `replace-recovery-codes`). Consume it once alongside the sensitive write. A confirmation string or recently opened settings page is not proof of the factor. Do not change the deliberate standalone-TOTP policy while describing it as an accidental missing second factor.

<a id="i03"></a>

### I03 — Stable owner identity and immutable runtime state (R05–R06)

Introduce an installation-owner binding rather than deriving identity from a configurable email on each path:

```sql
CREATE TABLE installation_owner (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    user_id uuid NOT NULL UNIQUE REFERENCES users(id)
);
-- Bootstrap acquires an installation advisory transaction lock, then:
-- 1. Read this row. If present, reuse its immutable user_id.
-- 2. If absent, create the users row and this binding in the SAME transaction.
-- Existing installations require an explicit owner-selection/baseline migration.
```

All setup/password/passkey bootstrap paths must call that same transaction. Configuration email changes update the bound user record; they must not implicitly create another owner. Before enforcing the binding, inspect and reconcile existing users, credentials, and account ownership. Never delete “extra” users automatically to make a migration succeed.

Publish one immutable runtime snapshot after constructing the matching WebAuthn instance. An `atomic.Pointer` or one consistently used RWMutex is sufficient only when **all** reads/writes use it:

```go
type runtimeSnapshot struct {
    Config Config
    WebAuthn *webauthn.WebAuthn
    SetupTokenCreated time.Time
}

type runtimeConfig struct { current atomic.Pointer[runtimeSnapshot] }

func (r *runtimeConfig) Load() *runtimeSnapshot { return r.current.Load() }
func (r *runtimeConfig) Publish(next runtimeSnapshot) {
    // Never mutate next or its nested state after publication.
    r.current.Store(&next)
}
```

Prepare the candidate on a copy, validate origin and construct WebAuthn, then atomically publish both together. Move mutable setup-token fields into the same guarded state or a separate coherent state machine. Do not leave direct `a.cfg` reads/writes in other handlers while protecting only the setter. A ceremony must remain bound to the origin/configuration used when it began.

<a id="i04"></a>

### I04 — Idempotent replay and transactional offline generations (R07–R08)

Use an immutable `(installation_id, user_id)` from authenticated server state, not an email address, for every private namespace. Include the scope in cache, queue, and attachment keys. Migrate `es-drafts`, `es-draft-*`, and image-consent keys; they otherwise remain outside an IndexedDB-only wipe.

**Server mutation deduplication schema:**

```sql
CREATE TABLE api_mutation_results (
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    request_id text NOT NULL CHECK (length(request_id) BETWEEN 1 AND 128),
    method text NOT NULL,
    path text NOT NULL,
    body_hash bytea NOT NULL,
    status integer,
    response_json jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, request_id)
);
```

For each queueable SQL mutation: begin a transaction; insert its request ID/body hash with `ON CONFLICT DO NOTHING`; if another completed result exists, compare method/path/body hash and return the recorded response (409 on reuse with different input); otherwise apply the mutation and save the response in that same transaction. Commit before replying. The conflict insert waits for the competing transaction, so a lost HTTP response can safely return the committed result on retry. Never commit a placeholder row separately from the mutation. Do not use this local-transaction protocol to pretend an external provider call is atomic; sending uses I01.

Send the queue item's durable ID as `Idempotency-Key`. A Web Lock coalesces tabs, while server deduplication handles missing browser support and crashes:

```ts
async function replayWithTabLock(owner: string): Promise<ReplaySummary> {
  if (navigator.locks) {
    return navigator.locks.request(`lullmail:replay:${owner}`, () => replayMutations());
  }
  // Must still be single-flight in this tab; server idempotency handles other tabs.
  return replayMutations();
}
```

Add an IndexedDB `meta` store holding the active scope/generation. The same transaction that clears private records must advance it. Every delayed writer checks the generation **inside its write transaction**, not only in JavaScript before opening one:

```ts
interface StoredScope { key: "active-scope"; owner: string; generation: number }
function putScopedCache(db: IDBDatabase, scope: OfflineScope,
  path: string, value: unknown): Promise<void> {
  return new Promise((resolve, reject) => {
    const tx = db.transaction(["meta", "responses"], "readwrite");
    tx.oncomplete = () => resolve();
    tx.onabort = () => reject(tx.error ?? new Error("Offline generation changed"));
    tx.onerror = () => {}; // onabort reports the transaction result.
    const lookup = tx.objectStore("meta").get("active-scope");
    lookup.onsuccess = () => {
      const current = lookup.result as StoredScope | undefined;
      if (!current || current.owner !== scope.owner || current.generation !== scope.generation) {
        tx.abort();
        return;
      }
      tx.objectStore("responses").put({
        key: scope.owner + "\n" + path, owner: scope.owner,
        generation: scope.generation, savedAt: Date.now(), value,
      });
    };
  });
}
```

Apply this pattern to queue/draft writes, not just cache responses. The generation must come from a transactionally updated persisted counter, not independent per-tab counters. BroadcastChannel messages tell other tabs to suspend and clear in-memory state; the persisted check remains authoritative if a tab misses the message. Cancel in-flight fetches and invalidate UI generations too.

Legacy localStorage cleanup cannot be atomic with IndexedDB. First make the old scope inaccessible, then perform and verify both cleanup phases; resume under the new scope only after success. On failure, retain a visible cleanup-required state. Bound queue age and align server deduplication retention with it so very old queued requests cannot outlive their idempotency receipts unnoticed.

<a id="i05"></a>

### I05 — Pagination, absolute snooze, and exact undo (R09–R11)

For each endpoint, use a total ordering and fetch `limit+1` rows. Include account identity in the ordering because IDs from different providers/accounts may coincide:

```sql
-- Substitute the endpoint's existing ownership/filtering joins.
SELECT m.account_id, m.id, m.received_at
FROM mail_messages m
JOIN email_accounts ea ON ea.mirror_account_id=m.account_id
WHERE ea.user_id=$1
  AND (COALESCE(m.received_at, '-infinity'), m.account_id, m.id) < ($2,$3,$4)
ORDER BY COALESCE(m.received_at, '-infinity') DESC, m.account_id DESC, m.id DESC
LIMIT $5;
```

Omit the cursor predicate on page one. Encode/validate all cursor fields, retain the database column's timestamp type, and scope cursors to the query/account/filter version. Return a `next_cursor` only when the extra row exists. Thread-grouped results must paginate the grouped newest-message relation, not paginate raw messages before grouping. Add matching indexes after measuring plans on representative data.

Replace relative replay payloads with an absolute timestamp selected in the user's timezone:

```ts
function snoozePayload(localReturnTime: Date) {
  if (!Number.isFinite(localReturnTime.getTime())) throw new Error("Invalid return time");
  return { action: "set_aside", until: localReturnTime.toISOString() };
}
```

The server validates an RFC3339 instant and a policy horizon but stores/replays that instant unchanged. Decide whether a past return time means immediate unsnooze or a validation conflict; never silently add fresh days during replay. Calendar-day controls need daylight-saving tests. Undo restores the original absolute value, including null.

Add a revision to product filing/card records. Under one transaction, lock affected rows in deterministic order, capture complete preimages, apply the mutation while incrementing revisions, and return an owner-bound undo token. Undo uses a revision check:

```sql
UPDATE hey_messages
SET bucket=$4, read_at=$5, set_aside_until=$6, revision=revision+1
WHERE user_id=$1 AND account_id=$2 AND message_id=$3 AND revision=$7
RETURNING revision;
```

Zero affected rows means a conflict or disappeared record, not success. Do not recreate a message deleted by retention merely to satisfy undo. Decide explicitly whether a batch undo is atomic or returns per-item conflicts. This also fixes the board-pin preimage problem in F20 when applied to cards.


<a id="i06"></a>

### I06 — Staged synchronization, retention generations, and identity migration (R12–R18)

Do not delete the live mailbox at the beginning of a reset. Persist a scan generation, continuation cursor, and the complete set of seen identities across pages and process restarts. Write staged envelopes/memberships separately from the live readable mirror.

```sql
CREATE TABLE mail_scan_generations (
    account_id text NOT NULL,
    mailbox_id text NOT NULL,
    generation uuid NOT NULL,
    cursor text NOT NULL DEFAULT '',
    complete boolean NOT NULL DEFAULT false,
    started_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, mailbox_id, generation)
);
CREATE TABLE mail_scan_seen (
    account_id text NOT NULL,
    mailbox_id text NOT NULL,
    generation uuid NOT NULL,
    message_id text NOT NULL,
    PRIMARY KEY (account_id, mailbox_id, generation, message_id),
    FOREIGN KEY (account_id, mailbox_id, generation)
      REFERENCES mail_scan_generations(account_id, mailbox_id, generation) ON DELETE CASCADE
);
ALTER TABLE email_accounts
    ADD COLUMN policy_requested_generation bigint NOT NULL DEFAULT 0,
    ADD COLUMN policy_applied_generation bigint NOT NULL DEFAULT 0,
    ADD COLUMN reconcile_requested boolean NOT NULL DEFAULT false,
    ADD COLUMN reconciliation_error text;
```

A page transaction must persist staged envelopes, seen IDs, and cursor together. Mark `complete` only on the provider's successful final page. A `MaxPages` return is not completion. Retry/resume the same generation, rather than losing earlier pages' seen set.

The final commit should acquire the account lifecycle/transaction lock, verify that the account and requested policy generation still match, merge staged records, remove **mailbox memberships** absent from the complete set, and only then consider globally unreferenced messages for deletion. Preserve a message still present in another mailbox. Migrate product state before retiring old identities. Do all cross-table changes through a shared transaction interface; two independent pool transactions do not provide atomicity.

```sql
-- Membership sweep, only after a complete generation was verified and locked.
DELETE FROM mail_message_mailboxes mm
WHERE mm.account_id=$1 AND mm.mailbox_id=$2
  AND NOT EXISTS (
      SELECT 1 FROM mail_scan_seen s
      WHERE s.account_id=mm.account_id AND s.mailbox_id=mm.mailbox_id
        AND s.generation=$3 AND s.message_id=mm.message_id
  );

-- Applied policy advances only when the requested generation still matches.
UPDATE email_accounts
SET policy_applied_generation=$2, reconcile_requested=false, reconciliation_error=NULL
WHERE id=$1 AND policy_requested_generation=$2;
```

For retention/backfill, either apply the bounded cleanup and configuration update in one transaction, or store desired versus applied values/generations and expose pending/failed status. Widening retention sets `reconcile_requested=true`; only a complete scan clears it. A sync/body writer must verify the same active account/policy generation before committing. Add foreign keys only after detecting/reconciling existing orphans and confirming mailbox/global-delete semantics; use `NOT VALID` followed by validation where an online rollout requires it.

**Identity promotion.** Persist an old→new mapping and migrate membership, cached body, `hey_messages`, push receipts, and other message references under one unit of work. Resolve duplicate-key merges by explicit policy: preserve read/snooze/user filing rather than choosing whichever write happens last. Keep the prior readability guarantee: the old envelope must not disappear before the replacement is safely committed.

**Graph.** The [Microsoft immutable-ID documentation](https://learn.microsoft.com/en-us/graph/outlook-immutable-id) supplies the translation API and explains the header's per-request behavior. Translate stored default IDs to immutable IDs in batches, migrate all dependent keys, and only then activate the preference for that migrated account. Preserve identifier case. The header is:

```go
req.Header.Set("Prefer", `IdType="ImmutableId"`)
```

A per-account migration/version flag must control this, including send/reply/folder-delta paths; do not toggle it globally before existing IDs have been handled. Moving to a different archive mailbox remains a separate identity boundary.

**Gmail attachment presence.** Use an explicit tri-state in the envelope/API model:

```go
type AttachmentPresence string
const (
    AttachmentsUnknown AttachmentPresence = "unknown"
    AttachmentsAbsent  AttachmentPresence = "absent"
    AttachmentsPresent AttachmentPresence = "present"
)
```

Metadata-only fetches return unknown. A complete MIME/body inspection resolves absent/present and updates the mirror. The UI must not render unknown as proof of no attachment. Measure quota and latency before replacing every metadata sync with full-message reads.

Test partial scans over page limits, restart on every phase boundary, failed envelope writes, folder moves, duplicate identities, retention races, and unchanged old messages after a widened policy. Rollback must leave the prior readable generation intact.

<a id="i07"></a>

### I07 — Resource boundaries, deployment/egress policy, and migrations (R19–R20, R23, R25)

**Request/response limits.** Reuse one strict decoder, with explicit per-route limits rather than one size for every endpoint:

```go
func decodeBoundedJSON(w http.ResponseWriter, r *http.Request, limit int64, dst any) error {
    r.Body = http.MaxBytesReader(w, r.Body, limit)
    dec := json.NewDecoder(r.Body)
    dec.DisallowUnknownFields()
    if err := dec.Decode(dst); err != nil { return err }
    var extra any
    if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
        if err == nil { return errors.New("exactly one JSON document is required") }
        return err
    }
    return nil
}
func readBounded(r io.Reader, limit int64) ([]byte, error) {
    if limit <= 0 || limit > 1<<30 { return nil, errors.New("invalid read limit") }
    data, err := io.ReadAll(io.LimitReader(r, limit+1))
    if err != nil { return nil, err }
    if int64(len(data)) > limit { return nil, errors.New("response exceeds configured limit") }
    return data, nil
}
```

Map `http.MaxBytesError` to 413 rather than generic malformed JSON. Existing account-settings routes already have strict bounded decoding; extend that behavior without regressing their validation. Keep the send allowance distinct from ordinary settings/note bodies. Bound provider error bodies too. Use per-export disk admission, cancellation, private temporary directories, and cleanup of orphaned app-owned staged files after crashes. Bound thread/body pagination and aggregate memory, not only the size of an individual attachment.

Set server idle/read-header limits and route-aware body/provider deadlines. Avoid a blanket short write timeout that breaks SSE or large exports. Streamed responses need integrity behavior such as F15, not merely a log on truncation. The official [Go HTTP documentation](https://pkg.go.dev/net/http#ErrAbortHandler) describes aborting a response with `ErrAbortHandler`.

**Default deployment and origin trust.** Change the development compose binding:

```yaml
ports:
  - "127.0.0.1:8080:8080"
```

Operators exposing the application should explicitly configure a TLS reverse proxy and `PUBLIC_URL`. Accept forwarded host/proto only when `r.RemoteAddr` belongs to a configured trusted proxy range and that proxy replaces untrusted client values. Validate that the public URL is an HTTP(S) origin without userinfo/path/query/fragment; require HTTPS away from an explicit local-development profile. Do not infer a trusted origin from arbitrary forwarded headers. In `mcp/newClient`, reject non-HTTP(S) schemes and non-loopback plaintext targets by default, reject credentials embedded in URLs, and restrict redirects to the configured origin so the agent token is not forwarded to another trust boundary.

**Egress.** Validate initial URL, redirects, effective port, and resolved destination. For push, allow only recognized operator-approved push origins. For JMAP, validate advertised API/download/upload origins against the operator-approved provider policy; an advertised URL is not inherently trusted. Below is the crucial DNS-rebinding-resistant dial step, used with a transport that has `Proxy: nil` and an origin-checking RoundTripper:

```go
func publicDialContext(ctx context.Context, network, address string) (net.Conn, error) {
    host, port, err := net.SplitHostPort(address)
    if err != nil { return nil, err }
    ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
    if err != nil { return nil, err }
    if len(ips) == 0 { return nil, errors.New("host has no addresses") }
    for _, ip := range ips {
        ip = ip.Unmap()
        if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
            blockedSpecialAddress(ip) {
            return nil, errors.New("destination is not permitted")
        }
    }
    dialer := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
    var failures []error
    for _, ip := range ips {
        conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
        if err == nil { return conn, nil }
        failures = append(failures, err)
    }
    return nil, errors.Join(failures...)
}

var specialEgressBlocks = []netip.Prefix{
    netip.MustParsePrefix("0.0.0.0/8"),
    netip.MustParsePrefix("100.64.0.0/10"),
    netip.MustParsePrefix("169.254.0.0/16"),
    netip.MustParsePrefix("192.0.0.0/24"),
    netip.MustParsePrefix("192.0.2.0/24"),
    netip.MustParsePrefix("198.18.0.0/15"),
    netip.MustParsePrefix("198.51.100.0/24"),
    netip.MustParsePrefix("203.0.113.0/24"),
    netip.MustParsePrefix("240.0.0.0/4"),
    netip.MustParsePrefix("2001:db8::/32"),
    netip.MustParsePrefix("64:ff9b::/96"),
    netip.MustParsePrefix("64:ff9b:1::/48"),
    netip.MustParsePrefix("2002::/16"),
    netip.MustParsePrefix("2001::/32"),
}
func blockedSpecialAddress(ip netip.Addr) bool {
    for _, prefix := range specialEgressBlocks { if prefix.Contains(ip) { return true } }
    return false
}
```

This helper is defense in depth, not a complete network policy: origin allowlisting, port restrictions, redirect checks, cloud/network egress controls, and a maintained special-use-address policy remain required. Preserve the original hostname in the URL so TLS certificate/SNI verification still occurs normally. Never disable TLS verification to connect to a pinned address. An explicit self-hosted/private-provider profile may allow operator-selected private destinations; it must not be inferred from untrusted push payloads or JMAP responses. Test IPv4-mapped IPv6, proxy environment variables, redirects, and DNS answers that change between validation and dialing.

**Migration implementation.** Make one component responsible for both product and engine schema upgrades. The following runner serializes transactional migrations and checks their content. It intentionally does not support commands such as concurrent index creation that cannot run in its transaction; provide a separate controlled migration mode for those.

```go
type Migration struct {
    Version int64
    Statements []string
}
func applyMigrations(ctx context.Context, db *sql.DB, migrations []Migration) error {
    tx, err := db.BeginTx(ctx, nil)
    if err != nil { return err }
    defer tx.Rollback()
    if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(73460121)); err != nil {
        return err
    }
    if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS app_schema_migrations (
        version bigint PRIMARY KEY, checksum text NOT NULL,
        applied_at timestamptz NOT NULL DEFAULT now())`); err != nil { return err }
    knownVersions := make(map[int64]bool, len(migrations))
    for _, migration := range migrations { knownVersions[migration.Version] = true }
    applied, err := tx.QueryContext(ctx, `SELECT version FROM app_schema_migrations`)
    if err != nil { return err }
    for applied.Next() {
        var version int64
        if err := applied.Scan(&version); err != nil { applied.Close(); return err }
        if !knownVersions[version] {
            applied.Close()
            return fmt.Errorf("database contains unsupported migration %d", version)
        }
    }
    if err := applied.Err(); err != nil { applied.Close(); return err }
    if err := applied.Close(); err != nil { return err }
    var previous int64
    for _, migration := range migrations {
        if migration.Version <= previous { return errors.New("migrations must be strictly ordered") }
        previous = migration.Version
        encoded, err := json.Marshal(migration.Statements)
        if err != nil { return err }
        sum := sha256.Sum256(encoded)
        checksum := hex.EncodeToString(sum[:])
        var stored string
        err = tx.QueryRowContext(ctx, `SELECT checksum FROM app_schema_migrations WHERE version=$1`,
            migration.Version).Scan(&stored)
        switch {
        case err == nil:
            if stored != checksum { return fmt.Errorf("migration %d checksum mismatch", migration.Version) }
            continue
        case !errors.Is(err, sql.ErrNoRows):
            return err
        }
        for _, statement := range migration.Statements {
            if _, err := tx.ExecContext(ctx, statement); err != nil {
                return fmt.Errorf("migration %d: %w", migration.Version, err)
            }
        }
        if _, err := tx.ExecContext(ctx,
            `INSERT INTO app_schema_migrations(version,checksum) VALUES ($1,$2)`,
            migration.Version, checksum); err != nil { return err }
    }
    return tx.Commit()
}
```

Rehearse a baseline migration against databases produced by earlier releases before enabling the ledger. A baseline must validate the actual schema/data, not mark unknown legacy databases current. The runner rejects applied versions absent from its input; also require the deployment to supply the complete ordered migration history and reject a missing intermediate version in that history. Rollbacks require a data-preserving plan and tested backup restoration, not an unconditional “down” script.

<a id="i08"></a>

### I08 — Owned task lifecycle and cancellable account admission (R21–R22)

A task group must serialize `Add` against shutdown so `Wait` cannot race new task admission:

```go
type TaskGroup struct {
    mu sync.Mutex
    closing bool
    ctx context.Context
    cancel context.CancelFunc
    wg sync.WaitGroup
}
func NewTaskGroup(parent context.Context) *TaskGroup {
    ctx, cancel := context.WithCancel(parent)
    return &TaskGroup{ctx:ctx, cancel:cancel}
}
func (g *TaskGroup) Go(fn func(context.Context)) bool {
    g.mu.Lock()
    if g.closing { g.mu.Unlock(); return false }
    g.wg.Add(1)
    g.mu.Unlock()
    go func() { defer g.wg.Done(); fn(g.ctx) }()
    return true
}
func (g *TaskGroup) StopAdmission() {
    g.mu.Lock(); g.closing = true; g.mu.Unlock()
}
func (g *TaskGroup) CancelAndWait() {
    g.StopAdmission()
    g.cancel()
    g.wg.Wait()
}
```

Route initial/manual sync, post-sync work, notification dispatch, and workers through it. A rejected task admission must be returned before an API claims success. Shutdown order: stop new work; drain/stop HTTP admission; cancel and join owned tasks; then close databases. Use a process-level shutdown policy for unresponsive operations, but do not close pools while still claiming tasks were joined. Durable outbox jobs remain queued or explicitly uncertain across termination.

Replace the global account-use RWMutex with per-account admission state. `Enter(ctx)` increments an active count only if the account is open and returns a once-only release function. `BeginDelete(ctx)` closes admission and waits on a drained channel using `select` on `ctx.Done()`, **without holding a global lock while waiting**. A rolled-back deletion reopens admission; a committed deletion leaves a tombstone/generation that denies old work. Full-owner deletion closes admission for every account and waits outside global critical sections.

For exclusive engine/refresh work, a channel semaphore is a simple cancellable alternative to `sync.Mutex`:

```go
type ContextMutex struct { token chan struct{} }
func NewContextMutex() *ContextMutex {
    m := &ContextMutex{token:make(chan struct{},1)}
    m.token <- struct{}{}
    return m
}
func (m *ContextMutex) Lock(ctx context.Context) (func(), error) {
    select {
    case <-ctx.Done(): return nil, ctx.Err()
    case <-m.token:
        var once sync.Once
        return func() { once.Do(func(){ m.token <- struct{}{} }) }, nil
    }
}
```

Use these locks per account; do not serialize unrelated accounts. After acquiring, recheck account existence/generation and cancellation before provider I/O. Coalesce duplicate sync requests into one result rather than accumulating waiting goroutines. Test deletion of account B while account A is exporting, cancellation while waiting for refresh, and shutdown under active traffic.

<a id="i09"></a>

### I09 — Product SQL and migration acceptance suite (R24)

Keep the engine PostgreSQL 17/race job already present. Add a real product integration test entry point that applies product and engine schema through the same migration owner, provisions the single owner, and exercises the production handler queries. The environment variable below is a **proposed new test contract**, not an existing variable magically enabling tests:

```yaml
- name: Product SQL integration and upgrade tests
  env:
    LULLMAIL_TEST_DATABASE_URL: postgres://test:test@localhost:5432/mailtest?sslmode=disable
  run: go test -race -count=1 -tags=integration ./...
```

Implement the harness to fail—not skip—when the variable is absent in this CI job. Use isolated databases/schemas and deterministic cleanup, especially for parallel tests. Fixture coverage should include previous-release schemas, partially applied historical DDL, duplicate/orphan rows, concurrent startup, and migration checksum mismatch. Require the whole integration job in `publish.needs` as in F02.

Test SQL authorization with two synthetic users even if the production product is single-owner. This catches missing `user_id` joins without changing the product's supported tenancy. Include deletion/retention/write races and the pool-cap test in F08. Browser tests should use real IndexedDB and two browser contexts; plain reducer/helper tests will not catch the lifecycle races reported here.

<a id="i10"></a>

### I10 — Keyring/ciphertext context and scoped agent tokens (R26)

The generated random key and AES-GCM nonce scheme are useful existing protections. Do not invalidate existing secrets by replacing the current derivation or deleting `secret.key` as a routine repair. Validate operator-provided key strength at setup, preserve the exact legacy decryptor, and introduce a versioned envelope with an explicit key ID.

```go
type SecretContext struct { Table, RowID, Column, OwnerID string }
type SecretEnvelope struct {
    Version int `json:"v"`
    KeyID string `json:"kid"`
    Data string `json:"data"`
}
func sealV2(keyID string, key []byte, scope SecretContext, plaintext []byte) (string, error) {
    if len(key) != 32 || keyID == "" { return "", errors.New("a named 32-byte key is required") }
    block, err := aes.NewCipher(key)
    if err != nil { return "", err }
    gcm, err := cipher.NewGCM(block)
    if err != nil { return "", err }
    aad, err := json.Marshal(struct {
        Domain string
        Context SecretContext
    }{"lullmail-secret-v2", scope})
    if err != nil { return "", err }
    nonce := make([]byte, gcm.NonceSize())
    if _, err := io.ReadFull(rand.Reader, nonce); err != nil { return "", err }
    sealed := gcm.Seal(nonce, nonce, plaintext, aad)
    encoded, err := json.Marshal(SecretEnvelope{2, keyID, base64.StdEncoding.EncodeToString(sealed)})
    return string(encoded), err
}
```

The V2 decryptor selects the key by ID, validates the version/nonce length, reconstructs the same authenticated context, and calls `gcm.Open`. Reject unknown key IDs; never fall back to another key or unauthenticated plaintext. The database row identity and owner must be known before sealing so ciphertext cannot be moved between records unnoticed. Rewrap legacy rows using compare-and-swap on their old ciphertext; keep old keys until every row and backup retention policy has been accounted for. Test wrong-row/wrong-owner substitution, partial rotation, rollback, missing keys, and backup restoration.


A matching V2 decryptor (legacy dispatch remains separate):

```go
func openV2(keys map[string][]byte, scope SecretContext, encoded string) ([]byte, error) {
    var envelope SecretEnvelope
    if err := json.Unmarshal([]byte(encoded), &envelope); err != nil { return nil, err }
    if envelope.Version != 2 { return nil, errors.New("unsupported ciphertext version") }
    key, ok := keys[envelope.KeyID]
    if !ok || len(key) != 32 { return nil, errors.New("unknown ciphertext key") }
    raw, err := base64.StdEncoding.DecodeString(envelope.Data)
    if err != nil { return nil, err }
    block, err := aes.NewCipher(key)
    if err != nil { return nil, err }
    gcm, err := cipher.NewGCM(block)
    if err != nil { return nil, err }
    if len(raw) < gcm.NonceSize()+gcm.Overhead() { return nil, errors.New("truncated ciphertext") }
    aad, err := json.Marshal(struct {
        Domain string
        Context SecretContext
    }{"lullmail-secret-v2", scope})
    if err != nil { return nil, err }
    return gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], aad)
}
```

Bound the encoded size at the caller. A V2 authentication failure must not trigger a fallback to the legacy decryptor.

Add token metadata and enforce it on every authenticated agent request:

```sql
ALTER TABLE agent_tokens ADD COLUMN expires_at timestamptz;
ALTER TABLE agent_tokens ADD COLUMN scopes text[] NOT NULL DEFAULT '{}';
-- Choose a documented legacy migration policy before requiring these fields.
-- New tokens should have explicit expiry and an allowlisted set of scopes.
```

```go
func hasScope(granted []string, required string) bool {
    for _, scope := range granted { if scope == required { return true } }
    return false
}
// After hash lookup: reject expired/revoked tokens, then require the route's scope.
// Examples: mail:read, mail:write, mail:send, accounts:manage.
```

Use a server-owned route-to-scope map, not client-supplied scope names or ambiguous prefix checks. Keep existing security/export restrictions unless a separately reviewed capability explicitly allows them. Token creation should use fresh proof from I02, return the raw token once, and never log it. Empty scopes must deny access, not imply all permissions.


## 6. Other implementation improvements and protocol checks

These are improvement/verification items rather than separately counted new vulnerabilities.

**MIME header length.** `mail-engine/send.go` now correctly transfer-encodes textual bodies, but direct header construction still needs a folding/validation pass. An ASCII subject is not shortened by `mime.QEncoding.Encode`, and long address/reference lists can produce oversized lines. [RFC 5322 §2.1.1 and §2.2.3](https://www.rfc-editor.org/rfc/rfc5322.html#section-2.1.1) define the line-length and folding rules. Use a MIME/header writer that folds at syntactically valid boundaries; do not split arbitrary UTF-8 bytes, quoted addresses, or encoded-words. As immediate protection, reject oversized rendered header lines before queue acceptance:

```go
func validateHeaderLineLengths(message []byte) error {
    header, _, found := bytes.Cut(message, []byte("\r\n\r\n"))
    if !found { return errors.New("message has no header/body separator") }
    for _, line := range bytes.Split(header, []byte("\r\n")) {
        if len(line) > 998 { return errors.New("rendered header line exceeds 998 bytes") }
    }
    return nil
}
```

This is a rejection safety net, not a folding implementation. Validate the exact rendered message before accepting it and persist that immutable representation or its equivalent with a fixed Message-ID; rendering again with unrelated IDs after validation breaks the contract. Test very long ASCII/non-ASCII subjects, display names, recipient lists, and reference chains with a strict MIME parser. This concern overlaps F14 and the outgoing-message hardening work; a live SMTP rejection was not reproduced.

**Operational diagnostics.** Add structured request/job/account IDs, observable outbox state, migration version, reconciliation generation, and offline storage/replay status. Do not log auth secrets, provider tokens, or full private message bodies. A console-only offline rejection is not sufficient recovery UX; expose failed actions, their reason, and an explicit retry/rebase/discard option.

**MCP boundaries.** `mcp/client.go` already bounds responses and tools escape opaque message/thread identifiers in the inspected code. Its URL construction/redirect policy still needs the origin hardening in I07. Advertise the attachment/response size limit to tool callers; consider a separate streaming/file capability rather than returning arbitrarily large base64 text. Revisit tool claims that every action is reversible once exact undo/conflict semantics in I05 are available. The complete MCP tool surface was not exhaustively audited.

**Dependency assurance.** A current advisory scan of the full Go/npm dependency graph was not performed. No CVE is asserted here merely because a version looks old or recent. Run supported advisory tooling on the root module, nested mail-engine and MCP modules, and the dashboard lockfile, and review reachable impact before treating an advisory as an exploitable finding.

## 7. Important protections already present / findings deliberately not repeated

The inspected implementation contains substantial prior remediation. Preserve it while making these changes.

The API responses are marked non-cacheable at the HTTP layer; the IndexedDB issues above are a separate persistence mechanism. Session/agent secrets are hashed in the database, provider credentials are sealed with randomized AES-GCM nonces, and browser sessions use the configured secure cookie protections. OAuth uses state and PKCE with owner binding. Passkey verification and transactional one-time recovery/TOTP consumption are present. Several credential changes now revoke other sessions in their mutation transaction; R02 concerns the remaining in-flight-login epoch race, not an absence of revocation altogether.

The send path has a 34 MiB wire cap, decoded attachment caps, queue count/byte admission, account-deletion delivery guards, and **Graph transport attachment validation before acceptance**. These are not reported as missing. Text bodies are transfer-encoded, address formatting uses `net/mail`, and Sent-copy concerns are distinguished from submission. The Gmail bug is specifically an allowed-host mismatch, not an instruction to remove redirect credential restrictions.

Account deletion uses a transaction and lifecycle gate. Export ZIPs are staged and checked before response headers, use safe collision-resistant filenames, and expose fallback provenance; the truncated-stream finding concerns individual attachment streaming instead. Sent-folder membership is required for correspondent evidence, addressing the previously reported forged-From classifier issue. Account-qualified selection/message keys are used in several previously fixed paths.

The CI already includes an engine PostgreSQL service and race checks for three Go modules. Its missing publication dependency is the new defect, not an assertion that no integration job exists. Historical tags no longer intentionally move `latest` backward. Docker's entrypoint avoids recursive ownership changes across an arbitrary tree. Atomic setup-file/lock protections and transactional IndexedDB wipe/commit semantics are already implemented, although the remaining async/privacy boundary issues still matter.

The reader uses a sanitizer plus restrictive iframe/CSP controls. No working HTML-email script-execution exploit was established in this review. TOTP is documented as an alternative login method, so its existence is not misreported as a bypass of a promised password-plus-TOTP flow. Board pins surviving account disconnection are intentional. A suspected Go return-expression evaluation issue was checked and rejected rather than included as a finding.

## 8. Verification and acceptance plan

Use a separate test environment with non-production provider accounts and backups. Pin the reviewed commit before applying patches so the test delta is attributable.

```sh
git clone https://github.com/lullmail/lullmail.git
cd lullmail
git checkout 52fe475a712348b744b36eea875c8817dacd376e

# Requires the project's supported Go toolchain and dependencies.
go test ./...
go vet ./...
(cd mail-engine && go test ./... && go vet ./...)
(cd mcp && go test ./... && go vet ./...)
(cd dashboard && npm ci && npm test && npm run typecheck && npm run build)

# With PostgreSQL provisioned as in CI:
(cd mail-engine && NEUTRON_MAIL_TEST_DATABASE_URL="$TEST_DATABASE_URL" go test -race -count=1 ./...)
go test -race -count=1 ./...
(cd mcp && go test -race -count=1 ./...)
```

These commands are a plan, not a claim that they ran here. The proposed product integration harness in I09 must be implemented before its environment variable enables anything.

Acceptance should cover five boundaries: authenticated API/SQL ownership and failure semantics; actual browser IndexedDB lifecycle with multiple tabs; provider protocol behavior and message fidelity; crash/restart/deletion/retention races; and migration/release gating. For the highest-priority changes, require the following concrete outcomes:

| Area | Required evidence before closing |
|---|---|
| Gmail | The real SDK/production transport sends authorization to the exact configured endpoint; a live test account connects and syncs. |
| Drafts | Delayed/failed restoration never overwrites saved attachments or permits a missing-file send; retired drafts do not reappear; quota errors are visible. |
| Replies | Correct defaults for Reply-To, sent-last threads, aliases, and explicit edits; parent ownership/account checks remain enforced. |
| Offline | Ordered replay, network-only retry, server deduplication, generation-fenced writes, failed-wipe behavior, and two-tab owner/logout/deletion tests. |
| Database | Classification completes with a one-connection pool and a capped concurrent burst; SQL failures do not masquerade as permanent rejection. |
| Streams | A provider read error is visible to the downloading client; completed exports are intact and resource-bounded. |
| Outbox | Process termination at every send phase yields durable pending/accepted/uncertain state and no automatic ambiguous resend. |
| Authentication | A login verified before a credential change cannot create a usable later session; failed logout remains retryable. |
| Sync/retention | Interrupted scans preserve readable old state, generation fences prevent resurrection, and widened retention re-fetches old mail. |
| Release | A failed database/race job prevents publication; upgrade fixtures and required integration tests cannot silently skip. |

## 9. Coverage ledger

Actual source reads included the following files or substantial targeted ranges. A file appearing here does not certify every possible behavior in it; where coverage was partial that is called out.

**Application/backend source inspected**

[`app.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/app.go), [`auth.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/auth.go), [`password.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/password.go), [`agent.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/agent.go), [`accounts.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/accounts.go), [`mail.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/mail.go), [`classify.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/classify.go), [`oauth.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/oauth.go), [`sendqueue.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/sendqueue.go), [`resolver.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/resolver.go), [`export.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/export.go), [`board.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/board.go), [`push.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/push.go), [`config.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/config.go), [`setup.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/setup.go), [`secretbox.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/secretbox.go), [`cmd_serve.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/cmd_serve.go), [`schema.sql`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/schema.sql), [`go.mod`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/go.mod).

**Dashboard source inspected (substantial ranges; not every UI path)**

[`dashboard/src/app/App.tsx`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/App.tsx), [`dashboard/src/app/lib/api.ts`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/lib/api.ts), [`dashboard/src/app/lib/actions.ts`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/lib/actions.ts), [`dashboard/src/app/lib/offline.ts`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/lib/offline.ts), [`dashboard/src/app/lib/store.ts`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/lib/store.ts), [`dashboard/src/app/ui/Compose.tsx`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/ui/Compose.tsx), [`dashboard/src/app/reader/Body.tsx`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/reader/Body.tsx), [`dashboard/src/app/reader/Thread.tsx`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/reader/Thread.tsx), [`dashboard/src/app/views/AccountsView.tsx`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/views/AccountsView.tsx), [`dashboard/src/app/views/SecurityView.tsx`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/dashboard/src/app/views/SecurityView.tsx).

**Engine source inspected**

[`mail-engine/send.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/mail-engine/send.go), [`mail-engine/dialer/dialer.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/mail-engine/dialer/dialer.go), [`mail-engine/go.mod`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/mail-engine/go.mod).

**Engine targeted ranges**

[`mail-engine/gmail/adapter.go#L1-L265`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/mail-engine/gmail/adapter.go#L1-L265), [`mail-engine/sync.go#L1-L285`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/mail-engine/sync.go#L1-L285).

**MCP targeted source**

[`mcp/client.go`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/mcp/client.go), [`mcp/tools.go#L1-L255`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/mcp/tools.go#L1-L255).

**Deployment/review context inspected**

[`.github/workflows/ci.yml`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/.github/workflows/ci.yml), [`Dockerfile`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/Dockerfile), [`compose.yaml`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/compose.yaml), [`docker-entrypoint.sh`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/docker-entrypoint.sh), [`README.md`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/README.md), [`AUDIT_OPEN.md`](https://github.com/lullmail/lullmail/blob/52fe475a712348b744b36eea875c8817dacd376e/AUDIT_OPEN.md).

The repository tree and existing open audit register were inspected. The upstream Google dependency was read at the exact pinned `v0.291.0` tag to verify the endpoint mismatch. Current primary documentation was checked for the Gmail endpoint, Graph immutable IDs, RFC 5322 header limits, and Go HTTP abort semantics.

**Not exhaustively inspected or executed:** the complete IMAP/JMAP/Graph adapter and engine store/scheduler implementation; every backend endpoint such as briefing/notes/personal export; all dashboard views, hooks, keyboard actions, service-worker paths and rendering combinations; the marketing site; the entire MCP tools file; every test fixture; deployed proxy/cloud policy; repository rulesets/secret configuration; live provider consent/scope behavior; a full dependency vulnerability database scan; production performance; all historical schema upgrades. Branch metadata alone was not used to claim missing branch protection, since repository rulesets were not audited.

This is therefore an actionable, substantial static audit with explicit verification gaps—not an exhaustive certification or a claim that unlisted code is safe.

## Appendix A. Reproduction checks actually executed

The following programs exercise isolated mechanisms drawn from the inspected code. They were run locally, not inside a checked-out/modified Lullmail application. In particular, the JavaScript checks do not run Preact effects or real IndexedDB transactions; the Go pool test uses a fake standard-library SQL driver rather than PostgreSQL. Use the full regression tests above to establish application-level behavior.

### Go results

```text
PASS: nested query blocks while the only connection has open rows
PASS: closing the outer result set removes the pool hold-and-wait
PASS: current allowlist omits authorization at the generated Gmail endpoint
PASS: correct Gmail hostname retains authorization
PASS: corrected hostname does not authorize an unrelated host
PASS: 80-byte slicing can produce invalid UTF-8
PASS: rune-boundary truncation preserves UTF-8
PASS: logged-only copy failure is exposed as successful truncated HTTP download
PASS: aborting after an upstream read failure makes truncation detectable
```

### Go program (`probes.go`)

```go
package main

import (
 "context"
 "database/sql"
 "database/sql/driver"
 "errors"
 "fmt"
 "io"
 "log"
 "net/http"
 "net/http/httptest"
 "strings"
 "time"
 "unicode/utf8"
)

type fakeDriver struct{}
type fakeConn struct{}
type heldRows struct{}
func (fakeDriver) Open(string) (driver.Conn,error) { return fakeConn{},nil }
func (fakeConn) Prepare(string) (driver.Stmt,error) { return nil, errors.New("not implemented") }
func (fakeConn) Close() error { return nil }
func (fakeConn) Begin() (driver.Tx,error) { return nil, errors.New("not implemented") }
func (fakeConn) QueryContext(context.Context,string,[]driver.NamedValue)(driver.Rows,error){return heldRows{},nil}
func (heldRows) Columns() []string { return []string{"value"} }
func (heldRows) Close() error { return nil }
func (heldRows) Next(v []driver.Value) error { v[0]="value"; return nil }

type recordTransport struct{ got string }
func (t *recordTransport) RoundTrip(r *http.Request)(*http.Response,error){
 t.got=r.Header.Get("Authorization")
 return &http.Response{StatusCode:200,Header:make(http.Header),Body:io.NopCloser(strings.NewReader("{}")),Request:r},nil
}
type bearerTransport struct{ token string; hosts map[string]bool; base http.RoundTripper }
// Exact decision and clone pattern from the reviewed dialer.
func (t bearerTransport) RoundTrip(r *http.Request)(*http.Response,error){
 clone:=r.Clone(r.Context())
 if r.URL.Scheme=="https" && t.hosts[strings.ToLower(r.URL.Hostname())] { clone.Header.Set("Authorization","Bearer "+t.token) } else { clone.Header.Del("Authorization") }
 return t.base.RoundTrip(clone)
}
type failingReader struct{ read bool }
func (r *failingReader) Read(p []byte)(int,error){
 if !r.read {r.read=true; return copy(p,"partial attachment"),nil}
 return 0,errors.New("upstream read interrupted")
}
func check(ok bool, name string){if !ok {panic(name)}; fmt.Println("PASS:",name)}
func main(){
 sql.Register("audit-held-rows",fakeDriver{})
 db,err:=sql.Open("audit-held-rows","");if err!=nil{panic(err)};defer db.Close();db.SetMaxOpenConns(1)
 first,err:=db.Query("first");if err!=nil{panic(err)}
 ctx,cancel:=context.WithTimeout(context.Background(),40*time.Millisecond)
 _,err=db.QueryContext(ctx,"nested");cancel()
 check(errors.Is(err,context.DeadlineExceeded),"nested query blocks while the only connection has open rows")
 if err=first.Close();err!=nil{panic(err)}
 ctx,cancel=context.WithTimeout(context.Background(),40*time.Millisecond)
 next,err:=db.QueryContext(ctx,"after close");cancel()
 check(err==nil,"closing the outer result set removes the pool hold-and-wait");next.Close()

 rec:=&recordTransport{}
 req,_:=http.NewRequest("GET","https://gmail.googleapis.com/gmail/v1/users/me/labels",nil)
 old:=bearerTransport{"example",map[string]bool{"www.googleapis.com":true},rec}
 res,err:=old.RoundTrip(req);if err!=nil{panic(err)};res.Body.Close()
 check(rec.got=="","current allowlist omits authorization at the generated Gmail endpoint")
 fixed:=bearerTransport{"example",map[string]bool{"gmail.googleapis.com":true},rec}
 res,err=fixed.RoundTrip(req);if err!=nil{panic(err)};res.Body.Close()
 check(rec.got=="Bearer example","correct Gmail hostname retains authorization")
 req,_=http.NewRequest("GET","https://untrusted.invalid/",nil)
 res,err=fixed.RoundTrip(req);if err!=nil{panic(err)};res.Body.Close()
 check(rec.got=="","corrected hostname does not authorize an unrelated host")

 s:=strings.Repeat("a",79)+"é"
 check(!utf8.ValidString(s[:80]),"80-byte slicing can produce invalid UTF-8")
 for len(s)>80 {_,size:=utf8.DecodeLastRuneInString(s);s=s[:len(s)-size]}
 check(utf8.ValidString(s)&&len(s)<=80,"rune-boundary truncation preserves UTF-8")

 for _,abort:=range []bool{false,true}{
  srv:=httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
   w.Header().Set("Content-Type","application/octet-stream")
   _,copyErr:=io.Copy(w,&failingReader{})
   if copyErr!=nil && abort {w.(http.Flusher).Flush();panic(http.ErrAbortHandler)}
  }))
  srv.Config.ErrorLog=log.New(io.Discard,"",0);srv.Start()
  response,getErr:=srv.Client().Get(srv.URL)
  if getErr!=nil {srv.Close();panic(getErr)}
  body,readErr:=io.ReadAll(response.Body);response.Body.Close();srv.Close()
  if !abort {check(response.StatusCode==200 && readErr==nil && string(body)=="partial attachment","logged-only copy failure is exposed as successful truncated HTTP download")
  }else{check(readErr!=nil,"aborting after an upstream read failure makes truncation detectable")}
 }
}
```

### JavaScript results

```text
PASS: normal restored draft starts ready before attachment load
PASS: current send guard does not block missing attachment readiness
PASS: unmount flush recreates a retired localStorage entry
PASS: continue at backed-off queue head sends newer edit first
PASS: generic connect-form trim changes a significant password
PASS: outgoingWeight omits a large subject
6 JavaScript mechanism checks passed; no browser or IndexedDB integration was executed.
```

### JavaScript program (`probes.mjs`)

```js
import assert from 'node:assert/strict';
let count=0;
function check(name,fn){fn();count++;console.log(`PASS: ${name}`);}
check('normal restored draft starts ready before attachment load',()=>{
 const seed={id:'draft-1'};
 const attachments=seed.attachments?[...seed.attachments]:[];
 const ready=!seed.attachments;
 assert.equal(ready,true);assert.deepEqual(attachments,[]);
});
check('current send guard does not block missing attachment readiness',()=>{
 const to='recipient@example.invalid',busy=false,attachmentsReady=false;
 const passesGuard=!(!to.trim()||busy);
 assert.equal(passesGuard,true);assert.equal(attachmentsReady,false);
});
check('unmount flush recreates a retired localStorage entry',()=>{
 const storage=new Map([['es-draft-a','old']]);
 const write=()=>storage.set('es-draft-a','latest private text');
 storage.delete('es-draft-a');write();
 assert.equal(storage.get('es-draft-a'),'latest private text');
});
check('continue at backed-off queue head sends newer edit first',()=>{
 const now=100;
 const due=[{body:'old',queuedAt:1,nextAttemptAt:200},{body:'new',queuedAt:2}];
 const sent=[];
 for(const item of due){if(item.nextAttemptAt&&item.nextAttemptAt>now)continue;sent.push(item.body);}
 assert.deepEqual(sent,['new']);
 const fixed=[];
 for(const item of due){if(item.nextAttemptAt&&item.nextAttemptAt>now)break;fixed.push(item.body);}
 assert.deepEqual(fixed,[]);
});
check('generic connect-form trim changes a significant password',()=>{
 const password=' secret with spaces ';
 assert.notEqual(String(password).trim(),password);
});
check('outgoingWeight omits a large subject',()=>{
 const outgoingWeight=(msg)=>1024+(msg.Text?.length||0)+(msg.HTML?.length||0)+(msg.Attachments||[]).reduce((n,a)=>n+a.Data.length,0);
 const small={Subject:'small',Text:'body'};
 const large={Subject:'a'.repeat(1024*1024),Text:'body'};
 assert.equal(outgoingWeight(small),outgoingWeight(large));
});
console.log(`${count} JavaScript mechanism checks passed; no browser or IndexedDB integration was executed.`);
```

## Appendix B. Implementation status

No fixes were committed to GitHub and no deployed environment was changed. Local patch blocks are proposed modifications to the reviewed commit. Integration implementations require the accompanying schema/API/UI wiring; their acceptance criteria are part of the fix, not optional follow-up. In particular, the durable outbox, owner generations, Graph ID migration, cryptographic key rotation, and schema migration baseline must not be reduced to a one-line patch that leaves the stated invariant broken.

The most urgent practical changes remain the Gmail host correction, draft attachment/retirement fixes, reply-recipient correction, classifier connection release, offline ordering/isolation work, and the CI publication gate.
