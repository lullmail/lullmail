# Vendored from ../../Neutron/mail

Upstream: `~/Documents/Code Projects/Neutron/mail` (module
`github.com/neutron-build/neutron/mail`). Referenced from the root go.mod via
a `replace` directive — the module has no published version, and deploy builds
run on servers with no sibling Neutron checkout (see NEUTRON_BUGS.md N4).

Synced: 2026-09-05, after backflowing this tree's work to Neutron 3dfb20e6.

## Re-vendoring safely

As of the 2026-09-05 sync this tree and upstream agree: the re-copy below
changed only a README line and three tests, because everything else had already
been backflowed. That is the state a re-vendor is safe from, and it is not
automatic — it was reached by reconciling three weeks of one-directional drift.

```
rm -rf mail-engine && cp -R ../../Neutron/mail ./mail-engine
git checkout mail-engine/VENDOR.md   # this file is local, upstream has no copy
```

Do not run that blind. Check first that nothing here is newer than upstream:

```
diff -rq ../../Neutron/mail mail-engine       # expect only VENDOR.md
git log --oneline -- mail-engine              # what this repo changed since the sync
```

If any .go file differs, upstream is missing work and the copy would delete it.
Backflow to Neutron first, as its own commit, and only copy once the diff is
down to VENDOR.md alone. In August 2026 this was skipped and the vendor silently
became the real implementation — a paginated JMAP sync and the whole blob
download lived only here while upstream still had `not implemented` stubs.

## Where a fix belongs

Upstream, in the Neutron repo, then re-copy — that keeps the two from
diverging and is what makes a future re-vendor safe. Editing here directly is
what created the drift above. When a fix must land here first (a production
incident), open the matching upstream change in the same session and note it
here, so the next person inherits a diff that closes rather than one that grows.
