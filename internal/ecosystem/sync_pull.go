package ecosystem

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// R057's read direction, and §6a.19: "pull metadata, push edits through the proposal path".
// Push is built; this is Pull.
//
// WHY THE TRANSPORT IS AN INTERFACE
//
// The fork already speaks stash-box's GraphQL as a client (§3), and it would be tempting to
// reuse `pkg/stashbox.Client` here. A StashForge peer is NOT a stash-box instance though — it is
// the same *shape* of client pointed at a different protocol, and coupling the peer protocol to
// a generated schema this fork does not control would make every schema change upstream a
// breaking change here. A one-method interface keeps the seam explicit and, more usefully,
// makes a pull testable without a network — which is the only reason the consent and identity
// rules below are testable at all.

// Peer is the read side of another instance, as this package needs it.
//
// One method, deliberately: the narrowest interface that lets the interesting behaviour be
// tested. Anything richer would be a second protocol definition to keep in sync.
type Peer interface {
	// Pull returns entities from the peer matching q. Implementations SHOULD honour q's bound;
	// Syncer.Pull does not rely on that, and clamps the result regardless.
	Pull(ctx context.Context, from string, q PublicQuery) ([]PublicEntity, error)
}

// PullOptions is the LOCAL state a pull is evaluated against.
//
// This is the part that makes the direction of consent explicit. The peer's answer is INPUT;
// these fields are what THIS instance decides, and they are why a pull can never widen what
// this instance exposes.
type PullOptions struct {
	// Share is this instance's own consent decision for the entity type being pulled, as
	// collab.ShareChoice's string form ("opted-in" / "opted-out"). It is a string rather than
	// the typed constant so a transport can carry it without importing collab.
	//
	// An UNRECOGNISED value is treated as opted-OUT, matching Publishable's rule that only an
	// explicit opt-in publishes. The default zero value is therefore the safe one.
	Share string
}

// ErrNoPeer is returned when a pull names no instance, which would produce entities whose
// origin is blank and therefore indistinguishable from local ones.
var ErrNoPeer = errors.New("ecosystem: pull must name the peer instance it reads from")

// Pull reads metadata from a peer.
//
// IT WRITES NOTHING. The board is untouched and no local field is set, so a pull needs no
// proposal and is safe to run against a hostile peer — which is the point: the only direction
// that changes this instance is Push, and Push goes through governance.
//
// # THE CONSENT DIRECTION, which is the trap in this function
//
// A peer returning `Published: true` is making a claim about ITS OWN owner's consent, on ITS
// OWN instance. §6a.21's rule is per-instance. So the peer's flag is DISCARDED and replaced
// with this instance's own decision:
//
//   - pulled and this instance opted OUT -> the row is still returned (the metadata is
//     readable, which is what discovery needs) but Published is false, so nothing downstream
//     treats it as publishable here.
//   - pulled and this instance opted IN -> Published reflects the LOCAL decision.
//
// Getting this backwards is the failure that matters: a peer could otherwise publish this
// instance's library simply by answering a query about it.
//
// IDENTITY. Every returned entity gets Origin set to the peer, so §6a.6's (instance, local)
// pair is preserved. Two peers both using id 42 are two different entities, and the caller can
// tell them apart.
// NO nil check here on purpose: `pull` has one, and it is reachable from both this method and
// PullFrom. I had a second guard in this method first and a mutation removing it SURVIVED, which
// is how I established the two are equivalent -- the same error, from the same place. Two guards
// on one invariant is one more thing to keep in sync, so this delegates.
func (s *Syncer) Pull(ctx context.Context, peerName string, q PublicQuery, opts PullOptions) ([]PublicEntity, error) {
	var peer Peer
	if s != nil {
		peer = s.peer
	}
	return pull(ctx, peer, peerName, q, opts)
}

// PullFrom is Pull with the peer supplied explicitly, for a caller that has one client per
// peer rather than a single multi-peer transport. Passing a nil peer is an error rather than a
// silent empty result.
func PullFrom(ctx context.Context, peer Peer, peerName string, q PublicQuery, opts PullOptions) ([]PublicEntity, error) {
	return pull(ctx, peer, peerName, q, opts)
}

func pull(ctx context.Context, peer Peer, peerName string, q PublicQuery, opts PullOptions) ([]PublicEntity, error) {
	if strings.TrimSpace(peerName) == "" {
		return nil, ErrNoPeer
	}
	if peer == nil {
		return nil, fmt.Errorf("ecosystem: no peer transport wired for %q", peerName)
	}

	// The bound is applied HERE rather than trusted from the peer, and it is sent as well as
	// applied: a well-behaved peer then never returns more than the cap, and a peer that
	// ignores the bound still cannot widen this instance's read.
	bounded := q.bounded()

	got, err := peer.Pull(ctx, peerName, bounded)
	if err != nil {
		// Surfaced rather than swallowed. A sync that reports success having pulled nothing is
		// worse than one that fails, because it is indistinguishable from "there was nothing
		// to pull".
		return nil, err
	}

	// Only an explicit opt-in publishes. Anything else -- including the zero value and any
	// string this build does not recognise -- is treated as opted out, so a typo cannot widen
	// the surface.
	localPublishes := opts.Share == "opted-in"

	out := make([]PublicEntity, 0, len(got))
	for _, e := range got {
		// Clamp on the way out as well. A peer ignoring the bound is a remote peer; the number
		// of rows this instance acts on is a local decision, not one it delegates.
		if len(out) >= bounded.Limit {
			break
		}
		e.Origin = peerName
		e.Published = localPublishes
		out = append(out, e)
	}

	return out, nil
}
