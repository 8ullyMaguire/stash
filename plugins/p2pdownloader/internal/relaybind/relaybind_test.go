package relaybind

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// R088: a relay behind NAT serves by dialling OUT, so it needs no port forwarded and
// no address crosses between the two endpoints.

func outboundLeg(digest, addr string) Leg {
	return Leg{To: Peer{Digest: digest, Addr: addr, Name: "peer-" + digest}, Dialed: time.Now(), Outbound: true}
}

// THE INVARIANT IS CONSTRUCTIBLE, NOT JUST DESCRIBED.
//
// probe_nat concluded it from RFC behaviour; this asserts that a pair of outbound legs
// is the shape the package hands out, so the conclusion is a fact about the code rather
// than a claim about a probe's output.
func TestATransferIsTwoOutboundLegs(t *testing.T) {
	p, err := NewPair(
		outboundLeg("sha256:seeder", "10.0.0.5:4711"),
		outboundLeg("sha256:requester", "10.0.0.9:4712"),
	)
	require.NoError(t, err)

	assert.True(t, p.BothOutbound(), "both legs were dialled by the relay")
	assert.Equal(t, 2, p.OutboundCount())
	assert.Equal(t, 0, p.InboundCount(),
		"a relay holds zero inbound legs. This is the NAT case R088 removed, and it "+
			"is why a volunteer relay behind a router can serve")
}

// AN INBOUND LEG IS REFUSED AT CONSTRUCTION.
//
// The refusal has to happen WHERE THE TOPOLOGY IS DECIDED. Accepting one and letting a
// later step notice it would mean the property depends on every call site remembering,
// and the caller who forgot would be the one writing the simpler handshake.
func TestAnInboundLegIsRefusedAtConstruction(t *testing.T) {
	t.Run("from leg", func(t *testing.T) {
		inbound := outboundLeg("sha256:seeder", "10.0.0.5:4711")
		inbound.Outbound = false
		_, err := NewPair(inbound, outboundLeg("sha256:requester", "10.0.0.9:4712"))
		require.ErrorIs(t, err, ErrInboundLegRejected)
	})

	t.Run("to leg", func(t *testing.T) {
		inbound := outboundLeg("sha256:requester", "10.0.0.9:4712")
		inbound.Outbound = false
		_, err := NewPair(outboundLeg("sha256:seeder", "10.0.0.5:4711"), inbound)
		require.ErrorIs(t, err, ErrInboundLegRejected,
			"a pair with either leg inbound is the topology R088 ruled out")
	})

	t.Run("and the refusal names the shape, not the string", func(t *testing.T) {
		inbound := outboundLeg("sha256:seeder", "10.0.0.5:4711")
		inbound.Outbound = false
		_, err := NewPair(inbound, outboundLeg("sha256:r", "10.0.0.9:4712"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "FROM leg",
			"the message says WHICH leg was wrong, because the fix differs per leg")
	})
}

// ADDINBOUND ALWAYS FAILS, AND THAT IS THE POINT.
//
// A method that cannot succeed looks like dead code, and the reason it exists is the
// opposite: it is the enforcement point a caller finds when they go looking for the
// other shape. Without a name to hit, they would work around it by dialling the relay
// from outside, which reintroduces the inbound path in a place nothing checks.
func TestAddInboundAlwaysRefusesAndNamesTheAddress(t *testing.T) {
	p, err := NewPair(
		outboundLeg("sha256:seeder", "10.0.0.5:4711"),
		outboundLeg("sha256:requester", "10.0.0.9:4712"),
	)
	require.NoError(t, err)

	err = p.AddInbound("203.0.113.7:4711")
	require.ErrorIs(t, err, ErrInboundLegRejected)
	assert.Contains(t, err.Error(), "203.0.113.7:4711",
		"the refused address appears, so the caller can see WHICH dial they were "+
			"attempting -- and can see it was their own address, not a peer's")
	assert.Zero(t, p.InboundCount(), "and the count is still zero after the attempt")
}

// WHAT MAY BE FORWARDED IS THE DIGEST, AND NEVER THE ADDRESS.
//
// This is R077 as CODE rather than as a rule: the accessor physically cannot return an
// address, so there is no call site at which someone could leak one by forgetting not
// to. The test asserts on the SHAPE of the output rather than on the absence of a
// substring, because "does not contain 10.0.0.5" is a weaker statement than "returns
// exactly these two strings and nothing else".
func TestWhatMayBeForwardedIsTheDigestAndNeverTheAddress(t *testing.T) {
	const seederAddr = "10.0.0.5:4711"
	const reqAddr = "10.0.0.9:4712"

	p, err := NewPair(
		outboundLeg("sha256:seeder", seederAddr),
		outboundLeg("sha256:requester", reqAddr),
	)
	require.NoError(t, err)

	got := p.Peers()
	require.Len(t, got, 2)
	assert.ElementsMatch(t, []string{"sha256:seeder", "sha256:requester"}, got,
		"the forwarded set is exactly the two digests")

	// AND NEITHER ADDRESS APPEARS IN THE FORWARDED SET. Checked by scanning every
	// returned string for the address literal, including the port, because an address
	// could leak as a bare host.
	for _, s := range got {
		assert.NotContains(t, s, "10.0.0.5", "the seeder's address must not be forwardable")
		assert.NotContains(t, s, "10.0.0.9", "the requester's address must not be forwardable")
		assert.NotContains(t, s, ":471", "no port either")
	}
}

// A PAIR WITH NO DIGESTS IS REFUSED.
//
// Two legs whose peers have no identifiers relay nothing forwardable, so the pair is a
// transfer nobody could ever join -- which is a configuration mistake worth refusing at
// construction rather than discovering when a client asks who else is here.
func TestAPairWithNoDigestsIsRefused(t *testing.T) {
	_, err := NewPair(
		outboundLeg("", "10.0.0.5:4711"),
		outboundLeg("sha256:requester", "10.0.0.9:4712"),
	)
	require.Error(t, err, "a leg with no identifier cannot be forwarded to anyone")

	_, err = NewPair(
		outboundLeg("sha256:seeder", "10.0.0.5:4711"),
		outboundLeg("", "10.0.0.9:4712"),
	)
	require.Error(t, err)
}

// CHARGING ACCUMULATES AND RETURNS THE RUNNING TOTAL, FOR R089'S CAP.
//
// The total is RETURNED rather than only recorded so the caller compares against the
// same number this type accumulated. A caller keeping its own counter could disagree
// with this one, and then the budget R089 depends on would be enforced against a
// different figure than the one being reported to the operator.
func TestChargingAccumulatesAndReturnsTheRunningTotal(t *testing.T) {
	p, err := NewPair(
		outboundLeg("sha256:seeder", "10.0.0.5:4711"),
		outboundLeg("sha256:requester", "10.0.0.9:4712"),
	)
	require.NoError(t, err)

	assert.Equal(t, uint64(65536), p.Charge(65536))
	assert.Equal(t, uint64(196608), p.Charge(131072), "a larger frame accumulates rather "+
		"than replacing, so a relay copying at full speed reports a growing total")
	assert.Equal(t, uint64(196608), p.Copied())
}

// A PAIR IS EXACTLY TWO LEGS, NOT N.
//
// A relay able to splice an arbitrary number of legs is a router, and a router is the
// thing R077 was written to avoid. Two is what "copy from one peer to another" means.
// The type holds the line by having no field a third leg could go into, and this test
// records the consequence so the constraint is visible rather than implicit.
func TestAPairIsExactlyTwoLegs(t *testing.T) {
	p, err := NewPair(
		outboundLeg("sha256:seeder", "10.0.0.5:4711"),
		outboundLeg("sha256:requester", "10.0.0.9:4712"),
	)
	require.NoError(t, err)

	assert.Equal(t, 2, p.OutboundCount(),
		"two legs, and there is no way to express a third: a relay that could splice N "+
			"legs would be a router, which is the thing R077 avoids")

	// AND THE PAIR IS NOT A SET, so a third peer cannot be swapped in by assignment.
	// The assertion is on the API surface: AddInbound is the only mutator that adds
	// anything, and it refuses.
	err = p.AddInbound("198.51.100.4:4711")
	require.ErrorIs(t, err, ErrInboundLegRejected)
}

// WHAT THIS PACKAGE DOES NOT CLAIM.
//
// A client's peers do learn the relay's address, and that is unavoidable for any mesh
// with a rendezvous. R077 never claimed otherwise -- it claimed no routable address
// crosses BETWEEN THE TWO ENDPOINTS. The distinction is the whole reason this shape
// works, so it is asserted rather than left to the reader.
func TestWhatThisDoesNotClaim(t *testing.T) {
	p, err := NewPair(
		outboundLeg("sha256:seeder", "10.0.0.5:4711"),
		outboundLeg("sha256:requester", "10.0.0.9:4712"),
	)
	require.NoError(t, err)

	// GUARANTEED: no endpoint address is forwardable between the endpoints.
	require.Len(t, p.Peers(), 2)

	// NOT GUARANTEED, and deliberately so: that nobody learns the relay's address.
	// A client that connected to a relay knows where that relay is. This is not the
	// property R077 established, and a test asserting it would be asserting something
	// false -- so the honest form of this test is to name what IS claimed.
	assert.True(t, p.BothOutbound(),
		"the guarantee is about INBOUND PATHS and about ADDRESSES NOT CROSSING "+
			"BETWEEN ENDPOINTS -- not about a peer learning where the relay is, which "+
			"is inherent to any rendezvous")
}

// ERRORS ARE DISTINGUISHABLE, so a caller can react differently.
//
// An inbound-leg refusal and a missing-digest refusal need different fixes -- one is a
// topology change, the other is a configuration fix -- and a caller told only "invalid"
// has to guess which.
func TestTheTwoRefusalsAreDistinguishable(t *testing.T) {
	inbound := outboundLeg("s", "10.0.0.5:4711")
	inbound.Outbound = false
	_, inboundErr := NewPair(inbound, outboundLeg("r", "10.0.0.9:4712"))
	_, digestErr := NewPair(outboundLeg("s", "10.0.0.5:4711"), outboundLeg("", "10.0.0.9:4712"))

	require.True(t, errors.Is(inboundErr, ErrInboundLegRejected))
	require.False(t, errors.Is(digestErr, ErrInboundLegRejected),
		"a missing digest is a different problem with a different fix, and collapsing "+
			"them would send a caller off to re-plumb the transport instead of setting "+
			"an identifier")
}
