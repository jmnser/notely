package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters for new hashes: 64 MiB, 3 passes, 1 thread.
const (
	argonMemory  = 64 * 1024
	argonTime    = 3
	argonThreads = 1
	argonSalt    = 16
	argonKey     = 32
)

var b64 = base64.RawStdEncoding

// hashPassword returns the argon2id hash of password with a random salt, in
// the PHC string format.
func hashPassword(password string) string {
	salt := make([]byte, argonSalt)
	_, _ = rand.Read(salt) // never fails
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKey)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key))
}

// argonHash is a parsed argon2id PHC string.
type argonHash struct {
	memory, time uint32
	threads      uint8
	salt, key    []byte
}

func parseHash(s string) (argonHash, error) {
	var h argonHash
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return h, errors.New("not an argon2id hash")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return h, errors.New("unsupported argon2 version")
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &h.memory, &h.time, &h.threads); err != nil {
		return h, fmt.Errorf("argon2 parameters: %w", err)
	}
	// Bounds keep a bad users file from exhausting memory or CPU.
	if h.memory < 8*1024 || h.memory > 1024*1024 || h.time < 1 || h.time > 16 || h.threads < 1 || h.threads > 16 {
		return h, errors.New("argon2 parameters out of range")
	}
	var err error
	if h.salt, err = b64.DecodeString(parts[4]); err != nil || len(h.salt) < 8 {
		return h, errors.New("invalid argon2 salt")
	}
	if h.key, err = b64.DecodeString(parts[5]); err != nil || len(h.key) < 16 || len(h.key) > 64 {
		return h, errors.New("invalid argon2 key")
	}
	return h, nil
}

// check reports whether password matches h, in constant time.
func (h argonHash) check(password string) bool {
	n := uint32(len(h.key)) //nolint:gosec // parseHash limits it to 64
	key := argon2.IDKey([]byte(password), h.salt, h.time, h.memory, h.threads, n)
	return subtle.ConstantTimeCompare(key, h.key) == 1
}
