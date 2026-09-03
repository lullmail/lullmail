# AGENTS.md

This repository is **public** and mirrored to GitHub at https://github.com/lullmail/lullmail.
Product site: https://lullmail.com. Every commit is publicly visible — treat all work as public-facing.

## Do not commit private/transient context
Never create or commit session, handoff, or local-only context as tracked files:
- No `SESSION_NOTES.md`, `*_NOTES.md`, `HANDOFF*.md`, `CONTINUATION_PROMPT.md`, `*_NEXT_SESSION.md`
- No `.claude/`, `.opencode/` local configs
- No secrets — `.env`, API keys, tokens, VAPID keys
- `SPEC.md`, `TASKS.md`, `NEUTRON_BUGS.md` are local-only working docs: gitignored as a
  safety net, but do not create tracked variants of them. Keep scratchpads in session
  memory or outside the repo.

## Deployment notes
- The committed `teploy.yml` is a public template. The production instance's real
  deploy config lives in the private infra repo — never copy it here.
- Production on infra-home was migrated on 2026-09-02 from the email-soft
  identity to app `lullmail` on the GHCR image (pg dump restored, secret.key
  carried, origin unchanged). The pre-migration backup and rollback live in
  /root/lullmail-migration on that host; the old email-soft containers were
  left stopped as the rollback path.
- Agent API tokens minted before the rename carry the `es_` prefix and must stay
  valid; new tokens are `lull_`.

## Commits
- Conventional style (`feat:`, `fix:`, `chore:`, `docs:`, `deploy:`).
- History is public — keep it clean.

## Remotes
`git push origin` fans out to both Forgejo and GitHub (dual push-URL) — a single push mirrors to both.
