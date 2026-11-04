package main

import (
	"errors"
	"os"
	"strconv"
	"syscall"
)

// setNice sets the scheduling priority of the whole process. On Linux the
// nice value is per thread, so every thread of the process is changed;
// threads started later inherit it. Raising the priority back (a lower
// nice) usually needs privileges: the error then says so.
func setNice(nice int) error {
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return syscall.Setpriority(syscall.PRIO_PROCESS, 0, nice)
	}
	var errs []error
	for _, t := range tasks {
		tid, err := strconv.Atoi(t.Name())
		if err != nil {
			continue
		}
		if err := syscall.Setpriority(syscall.PRIO_PROCESS, tid, nice); err != nil && !errors.Is(err, syscall.ESRCH) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
