# Vendored from ../../Neutron/mail

Upstream: `~/Documents/Code Projects/Neutron/mail` (module
`github.com/neutron-build/neutron/mail`). Referenced from the root go.mod via
a `replace` directive — the module has no published version, and deploy builds
run on servers with no sibling Neutron checkout (see NEUTRON_BUGS.md N4).

Synced: 2026-08-18.

## This copy is ahead of upstream in places — do not blind-copy

The one-line re-vendor this file used to recommend
(`rm -rf mail-engine && cp -R ../../Neutron/mail ./mail-engine`) would now
destroy work. Five lullmail commits have edited the engine since the sync, and
only some of them were backflowed. As of 2026-09-05 this copy carried a
paginated JMAP initial sync, session validation, and a complete JMAP blob
download (`Raw`, `Attachment`, `download`) that upstream had left as
`not implemented` stubs.

Before re-vendoring, diff in both directions and backflow first:

```
diff -ru ../../Neutron/mail mail-engine        # what each side has that the other lacks
git log --oneline -- mail-engine               # what this repo changed since the sync
```

Anything this copy has and upstream lacks goes to Neutron as its own commit
*before* the re-copy, or it is gone. Only once the diff is one-directional is
the wholesale copy safe.

## Where a fix belongs

Upstream, in the Neutron repo, then re-copy — that keeps the two from
diverging and is what makes a future re-vendor safe. Editing here directly is
what created the drift above. When a fix must land here first (a production
incident), open the matching upstream change in the same session and note it
here, so the next person inherits a diff that closes rather than one that grows.
