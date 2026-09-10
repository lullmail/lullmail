# Vendored from ../../Neutron/mail

Upstream: `~/Documents/Code Projects/Neutron/mail` (module
`github.com/neutron-build/neutron/mail`), referenced from the root go.mod via a
`replace`. The module has no published version and deploy builds run on servers
with no Neutron checkout, so the copy has to be in-tree.

Synced: 2026-09-10, from Neutron 1549c4a7.

## Rule

Fix upstream first, then re-copy. Editing here directly is how, in August 2026,
a paginated JMAP sync and blob download came to exist only in this copy while
upstream still had stubs. If a fix must land here first (production incident),
open the matching upstream change in the same session.

## Re-vendor

```
diff -rq ../../Neutron/mail mail-engine -x "*.local.md"   # expect only VENDOR.md
rm -rf mail-engine && cp -R ../../Neutron/mail ./mail-engine
rm -f mail-engine/*.local.md && git checkout mail-engine/VENDOR.md
```

If any `.go` file differs before the copy, upstream is missing work: backflow
it to Neutron as its own commit and only copy once the diff is VENDOR.md alone.
