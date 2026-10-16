package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"

	"lullmail/internal/app"
)

// The Neutron TS dashboard builds to dashboard/dist (static preset, real files
// per route). Until the first `npm run build` the directory is empty and serve
// falls back to a build instruction instead of a blank page. The product
// package lives in internal/app; this file is the only wiring at the root.
//
//go:embed all:dashboard/dist
var dashboard embed.FS

func main() {
	dist, err := fs.Sub(dashboard, "dashboard/dist")
	if err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
	if len(os.Args) < 2 {
		app.Serve(dist)
		return
	}
	switch os.Args[1] {
	case "serve":
		app.Serve(dist)
	case "migrate":
		if err := app.Migrate(); err != nil {
			fmt.Fprintln(os.Stderr, "migrate:", err)
			os.Exit(1)
		}
		fmt.Println("migrate: ok")
	case "repair-scans":
		if err := app.RepairScans(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "repair-scans:", err)
			os.Exit(1)
		}
	case "backfill-bodies":
		if err := app.BackfillBodies(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "backfill-bodies:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: lullmail [serve|migrate|backfill-bodies|repair-scans]")
		os.Exit(2)
	}
}
