package ed2kwire

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/monkeyWie/goed2k/protocol"
)

// Sources: peers holding a file, as opposed to the servers that index them.
//
// # WHAT THIS IS, AND WHY IT IS NOT A Server
//
// A source is reached the same way a server is — TCP, the client speaks first,
// the first packet obfuscated and every packet after it plain — and it is
// tempting to make one type do both jobs. This is a separate type anyway,
// for a reason that is about what a caller can conclude from a field:
//
// A source has no OP_SERVERINFO and no user or file count. Giving it those
// fields would invite a caller to read a number that is always zero, and a
// zero that reads as a fact is worse than a missing field: "this source
// serves 0 users" is a claim, and nothing here can support it. So Source
// carries a connection and an address, and nothing else to misread.
//
// # WHAT IS SHARED AND WHAT IS NOT, AND WHERE THAT IS DECIDED
//
// The handshake is genuinely identical — same framing, same obfuscation rule,
// same client-speaks-first shape. It is not factored out of Dial in this
// commit, deliberately.
//
// A commit that both extracts a function and adds a feature to its callers
// produces a diff where neither can be reviewed: is the behaviour change the
// extraction or the new code? And the extraction's test coverage would be the
// feature's, which means a refactor that broke Dial would be reported as a
// transfer failure. The two call sites are the thing that makes the
// extraction honest, so it comes after both work — step 6 of the transfer
// plan, not step 1.
//
// Until then the duplication is real and the comment below says so. A
// duplicated twelve lines that is honest about being temporary is cheaper
// than an extraction made too early.

// Source is one connected peer that may hold a file.
//
// A Source is a connection and a claim about where it is. It is NOT a promise
// that the peer holds the file being asked for: UserID/Port comes from a
// server's search result, and a server is a stranger making a claim.
type Source struct {
	addr string
	conn net.Conn

	// guid is the peer's user hash, read from the handshake if it sends one.
	//
	// Held as the bytes received and not interpreted. A peer's GUID is used
	// to tell one peer from another and nothing in this step depends on its
	// value, so a field that looked meaningful and could not be relied on
	// would be a trap. Step 2 quotes it back, and that is when it starts
	// being load-bearing.
	guid [16]byte

	// sentFirst records that the connection's first packet has gone out, and
	// so is the only packet allowed to have been obfuscated.
	//
	// The same field, with the same reason, as Server.sentFirst: the
	// invariant belongs to the CONNECTION, and two call sites each deciding
	// "is this the first packet yet" is how a connection gets two
	// obfuscated packets. A server drops that without replying, so the
	// mistake is invisible on the wire.
	sentFirst bool
}

// Addr is the source's address, as dialled.
func (s *Source) Addr() string { return s.addr }

// Close releases the connection.
//
// Closing twice is not an error to report, because the second close's only
// possible cause is a caller cleaning up twice and the information is not
// worth an error line.
func (s *Source) Close() error {
	if err := s.conn.Close(); err != nil {
		return fmt.Errorf("closing the connection to the source at %s: %w",
			s.addr, err)
	}
	return nil
}

// DialSource connects to a source at addr and completes the handshake.
//
// # WHAT "COMPLETES THE HANDSHAKE" MEANS, AND WHY IT IS NOT LOOSER
//
// The client speaks first: the obfuscated login request goes out, and then
// the source's answer is read. A source that sends nothing before the
// deadline is ErrRefused — the same sentinel a silent SERVER returns, reused
// deliberately so a caller handles "this peer never spoke" with one case
// rather than two that mean the same thing.
//
// The deadline is set on the CONNECTION, not only on the context. Go's
// net.Conn does not consult a context after the connect returns, so a
// context-only deadline expires while the read blocks anyway, and the caller
// sees a transfer that never finishes rather than an error. This is the same
// reasoning as Dial's, and the reason it is written out again here is that
// the next person to add a third connection type will not connect these two.
//
// # AND WHAT A HANDSHAKE DOES NOT PROVE
//
// Reaching a peer does not mean it holds the file, and it does not mean it
// will answer a part request. Both of those are separate failures with
// separate names, and conflating them here would make "the source is
// unreachable" and "the source declined the file" the same error.
func DialSource(ctx context.Context, addr string) (*Source, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: no context, so nothing can be "+
			"cancelled or given a deadline", ErrRefused)
	}

	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dialling the ed2k source at %s: %w", addr, err)
	}

	// # THE DEADLINE GOES ON THE CONNECTION, AND A MUTATION PROVED WHY
	//
	// The comment above says a context alone does not bound a read that
	// has already started, and that is true -- but the mutation harness
	// found that removing THIS call changed no test at all, and the
	// reason is instructive.
	//
	// readHandshakeAnswer sets its own read deadline on every iteration,
	// so the reads were already bounded and this call was doing nothing
	// for them. The first draft of the test suite therefore protected a
	// line that no behaviour depended on.
	//
	// What it does cover is the WRITE, and a write is the one operation
	// here that no later call bounds: a peer that accepts the connection
	// and then stops reading can leave our first packet sitting in a full
	// send buffer. A 28-byte write into a default buffer does not block
	// (measured: 17 microseconds), so this is defence against a case that
	// is unlikely rather than one that is observed -- and defence that is
	// only reachable under conditions this step cannot produce is worth
	// saying out loud, which is why it is here rather than deleted.
	//
	// The alternative was deleting the call and letting step 2's part
	// requests -- which send up to 9500 bytes -- inherit whatever the OS
	// does with a full buffer. That is a real risk arriving in the next
	// step, and a deadline already on the connection is the cheapest way
	// to be safe when it does.
	if err := conn.SetDeadline(time.Now().Add(dialTimeout)); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("cannot bound the handshake read on %s: %w",
			addr, err)
	}

	src := &Source{addr: addr, conn: conn}

	if err := src.sendFirstPacket(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%s: cannot send the source handshake: %w",
			addr, err)
	}

	if err := src.readHandshakeAnswer(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("%s: %w", addr, err)
	}

	return src, nil
}

// sendFirstPacket writes the obfuscated login request.
//
// # THE SAME BYTES A SERVER RECEIVES, AND WHY
//
// A source is a peer, and a peer speaks the ed2k protocol the same way a
// server does: sixteen zero user-hash bytes, a four-byte callback port, an
// empty tag list. The Port is zero because this build does not accept
// callbacks, and it is a uint32 for the reason in loginRequest.body — a
// uint16 there is a packet two bytes short, which a peer reads as a tag count
// of whatever follows.
//
// The obfuscation is the same first-packet-only rule, from the same
// functions, and it is NOT copied. obfuscate and newObfuscationSeed are
// called directly, which is the one part of the duplication that would have
// been genuinely wrong to reimplement: a second obfuscator is a second thing
// whose correctness nobody tests.
func (s *Source) sendFirstPacket() error {
	req := loginRequest{
		Hash: [16]byte{},
		Port: 0,
		Tags: protocol.TagList{},
	}
	body, err := req.body()
	if err != nil {
		return fmt.Errorf("encoding the source handshake: %w", err)
	}

	frame, err := frameBytes(protocol.EdonkeyHeader, opLoginRequest, body)
	if err != nil {
		return err
	}

	seed, err := newObfuscationSeed()
	if err != nil {
		return err
	}
	obfuscated, err := obfuscate(frame, seed)
	if err != nil {
		return err
	}

	s.sentFirst = true
	if _, err := s.conn.Write(obfuscated); err != nil {
		return fmt.Errorf("writing the first packet: %w", err)
	}
	return nil
}

// readHandshakeAnswer reads whatever the peer sends in reply.
//
// # IT READS A BOUNDED BURST AND STOPS, AND IT DOES NOT CONFIRM ANYTHING
//
// A server's answer is a long, self-describing conversation: status, a
// confirmation, messages, an info packet. A source's answer is short and
// mostly of no interest, and the protocol has no "handshake complete" packet
// on the source side that this client can rely on — a claim this step has
// not verified against a real peer, because step 1 has no live test of its
// own and step 5 is where a source is observed for the first time.
//
// So the rule here is deliberately weak: read packets until the peer goes
// quiet, keep anything that looks like a GUID, and return success for having
// spoken. That is the honest description of what "the handshake completed"
// means so far, and the alternative — refusing until a specific packet
// arrives — would fail against every real peer on the strength of an
// assumption nobody has tested.
//
// # "SPOKE" IS A REQUIREMENT, NOT A FORMALITY
//
// Weak about WHICH packets, never about WHETHER any arrived. A peer that
// accepts the connection and then says nothing has not completed a
// handshake, and returning success there turns the deadline into a way to
// report a hang as a done deal. The first version did exactly that.
//
// # AND THE BURST IS BOUNDED
//
// A stranger can keep sending. The bound is what stops a peer that never
// stops talking from turning a handshake into an unbounded read, and a
// hang here is the one outcome with no error to report.
func (s *Source) readHandshakeAnswer() error {
	// # THE TIMEOUT IS HALF THE DIAL BUDGET ON PURPOSE
	//
	// A source that has accepted the connection and then gone quiet is a
	// different situation from a server that never answered, and it should
	// not cost a caller the whole budget to discover. The remainder of
	// dialTimeout is left to the first part request, which is the thing
	// that actually needs patience.
	//
	// A var and not a const, because dialTimeout is a var: the tests
	// shorten it so a timeout test does not take forty seconds. That is why
	// this is computed rather than declared — a const reading a var does not
	// compile, which is the compiler being right about a small thing.
	burstTimeout := dialTimeout / 2

	deadline := time.Now().Add(burstTimeout)
	heard := false
	for {
		if err := s.conn.SetReadDeadline(deadline); err != nil {
			return fmt.Errorf("cannot bound the handshake read: %w", err)
		}

		_, payload, err := readFrame(s.conn)
		if err != nil {
			// # GOING QUIET IS ONLY FINE IF SOMETHING WAS SAID
			//
			// The first version of this loop returned success on any
			// timeout, on the reasoning that "the peer spoke and then
			// stopped" is the normal shape of a source handshake. That is
			// true of a peer that spoke, and it made a peer that said
			// NOTHING at all indistinguishable from a completed
			// handshake -- the test caught it as "a peer that accepted
			// the connection and then said nothing was reported as a
			// successful handshake".
			//
			// The distinction is the whole point of the deadline: a
			// stranger who accepts a connection and then goes quiet is
			// silent, and reporting that as success is how a transfer
			// becomes a hang with no error anywhere.
			if isTimeout(err) {
				if !heard {
					return fmt.Errorf("%w: %s accepted the connection "+
						"and then said nothing for %v", ErrRefused,
						s.addr, burstTimeout)
				}
				return nil
			}
			return fmt.Errorf("reading the source's answer: %w", err)
		}

		heard = true
		s.noteHandshakePacket(payload)
	}
}

// noteHandshakePacket records what a peer's opening packet told us.
//
// # NOTHING HERE IS REQUIRED, AND THAT IS WHY IT IS SAFE TO IGNORE
//
// A peer may send a GUID, a message, or a packet this client does not model.
// Only the GUID is kept, and only as bytes: a peer that sends a malformed
// one is a peer whose GUID we do not use, not a handshake that failed.
//
// The refusal case is deliberately absent. readFrame has already refused a
// malformed FRAME, and a payload we do not understand is not a malformed
// connection — refusing here would throw away a working connection over a
// banner, which is the mistake Server.notePostLoginPacket documents and does
// not make.
func (s *Source) noteHandshakePacket(payload []byte) {
	if len(payload) < len(s.guid) {
		return
	}
	copy(s.guid[:], payload[:len(s.guid)])
}
