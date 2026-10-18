// Package auth handles who someone is: credentials, sessions and tokens.
//
// What they may do afterwards is internal/rbac's job. The split matters --
// authentication answers a question with one right answer, authorization
// answers one whose right answer changes with policy, and mixing them is how
// a policy change quietly becomes a login change.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters.
//
// Tuned for roughly 60-90ms on a modest server core, which is slow enough to
// make offline cracking expensive and fast enough that a district office
// signing in at 9am does not queue. RFC 9106's second recommended profile
// (64 MiB, t=3, p=4) with parallelism reduced to 2, because API containers
// are sized by request concurrency and four threads per sign-in oversubscribes
// them.
//
// These are recorded in every hash, so raising them later re-hashes existing
// passwords on next sign-in rather than invalidating them; see NeedsRehash.
const (
	argonTime    uint32 = 3
	argonMemory  uint32 = 64 * 1024 // KiB
	argonThreads uint8  = 2
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
)

// MinPasswordLength is deliberately length-first rather than a composition
// rule. Character-class requirements produce Solapur@2026 across an entire
// district office; length does not.
const MinPasswordLength = 12

// Errors from credential handling.
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrMalformedHash      = errors.New("malformed password hash")
	ErrWeakPassword       = errors.New("password does not meet policy")
)

// HashPassword returns a PHC-encoded argon2id hash.
func HashPassword(plain string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generating salt: %w", err)
	}

	key := argon2.IDKey([]byte(plain), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether plain matches the encoded hash.
//
// The comparison is constant-time. So is the failure path for a malformed
// hash: returning early on a bad record would let a caller distinguish "no
// such user" from "user exists with a corrupt hash", which is exactly the
// distinction an enumeration attack is looking for.
func VerifyPassword(encoded, plain string) (ok bool, needsRehash bool, err error) {
	p, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false, false, err
	}

	got := argon2.IDKey([]byte(plain), salt, p.time, p.memory, p.threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false, nil
	}

	return true, p.outdated(), nil
}

// NeedsRehash reports whether a stored hash was produced with weaker
// parameters than the current ones.
func NeedsRehash(encoded string) bool {
	p, _, _, err := decodeHash(encoded)
	if err != nil {
		// An unreadable hash cannot be verified against either, so the user
		// will be forced through a reset regardless.
		return true
	}
	return p.outdated()
}

type params struct {
	memory  uint32
	time    uint32
	threads uint8
}

func (p params) outdated() bool {
	return p.memory < argonMemory || p.time < argonTime || p.threads != argonThreads
}

func decodeHash(encoded string) (params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return params{}, nil, nil, ErrMalformedHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return params{}, nil, nil, ErrMalformedHash
	}
	if version != argon2.Version {
		return params{}, nil, nil, fmt.Errorf(
			"%w: argon2 version %d, this build speaks %d",
			ErrMalformedHash, version, argon2.Version)
	}

	var p params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return params{}, nil, nil, ErrMalformedHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return params{}, nil, nil, ErrMalformedHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return params{}, nil, nil, ErrMalformedHash
	}

	return p, salt, key, nil
}

// CheckPasswordPolicy validates a proposed password.
//
// Length first, then the two failures that actually occur in the field: the
// account name reused as its own password, and a handful of passwords that
// every deployment is issued and never changes.
func CheckPasswordPolicy(password, username, fullName string) error {
	if len([]rune(password)) < MinPasswordLength {
		return fmt.Errorf("%w: at least %d characters", ErrWeakPassword, MinPasswordLength)
	}
	if len(password) > 256 {
		// Bounded because argon2 hashes whatever it is handed, and a
		// megabyte password is a cheap way to make a server do work.
		return fmt.Errorf("%w: at most 256 bytes", ErrWeakPassword)
	}

	lower := strings.ToLower(password)
	if username != "" && strings.Contains(lower, strings.ToLower(username)) {
		return fmt.Errorf("%w: it contains the username", ErrWeakPassword)
	}
	for _, part := range strings.Fields(strings.ToLower(fullName)) {
		if len(part) >= 4 && strings.Contains(lower, part) {
			return fmt.Errorf("%w: it contains part of the account holder's name", ErrWeakPassword)
		}
	}
	for _, banned := range commonPasswords {
		if lower == banned {
			return fmt.Errorf("%w: it is one of the most commonly used passwords", ErrWeakPassword)
		}
	}

	// A password of one repeated rune passes a length check and nothing else.
	distinct := map[rune]struct{}{}
	for _, r := range password {
		if !unicode.IsSpace(r) {
			distinct[r] = struct{}{}
		}
	}
	if len(distinct) < 5 {
		return fmt.Errorf("%w: too few distinct characters", ErrWeakPassword)
	}

	return nil
}

// commonPasswords is short on purpose. A full breach corpus belongs behind a
// lookup service; this catches the handful that a hurried deployment reaches
// for, which is the realistic failure here.
var commonPasswords = []string{
	"password", "password1", "password123", "passw0rd123",
	"123456789012", "1234567890123", "qwertyuiop12",
	"administrator", "admin@12345", "admin@123456",
	"welcome@1234", "welcome123456", "changeme1234",
	"esurvey@1234", "esurvey12345", "agribid@1234",
	"maharashtra1", "maharashtra@1",
}
