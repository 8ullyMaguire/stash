package auth_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHash_ProducesArgon2idPHCString(t *testing.T) {
	h, err := auth.Hash("correct horse battery staple")
	require.NoError(t, err)

	// The exact prefix matters: a stored value that does not start with the
	// algorithm and version is a value this package will refuse to verify,
	// which locks the account out rather than failing loudly at write time.
	assert.True(t, strings.HasPrefix(h, "$argon2id$v=19$m="),
		"hash must be a PHC argon2id string, got %q", h[:min(len(h), 32)])

	p, salt, key, err := auth.Decode(h)
	require.NoError(t, err)
	assert.Equal(t, auth.DefaultParams, p)
	assert.Len(t, salt, auth.SaltLen)
	assert.Len(t, key, int(auth.KeyLen))
}

func TestHash_SaltIsRandomPerCall(t *testing.T) {
	// The same password hashed twice must differ. Equal outputs mean a fixed or
	// shared salt, and a shared salt makes one rainbow table worth building for
	// every account at once.
	a, err := auth.Hash("same password")
	require.NoError(t, err)
	b, err := auth.Hash("same password")
	require.NoError(t, err)

	assert.NotEqual(t, a, b, "identical passwords must not produce identical hashes")
}

func TestVerify_AcceptsTheCorrectPassword(t *testing.T) {
	h, err := auth.Hash("s3cret")
	require.NoError(t, err)
	assert.NoError(t, auth.Verify("s3cret", h))
}

func TestVerify_RejectsTheWrongPassword(t *testing.T) {
	h, err := auth.Hash("s3cret")
	require.NoError(t, err)

	err = auth.Verify("s3cret ", h)
	assert.ErrorIs(t, err, auth.ErrPasswordMismatch,
		"a near-miss must fail exactly like a wrong password")
}

func TestVerify_RejectsAnEmptyPassword(t *testing.T) {
	// Guards against a stray empty string verifying against an empty hash, and
	// against a blank submit being treated as "no password supplied, allow".
	h, err := auth.Hash("")
	require.NoError(t, err)

	// Hashing an empty password is permitted (an admin may create such an
	// account), but it must not verify as anything else, and must certainly not
	// verify against a well-formed non-empty password's hash.
	other, err := auth.Hash("x")
	require.NoError(t, err)
	assert.ErrorIs(t, auth.Verify("", other), auth.ErrPasswordMismatch)
	assert.NoError(t, auth.Verify("", h))
}

func TestVerify_RejectsAPreviouslyHashedPasswordAsAPassword(t *testing.T) {
	// Guards against a "double hashing" bug where the hash itself becomes a
	// valid credential. A leaked hash must not be replayable as a password.
	h, err := auth.Hash("original")
	require.NoError(t, err)

	assert.ErrorIs(t, auth.Verify(h, h), auth.ErrPasswordMismatch,
		"a hash must not verify against itself as a password")
}

// A malformed stored value must be distinguishable from a wrong password.
// Conflating them turns a data-integrity bug into invisible auth failures.
func TestVerify_DistinguishesCorruptHashFromMismatch(t *testing.T) {
	for name, stored := range map[string]string{
		"empty":            "",
		"not a phc string": "hunter2",
		"truncated":        "$argon2id$v=19$m=65536,t=3,p=2$",
		"missing hash":     "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0",
		"bad salt b64":     "$argon2id$v=19$m=65536,t=3,p=2$!!!$aGFzaA",
		"bad params":       "$argon2id$v=19$m=zero,t=3,p=2$c2FsdA$aGFzaA",
		"zero memory":      "$argon2id$v=19$m=0,t=3,p=2$c2FsdA$aGFzaA",
		"future version":   "$argon2id$v=99$m=65536,t=3,p=2$c2FsdA$aGFzaA",
		"argon2i not ok":   "$argon2i$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",
		"md5 not ok":       "$2y$10$abcdefghijklmnopqrstuv",
	} {
		t.Run(name, func(t *testing.T) {
			err := auth.Verify("anything", stored)
			require.Error(t, err)
			assert.ErrorIs(t, err, auth.ErrInvalidHash,
				"a malformed stored hash must report ErrInvalidHash, not a mismatch")
			assert.NotErrorIs(t, err, auth.ErrPasswordMismatch,
				"a corrupt row must be distinguishable from a user typing the wrong password")
		})
	}
}

// The cost parameters live in the hash, not in a config file. If they did not,
// changing them would make every existing password unverifiable.
func TestVerify_AcceptsHashesMadeWithDifferentParams(t *testing.T) {
	weak := auth.Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1}
	h, err := auth.HashWithParams("s3cret", weak)
	require.NoError(t, err)

	assert.NoError(t, auth.Verify("s3cret", h),
		"a hash written with older, weaker parameters must still verify")

	// ...and the caller is told to upgrade it.
	assert.True(t, auth.NeedsRehash(h, auth.DefaultParams),
		"a weak hash must be flagged for upgrade on next login")
}

func TestNeedsRehash(t *testing.T) {
	current, err := auth.Hash("s3cret")
	require.NoError(t, err)
	assert.False(t, auth.NeedsRehash(current, auth.DefaultParams),
		"a hash at current parameters must not be flagged")

	stronger := auth.Params{Memory: 128 * 1024, Iterations: 4, Parallelism: 4}
	assert.True(t, auth.NeedsRehash(current, stronger),
		"raising the cost parameters must flag existing hashes for rehash")

	// Lowering the parameters must NOT flag: that would cause a login to
	// silently rewrite a strong hash as a weak one.
	assert.False(t, auth.NeedsRehash(current, auth.Params{Memory: 1024, Iterations: 1, Parallelism: 1}),
		"weakening parameters must not trigger a downgrade rehash")

	// Unparseable input must be flagged, so a corrupt row gets rewritten
	// rather than lingering forever.
	assert.True(t, auth.NeedsRehash("garbage", auth.DefaultParams))
}

func TestHashWithParams_RejectsZeroParams(t *testing.T) {
	// A zero cost parameter would either panic inside argon2 or produce a hash
	// that is trivially cheap to attack. Refuse at the boundary.
	for name, p := range map[string]auth.Params{
		"zero memory":      {Memory: 0, Iterations: 3, Parallelism: 2},
		"zero iterations":  {Memory: 64 * 1024, Iterations: 0, Parallelism: 2},
		"zero parallelism": {Memory: 64 * 1024, Iterations: 3, Parallelism: 0},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := auth.HashWithParams("s3cret", p)
			require.Error(t, err)
			assert.False(t, errors.Is(err, auth.ErrPasswordMismatch))
		})
	}
}

// The hash must not be recoverable from its own representation, and the
// password must not appear in it. Obvious for a real KDF, but this is the kind
// of test that catches a future refactor to a plain digest.
func TestHash_DoesNotLeakThePassword(t *testing.T) {
	const pw = "unmistakable-password-1234"
	h, err := auth.Hash(pw)
	require.NoError(t, err)
	assert.NotContains(t, h, pw, "the password must not appear in the hash")
	assert.Greater(t, len(h), 40, "a raw digest would be suspiciously short")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
