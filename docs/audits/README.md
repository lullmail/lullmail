# Audit rounds

Frozen, as-submitted reports from the external audit series that shaped this
codebase. They are kept verbatim for traceability: every commit that closes a
finding references its report and finding ID.

- **Live tracker:** [`AUDIT_OPEN.md`](../../AUDIT_OPEN.md) at the repo root —
  open items, deferred findings, and the decision recorded for each. Read it
  before treating related work as done. It is the authority; these files are
  the source material.
- **Passes 1–5** (2026-09-09 → 2026-09-12) predate this numbering; every
  P0/P1 was fixed, the P2 tail closed in commits `83e0dee`..`025a979`
  (tagged `audit lullmail-NN`).

| Report | Series | Notes |
| --- | --- | --- |
| `AUDIT-CHATGPT-2.md` … `AUDIT-CHATGPT-8.md` | ChatGPT-led passes 6–12 (2026-09-17 →) | Finding IDs cited in the closing commits; per-pass counts and dates in `AUDIT_OPEN.md` |

Later GPT-led rounds (2026-10) were working sessions; their reports remain
local-only under `_archive/` (gitignored) alongside other local scratch.
