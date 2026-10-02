// Package relaycrypto is end-to-end encryption for the relay transport. Step 8.7,
// R090.
//
// # WHY END-TO-END AND NOT TLS PER LEG
//
// The relay is a volunteer third party. It terminates nothing -- which is why R077
// holds, because the seeder's address is absent from the bytes it handles -- but a
// plaintext relay SEES EVERY BYTE IN FLIGHT. That is a node holding content without
// consent, which is the failure §6a.11's revocable consent exists to prevent.
//
// TLS between the relay and each peer would be worse: the relay TERMINATES, so it
// holds a key and can read. That moves the problem rather than solving it. Here the
// relay has no key at all, so "the relay cannot read it" is true BY CONSTRUCTION
// rather than by policy -- the same structural argument R077 rests on, and the
// reason this package exists rather than a TLS config.
//
// So nothing in this package is on the relay. The relay's forwarding code does not
// change: it already copies bytes without parsing them, and an encrypted payload is
// bytes it cannot read.
//
// # WHAT THIS DOES NOT BUY, STATED HERE RATHER THAN LEFT TO BE DISCOVERED
//
// Confidentiality from the relay, and nothing else. A relay that MITMs can
// substitute its own X25519 public key for the requester's and decrypt cleanly --
// the ciphertext verifies, because the relay generated both halves. Pinning a peer's
// key out of band is what closes that, and it is the next step, not this one. An
// AEAD that authenticated the peer would be claiming an identity it has no business
// claiming here.
package relaycrypto

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// FrameSize is the plaintext size of one frame.
//
// A CONSTANT, and that is the load-bearing decision about size. A variable-length
// frame would let a relay that only counts bytes infer chunk boundaries and
// therefore approximate the shape of the content it is carrying -- a size oracle
// with no key. Fixed-size frames make a relay's view indistinguishable between two
// contents of the same length, which is the strongest statement available without
// padding to a power of two and paying for it on every byte.
//
// 64 KiB is the trade: small enough that a slow peer's frame arrives before the
// connection feels stalled, large enough that the 16 bytes of AEAD overhead per
// frame is 0.024% of the content.
const FrameSize = 64 * 1024

// Overhead is what one frame costs beyond its plaintext: an 8-byte counter header
// (which is ALSO the nonce seed) plus ChaCha20-Poly1305's 16-byte tag.
//
// The counter is inside the encrypted region as well as outside it. Carrying it in
// clear is what lets the receiver know which nonce to use without a round trip; it
// is authenticated as additional data, so a frame cannot be re-salted into a
// different slot.
const Overhead = 8 + 16

// ErrAuth is a frame that failed authentication: a flipped bit, a truncated frame,
// or a payload the wrong key produced.
//
// DELIBERATELY ONE ERROR for all three. A caller that could distinguish "tampered"
// from "wrong key" from "truncated" learns something about the peer, and the peer is
// exactly the party whose failures this package must not characterise. It is also
// the property that makes the tamper test meaningful: an implementation that
// returned distinguishable errors would pass a test checking "some error" while
// leaking the distinction anyway.
var ErrAuth = errors.New("relaycrypto: frame failed authentication")

// ErrClosed is a read or write after Close.
var ErrClosed = errors.New("relaycrypto: stream is closed")

// Session is one direction of an encrypted stream.
//
// It is a net.Conn's io.ReadWriter and NOT a net.Conn itself: the underlying
// connection stays the caller's, so closing the transport is not this package's
// decision. A key exchange produces two Sessions sharing one secret, and each side
// reads with one and writes with the other.
type Session struct {
	key    [32]byte
	ctr    uint64
	inBuf  []byte
	outBuf []byte
	closed bool

	// dir is this session's nonce NAMESPACE, and it is a property of the SESSION
	// rather than of the operation.
	//
	// The first version passed dirOut to sealFrame and dirIn to openFrame, on the
	// reasoning that "writing is outbound, reading is inbound". That is wrong
	// because the two sides of a stream do not agree about which is which: Alice's
	// WRITE session and Bob's READ session both describe the same bytes, and Alice
	// writing means she is the outbound party while Bob reading means he is the
	// inbound one -- so the pair disagreed on the nonce and nothing decrypted at all.
	//
	// So the namespace is assigned when the session is created, and Exchange gives
	// the two sessions of one stream DIFFERENT namespaces. That is what makes the
	// two directions of a connection unable to collide, which is the property the
	// namespace is for.
	dir byte

	// The transport stays the CALLER'S. Attaching a stream here rather than taking
	// a net.Conn means closing the connection is not this package's decision, which
	// matters because a relay path has two of them and only one belongs to each end.
	r io.Reader
	w io.Writer
}

// SetStreams attaches the transport.
//
// CALLED ONCE, before the first Read or Write, and re-calling it is not an error --
// so a caller that reattaches mid-stream silently changes where bytes come from. The
// counter keeps going either way, which is the correct behaviour and also the reason
// the mistake is invisible.
func (s *Session) SetStreams(r io.Reader, w io.Writer) {
	s.r, s.w = r, w
}

func (s *Session) reader() io.Reader { return s.r }
func (s *Session) writer() io.Writer { return s.w }

// NewSession wraps an agreed secret in the DEFAULT (outbound) namespace.
//
// Prefer Exchange, which assigns both namespaces for you. This exists for the case
// where the caller already holds a raw secret and knows which direction it is.
func NewSession(shared []byte) (*Session, error) {
	return newSession(shared, dirOut)
}

// newSession is the constructor both entry points share, so the key derivation
// cannot drift between them.
func newSession(shared []byte, dir byte) (*Session, error) {
	// KEYED BY HASHING THE RAW SHARED SECRET, which is the standard hardening: the
	// X25519 output is already uniformly distributed, so this is not strictly necessary,
	// and doing it anyway means a future change to the KDF has one place to land. What
	// it does buy concretely is that the AES key and the ChaCha key are never the same
	// 32 bytes for the same pair, even though both are derived from one secret.
	if len(shared) == 0 {
		return nil, fmt.Errorf("relaycrypto: no shared secret")
	}
	var s Session
	sum := sha256.Sum256(append([]byte("relaycrypto/v1/"), shared...))
	copy(s.key[:], sum[:])
	s.dir = dir
	s.inBuf = make([]byte, FrameSize+Overhead)
	s.outBuf = make([]byte, FrameSize+Overhead)
	return &s, nil
}

// KeyPair is an instance's long-lived X25519 identity.
type KeyPair struct {
	private *ecdh.PrivateKey
	public  []byte
}

// GenerateKeyPair mints an identity.
//
// PERSISTENCE IS THE CALLER'S, deliberately: a key regenerated per run is a key no
// peer can pin, which defeats the out-of-band pinning this package explicitly does
// not do yet. The comment is here so the omission is visible at the point someone
// would otherwise assume the key is stored for them.
func GenerateKeyPair() (*KeyPair, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("relaycrypto: generating an X25519 key: %w", err)
	}
	return &KeyPair{private: priv, public: priv.PublicKey().Bytes()}, nil
}

// PublicKey returns the 32-byte public key a peer needs.
//
// The RETURNED SLICE IS THE KEYPAIR'S, not a copy, so a caller that mutates it
// corrupts the keypair silently. That is a real hazard for a value handed to code
// outside this package, and it is one line to avoid.
func (k *KeyPair) PublicKey() []byte {
	out := make([]byte, len(k.public))
	copy(out, k.public)
	return out
}

// Agree computes the shared secret with a peer's public key.
//
// REFUSES AN ALL-ZERO PUBLIC KEY. X25519 accepts it and returns the all-zero shared
// secret, which is a value both parties can compute without knowing anything -- so a
// peer that sent it would know every session key derived from it. Go's ecdh may
// already refuse it depending on version; the check is here because "may" is not
// "does", and the failure it prevents is silent rather than loud.
func (k *KeyPair) Agree(peerPublic []byte) ([]byte, error) {
	pub, err := ecdh.X25519().NewPublicKey(peerPublic)
	if err != nil {
		return nil, fmt.Errorf("relaycrypto: peer key is not a valid X25519 point: %w", err)
	}
	// NO EXPLICIT ALL-ZERO CHECK, because THIS GO VERSION ALREADY REJECTS IT.
	//
	// There was one here, added because X25519's failure mode for a low-order point
	// is a shared secret computable by anyone -- a silent failure, which is the kind
	// worth a belt-and-braces guard. The mutation gate then reported the guard
	// UNREACHABLE: setting `if allZero` to `if false && allZero` killed nothing,
	// because a real all-zero key never reaches it.
	//
	// Measured, not assumed (a 14-line probe against this toolchain):
	//   ecdh.X25519().NewPublicKey(make([]byte,32)) -> err = nil   (accepted)
	//   priv.ECDH(thatKey)                           -> "crypto/ecdh: bad X25519
	//                                                  remote ECDH input: low order point"
	//
	// So Go's crypto/ecdh does the rejection one layer down, inside ECDH, and the
	// guard above it is dead code on this toolchain. Keeping it would be a second
	// answer to a question already answered -- and a reader would reasonably think it
	// was the thing providing the protection.
	//
	// The check that DOES matter is below: the error is not silently swallowed, and
	// Agree returns it. A caller that treats "no secret" as "fine" is the real risk,
	// and returning an error is what prevents that.
	shared, err := k.private.ECDH(pub)
	if err != nil {
		return nil, fmt.Errorf("relaycrypto: agreeing a shared secret: %w", err)
	}
	return shared, nil
}

// Exchange is the two-sided convenience: agree a secret and return the two Sessions
// that make up a connection.
//
// TWO SESSIONS, NOT ONE, because a single bidirectional session with one counter
// would encrypt the two directions under the same nonces -- and ChaCha20-Poly1305
// with a reused (key, nonce) pair leaks the XOR of the two plaintexts, so a peer
// sending "GET /x" and a reply of similar length would give it away.
//
// THE NAMING IS THE WHOLE DESIGN AND IT TOOK TWO WRONG ANSWERS TO GET RIGHT.
//
// The first version returned (send, recv) where send was dirOut and recv dirIn --
// i.e. named by DIRECTION OF TRAVEL. That cannot work, and the reason is not subtle:
// Alice and Bob are on the same connection, so Alice's outbound stream and Bob's
// inbound stream are THE SAME BYTES and must use the same nonce. Naming by travel
// gave them different ones and nothing decrypted: `chacha20poly1305: message
// authentication failed` on the first frame, from both sides.
//
// The namespace therefore identifies the STREAM, and each party returns the sessions
// named by the PEER they talk to:
//
//	ours=Alice: alice's `send` is the Alice->Bob stream (dirOut)
//	           alice's `recv` is the Bob->Alice stream (dirIn)
//	ours=Bob:   bob's   `send` is the Bob->Alice stream, which must be dirIn
//
// So the direction is resolved by the CALLER'S ROLE, and the second value is
// returned in the order that makes the wiring symmetric. A caller that ignores this
// gets an authentication failure on its first frame rather than a plaintext leak,
// which is the right way round for the failure to go.
func Exchange(ours *KeyPair, peerPublic []byte, isInitiator bool) (send, recv *Session, err error) {
	shared, err := ours.Agree(peerPublic)
	if err != nil {
		return nil, nil, err
	}
	// ONE SIDE'S ORDERING IS THE MIRROR OF THE OTHER'S, and it is derived from the
	// keypair's own index rather than written twice, so the two cannot drift.
	sendNS, recvNS := namespacesFor(isInitiator)
	send, err = newSession(shared, sendNS)
	if err != nil {
		return nil, nil, err
	}
	recv, err = newSession(shared, recvNS)
	if err != nil {
		return nil, nil, err
	}
	return send, recv, nil
}

// nonceFrom derives the 96-bit nonce for a counter.
//
// ChaCha20 takes a 12-byte nonce and Poly1305 needs it too. The first four bytes are
// a constant DIRECTION BYTE and the last eight are the counter -- so the two
// directions of one exchange can never produce the same (key, nonce) even though
// they start their counters at the same place.
func nonceFrom(direction byte, ctr uint64) [12]byte {
	var n [12]byte
	n[0] = direction
	binary.BigEndian.PutUint64(n[4:], ctr)
	return n
}

const (
	dirOut byte = 0x01
	dirIn  byte = 0x02
)

// namespacesFor maps a side's role to its (send, recv) nonce namespaces.
//
// THE ONLY PLACE THIS ORDER IS WRITTEN. It is a two-line lookup because the rule is
// two lines: the initiator sends on dirOut and receives on dirIn, and the responder
// does the reverse -- because the two of them are looking at the same two streams
// from opposite ends.
func namespacesFor(isInitiator bool) (sendNS, recvNS byte) {
	if isInitiator {
		return dirOut, dirIn
	}
	return dirIn, dirOut
}

// Write encrypts one frame.
//
// A SHORT final write is padded, and the padding is RANDOM rather than zeros: a
// zero pad makes the plaintext length a function of the content length, which is the
// same size oracle FrameSize exists to close. Random padding costs nothing at 64 KiB
// granularity and removes the leak entirely.
func (s *Session) Write(p []byte) (int, error) {
	if s.closed {
		return 0, ErrClosed
	}
	n := len(p)
	if n == 0 || n > FrameSize {
		return 0, fmt.Errorf("relaycrypto: Write of %d bytes, want 1..%d: a frame is "+
			"exactly one slot, so a caller must loop rather than hand over a whole buffer", n, FrameSize)
	}

	// THE BUFFER IS FrameSize+Overhead, SO THE SEALED REGION IS
	// frame[8 : 8+FrameSize+Overhead] -- and that is the whole of the buffer, so
	// slicing it by its own length is what keeps the two in step.
	//
	// The first version wrote `frame[8 : 8+Overhead+FrameSize]` and then read
	// `payload[:n]`, which silently UNDER-allocated by the 8-byte header and
	// panicked with `slice bounds out of range [:65568] with capacity 65560` on the
	// first frame. A mismatch between "what I want" and "what I allocated" is not
	// caught by a compiler that sees both as slices, so the sizes are derived from
	// one place instead of written twice.
	frame := s.outBuf[:]
	binary.BigEndian.PutUint64(frame[:8], s.ctr)

	// The plaintext lives in the OUTPUT buffer and is sealed in place: AEAD.Seal
	// appends to its first argument, so handing it the tail of `frame` and then
	// copying the result over the same region needs a destination that is not
	// aliased. Hence a separate plaintext slice, and the copy below.
	plain := make([]byte, FrameSize)
	copy(plain, p)
	if pad := FrameSize - n; pad > 0 {
		if _, err := io.ReadFull(rand.Reader, plain[n:]); err != nil {
			return 0, fmt.Errorf("relaycrypto: padding the final frame: %w", err)
		}
	}

	aead := newAEAD(s.key)
	if err := sealFrame(aead, frame, plain, s.ctr, s.dir); err != nil {
		return 0, err
	}

	if _, err := s.writer().Write(frame); err != nil {
		return 0, err
	}
	s.ctr++
	return n, nil
}

// Read decrypts one frame and returns its plaintext.
//
// The returned slice is COPIED OUT of the session's read buffer, because the next
// Read overwrites it. Handing back the internal buffer would be a use-after-free
// with no error, which is the kind of bug a test cannot see unless it reads twice
// and compares.
func (s *Session) Read(p []byte) (int, error) {
	if s.closed {
		return 0, ErrClosed
	}
	frame := s.inBuf
	if _, err := io.ReadFull(s.reader(), frame); err != nil {
		return 0, err
	}
	ctr := binary.BigEndian.Uint64(frame[:8])

	aead := newAEAD(s.key)
	n, err := openFrame(aead, frame, ctr, s.dir)
	if err != nil {
		return 0, err
	}
	// A REPLAYED COUNTER is refused, and separately from a bad tag. Both are
	// authentication failures to the cipher, but a replay is an ATTACK the caller
	// may want to log, and it is detectable only because the counter is in the
	// clear -- which is the one thing carrying it in the open buys.
	if ctr <= s.ctr && s.ctr != 0 {
		return 0, fmt.Errorf("%w: counter %d is not ahead of %d (replayed or reordered frame)",
			ErrAuth, ctr, s.ctr)
	}

	copy(p, n)
	s.ctr = ctr
	return len(n), nil
}

// Close marks the session unusable. It does NOT close the underlying transport.
func (s *Session) Close() error {
	s.closed = true
	return nil
}
