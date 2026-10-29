//go:build !wasm && !linux && !darwin

package main

import (
	"context"
	"errors"
)

// runDaemon is only available on Linux and macOS.
func runDaemon(context.Context, *client, config, string, string) error {
	return errors.New("the daemon runs on Linux and macOS only")
}
