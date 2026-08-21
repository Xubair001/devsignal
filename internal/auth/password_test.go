package auth

import (
	"strings"
	"testing"
)

func TestHashVerifyRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := VerifyPassword(h, "correct horse battery staple"); err != nil {
		t.Fatalf("verify correct password: %v", err)
	}
	if err := VerifyPassword(h, "wrong password"); err == nil {
		t.Fatal("wrong password was accepted")
	}
}

func TestHashIsSaltedPerCall(t *testing.T) {
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	if a == b {
		t.Fatal("identical hashes for the same password: salt is not random")
	}
}

func TestHashEncodesParameters(t *testing.T) {
	h, _ := HashPassword("x")
	// Parameters must travel with the hash so they can be raised later.
	for _, want := range []string{"$argon2id$", "m=65536", "t=3", "p=4"} {
		if !strings.Contains(h, want) {
			t.Errorf("hash %q missing %q", h, want)
		}
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"", "not-a-hash", "$argon2id$v=19$bad$$", "$bcrypt$x$y$z$w"} {
		if err := VerifyPassword(bad, "x"); err == nil {
			t.Errorf("malformed hash %q was accepted", bad)
		}
	}
}

func TestNeedsRehash(t *testing.T) {
	current, _ := HashPassword("x")
	if NeedsRehash(current) {
		t.Error("a hash at current parameters should not need rehashing")
	}
	// Weaker memory than policy must be flagged for transparent upgrade.
	weak := "$argon2id$v=19$m=1024,t=1,p=1$c2FsdHNhbHRzYWx0$aGFzaGhhc2hoYXNoaGFzaA"
	if !NeedsRehash(weak) {
		t.Error("weak parameters should need rehashing")
	}
	if !NeedsRehash("garbage") {
		t.Error("unparseable hash should need rehashing")
	}
}

func TestNewTokenIsUniqueAndHashed(t *testing.T) {
	s1, h1, err := NewToken()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	s2, _, _ := NewToken()
	if s1 == s2 {
		t.Fatal("tokens repeat")
	}
	if len(h1) != 32 {
		t.Fatalf("hash length = %d, want 32", len(h1))
	}
	if strings.Contains(string(h1), s1) {
		t.Fatal("hash contains the plaintext")
	}
	// The hash must be reproducible from the secret, or lookup cannot work.
	if got := HashToken(s1); string(got) != string(h1) {
		t.Fatal("HashToken is not deterministic")
	}
}
