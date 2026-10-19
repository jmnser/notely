//go:build !wasm && !windows

package main

import (
	"errors"
	"syscall"
)

// running reports whether process pid exists.
func running(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
