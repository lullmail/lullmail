//go:build !unix

package main

import "os"

// flockExclusive is a no-op where advisory file locks are unavailable;
// lullmail deploys on Unix (containers), so this only keeps other
// platforms building rather than silently claiming serialization.
func flockExclusive(f *os.File) error { return nil }

func funlock(f *os.File) {}
