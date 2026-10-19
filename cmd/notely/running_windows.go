package main

import "os"

// running reports whether process pid exists; on Windows finding it opens it.
func running(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	p.Release() //nolint:errcheck,gosec // only the handle
	return true
}
