// Package auth provides password hashing for StashForge accounts.
//
// Upstream Stash has no user table and so no password hashing: it stores a
// single credential in config. Everything here is new, and it is the part of
// the auth subsystem where a mistake is unrecoverable -- a weak hash or a
// silent failure to verify turns a public instance's user table into a
// plaintext-ish password dump.
//
// # Hash format
//
// The stored value is a PHC-format string:
//
//	$argon2id$v=19$m=65536,t=3,p=2$<base64 salt>$<base64 hash>
//
// Parameters are embedded in the hash, never read from a global. A future
// change to the cost parameters can then be rolled out to new hashes while old
// ones keep verifying, and each user can be migrated on next login. Storing a
// bare digest with the parameters in a config file would make every existing
// password unverifiable the moment the parameters changed.
//
// # Why argon2id
//
// OWASP's current first choice for password hashing, and memory-hard: it makes
// large-scale GPU cracking uneconomic, which is the realistic threat for a
// public instance whose user table might be dumped. bcrypt is the previous
// recommendation and is not memory-hard; scrypt is acceptable but slower to
// parameterise for the same protection.
//
// # Timing
//
// argon2.Key is not constant-time with respect to the password, so a wrong
// password and an unknown user are not distinguished by timing here. The caller
// is responsible for that: see VerifyPassword's note on the dummy hash, and
// authService.Login, which always runs a verification even when no user matched.
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

// Params are the argon2id cost parameters for a NEW hash.
//
// These follow the OWASP minimum for argon2id (19 MiB of memory, t=2, p=1) with
// the time cost doubled to 3, because this instance is internet-facing and
// login is not latency-sensitive. They are recorded in every hash, so raising
// them later does not invalidate existing passwords.
type Params struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
}

// DefaultParams is what Hash uses.
var DefaultParams = Params{
	Memory:      64 * 1024, // 64 MiB
	Iterations:  3,
	Parallelism: 2,
}

// KeyLen is the derived-key length in bytes. 32 matches the argon2id
// recommendation and the output of SHA-256, so there is no reason to go longer.
const KeyLen uint32 = 32

// SaltLen is the per-password salt length. 16 bytes is the argon2
// recommendation and is ample at this cost.
const SaltLen = 16

var (
	// ErrInvalidHash means the stored value is not a parseable PHC string.
	// It is deliberately distinct from a mismatch so a corrupt row is
	// diagnosable in logs without being reportable to the caller.
	ErrInvalidHash = errors.New("invalid password hash format")

	// ErrPasswordMismatch is returned for a wrong password. It is the only
	// error a login endpoint may surface.
	ErrPasswordMismatch = errors.New("password mismatch")
)

// Hash derives a PHC string for password using a fresh random salt.
//
// The salt is generated here, never supplied by the caller: a salt that an
// endpoint can influence is a salt an attacker can fix, and fixed salts are
// what make a rainbow table worth building.
func Hash(password string) (string, error) {
	return HashWithParams(password, DefaultParams)
}

// HashWithParams is Hash with explicit cost parameters. It exists so a
// migration can raise costs and re-verify, not so an endpoint can weaken them.
func HashWithParams(password string, p Params) (string, error) {
	if p.Memory == 0 || p.Iterations == 0 || p.Parallelism == 0 {
		return "", fmt.Errorf("auth: argon2 params must be non-zero, got %+v", p)
	}

	salt := make([]byte, SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: reading salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, KeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Iterations, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// Verify reports whether password matches the stored PHC string.
//
// It returns ErrPasswordMismatch for a wrong password and ErrInvalidHash for a
// malformed stored value. The two are kept apart on purpose: a corrupt row is
// a bug to fix, a mismatch is a user typing the wrong thing, and conflating
// them makes a data-integrity problem look like routine auth failure.
//
// The comparison of the derived key uses subtle.ConstantTimeCompare. That is
// belt-and-braces -- argon2's own output comparison is already constant-time in
// the hash bytes -- but a direct bytes.Equal here would be a future regression
// if the derivation ever changed.
func Verify(password, encoded string) error {
	p, salt, want, err := Decode(encoded)
	if err != nil {
		return err
	}

	got := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, uint32(len(want)))

	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrPasswordMismatch
	}

	return nil
}

// NeedsRehash reports whether encoded was produced with parameters weaker than
// current. The login path calls this after a successful verify and re-hashes,
// which is how raising the cost parameters actually reaches existing accounts
// instead of only new ones.
//
// The comparison is deliberately one-directional: a hash is flagged only if it
// is WEAKER than the current parameters on some axis. It is not flagged merely
// because its parameters differ, and it is never flagged for being stronger.
// An equality test on Parallelism alone was wrong here -- it reported a hash
// with 128 MiB of memory but fewer threads as needing a downgrade, and the
// login path would then have quietly rewritten a strong hash into a weaker one.
func NeedsRehash(encoded string, current Params) bool {
	p, _, _, err := Decode(encoded)
	if err != nil {
		// Unparseable: force a re-hash on next successful login rather than
		// leaving a row that can never be upgraded.
		return true
	}

	return p.Memory < current.Memory || p.Iterations < current.Iterations
}

// Decode parses a PHC-format argon2id string into its parameters, salt and
// derived key.
func Decode(encoded string) (Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash
	if len(parts) != 6 || parts[0] != "" {
		return Params{}, nil, nil, fmt.Errorf("%w: expected 6 $-separated fields, got %d", ErrInvalidHash, len(parts))
	}

	if parts[1] != "argon2id" {
		// argon2i is not accepted: this instance has no legacy hashes to keep
		// working, and argon2id's hybrid resistance is the point of choosing
		// it. Rejecting loudly beats silently accepting a weaker scheme.
		return Params{}, nil, nil, fmt.Errorf("%w: algorithm %q is not argon2id", ErrInvalidHash, parts[1])
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return Params{}, nil, nil, fmt.Errorf("%w: bad version field %q", ErrInvalidHash, parts[2])
	}
	if version != argon2.Version {
		return Params{}, nil, nil, fmt.Errorf("%w: unsupported version %d", ErrInvalidHash, version)
	}

	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Iterations, &p.Parallelism); err != nil {
		return Params{}, nil, nil, fmt.Errorf("%w: bad params field %q", ErrInvalidHash, parts[3])
	}
	if p.Memory == 0 || p.Iterations == 0 || p.Parallelism == 0 {
		return Params{}, nil, nil, fmt.Errorf("%w: zero cost parameter in %q", ErrInvalidHash, parts[3])
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return Params{}, nil, nil, fmt.Errorf("%w: bad salt: %v", ErrInvalidHash, err)
	}

	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return Params{}, nil, nil, fmt.Errorf("%w: bad hash: %v", ErrInvalidHash, err)
	}
	if len(hash) == 0 {
		return Params{}, nil, nil, fmt.Errorf("%w: empty hash", ErrInvalidHash)
	}

	return p, salt, hash, nil
}
