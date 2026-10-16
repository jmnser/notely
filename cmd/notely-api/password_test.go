package main

import (
	"strings"
	"testing"
)

func TestHashPassword(t *testing.T) {
	a, b := hashPassword("secret"), hashPassword("secret")
	if a == b {
		t.Error("hashes share a salt")
	}
	if !strings.HasPrefix(a, "$argon2id$v=19$m=65536,t=3,p=1$") {
		t.Errorf("hash = %s", a)
	}
	h, err := parseHash(a)
	if err != nil {
		t.Fatal(err)
	}
	if !h.check("secret") || h.check("Secret") || h.check("") {
		t.Error("check gives wrong results")
	}
}

func TestParseHash(t *testing.T) {
	if _, err := parseHash(defaultHash); err != nil {
		t.Errorf("default hash: %v", err)
	}
	for _, s := range []string{
		"",
		"plain",
		"$argon2i$v=19$m=65536,t=3,p=1$c2FsdHNhbHQ$" + strings.Repeat("A", 43),
		"$argon2id$v=16$m=65536,t=3,p=1$c2FsdHNhbHQ$" + strings.Repeat("A", 43),
		"$argon2id$v=19$m=4294967295,t=3,p=1$c2FsdHNhbHQ$" + strings.Repeat("A", 43),
		"$argon2id$v=19$m=65536,t=99,p=1$c2FsdHNhbHQ$" + strings.Repeat("A", 43),
		"$argon2id$v=19$m=65536,t=3,p=1$!!$" + strings.Repeat("A", 43),
		"$argon2id$v=19$m=65536,t=3,p=1$c2FsdHNhbHQ$QUJD",
	} {
		if _, err := parseHash(s); err == nil {
			t.Errorf("parseHash(%q) accepted", s)
		}
	}
}
