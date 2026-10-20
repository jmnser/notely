// Package serve runs HTTP servers with timeouts and graceful shutdown.
package serve

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Run serves h on addr until the process is interrupted. onShutdown runs
// when shutdown starts, e.g. to end long-lived responses.
func Run(addr string, h http.Handler, onShutdown ...func()) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	for _, f := range onShutdown {
		srv.RegisterOnShutdown(f)
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Printf("listening on http://%s", addr)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}
