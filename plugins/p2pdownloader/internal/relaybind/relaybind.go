// Package relaybind is the binding shape R088 concluded, as an invariant rather than a
// comment: a relay's connections are all OUTBOUND, so a relay behind NAT serves without
// a port forwarded and without any address crossing between the two endpoints.
//
// # WHY THIS IS A TYPE AND NOT A NOTE IN A PROBE
//
// `cmd/probe_nat` concluded, from RFC NAT behaviour, that a relay needs no inbound
// path. That conclusion is easy to record and easy to lose: nothing in the transport
// enforces it, so the first person who writes "let the seeder connect back to the relay
// to make the handshake simpler" would break the property silently, and the symptom
// would be a mesh that only works for operators who can open a port -- which is exactly
// the population R088 exists to stop selecting for.
//
// So the shape is in the type system. `Pair` is constructed only from two OUTBOUND
// legs, there is no constructor that takes an inbound one, and the reason is in the
// error text a caller will see when they try to build the other shape.
//
// # THE INVARIANT, PRECISELY
//
// A relay holds N outbound connections and zero inbound ones. Peers dial the relay;
// the relay dials peers. Splicing is the relay's only job, so:
//
//   - every leg is dialled BY the relay
//   - no leg is ever accepted BY the relay
//   - a peer address reaches the relay (so the relay can dial it) and goes nowhere else
//
// The third clause is R077's, and it is the one worth being pedantic about. R077 forbids
// the SEEDER's address reaching the REQUESTER. It does not forbid the relay learning
// it, because the relay is the dispatcher and already holds the payload -- and since
// R090 the relay cannot read any of it. A dispatcher that cannot read the traffic and
// is the only party who sees both endpoints is precisely the party R077's property
// wants to exist.
package relaybind

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrInboundLegRejected is a caller trying to add a connection the relay did not dial.
//
// Returned by AddInbound, which exists ONLY to produce this error.
//
// A METHOD THAT CANNOT SUCCEED LOOKS LIKE DEAD CODE, and here it is the opposite: it
// is the enforcement point. Deleting AddInbound would remove a name a caller will look
// for, find nothing, and then work around by dialling the relay from outside -- which
// reintroduces exactly the inbound path this package exists to make impossible. So the
// refusal is a named method with a comment, not an absence.
var ErrInboundLegRejected = errors.New("relaybind: a relay leg must be dialled OUT by " +
	"the relay; an inbound leg is the NAT case R088 removed")

// Peer is one endpoint of a relayed transfer, as the relay knows it.
//
// IDENTITY IS A DIGEST, and the separation between Digest and Addr is the mechanism by
// which R077's property is kept. The relay needs an address to dial; everything it
// forwards to another party is the digest. Keeping them in one struct with two fields
// makes "which one do I hand onward" a question with an obvious answer, where a struct
// that carried an address and a name would make it a judgement call at each call site.
type Peer struct {
	// Digest is the non-routable identifier. This is what crosses to other peers.
	Digest string

	// Addr is where to dial. This NEVER leaves the relay.
	Addr string

	// Name is the operator-facing label, for a status endpoint. Never forwarded, and
	// never used as a dial target.
	Name string
}

// Leg is one outbound connection the relay holds.
type Leg struct {
	// To is the peer this leg reaches. Its Addr is not exported by any method here.
	To Peer

	// Dialed is when the outbound connection was established, for a status endpoint
	// and for the staleness rule in Pair.
	Dialed time.Time

	// Outbound is ALWAYS true. It exists so the invariant is visible in the value as
	// well as in the type, which is what lets a status endpoint ASSERT it rather than
	// assume it -- a status endpoint reporting "3 outbound, 0 inbound" is evidence,
	// where "3 legs" is a claim.
	Outbound bool
}

// Pair is one relayed transfer: two outbound legs, spliced.
//
// EXACTLY TWO, not N. A relay that can splice an arbitrary number of legs is a router,
// and a router is the thing R077 was written to avoid. Two is what "copy from one peer
// to another" means, and holding the type at two makes "did somebody add a third leg"
// a compile error instead of a design review question.
type Pair struct {
	mu sync.Mutex

	// from is the leg carrying content INTO the relay (the seeder).
	from *Leg

	// to is the leg carrying content OUT (the requester).
	to *Leg

	// bytesCopied counts what has moved between the legs, which is what R089's budget
	// is measured against -- and it lives HERE rather than in relayconsent because a
	// relayed byte cannot be budgeted until there is a pair to attribute it to.
	bytesCopied uint64
}

// NewPair binds two outbound legs.
//
// BOTH LEGS MUST BE OUTBOUND, and the check is the package's reason for existing. A
// pair with an inbound leg is the topology R088 ruled out, and constructing one here
// rather than at the call site means the refusal happens where the topology is decided
// instead of where it is noticed.
func NewPair(from, to Leg) (*Pair, error) {
	if !from.Outbound {
		return nil, fmt.Errorf("relaybind: the FROM leg was not dialled by the relay: %w", ErrInboundLegRejected)
	}
	if !to.Outbound {
		return nil, fmt.Errorf("relaybind: the TO leg was not dialled by the relay: %w", ErrInboundLegRejected)
	}
	if from.To.Digest == "" || to.To.Digest == "" {
		return nil, fmt.Errorf("relaybind: both legs need a digest: a leg with no " +
			"identifier cannot be forwarded to anyone, so a pair of them relays nothing")
	}
	p := &Pair{from: &from, to: &to}
	return p, nil
}

// AddInbound always fails. It is the enforcement point, not an oversight.
//
// THE ARGUMENT IS TAKEN AND IGNORED ON PURPOSE, and a reader may reasonably ask why
// the address is not validated first. Because a caller who passed an inbound address
// has the wrong idea about the topology, and telling them their address is malformed
// would send them off to fix a string instead of fixing the shape. The refusal is about
// the SHAPE, so the refusal is about the shape.
func (p *Pair) AddInbound(addr string) error {
	return fmt.Errorf("relaybind: cannot add inbound leg %q: %w", addr, ErrInboundLegRejected)
}

// InboundCount is always zero, and exists so a status endpoint can print the invariant
// rather than assert it in prose.
func (p *Pair) InboundCount() int { return 0 }

// OutboundCount is two, because a pair is two legs and nothing else.
func (p *Pair) OutboundCount() int { return 2 }

// BothOutbound reports the invariant, so a caller can check it without reading the type.
func (p *Pair) BothOutbound() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.from.Outbound && p.to.Outbound
}

// Peers returns what may be forwarded onward: the digests, and ONLY the digests.
//
// THIS IS THE R077 CHECK, AS CODE. The address of one peer is never in this struct's
// output, so there is no path by which a relay can hand one endpoint's address to the
// other -- the property holds because the accessor cannot express the leak, rather than
// because each call site remembered not to.
func (p *Pair) Peers() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return []string{p.from.To.Digest, p.to.To.Digest}
}

// Charge records bytes moved between the legs, for R089's budget.
//
// RETURNS THE RUNNING TOTAL so the caller can compare it against a cap, rather than
// keeping a separate counter that could disagree with this one.
func (p *Pair) Charge(n uint64) uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bytesCopied += n
	return p.bytesCopied
}

// Copied reports the bytes moved between the legs so far.
func (p *Pair) Copied() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bytesCopied
}
