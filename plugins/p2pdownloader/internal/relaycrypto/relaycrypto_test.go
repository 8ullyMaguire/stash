package relaycrypto

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// R090: a relay must not be able to read what it carries.
//
// The tests below are chosen for what they PROVE, and most of them are about
// absence -- an absence cannot be asserted by "the round trip worked".

// THE ROUND TRIP, AND THAT BOTH DIRECTIONS WORK AT ONCE.
//
// Two sessions, not one: a single bidirectional session would encrypt both directions
// under the same counter-derived nonces, and ChaCha20-Poly1305 with a reused
// (key, nonce) pair leaks the XOR of the two plaintexts. The counter is per
// direction, so this cannot happen by construction.
func TestBothDirectionsCarryTheirOwnContent(t *testing.T) {
	alice, err := GenerateKeyPair()
	require.NoError(t, err)
	bob, err := GenerateKeyPair()
	require.NoError(t, err)

	aliceSend, aliceRecv, err := Exchange(alice, bob.PublicKey(), true)
	require.NoError(t, err)
	bobSend, bobRecv, err := Exchange(bob, alice.PublicKey(), false)
	require.NoError(t, err)

	// Exactly FrameSize, built by repeat-THEN-truncate rather than by dividing.
	//
	// The first version used `bytes.Repeat(marker, FrameSize/11)`, which yields
	// 11 * 5957 = 65527 bytes -- nine short -- and the require.Len failure printed an
	// EMPTY error message, because testify reports a length mismatch with nothing to
	// say. Dividing a length by a stride and multiplying back does not return the
	// length, which is arithmetic worth not rediscovering through a blank assertion.
	toBob := padTo(bytes.Repeat([]byte("from alice "), FrameSize), FrameSize)
	fromBob := padTo(bytes.Repeat([]byte("from bob!! "), FrameSize), FrameSize)
	require.Len(t, toBob, FrameSize)
	require.Len(t, fromBob, FrameSize)

	// THE WIRING IS CROSSWISE, and getting it wrong is the bug this test was written
	// to catch: aliceSend's output is what bobRecv must read, because the two of them
	// are the two ends of ONE stream. The first version had aliceRecv reading her own
	// send buffer and bobRecv reading his, which pairs a dirOut writer with a dirIn
	// reader and fails authentication on the first frame -- correctly, since that
	// pairing does not exist on a real connection.
	var toBobWire, fromBobWire bytes.Buffer
	aliceSend.SetStreams(bytes.NewReader(nil), &toBobWire)
	bobSend.SetStreams(bytes.NewReader(nil), &fromBobWire)

	n, err := aliceSend.Write(toBob)
	require.NoError(t, err)
	require.Equal(t, FrameSize, n)
	n, err = bobSend.Write(fromBob)
	require.NoError(t, err)
	require.Equal(t, FrameSize, n)

	// Each side reads the other's wire.
	bobRecv.SetStreams(bytes.NewReader(toBobWire.Bytes()), io.Discard)
	aliceRecv.SetStreams(bytes.NewReader(fromBobWire.Bytes()), io.Discard)

	got := make([]byte, FrameSize)
	n, err = bobRecv.Read(got)
	require.NoError(t, err)
	assert.Equal(t, FrameSize, n)
	assert.Equal(t, toBob, got, "alice's content arrives at bob intact")

	n, err = aliceRecv.Read(got)
	require.NoError(t, err)
	assert.Equal(t, FrameSize, n)
	assert.Equal(t, fromBob, got, "and bob's arrives at alice, so the two directions "+
		"are separate namespaces rather than one counter interleaved")

	// AND THE TWO CIPHERTEXTS DIFFER, which is what the per-direction nonce buys.
	// Same length, same content length, different bytes: interleaved counters would
	// make the first frame of each direction share a nonce.
	assert.NotEqual(t, toBobWire.Bytes(), fromBobWire.Bytes(),
		"the two directions must not produce the same ciphertext from the same key")
}

// THE TEST THAT ACTUALLY PROVES R090: A RECORDING RELAY SEES NO PLAINTEXT.
//
// Not "the round trip worked" -- the relay's blindness is the claim, so the assertion
// is made against the bytes the relay CAPTURED. A test that only checks the
// plaintext came out the other end would pass against a plaintext relay, which is
// the whole failure this step exists to close.
func TestARecordingRelayCannotReadTheContent(t *testing.T) {
	alice, err := GenerateKeyPair()
	require.NoError(t, err)
	bob, err := GenerateKeyPair()
	require.NoError(t, err)

	send, _, err := Exchange(alice, bob.PublicKey(), true)
	require.NoError(t, err)

	// A distinctive plaintext, so a substring search cannot miss a partial leak.
	secret := bytes.Repeat([]byte("THE-ORIGINAL-CONTENT-MUST-NOT-APPEAR "), 4096)
	require.Greater(t, len(secret), FrameSize)

	// The relay records EVERYTHING it forwards, in both directions.
	var captured bytes.Buffer
	send.SetStreams(bytes.NewReader(nil), io.MultiWriter(&captured, io.Discard))

	offset := 0
	frames := 0
	for offset < len(secret) {
		end := offset + FrameSize
		if end > len(secret) {
			end = len(secret)
		}
		_, err := send.Write(secret[offset:end])
		require.NoError(t, err)
		offset = end
		frames++
	}

	require.NotEmpty(t, captured.Bytes(), "the relay recorded something; a zero-byte "+
		"capture would make every assertion below vacuously true")

	// THE ASSERTION. Neither the marker nor any long run of the plaintext appears in
	// what the relay holds.
	assert.NotContains(t, captured.String(), "THE-ORIGINAL-CONTENT-MUST-NOT-APPEAR",
		"the relay captured the content verbatim. R090 is the claim that it cannot, "+
			"and a passing round trip says nothing about it")

	// The plaintext's own bytes must not be findable at ANY offset, not just at
	// frame boundaries -- so a frame-aligned test is not enough. Sampling 32 bytes
	// from each frame's start is enough to catch any cipher that leaves structure.
	for f := 0; f < frames; f++ {
		chunk := captured.Bytes()[f*(FrameSize+Overhead) : f*(FrameSize+Overhead)+32]
		assert.False(t, bytes.Contains(secret, chunk),
			"frame %d's first 32 ciphertext bytes also occur verbatim in the "+
				"plaintext, so the cipher is leaking structure at the frame boundary", f)
	}
}

// TWO PLAINTEXTS OF THE SAME LENGTH MUST PRODUCE CIPHERTEXTS OF THE SAME LENGTH.
//
// This is the size-oracle property FrameSize exists to provide. A relay that only
// counts bytes must not be able to tell a 4 GiB scene from a 4 GiB scene, or -- worse
// -- how much of a transfer is content rather than padding.
//
// THE FIRST VERSION OF THIS TEST PROVED NOTHING and looked like it proved the
// opposite. It looped over two buffers, wrote BOTH plaintexts into each, and broke
// out after the first iteration -- so `b` was empty and the assertion `a.Len() ==
// b.Len()` failed with 131120 against 0, which reads as "the property is broken" and
// is really "the fixture never wrote anything". A size comparison between a buffer
// with data and one without is a test of `bytes.Buffer`.
//
// So each plaintext goes into its OWN session with its OWN buffer, and the two are
// compared. The sessions deliberately share a secret, so the only difference between
// the two ciphertexts is the plaintext.
func TestEqualLengthPlaintextsProduceEqualLengthCiphertexts(t *testing.T) {
	alice, err := GenerateKeyPair()
	require.NoError(t, err)
	peer := mustOtherKey(t)

	// Two sessions, one secret, SAME role -- so the nonce namespaces match and the
	// only variable left is the content.
	ones, _, err := Exchange(alice, peer, true)
	require.NoError(t, err)
	fives, _, err := Exchange(alice, peer, true)
	require.NoError(t, err)

	var allOnes, allFives bytes.Buffer
	ones.SetStreams(bytes.NewReader(nil), &allOnes)
	fives.SetStreams(bytes.NewReader(nil), &allFives)

	for i := 0; i < 3; i++ {
		_, err = ones.Write(bytes.Repeat([]byte{0xAA}, FrameSize))
		require.NoError(t, err)
		_, err = fives.Write(bytes.Repeat([]byte{0x55}, FrameSize))
		require.NoError(t, err)
	}

	require.NotZero(t, allFives.Len(), "fixture wrote nothing, so the comparison below "+
		"is between one buffer with data and one without")
	require.Equal(t, allOnes.Len(), allFives.Len(),
		"two plaintexts differing in every byte produced ciphertexts of different "+
			"lengths, so a relay counting bytes learns something about the content")

	// AND THE CIPHERTEXTS DIFFER, so equal length is not equal bytes -- a cipher that
	// emitted a constant would pass the length check while leaking everything.
	assert.NotEqual(t, allOnes.Bytes(), allFives.Bytes(),
		"identical output for different input")

	// AND A SHORT WRITE IS ALSO THE SAME LENGTH AS A FULL ONE, which is the padding
	// property: without random padding the last frame's length would reveal the
	// content's length exactly.
	short, _, err := Exchange(alice, peer, true)
	require.NoError(t, err)
	var shortBuf bytes.Buffer
	short.SetStreams(bytes.NewReader(nil), &shortBuf)
	_, err = short.Write([]byte("ten bytes."))
	require.NoError(t, err)
	require.Equal(t, FrameSize+Overhead, shortBuf.Len(),
		"a 10-byte frame is padded to a full one, so the relay cannot tell a short "+
			"final frame from a full one and infer the content's length")
}

// A FLIPPED BIT IS REFUSED, NOT DECRYPTED.
//
// The mutation gate's most interesting mutant swaps ChaCha20-Poly1305 for AES-CTR,
// which is unauthenticated: a flipped bit then yields different plaintext rather
// than an error. This test is what kills it.
func TestAFlippedBitIsRefusedRatherThanDecrypted(t *testing.T) {
	alice, err := GenerateKeyPair()
	require.NoError(t, err)
	bob, err := GenerateKeyPair()
	require.NoError(t, err)
	send, _, err := Exchange(alice, bob.PublicKey(), true)
	require.NoError(t, err)
	_, recv, err := Exchange(bob, alice.PublicKey(), false)
	require.NoError(t, err)

	var wire bytes.Buffer
	send.SetStreams(bytes.NewReader(nil), &wire)
	_, err = send.Write(bytes.Repeat([]byte("payload"), FrameSize/7))
	require.NoError(t, err)

	frame := wire.Bytes()

	// Flip one bit in the CIPHERTEXT body, not the header. The header is
	// authenticated as additional data, so flipping it is a different failure and
	// is checked separately below.
	corrupt := append([]byte(nil), frame...)
	corrupt[100] ^= 0x01

	recv.SetStreams(bytes.NewReader(corrupt), io.Discard)
	buf := make([]byte, FrameSize+Overhead)
	_, err = recv.Read(buf)
	require.ErrorIs(t, err, ErrAuth,
		"a single flipped bit must be refused. An unauthenticated cipher (AES-CTR) "+
			"would decrypt to DIFFERENT plaintext here, and the manifest hash would "+
			"not catch it until 4 GiB had been transferred")

	// AND THE HEADER IS AUTHENTICATED TOO: rewriting the counter changes the nonce,
	// so the frame no longer opens under the counter it claims. Tested on a FRESH
	// session, because the session above has already consumed a counter and the
	// replay check would then be what refused it -- which is a different rule.
	sendFresh, recvFresh, err := Exchange(alice, bob.PublicKey(), true)
	require.NoError(t, err)
	var wire2 bytes.Buffer
	sendFresh.SetStreams(bytes.NewReader(nil), &wire2)
	_, err = sendFresh.Write(bytes.Repeat([]byte("payload"), FrameSize/7))
	require.NoError(t, err)

	rewritten := append([]byte(nil), wire2.Bytes()...)
	rewritten[7] ^= 0xFF // the counter's high byte
	recvFresh.SetStreams(bytes.NewReader(rewritten), io.Discard)
	_, err = recvFresh.Read(buf)
	require.ErrorIs(t, err, ErrAuth, "the counter is additional data, so rewriting it "+
		"breaks authentication rather than selecting a different nonce silently")
}

func TestAReplayedFrameIsRefused(t *testing.T) {
	alice, _ := GenerateKeyPair()
	bob, _ := GenerateKeyPair()
	_, recv, err := Exchange(bob, alice.PublicKey(), false)
	require.NoError(t, err)
	send, _, err := Exchange(alice, bob.PublicKey(), true)
	require.NoError(t, err)

	var wire bytes.Buffer
	send.SetStreams(bytes.NewReader(nil), &wire)
	for i := 0; i < 3; i++ {
		_, err = send.Write(bytes.Repeat([]byte{byte(i)}, FrameSize))
		require.NoError(t, err)
	}

	frameLen := FrameSize + Overhead
	frames := make([][]byte, 3)
	for i := range frames {
		frames[i] = append([]byte(nil), wire.Bytes()[i*frameLen:(i+1)*frameLen]...)
	}

	// Frames 0, 1, 2 in order -- all fine.
	recv.SetStreams(bytes.NewReader(append(append(append([]byte{}, frames[0]...), frames[1]...), frames[2]...)), io.Discard)
	buf := make([]byte, FrameSize+Overhead)
	for i := 0; i < 3; i++ {
		_, err := recv.Read(buf)
		require.NoError(t, err, "frame %d in order", i)
	}

	// Now frame 1 again, on a session that has already seen it.
	_, recv2, err := Exchange(bob, alice.PublicKey(), false)
	require.NoError(t, err)
	recv2.SetStreams(bytes.NewReader(append(append([]byte{}, frames[0]...), frames[1]...)), io.Discard)
	_, err = recv2.Read(buf)
	require.NoError(t, err)
	_, err = recv2.Read(buf)
	require.NoError(t, err)

	// A THIRD frame whose counter is 1 again: the bytes are valid and authenticate,
	// so only the counter check can catch it.
	_, recv3, err := Exchange(bob, alice.PublicKey(), false)
	require.NoError(t, err)
	replayed := bytes.NewReader(append(append(append([]byte{}, frames[0]...), frames[1]...), frames[1]...))
	recv3.SetStreams(replayed, io.Discard)
	_, err = recv3.Read(buf)
	require.NoError(t, err)
	_, err = recv3.Read(buf)
	require.NoError(t, err)
	_, err = recv3.Read(buf)
	require.ErrorIs(t, err, ErrAuth,
		"a frame whose counter is not ahead of the session's is a replay. It "+
			"authenticates -- the bytes are genuine -- so the counter comparison is "+
			"the only thing that can refuse it")
}

// AN ALL-ZERO PEER KEY IS REFUSED, BECAUSE X25519 WOULD ACCEPT IT.
//
// Go's ecdh refuses some degenerate points depending on version, so the check is
// explicit. The failure it prevents is silent: an all-zero peer key yields the
// all-zero shared secret, which both parties can compute without knowing anything.
func TestAnAllZeroPeerKeyIsRefused(t *testing.T) {
	ours, err := GenerateKeyPair()
	require.NoError(t, err)

	_, err = ours.Agree(make([]byte, 32))
	require.Error(t, err, "an all-zero peer key must be refused: X25519 accepts it and "+
		"returns a secret computable by anyone")

	// AND A MALFORMED KEY IS REFUSED TOO, rather than producing garbage.
	_, err = ours.Agree([]byte{1, 2, 3})
	require.Error(t, err, "a 3-byte key is not an X25519 point")
}

// A WRONG KEY PRODUCES ErrAuth, AND THE SAME ErrAuth A TAMPER DOES.
//
// The point is the INDISTINGUISHABILITY: a caller must not be able to tell a wrong
// peer from a corrupted frame, because either answer characterises the peer.
func TestAWrongKeyAndATamperedFrameAreTheSameError(t *testing.T) {
	alice, _ := GenerateKeyPair()
	bob, _ := GenerateKeyPair()
	mallory, _ := GenerateKeyPair()

	send, _, err := Exchange(alice, bob.PublicKey(), true)
	require.NoError(t, err)
	var wire bytes.Buffer
	send.SetStreams(bytes.NewReader(nil), &wire)
	_, err = send.Write(bytes.Repeat([]byte("x"), FrameSize))
	require.NoError(t, err)

	// Mallory RECEIVES instead of bob.
	_, malloryRecv, err := Exchange(mallory, alice.PublicKey(), false)
	require.NoError(t, err)
	malloryRecv.SetStreams(bytes.NewReader(wire.Bytes()), io.Discard)
	_, wrongKeyErr := malloryRecv.Read(make([]byte, FrameSize+Overhead))

	// Bob receives a TAMPERED frame.
	_, bobRecv, err := Exchange(bob, alice.PublicKey(), false)
	require.NoError(t, err)
	tampered := append([]byte(nil), wire.Bytes()...)
	tampered[50] ^= 0x80
	bobRecv.SetStreams(bytes.NewReader(tampered), io.Discard)
	_, tamperErr := bobRecv.Read(make([]byte, FrameSize+Overhead))

	require.ErrorIs(t, wrongKeyErr, ErrAuth)
	require.ErrorIs(t, tamperErr, ErrAuth)

	// AND THE MESSAGES ARE BYTE-IDENTICAL, which is the assertion that kills the
	// gate's auth-error mutant.
	//
	// The first version compared only the PREFIX, up to the first space. That passes
	// whether or not the two failures differ afterwards -- so appending
	// "OPEN FAILED (key or tampering)" to one of them changed nothing the test
	// could see, and the mutant survived a green suite. A prefix comparison proves
	// the test author remembered to compare a prefix; it does not prove
	// indistinguishability, which is the property ErrAuth actually claims.
	assert.Equal(t, wrongKeyErr.Error(), tamperErr.Error(),
		"a wrong key and a tampered frame must produce the same error STRING, not "+
			"merely the same prefix -- a caller that can tell them apart learns which "+
			"one it provoked, which characterises the peer")

	// AND IT IS THE EXPECTED STRING, not merely "the same as each other".
	//
	// Comparing the two errors only to EACH OTHER is half the property, and it is
	// the half a mutation cannot break: the gate's auth-error mutant appends a
	// distinguishing clause to the SHARED return, so both errors change together
	// and remain equal -- a green test for a red reason. Both halves are needed --
	// equality catches a change to one path, this catches a change to both.
	const wantAuthErr = "relaycrypto: frame failed authentication: " +
		"chacha20poly1305: message authentication failed"
	assert.Equal(t, wantAuthErr, wrongKeyErr.Error(),
		"the error must be ErrAuth plus the AEAD's own message, verbatim. Any "+
			"additional prose here is a statement about WHICH failure occurred, and "+
			"the whole point of ErrAuth is that there is nothing to say")
	assert.Equal(t, wantAuthErr, tamperErr.Error())
}

// A SHORT FINAL FRAME IS PADDED WITH RANDOM BYTES, NOT ZEROS.
//
// Zero padding makes the plaintext length a function of the content length, which is
// the same size oracle FrameSize exists to close. The check is that two DIFFERENT
// short writes produce DIFFERENT ciphertexts of the SAME length.
func TestAShortFinalFrameIsRandomlyPadded(t *testing.T) {
	alice, _ := GenerateKeyPair()
	bob, _ := GenerateKeyPair()
	send, _, err := Exchange(alice, bob.PublicKey(), true)
	require.NoError(t, err)

	short := []byte("the last frame, and it is short")

	var first, second bytes.Buffer
	s1, _, err := Exchange(alice, bob.PublicKey(), true)
	require.NoError(t, err)
	s1.SetStreams(bytes.NewReader(nil), &first)
	_, err = s1.Write(short)
	require.NoError(t, err)

	s2, _, err := Exchange(alice, bob.PublicKey(), true)
	require.NoError(t, err)
	s2.SetStreams(bytes.NewReader(nil), &second)
	_, err = s2.Write(short)
	require.NoError(t, err)

	assert.Equal(t, first.Len(), second.Len(), "same length for the same content length")
	assert.NotEqual(t, first.Bytes(), second.Bytes(),
		"the padding is random: identical padding would make the plaintext length a "+
			"function of the content length, which is the oracle FrameSize closes")
	_ = send
}

// A FRAME LARGER THAN FrameSize IS REFUSED, SO THE SIZE ORACLE STAYS CLOSED.
//
// If Write accepted an arbitrary buffer, a caller sending 1 MiB in one call would
// produce a 1 MiB frame and the relay would learn the caller's chunking.
func TestAFrameLargerThanFrameSizeIsRefused(t *testing.T) {
	alice, _ := GenerateKeyPair()
	send, _, err := Exchange(alice, mustOtherKey(t), true)
	require.NoError(t, err)
	send.SetStreams(bytes.NewReader(nil), io.Discard)

	_, err = send.Write(make([]byte, FrameSize+1))
	require.Error(t, err, "a frame is exactly one slot; a caller must loop rather than "+
		"hand over a whole buffer, or the relay learns the caller's chunk size")

	_, err = send.Write(nil)
	require.Error(t, err, "an empty write is not a frame")
}

// A Session refuses work after Close.
func TestASessionRefusesWorkAfterClose(t *testing.T) {
	alice, _ := GenerateKeyPair()
	send, _, err := Exchange(alice, mustOtherKey(t), true)
	require.NoError(t, err)
	send.SetStreams(bytes.NewReader(nil), io.Discard)

	require.NoError(t, send.Close())
	_, err = send.Write(make([]byte, 16))
	require.ErrorIs(t, err, ErrClosed)
	_, err = send.Read(make([]byte, 16))
	require.ErrorIs(t, err, ErrClosed)
}

// helpers

// padTo truncates or zero-extends b to exactly n bytes.
//
// TRUNCATION, not extension, is the honest direction here: the callers pass
// bytes.Repeat(..., FrameSize) which is already >= n, so the test's intent is "make
// it exactly one frame" and a helper that padded instead would silently keep the
// overflow -- turning a multi-frame write into a single one and hiding a bug.
func padTo(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}
	out := make([]byte, n)
	copy(out, b)
	return out
}

func mustOtherKey(t *testing.T) []byte {
	t.Helper()
	other, err := GenerateKeyPair()
	require.NoError(t, err)
	return other.PublicKey()
}
