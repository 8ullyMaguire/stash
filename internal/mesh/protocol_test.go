package mesh

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validHandshake() Handshake {
	return Handshake{
		Version:     Version,
		InstanceID:  "inst-peer",
		DisplayName: "A Peer",
		PublicKey:   []byte{0x01, 0x02},
	}
}

func TestAWellFormedHandshakeValidates(t *testing.T) {
	require.NoError(t, validHandshake().Validate())
}

// THE VERSION CHECK IS FATAL, NOT NEGOTIATED.
//
// A peer speaking a higher version is refused rather than best-effort parsed,
// because a partially understood replication manifest is how a peer convinces
// you to store a file you cannot describe. That is a silent data-loss path, and
// silently is the part that makes it dangerous.
func TestAHigherPeerVersionIsRefusedNotDowngraded(t *testing.T) {
	h := validHandshake()
	h.Version = Version + 1

	err := h.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsupportedVersion,
		"this must be distinguishable from a malformed handshake: 'we do not speak "+
			"that' and 'your message is wrong' are different operator problems")
}

func TestAnOlderPeerVersionIsAlsoRefused(t *testing.T) {
	// Not "downgraded to 1" and not "best effort". There is no v0 to fall back to,
	// and pretending otherwise would mean writing a second parser for a format
	// that never shipped.
	h := validHandshake()
	h.Version = 0

	assert.ErrorIs(t, h.Validate(), ErrUnsupportedVersion)
}

func TestAnUnsignedHandshakeIsRefusedDistinctlyFromABadSignature(t *testing.T) {
	// "unverified" and "unsigned" are different states. A peer that arrives with
	// no key has not been asked to check anything; a peer whose signature fails
	// has proved something false about itself. Storing the first as if it were
	// the second -- or treating either as merely "not known yet" -- is how an
	// unverifiable peer becomes a trusted one.
	h := validHandshake()
	h.PublicKey = nil

	err := h.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsigned)
	assert.NotErrorIs(t, err, ErrBadSignature,
		"there is no signature to be bad yet; conflating the two would make an "+
			"unverifiable peer look like an impostor rather than an unsigned one")
}

func TestABlankInstanceIDIsRefused(t *testing.T) {
	// Whitespace, not just empty: the same trim() lesson as the schema CHECKs,
	// and for the same reason -- a blank identity is not an identity.
	for _, blank := range []string{"", "   ", "\t", "\n"} {
		h := validHandshake()
		h.InstanceID = blank
		assert.Error(t, h.Validate(), "instance_id %q must be refused", blank)
	}
}

// Claims are carried, and Validate does NOT reject them. A peer that advertises
// a huge store size is making a claim, not breaking the format; refusing the
// handshake would turn "this peer is lying" into "this peer is unreachable", and
// the second loses the information.
func TestClaimsAreCarriedAndNotRejectedByValidate(t *testing.T) {
	h := validHandshake()
	h.Claims = Claims{ClaimStoreBytes: 1 << 50, ClaimBandwidthBps: 10 << 30}

	require.NoError(t, h.Validate(),
		"a peer making an implausible claim has still sent a well-formed "+
			"handshake; the claim is not the format's business")
	assert.EqualValues(t, 1<<50, h.Claims.ClaimStoreBytesOrZero())
}

// The accessor's name is load-bearing and worth a test: HasCapacity or CanStore
// would invite the use the whole type prevents.
func TestTheClaimAccessorIsNamedForWhatItIs(t *testing.T) {
	c := Claims{ClaimStoreBytes: 7}
	assert.EqualValues(t, 7, c.ClaimStoreBytesOrZero())
	assert.EqualValues(t, 0, Claims{}.ClaimStoreBytesOrZero(),
		"an absent claim reads as zero, which is a number a caller could still "+
			"size from -- so the zero case is asserted rather than left implied")
}

// IsActive treats a FUTURE revocation as not-yet-revoked. Migration 112 forbids
// that row, so inside the database the two agree; this is about the moment
// something else writes the column and they stop agreeing.
func TestARevocationInTheFutureMeansNotYetRevoked(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)

	var revokedPast, revokedFuture *time.Time = &past, &future

	assert.False(t, IsActive(revokedPast),
		"a peer revoked an hour ago is revoked")
	assert.True(t, IsActive(revokedFuture),
		"a peer revoked an hour from now is not revoked YET, and must come back "+
			"rather than staying revoked forever over a typo")
	assert.True(t, IsActive(nil),
		"an unset revocation is the active state")
}

func TestVersionIsOneAndTheTestsPinIt(t *testing.T) {
	// A wire format version is not a version of this binary. Pinning it here means
	// changing it is a deliberate edit to a documented constant rather than a
	// side effect of some refactor.
	assert.Equal(t, 1, Version)
}
