package collab

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// 2FA secrets at rest. M4 step 4.2, the "encrypted" half.
//
// The property: a database read must not be a permanent 2FA bypass. Every test
// here treats the ciphertext as something an attacker would have -- a backup, a
// replica, a support dump, an injection on a neighbouring table.

var testInstanceKey = []byte("0123456789abcdef0123456789abcdef")

// TestEncryptTOTPSecret_RoundTrip: encrypt then decrypt returns the secret.
func TestEncryptTOTPSecret_RoundTrip(t *testing.T) {
	secret, err := NewTOTPSecret()
	require2(t, err)

	sealed, err := EncryptTOTPSecret(secret, testInstanceKey)
	require2(t, err)

	if !IsEncryptedTOTPSecret(sealed) {
		t.Errorf("stored value %q is not marked as sealed", sealed)
	}
	if strings.Contains(sealed, secret.Reveal()) {
		t.Error("the ciphertext contains the plaintext secret")
	}
	// The base32 secret is upper-case alphanumerics; a leak would show up as a
	// long run of them.
	if hasRun(sealed, 16) {
		t.Errorf("stored value %q looks like it contains plaintext", sealed)
	}

	got, err := DecryptTOTPSecret(sealed, testInstanceKey)
	require2(t, err)
	if got != secret {
		t.Error("the round trip did not return the original secret")
	}
}

// TestEncryptTOTPSecret_NonceIsFreshPerCall: GCM nonce reuse under one key is
// catastrophic, so two encryptions of the SAME secret must not be identical.
func TestEncryptTOTPSecret_NonceIsFreshPerCall(t *testing.T) {
	secret, err := NewTOTPSecret()
	require2(t, err)

	seen := map[string]bool{}
	for i := 0; i < 25; i++ {
		sealed, err := EncryptTOTPSecret(secret, testInstanceKey)
		require2(t, err)
		if seen[sealed] {
			t.Fatalf("encryption %d produced an identical ciphertext: the nonce is not fresh", i)
		}
		seen[sealed] = true

		// And every one of them still decrypts.
		got, err := DecryptTOTPSecret(sealed, testInstanceKey)
		require2(t, err)
		if got != secret {
			t.Fatalf("ciphertext %d did not decrypt back to the secret", i)
		}
	}
}

// TestDecryptTOTPSecret_RejectsTheWrongInstanceKey: an attacker with the
// ciphertext and a candidate key learns nothing beyond "wrong".
func TestDecryptTOTPSecret_RejectsTheWrongInstanceKey(t *testing.T) {
	secret, _ := NewTOTPSecret()
	sealed, err := EncryptTOTPSecret(secret, testInstanceKey)
	require2(t, err)

	_, err = DecryptTOTPSecret(sealed, []byte("ffffffffffffffffffffffffffffffff"))
	if !errors.Is(err, ErrTOTPSecretCorrupt) {
		t.Errorf("decrypting with the wrong key returned %v, want ErrTOTPSecretCorrupt", err)
	}
	// The message must not say WHICH key was wrong, or it becomes an oracle for
	// a key search.
	if strings.Contains(err.Error(), "ffffffff") {
		t.Errorf("error %q echoes the key", err)
	}
}

// TestDecryptTOTPSecret_RejectsModifiedCiphertext: GCM authenticates, so a
// flipped bit is refused rather than decrypted into a different secret.
//
// This is the difference that matters for a credential. An unauthenticated mode
// would happily return a corrupted plaintext, and a user whose secret quietly
// became invalid would be locked out with no explanation.
func TestDecryptTOTPSecret_RejectsModifiedCiphertext(t *testing.T) {
	secret, _ := NewTOTPSecret()
	sealed, err := EncryptTOTPSecret(secret, testInstanceKey)
	require2(t, err)

	payload := []byte(strings.TrimPrefix(sealed, "v1."))
	decoded, err := base64.RawStdEncoding.DecodeString(string(payload))
	require2(t, err)

	// Flip one bit in the ciphertext body (after the nonce).
	decoded[len(decoded)-1] ^= 0x01
	tampered := "v1." + base64.RawStdEncoding.EncodeToString(decoded)

	_, err = DecryptTOTPSecret(tampered, testInstanceKey)
	if !errors.Is(err, ErrTOTPSecretCorrupt) {
		t.Errorf("a modified ciphertext returned %v, want ErrTOTPSecretCorrupt: GCM must authenticate, not just decrypt", err)
	}
}

// TestEncryptTOTPSecret_RefusesAnEmptySecret: sealing "" would produce a
// ciphertext that decrypts to "", which every reader would read as "not enrolled".
func TestEncryptTOTPSecret_RefusesAnEmptySecret(t *testing.T) {
	if _, err := EncryptTOTPSecret(TOTPSecret(""), testInstanceKey); err == nil {
		t.Error("an empty secret was sealed; it would decrypt to empty and read as 'not enrolled'")
	}
}

// TestEncryptTOTPSecret_RefusesWithoutAnInstanceKey: a zero key is worse than no
// encryption, because it looks like encryption.
func TestEncryptTOTPSecret_RefusesWithoutAnInstanceKey(t *testing.T) {
	secret, _ := NewTOTPSecret()
	if _, err := EncryptTOTPSecret(secret, nil); err == nil {
		t.Error("a secret was sealed with no instance key")
	}
	if _, err := EncryptTOTPSecret(secret, []byte{}); err == nil {
		t.Error("a secret was sealed with an empty instance key")
	}
	// And the same on the way out.
	sealed, _ := EncryptTOTPSecret(secret, testInstanceKey)
	if _, err := DecryptTOTPSecret(sealed, nil); err == nil {
		t.Error("a secret was decrypted with no instance key")
	}
}

// TestDecryptTOTPSecret_RejectsTruncatedAndMalformedValues: a column that is
// corrupt must produce an error, never a short or empty secret.
func TestDecryptTOTPSecret_RejectsTruncatedAndMalformedValues(t *testing.T) {
	for _, bad := range []string{
		"v1.",
		"v1.!!!!not-base64!!!!",
		"v1.AAAA",      // valid base64, shorter than a nonce
		"v2.something", // a future version this build cannot read
	} {
		_, err := DecryptTOTPSecret(bad, testInstanceKey)
		if err == nil {
			t.Errorf("DecryptTOTPSecret(%q) succeeded, want an error", bad)
		}
	}
	// Empty is not corrupt: it is "not enrolled", and the store relies on that.
	got, err := DecryptTOTPSecret("", testInstanceKey)
	if err != nil || got != "" {
		t.Errorf("DecryptTOTPSecret(\"\") = %q, %v; want \"\", nil", got, err)
	}
}

// TestDecryptTOTPSecret_PassesThroughLegacyPlaintext: a deployment that predates
// encryption has plaintext secrets in the column, and refusing to read them locks
// every enrolled user out of their account.
//
// The value is returned UNCHANGED and IsEncryptedTOTPSecret reports false, so the
// caller can tell it needs migrating. The alternative -- erroring -- is the
// availability disaster; the alternative -- silently accepting anything — is a
// hole.
func TestDecryptTOTPSecret_PassesThroughLegacyPlaintext(t *testing.T) {
	legacy := "JBSWY3DPEHPK3PXP"
	got, err := DecryptTOTPSecret(legacy, testInstanceKey)
	if err != nil {
		t.Fatalf("a legacy plaintext secret returned %v, want it passed through", err)
	}
	if string(got) != legacy {
		t.Errorf("legacy secret = %q, want %q", got, legacy)
	}
	if IsEncryptedTOTPSecret(legacy) {
		t.Error("a plaintext secret was reported as sealed, so a migration would skip it")
	}
}

// TestEncryptTOTPSecret_KeysAreDomainSeparated: the derived key must differ from
// the instance key itself, or the same key does two jobs and a bug in either use
// can weaken the other.
func TestEncryptTOTPSecret_KeysAreDomainSeparated(t *testing.T) {
	a, err := deriveSecretKey(testInstanceKey)
	require2(t, err)
	b, err := deriveSecretKey(testInstanceKey)
	require2(t, err)

	if len(a) != 32 {
		t.Errorf("derived key is %d bytes, want 32 for AES-256", len(a))
	}
	if string(a) == string(testInstanceKey) {
		t.Error("the derived key equals the instance key: no domain separation")
	}
	if string(a) != string(b) {
		t.Error("the derivation is not deterministic; a restart would lose every secret")
	}

	// And a different instance key gives a different derived key.
	c, err := deriveSecretKey([]byte("ffffffffffffffffffffffffffffffff"))
	require2(t, err)
	if string(a) == string(c) {
		t.Error("two instance keys derived the same secret key")
	}
}

func require2(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// hasRun reports whether s contains n consecutive base32-ish characters, which is
// what a leaked TOTP secret looks like.
func hasRun(s string, n int) bool {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	run := 0
	for _, c := range s {
		if strings.ContainsRune(alphabet, c) {
			run++
			if run >= n {
				return true
			}
			continue
		}
		run = 0
	}
	return false
}
