# Open audit items

Unresolved findings for this repository from the ChatGPT-led audit series (2026-09-09 through 2026-09-11, passes 1-5; register: teploy-neutron-lullmail expanded audit). Every P0/P1 finding has been fixed and verified. The P2 tail was worked on 2026-09-11: 12 of 13 items fixed (commits `83e0dee`..`025a979`, each tagged `audit lullmail-NN`); the one remainder is deferred against a recorded decision below.

Open items: 1 P2 (deferred)

## lullmail__lullmail-10 - P2 - DEFERRED (decision)

**Expose a durable send outcome instead of only an undo token**

- Kind: Improvement
- Evidence: enqueue immediately returns queued/undo_seconds. The worker sends its result to a buffered done channel, logs failures and removes the map entry. The inspected product routes expose undo but no delivery-status lookup; no reader of done appears in the inspected queue code.
- Impact: The enqueue response is not a delivery acknowledgment. A later provider failure or restart during the undo window has no durable outcome in this path. The frontend draft lifecycle was not inspected, so draft loss itself is not asserted.
- Proposed fix: Add persistent outbox states and a delivery-result event or status endpoint. Retain/recover draft content until an outcome is known; distinguish accepted, submitting, submitted, failed and ambiguous. Define retry/idempotency behavior rather than blindly retrying sends.
- Deferral: sendqueue.go's design comment is the recorded decision this item argues against — "Five seconds is the whole feature — no queue table, no worker, just a timer map" (SPEC §6.1; the in-process undo window IS the shipped feature). A durable outbox with delivery states, a status endpoint, and retry/idempotency semantics is a product redesign of that surface, not a defect repair; reopening it is a product call, not an audit action.
- Review commit: `49d159b6654d3dbd27866f87ac783402f19c6cb7` (last reviewed 2026-09-10)
