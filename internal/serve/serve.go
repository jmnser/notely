// Package serve runs HTTP servers with timeouts and graceful shutdown.
package serve

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// TLS names the PEM certificate and key files to serve HTTPS with. Both empty
// serves plain HTTP.
type TLS struct {
	CertFile, KeyFile string
}

// Run serves h on addr until the process is interrupted. onShutdown runs
// when shutdown starts, e.g. to end long-lived responses. With TLS files, it
// serves HTTPS and picks up renewed files without a restart.
func Run(addr string, files TLS, h http.Handler, onShutdown ...func()) error {
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
	scheme := "http"
	switch {
	case files.CertFile == "" && files.KeyFile == "":
	case files.CertFile == "" || files.KeyFile == "":
		return errors.New("TLS needs both a certificate and a key file")
	default:
		kp, err := newKeyPair(files.CertFile, files.KeyFile)
		if err != nil {
			return err
		}
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: kp.get}
		scheme = "https"
	}
	errc := make(chan error, 1)
	go func() {
		if srv.TLSConfig != nil {
			errc <- srv.ListenAndServeTLS("", "")
		} else {
			errc <- srv.ListenAndServe()
		}
	}()
	log.Printf("listening on %s://%s", scheme, addr)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}
