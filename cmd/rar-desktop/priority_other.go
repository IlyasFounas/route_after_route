//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !windows

package main

// setNice is not supported on this system: the effort level only changes
// the cadence and the cores used.
func setNice(int) error { return nil }
