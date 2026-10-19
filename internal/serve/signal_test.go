//go:build !windows

package serve

import (
	"net/http"
	"syscall"
	"testing"
	"time"
)

func TestRunStopsOnSIGTERM(t *testing.T) {
	errc := make(chan error, 1)
	go func() { errc <- Run("127.0.0.1:0", http.NotFoundHandler()) }()
	time.Sleep(100 * time.Millisecond)
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errc:
		if err != nil {
			t.Errorf("shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no shutdown on SIGTERM")
	}
}
