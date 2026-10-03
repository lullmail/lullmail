# Gmail read recovery: local proposed patch

The default review candidate combines the frozen 2026-10-02 systematic-review
hardening with this Gmail synchronization delta (public base `7e3ba75`). It has not been published,
merged, deployed or exercised against a real mailbox. Engine changes require
matching Neutron upstream work followed by re-vendoring under
`mail-engine/VENDOR.md`; downstream-only edits are not a release-ready solution.

## Confirmed failures

1. An initial label page listed as many as 500 IDs, then fetched all metadata
   before returning any changes. A single throttled metadata GET discarded the
   in-memory page. Repeated runs re-listed and re-fetched the same prefix.
2. A Gmail 401 during a long sync was immediately classified as permanent
   reauthentication. The fixed run bearer could expire while its refresh grant
   remained valid; scheduler eligibility then prevented the next token refresh.
3. An OAuth `invalid_grant` during token admission was returned as an untyped
   error. Even a typed admission failure bypassed engine classification, so
   scheduler eligibility could keep admitting the failed account.

## Proposed behavior

- A label-enumeration page requests at most 100 IDs. The captured starting
  history ID, Spam/Trash inclusion and continuation encoding are unchanged.
  Completed pages still use the existing staged transaction/finalization path.
- All Gmail adapter reads, including profile, listing, metadata, body, raw and
  attachment reads, share a conservative 250 ms minimum start spacing. The
  product dialer uses 256 fixed limiter stripes shared by all resolver instances
  in the process. No account names or credentials are retained by these stripes;
  a hash collision only slows unrelated accounts. Direct adapter callers can
  explicitly share a `ReadLimiter`.
- A throttled read gets at most three total attempts, with 1 s / 2 s exponential
  waits plus up to 249 ms jitter. A larger valid `Retry-After` is honored within
  the 30 s wait bound. Longer requested waits return the rate-limit error rather
  than retrying early inside that call. A shared process-local cooldown prevents later/concurrent adapters from
  ignoring the provider deadline; cooldowns longer than the bounded wait fail
  locally without HTTP rather than tying up a worker. Cancellation applies to pacing,
  contention, backoff, refresh and HTTP requests.
- Only the failed individual GET is retried. An unsuccessful metadata batch
  returns no page, never a partial envelope set with an advanced cursor. Existing
  staged envelopes, seen evidence, scan generation and continuation survive.
- Stored-account sync resolvers can renew a rejected Gmail bearer once and replay
  a bodyless GET only on the existing allowed HTTPS provider origin. A second
  401 reaches the existing permanent rejection path. Refresh stays in the
  application's encrypted credential store, context-aware refresh lock and
  compare-and-swap update. Only the exact rejected bearer is forced expired;
  a concurrent replacement is reused. No new grant or scope is created.
- A refresh endpoint's `invalid_grant` is typed as reauthentication; scheduler
  token-admission and prefetch failures persist that state before later ticks.
  Transient refresh failures do not mark permanent reauthentication.
- Send and label-mutation requests have no new retry or refresh/replay behavior.
  Metadata is still fetched on each label visit, preserving changed flags and
  authoritative memberships. No cached-created-ID shortcut was added.

## Quota interpretation and limits

Google's [current quota reference](https://developers.google.com/workspace/gmail/api/reference/quota)
(as checked 2026-10-02) lists 6,000 quota units per user/project/minute and 20 units
for `messages.get` for the new quota model; qualifying older projects retain
previous settings. Metadata reduces transferred bytes, not that method's quota
cost. The contributor PR's fixed 250-unit/second assumption was not adopted.
Google's [error guidance](https://developers.google.com/workspace/gmail/api/guides/handle-errors)
recommends backoff for quota failures.

The local limiter is not a provider quota reservation. Other processes, other
clients and mutations still consume shared quota. The shared cooldown is process-local, not durable across restarts or
coordinated across processes. The scheduler's existing account backoff remains
in force after retries exhaust. A sustained
throttle can still prevent completion of one atomic page; no progress guarantee
is claimed when Gmail refuses all bounded attempts.

A fake-clock test demonstrates 25.25 s of pacing overhead for profile + listing +
100 metadata requests within a 30 s synthetic budget. It assumes zero network
latency and does not claim a live deadline SLA. Optional body prefetch occurs
only after the page is staged, so later cancellation preserves that checkpoint.
Arbitrarily short caller deadlines can still prevent one page completing.

## Verification

The new tests use synthetic HTTP round-trippers, a fake pacing clock and
in-memory SQL/store doubles. No listening socket, real OAuth token, account,
message, provider send, deletion or security grant is required.

- Failure before the patch: metadata 429 yields no page; 401 marks reauth without
  refreshing; invalid_grant remains eligible for later retries
- Passing after the patch: isolated failed-read recovery; bounded repeated
  throttles; 100-ID page budget; backoff cancellation/deadline; Retry-After;
  non-retried mutations/foreign origins; repeated-label metadata freshness;
  staged continuation/generation/seen retention; scheduler refresh/re-admission;
  invalid-grant exclusion; concurrent replacement reuse; refresh-lock cancellation
- Existing endpoint/bearer restrictions, body-prefetch, destruction and terminal
  scan tests remain part of the engine regression suite

PostgreSQL transaction execution, native browser checks, live-provider behavior,
upstream backflow and deployment remain separate release gates. The staged fake
proves call ordering and state retention, not actual SQL atomicity.

### Final local checks (2026-10-03 UTC)

`go test -race -count=1 ./...` and `go vet ./...` pass in all three modules:
product 123 passed / 40 skipped; engine 222 passed / 29 skipped; MCP 7 passed.
Top-level tests are counted, not nested subtests. The skips remain unverified.
The product build passes with `-buildvcs=false` in the source-only snapshot.
An independent read-only review found no remaining blocker after the shared
cooldown and default-port guard corrections.

The optional outbox patch is rebased onto this default main-plus-Gmail candidate
without a production-code overlap (only the audit register overlaps). A disposable
outbox + Gmail combination passes product race tests (134 passed / 49 skipped)
and vet. Those checks do not authorize publication or deployment, or remove the outbox's
PostgreSQL, restart/crash and browser gates. Frontend code is unchanged by this
Gmail delta; frontend checks were not rerun for this pass.
