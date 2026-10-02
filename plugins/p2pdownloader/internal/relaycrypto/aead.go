package relaycrypto

import (
	"crypto/cipher"
	"crypto/subtle"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

// newAEAD builds the per-frame cipher.
//
// ChaCha20-Poly1305, and the choice of it is worth stating because the mutation
// gate's most interesting mutant swaps it for AES-CTR: CTR is unauthenticated, so a
// flipped bit in the ciphertext produces different plaintext instead of a refusal,
// and the receiver reassembles content whose hash does not match -- which the
// manifest check catches, but only after 4 GiB of wasted transfer.
//
// Go's stdlib exposes ChaCha20-Poly1305 through `crypto/chacha20poly1305`, a
// deliberately restricted API: Seal and Open take the whole message at once and the
// nonce by value, which is the shape this framing wants. There is no streaming
// constructor, so a cipher is built per FRAME rather than once per session --
// 29 000 constructions for a 4 GiB replica, each a couple of microseconds.
//
// That is affordable, and the alternative (an unsafe `cipher.NewGCM`-style reuse) is
// not: the tag is 16 bytes per 64 KiB frame, so the authentication overhead is
// 0.024% of the content, and re-keying per frame costs 256 bits of Poly1305 key
// derivation each time, which is what buys the per-frame authentication.
func newAEAD(key [32]byte) cipher.AEAD {
	aead, err := chacha20poly1305.New(append([]byte(nil), key[:]...))
	if err != nil {
		// Only reachable for a wrong key LENGTH, and the only key in this package is
		// a [32]byte built by NewSession. So this cannot happen -- and returning nil
		// anyway would turn an impossible case into a nil-pointer panic three frames
		// later, so it is reported at the one place the impossibility is knowable.
		panic("relaycrypto: a [32]byte key cannot fail to construct an AEAD: " + err.Error())
	}
	return aead
}

// sealFrame encrypts one frame in place.
//
// THE COUNTER IS AUTHENTICATED AS ADDITIONAL DATA, which is what makes the replay
// check on the read side meaningful. It is carried in the clear so the receiver can
// pick the right nonce without a round trip, and a clear header is only safe because
// the tag covers it: without the AAD, anyone could rewrite the counter to point at a
// different slot and the payload would decrypt as valid ciphertext for that slot.
//
// Additional data is ChaCha20-Poly1305's AAD parameter, so it is authenticated but
// NOT encrypted. That is the intended split -- there is nothing secret about a frame
// index.
func sealFrame(aead cipher.AEAD, frame, plain []byte, ctr uint64, dir byte) error {
	// frame[:8] is the header (already written); frame[8:] is the sealed region.
	aad := frame[:8]
	nonce := nonceFrom(dir, ctr)
	sealed := aead.Seal(nil, nonce[:], plain, aad)
	if len(sealed) != len(plain)+aead.Overhead() {
		return fmt.Errorf("relaycrypto: sealed %d bytes into %d, want %d: the AEAD's "+
			"overhead does not match the frame layout", len(plain), len(sealed),
			len(plain)+aead.Overhead())
	}
	copy(frame[8:], sealed)
	return nil
}

// openFrame authenticates and decrypts one frame, returning the plaintext.
//
// THE TAG IS COMPARED BY THE AEAD, in constant time -- and that is the whole reason
// for using an AEAD rather than encrypting and comparing hashes separately. A
// hand-rolled `if subtle.ConstantTimeCompare(tag[:], computed[:]) != 1` is easy to
// write correctly and impossible to review at a glance, and the mutation gate's
// AES-CTR mutant exists precisely because the alternative (no tag at all) still
// decrypts.
//
// A failure here returns ErrAuth for a flipped bit, a truncated frame, and the wrong
// key alike. See ErrAuth for why they are not distinguished: a caller that could tell
// them apart would be characterising the peer, which is the party this package must
// not be characterising.
func openFrame(aead cipher.AEAD, frame []byte, ctr uint64, dir byte) ([]byte, error) {
	aad := frame[:8]
	sealed := frame[8:]
	if len(sealed) < aead.Overhead() {
		return nil, fmt.Errorf("%w: frame carries %d bytes, less than the %d-byte tag",
			ErrAuth, len(sealed), aead.Overhead())
	}
	nonce := nonceFrom(dir, ctr)
	plain, err := aead.Open(nil, nonce[:], sealed, aad)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAuth, err)
	}
	// An all-zero plaintext is refused rather than returned as a valid empty frame.
	// A padded final frame is FrameSize bytes of which the tail is random, so a
	// zero result means the key is wrong in a way the tag somehow accepted -- which
	// is not supposed to be reachable, and returning it as "an empty frame" would
	// turn an impossible case into a silently truncated download.
	if subtle.ConstantTimeEq(int32(len(plain)), 0) == 1 {
		return nil, fmt.Errorf("%w: decrypted to zero bytes, which no padded frame can be",
			ErrAuth)
	}
	return plain, nil
}
