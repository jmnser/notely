package serve

import (
	"crypto/tls"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// keyPair serves a certificate and key from files and reloads them when
// either file changes, so a renewed certificate is used without a restart.
type keyPair struct {
	certFile, keyFile string

	mu              sync.Mutex
	cert            *tls.Certificate
	certMod, keyMod time.Time
}

func newKeyPair(certFile, keyFile string) (*keyPair, error) {
	k := &keyPair{certFile: certFile, keyFile: keyFile}
	if _, err := k.get(nil); err != nil {
		return nil, err
	}
	return k, nil
}

// get is a tls.Config.GetCertificate callback. If reloading fails, for
// example while the files are being replaced, it keeps the last certificate.
func (k *keyPair) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	certMod, keyMod, err := modTimes(k.certFile, k.keyFile)
	if err == nil && k.cert != nil && certMod.Equal(k.certMod) && keyMod.Equal(k.keyMod) {
		return k.cert, nil
	}
	if err == nil {
		var cert tls.Certificate
		if cert, err = tls.LoadX509KeyPair(k.certFile, k.keyFile); err == nil {
			k.cert, k.certMod, k.keyMod = &cert, certMod, keyMod
			return k.cert, nil
		}
	}
	if k.cert == nil {
		return nil, fmt.Errorf("load TLS key pair: %w", err)
	}
	log.Printf("reload TLS key pair, keeping the previous one: %v", err)
	return k.cert, nil
}

func modTimes(files ...string) (cert, key time.Time, err error) {
	var mods [2]time.Time
	for i, f := range files {
		fi, err := os.Stat(f)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		mods[i] = fi.ModTime()
	}
	return mods[0], mods[1], nil
}
