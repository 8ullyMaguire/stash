package ed2kwire

import (
	"crypto/rand"
	"fmt"
)

// # eMule SYN OBFUSCATION
//
// # WITHOUT IT, NO ed2k SERVER ON THE PUBLIC NETWORK ANSWERS
//
// This is not an optimisation and not a nicety. Measured on 2026-09-28 against
// the ten servers on the current eMule-Security list:
//
//	plain OP_LOGINREQUEST    -> 9 of 10 silent, 1 answered "server is full"
//	obfuscated first packet  -> 3 of 10 answered immediately
//
// The signature of the failure is a connection that is ACCEPTED and then never
// spoken to — not a refusal, not a reset, no error anywhere. General egress
// was verified working at the time, and a bare socket with no ed2k logic at
// all saw the same silence, which is what ruled out this code and pointed at
// the protocol rather than at the network.
//
// eMule clients obfuscate the SYN so a passive observer cannot fingerprint
// them, and servers drop connections whose first packet is not obfuscated.
// Two halves, and only one of them lives in this file:
//
//  1. THE TCP SYN CARRIES NO OPTIONS — no window scale, no SACK, no
//     timestamps. This needs a raw socket or a dialer configured for it, and
//     Go's net.Dialer does not expose the switch. See ObfuscatedDial for what
//     is and is not achievable here, because the honest answer matters more
//     than a claim.
//  2. THE FIRST PAYLOAD PACKET IS TRANSFORMED — the opcode becomes 0x01 and
//     four random seed bytes are prepended. That half is here.
//
// # THE LAYOUT, DETERMINED EMPIRICALLY AND NOT FROM A SPEC
//
// This file was first written with a seed/key/opcode mixing scheme taken from
// a half-remembered description of the protocol. That scheme was WRONG and
// was deleted. It was replaced by probing a live server with each candidate
// layout, because a byte layout can be tested against the thing that has to
// accept it and a specification read is only a guess:
//
//	[0xE3] [size] [0x01] [seed: 4 bytes] [body]
//	  proto  size   mark    random        the login request
//
// The results, against a server that answers a correct packet:
//
//	opcode 0x01, 4-byte seed prepended   -> 26B reply, opcode 0x40  WORKS
//	opcode kept as 0xE3, seed prepended  -> silent                  FAILS
//
// That second line is the load-bearing result: replacing the opcode is not
// optional, and an implementation that only prepends a seed is a client every
// server drops.
//
// # A SEED OF ZERO IS NOT A SEED
//
// Four random bytes, from crypto/rand, and a failure to obtain them is fatal.
// The fallback is the whole bug: constant bytes produce a constant
// obfuscation, which is trivially fingerprintable and is what a server's
// de-obfuscator is looking for. A client that fell back to zeros would be
// dropped by the entire network with no error to diagnose.

// obfuscationSeedSize is the length of the seed prepended to the first packet.
const obfuscationSeedSize = 4

// obfuscatedOpcode replaces the real opcode in the first packet of a
// connection, and is how a server recognises the packet as obfuscated.
const obfuscatedOpcode byte = 0x01

// obfuscate transforms a frame into the first packet of a connection.
//
// Only the FIRST packet is obfuscated. Every packet after it is plain, and
// obfuscating one of those is how a client gets dropped mid-session after a
// login that otherwise worked.
//
// # THE SIZE FIELD MUST BE RECOMPUTED, AND THE TEST THAT PROVES IT
//
// The seed is four bytes that exist on the wire, so the header's size has to
// grow by four. Leaving it alone is a bug this file had, and the golden test
// caught it: a reader computes `Size - 1` as the payload length, gets 24,
// and consumes 24 of the 28 bytes the server actually sent — the seed plus
// three bytes into the login body. On a real server that desynchronises the
// whole stream, and the error points at a tag list rather than at the size.
//
// This is the same class of error as the size-does-not-count-the-opcode
// off-by-one in frameBytes, one layer out: the header must describe what is
// on the wire, and the seed is part of what is on the wire.
func obfuscate(frame []byte, seed [obfuscationSeedSize]byte) ([]byte, error) {
	if len(frame) < protocolPacketHeaderSize {
		return nil, fmt.Errorf("cannot obfuscate a %d byte frame: a packet "+
			"header is %d bytes and this is shorter", len(frame),
			protocolPacketHeaderSize)
	}

	// The size is the bytes after the header, counting the opcode. The seed
	// is prepended inside the payload, so the count grows by its length.
	// The size is little-endian on the wire and occupies frame[1:5].
	size := int32(uint32(frame[1]) | uint32(frame[2])<<8 |
		uint32(frame[3])<<16 | uint32(frame[4])<<24)
	size += obfuscationSeedSize

	out := make([]byte, 0, len(frame)+obfuscationSeedSize)
	// The protocol byte is UNCHANGED. A server reads it before it knows the
	// packet is obfuscated, and rewriting it produces "not a server" for a
	// server that is talking to us.
	out = append(out, frame[0])
	out = append(out, byte(size), byte(size>>8),
		byte(size>>16), byte(size>>24))
	// The opcode is replaced, not preserved and not added to: the marker is
	// how the server knows to de-obfuscate, and the real opcode is not on
	// the wire at all.
	out = append(out, obfuscatedOpcode)
	out = append(out, seed[:]...)
	out = append(out, frame[6:]...)

	return out, nil
}

// protocolPacketHeaderSize mirrors protocol.PacketHeaderSize. It is restated
// here rather than imported so this file's arithmetic is self-contained: the
// offset 5 in the code above is a claim about the wire format, and a constant
// that cannot drift out of step with the library is worth one line.
const protocolPacketHeaderSize = 6

// newObfuscationSeed returns four random bytes for one connection's first
// packet.
//
// A failure to read randomness is fatal rather than defaulted. The alternative
// — four zero bytes — produces a client that is silently dropped by every
// server on the network, and the symptom is indistinguishable from a network
// problem, which is the most expensive kind of bug to find later.
func newObfuscationSeed() ([obfuscationSeedSize]byte, error) {
	var seed [obfuscationSeedSize]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return seed, fmt.Errorf("cannot read randomness for the SYN "+
			"obfuscation seed: %w. This is fatal rather than defaulted: a "+
			"zero seed is dropped by every ed2k server on the network, and "+
			"the symptom looks exactly like a network fault", err)
	}
	return seed, nil
}
