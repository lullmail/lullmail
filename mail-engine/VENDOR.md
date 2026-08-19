# Vendored from ../../Neutron/mail

Upstream: `~/Documents/Code Projects/Neutron/mail` (module
`github.com/neutron-build/neutron/mail`). Referenced from the root go.mod via
a `replace` directive — the module has no published version, and deploy builds
run on servers with no sibling Neutron checkout (see NEUTRON_BUGS.md N4).

Synced: 2026-08-18. Re-vendor with:
`rm -rf mail-engine && cp -R ../../Neutron/mail ./mail-engine`

Local edits here will be overwritten by the next re-vendor — fix upstream in
the Neutron repo, then re-copy. (akiroo's neutron-go vendor convention.)
