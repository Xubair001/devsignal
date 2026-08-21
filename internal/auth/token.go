package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// tokenBytes is 32 bytes of CSPRNG output — 256 bits, so guessing is not a
// threat model we have to reason about.
const tokenBytes = 32

// NewToken returns the secret to hand to the client and the hash to store.
// The plaintext is never persisted: a database leak must not yield live tokens.
func NewToken() (secret string, hash []byte, err error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("auth: token: %w", err)
	}
	secret = base64.RawURLEncoding.EncodeToString(b)
	return secret, HashToken(secret), nil
}

// HashToken is SHA-256, deliberately not a slow KDF: these are high-entropy
// random tokens, not user-chosen passwords, so there is nothing to brute force
// and the lookup is on the hot path of every authenticated request.
func HashToken(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}
