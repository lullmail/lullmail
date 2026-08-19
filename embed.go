package main

import (
	"embed"
)

// The Neutron TS dashboard builds to dashboard/dist (static preset, real files
// per route). Until the first `npm run build` the directory is empty and serve
// falls back to a build instruction instead of a blank page.
//
//go:embed all:dashboard/dist
var assets embed.FS

//go:embed schema.sql
var schemaSQL string
