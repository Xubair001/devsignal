// Package auth owns credentials, sessions and the audit trail.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters, stated explicitly rather than left to a library default —
// CLAUDE.md requires them written down so they can be reviewed and raised.
// Tuned for ~50-100ms on commodity hardware; re-benchmark before changing.
const (
	argonTime    = 3         // iterations
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	errBadHashFormat      = errors.New("auth: malformed password hash")
)

// HashPassword returns the standard argon2id encoded form. The parameters travel
// with the hash, so they can be raised later without invalidating old passwords.
func HashPassword(plain string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: salt: %w", err)
	}
	key := argon2.IDKey([]byte(plain), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword re-derives with the stored parameters. Comparison is constant
// time; a timing difference here is a usable oracle.
func VerifyPassword(encoded, plain string) error {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return errBadHashFormat
	}
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return errBadHashFormat
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return errBadHashFormat
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return errBadHashFormat
	}

	got := argon2.IDKey([]byte(plain), salt, time, memory, threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrInvalidCredentials
	}
	return nil
}

// NeedsRehash reports whether a stored hash used weaker parameters than current
// policy, so it can be upgraded transparently on the next successful login.
func NeedsRehash(encoded string) bool {
	var memory, time uint32
	var threads uint8
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 {
		return true
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return true
	}
	return memory < argonMemory || time < argonTime || threads < argonThreads
}
