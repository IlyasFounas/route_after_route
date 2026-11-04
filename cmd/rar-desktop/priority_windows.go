package main

import "syscall"

const (
	normalPriorityClass      = 0x20
	belowNormalPriorityClass = 0x4000
)

var procSetPriorityClass = syscall.NewLazyDLL("kernel32.dll").NewProc("SetPriorityClass")

// setNice maps a nice value to a Windows priority class: below normal for
// any positive nice (never idle, so that exits are not starved), normal
// otherwise.
func setNice(nice int) error {
	class := uintptr(normalPriorityClass)
	if nice > 0 {
		class = belowNormalPriorityClass
	}
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		return err
	}
	if r, _, err := procSetPriorityClass.Call(uintptr(h), class); r == 0 {
		return err
	}
	return nil
}
