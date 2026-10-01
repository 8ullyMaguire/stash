// Package mesh holds the federation protocol's types and framing, and nothing
// else.
//
// M7 step 7.1 (R059, §6a.18). This package is deliberately narrow: protocol
// types, the framing, and no policy. No storage policy, no scheduling, no
// ranking, no access decisions.
//
// WHY IT IS SEPARATE AND WHY IT IS EMPTY-ISH. Taste-based peering,
// cross-instance discovery and replication scheduling all need the wire format,
// and building any of them first means inventing a private protocol three times.
// What the protocol needs from the rest of the system is only schema, which is
// migrations 111-113. So the schema and the format land together, before any
// algorithm that depends on them.
//
// Putting a policy decision in here is how the format stops being reusable: a
// replication scheduler inside the protocol package can read a peer's claims
// (see Claims below), and then the wire format has an opinion about how much
// storage a stranger said it has, which is the failure this package exists to
// prevent.
package mesh

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Version is the protocol version this build speaks.
//
// It is a single integer rather than a semver because the format is a document
// (docs/FEDERATION.md) and a peer is either speaking a version whose opcodes it
// knows or it is not. A peer advertising a HIGHER version is refused rather than
// best-effort parsed: a partially understood replication manifest is how a peer
// convinces you to store a file you cannot describe.
const Version = 1

// Errors the handshake returns. Sentinels rather than strings so a caller can
// branch on the cause without matching text, and so a test can assert the
// difference between "we do not speak that" and "your signature is wrong".
var (
	// ErrUnsupportedVersion means the peer's version is not one this build
	// speaks. Deliberately fatal rather than negotiated downwards.
	ErrUnsupportedVersion = errors.New("mesh: unsupported protocol version")

	// ErrUnsigned means a handshake arrived with no key. Distinct from
	// ErrBadSignature: unsigned is "we were not asked to check", which is the
	// state a peer should never be stored in.
	ErrUnsigned = errors.New("mesh: handshake is unsigned")

	// ErrBadSignature means the signature did not verify under the advertised
	// key. The peer is not merely unknown; it has proved something false about
	// itself, and the two deserve different handling.
	ErrBadSignature = errors.New("mesh: signature does not verify")
)

// Claims is what a peer says about itself.
//
// EVERY FIELD HERE IS A CLAIM. The struct is named for that on purpose, and the
// fields carry a Claim prefix so a call site has to write `p.ClaimStoreBytes` to
// read one. That is not decoration: the natural name is `StoreBytes`, and
// `StoreBytes` reads as a fact at the call site, and a fact gets used for
// sizing.
//
// The alternative was considered and rejected -- storing these as ordinary
// fields and relying on a comment. The failure mode is silent: a node sizes a
// buffer from a stranger's claim, works perfectly for every cooperative peer,
// and does not notice for a week. Nothing errors. The claim being three orders
// of magnitude off is indistinguishable from the allocator being generous.
//
// TestAProfileClaimIsLabelledAClaim is the guard, and it is a grep-level one:
// the claim fields must not appear in any file that sizes or allocates. Types
// are exempt (they have to name the fields to decode them), and that exemption
// is the whole subtlety -- a source scan that included the protocol package
// would match its own definitions and pass vacuously.
type Claims struct {
	// ClaimStoreBytes is the peer's claim about how much it has. NOT a fact and
	// never a sizing input; a peer that says 10 TB has told you nothing you can
	// act on until you have measured something yourself.
	ClaimStoreBytes uint64 `json:"claim_store_bytes,omitempty"`

	// ClaimBandwidthBps is the same kind of claim, about bandwidth.
	ClaimBandwidthBps uint64 `json:"claim_bandwidth_bps,omitempty"`
}

// HasStorageRoom answers "did this peer claim room?" and the answer is a CLAIM
// being present, not a capacity.
//
// The name is the point. A method called `CanStore` or `HasCapacity` invites the
// use this whole type exists to prevent; `ClaimStoreBytes` returns a number and
// the caller has to do something with it, which is the moment a decision becomes
// visible in review.
func (c Claims) ClaimStoreBytesOrZero() uint64 {
	return c.ClaimStoreBytes
}

// Handshake is the first message in a peering exchange.
//
// Order matters and is load-bearing: Peer carries the key, and the signature
// covers the rest. A verifier that reads Claims before checking the signature
// has already used data from an unauthenticated stranger, and if it then caches
// a decision based on those claims the forgery outlives the handshake.
type Handshake struct {
	// Version is the peer's protocol version. Checked first, against
	// ErrUnsupportedVersion.
	Version int `json:"version"`

	// InstanceID is the peer's claimed stable public identity.
	InstanceID string `json:"instance_id"`

	// DisplayName is the peer's human-readable name. Never used as a key; see
	// Claims for why a self-description is not an input to a decision.
	DisplayName string `json:"display_name"`

	// PublicKey is the key the peer signs with. It is the one field in this
	// struct that is checkable, which is why Validate requires it.
	PublicKey []byte `json:"public_key"`

	// Claims are the peer's statements about itself. See Claims.
	Claims Claims `json:"claims"`
}

// Validate checks a handshake's self-consistency, WITHOUT verifying its
// signature.
//
// Split deliberately: this answers "is this message well-formed and do we speak
// it", and VerifySignature answers "did the named key produce this signature".
// A single Validate that did both would let a caller pass by checking the cheap
// half and believe it checked both -- the same "a handle that is right about one
// property and useless about another" the spec names for federated ids.
//
// Signature verification is a separate step in the handshake sequence and is
// deliberately NOT done here, because this function has no key to verify
// against: the key arrives in the message it is supposed to have signed.
func (h Handshake) Validate() error {
	if h.Version != Version {
		return fmt.Errorf("%w: peer speaks %d, this build speaks %d",
			ErrUnsupportedVersion, h.Version, Version)
	}

	if strings.TrimSpace(h.InstanceID) == "" {
		return errors.New("mesh: handshake has no instance_id")
	}

	if len(h.PublicKey) == 0 {
		// ErrUnsigned rather than a bare error: a peer that cannot be verified
		// must not be stored as though it had been.
		return fmt.Errorf("%w: handshake carries no public key", ErrUnsigned)
	}

	return nil
}

// IsActive reports whether a peer row's revocation timestamp means revoked.
//
// Stated as a function over the timestamp rather than a `revoked_at IS NULL`
// repeated at each call site, because the two are not the same question and only
// one of them is right at the edges: migration 112 forbids a future revoked_at,
// so inside the database they agree, and the moment anything else writes that
// column -- a repair, a migration, an operator with a bad clock -- they stop
// agreeing. Treating "revoked in the future" as "not yet revoked" is the
// fail-safe direction: a peer comes back rather than staying revoked forever
// because of a typo in a timestamp.
func IsActive(revokedAt *time.Time) bool {
	if revokedAt == nil {
		return true
	}
	return revokedAt.After(time.Now())
}
