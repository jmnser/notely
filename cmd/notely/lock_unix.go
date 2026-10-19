//go:build !wasm && !windows

package main

import (
	"os"
	"syscall"
)

// lockFile takes an exclusive lock on f without waiting.
func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) //nolint:gosec // a file descriptor fits int
}
