//go:build darwin || freebsd || netbsd || openbsd

package main

import "syscall"

// setNice sets the scheduling priority of the process. Raising it back (a
// lower nice) may need privileges: the error then says so.
func setNice(nice int) error { return syscall.Setpriority(syscall.PRIO_PROCESS, 0, nice) }
