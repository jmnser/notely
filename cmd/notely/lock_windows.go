package main

import (
	"os"

	winapi "golang.org/x/sys/windows"
)

// lockFile takes an exclusive lock on f without waiting.
func lockFile(f *os.File) error {
	return winapi.LockFileEx(winapi.Handle(f.Fd()),
		winapi.LOCKFILE_EXCLUSIVE_LOCK|winapi.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, new(winapi.Overlapped))
}
