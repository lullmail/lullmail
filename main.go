package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		serve()
		return
	}
	switch os.Args[1] {
	case "serve":
		serve()
	case "migrate":
		if err := migrate(); err != nil {
			fmt.Fprintln(os.Stderr, "migrate:", err)
			os.Exit(1)
		}
		fmt.Println("migrate: ok")
	case "backfill-bodies":
		if err := backfillBodies(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "backfill-bodies:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: email-soft [serve|migrate|backfill-bodies]")
		os.Exit(2)
	}
}
