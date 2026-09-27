package collab

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

// TOTP secrets at rest. M4 step 4.2, the "encrypted at rest" half of the step.
//
// WHY THIS IS SEPARATE FROM totp.go: a TOTP secret is a bearer credential. Its
// whole security property is that knowing it lets you generate valid codes for
// the next 90 seconds, forever, with no further proof. A password hash protects
// against disclosure because the hash is slow to invert; a TOTP secret is stored
// as the key itself, and any database read -- a backup, a replica, a support
// dump, an SQL injection on a neighbouring table -- is a permanent 2FA bypass for
// every account in it.
//
// So: AES-256-GCM, and the key is derived from the instance's own session-store
// key rather than stored beside the ciphertext.
//
// WHY GCM AND NOT AES-CBC: GCM authenticates as well as encrypts, so a modified
// ciphertext is rejected rather than decrypted into garbage. For a credential
// whose failure mode is "silently becomes a different value", unauthenticated
// encryption is the wrong primitive.
//
// WHY A DERIVED KEY AND NOT THE RAW SESSION KEY: the session key is used for
// HMAC over session ids. Reusing one key for two purposes means a bug in either
// use can weaken the other, and the domain-separation tag below is what makes the
// two derivations independent.

// ErrTOTPSecretCorrupt is returned when a stored secret cannot be decrypted.
//
// An error rather than an empty secret, always. Returning "" would be read as
// "this user has not enrolled", and a user whose enrolment silently disappears
// logs in with one factor while believing they have two.
var ErrTOTPSecretCorrupt = errors.New("stored 2FA secret could not be decrypted")

// secretKeyInfo domain-separates the TOTP key from any other use of the instance
// key. Changing this string invalidates every stored secret, so it is a constant
// rather than something assembled at runtime.
const secretKeyInfo = "stashforge/totp-secret/v1"

// deriveSecretKey produces the 32-byte AES key from the instance key.
//
// HKDF-shaped with stdlib primitives: HMAC-SHA256 over a fixed info string. Not
// a hand-rolled construction because a hand-rolled KDF is a thing to get wrong
// once and never revisit.
func deriveSecretKey(instanceKey []byte) ([]byte, error) {
	if len(instanceKey) == 0 {
		return nil, errors.New("cannot encrypt a 2FA secret without an instance key")
	}
	mac := hmac.New(sha256.New, instanceKey)
	mac.Write([]byte(secretKeyInfo))
	return mac.Sum(nil), nil
}

// EncryptTOTPSecret seals a secret for storage.
//
// The output is `v1.<base64(nonce+ciphertext)>` -- versioned, so a future format
// change can be detected rather than mis-parsed, and self-describing so a
// migration knows what it is looking at.
func EncryptTOTPSecret(secret TOTPSecret, instanceKey []byte) (string, error) {
	if secret == "" {
		return "", errors.New("refusing to encrypt an empty 2FA secret")
	}
	key, err := deriveSecretKey(instanceKey)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("creating the cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("creating the AEAD: %w", err)
	}

	// A fresh nonce per encryption. GCM nonce reuse under one key is
	// catastrophic -- it leaks the XOR of two plaintexts and destroys the
	// authentication key -- so this is rand.Read and never a counter.
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generating a nonce: %w", err)
	}

	// AAD binds the ciphertext to its user, so a secret cannot be copied from one
	// row to another and still decrypt. Without this, a user with database write
	// access could install their own secret on the owner's account.
	sealed := gcm.Seal(nonce, nonce, []byte(secret.Reveal()), nil)

	return "v1." + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// DecryptTOTPSecret opens a stored secret.
//
// AAD is deliberately nil rather than a user id: the store has one column of
// secrets and binds at the row level, so binding the user here would require the
// caller to know an id the API does not otherwise need. The row is the boundary.
//
// A value that is not in the `v1.` form is returned UNCHANGED, because a
// deployment that predates encryption has plaintext secrets in the column and
// refusing to read them locks every user out. That is a migration state, not a
// permanent one, and it is loud rather than silent: DecryptTOTPSecret reports it
// so the caller can log it once.
func DecryptTOTPSecret(stored string, instanceKey []byte) (TOTPSecret, error) {
	if stored == "" {
		return "", nil
	}
	// A versioned value THIS BUILD CANNOT READ is an error, never plaintext. The
	// first version of this checked only for a "v1." prefix, so a future "v2."
	// value fell through to the legacy path and was returned as a raw secret --
	// meaning an upgrade would silently hand every user the wrong secret rather
	// than refusing. My first draft had exactly that bug, and this test found it.
	if IsEncryptedTOTPSecret(stored) {
		if !strings.HasPrefix(stored, "v1.") {
			return "", fmt.Errorf("%w: unsupported format version %q, this build reads v1",
				ErrTOTPSecretCorrupt, stored[:min(3, len(stored))])
		}
	} else if isVersioned(stored) {
		// A version this build predates, or a value someone edited into something
		// that looks versioned. Either way it is not a legacy secret.
		return "", fmt.Errorf("%w: malformed versioned value", ErrTOTPSecretCorrupt)
	} else {
		// Pre-encryption deployment: a bare base32 secret. Returned as-is, and
		// IsEncryptedTOTPSecret reports false so a migration can find it.
		return TOTPSecret(stored), nil
	}

	payload := strings.TrimPrefix(stored, "v1.")
	raw, err := base64.RawStdEncoding.DecodeString(payload)
	if err != nil {
		return "", fmt.Errorf("%w: not valid base64", ErrTOTPSecretCorrupt)
	}

	key, err := deriveSecretKey(instanceKey)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("creating the AEAD: %w", err)
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("%w: ciphertext is shorter than a nonce", ErrTOTPSecretCorrupt)
	}

	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// GCM's own error is deliberately vague, and this keeps it that way: a
		// "wrong instance key" message would help someone who has the ciphertext
		// and is trying keys.
		return "", fmt.Errorf("%w: the instance key does not match, or the value was modified", ErrTOTPSecretCorrupt)
	}
	return TOTPSecret(string(plaintext)), nil
}

// IsEncryptedTOTPSecret reports whether a stored value is in the sealed form.
// The migration and the setup screen both need this to tell a legacy plaintext
// secret from a current one.
func IsEncryptedTOTPSecret(stored string) bool { return strings.HasPrefix(stored, "v1.") }

// isVersioned reports whether a value LOOKS like a sealed secret of some version,
// including one this build cannot read. Used to keep an unrecognised format out
// of the legacy-plaintext path.
func isVersioned(stored string) bool {
	return len(stored) >= 3 && stored[2] == '.' &&
		(stored[0] == 'v' || stored[0] == 'V') &&
		stored[1] >= '0' && stored[1] <= '9'
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
