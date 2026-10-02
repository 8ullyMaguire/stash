package ecosystem

import (
	"fmt"
	"strings"
	"time"
)

// R057's "contribute to preservation" -- §6a.9, "Preservation is a protocol, not a feature".
//
// THE ONE THING THIS TYPE EXISTS TO ENFORCE, quoted from §6a.9:
//
//   "For a user who set metadata_share = 'opted-out', their scene is never a replication
//    subject, and no amount of mesh popularity or a preservation bounty changes that."
//
// The consent decision belongs to the SUBJECT's owner, not to the contributor. So the check
// below is not a filter applied to contributions after they arrive -- it runs BEFORE a
// contribution can be registered, and a refused contribution leaves nothing behind. A filter
// that discarded the contribution later would still have accepted the claim, updated a
// counter, and sent a message; §6a.9's rule is about the subject never being a subject at all.
//
// And note what makes this a trap rather than a check: a contribution is a GIFT, and "it's a
// good cause" is precisely the reasoning non-negotiable #7 exists to stop.

// DefaultReplicas is §6a.9's "a scene is hosted on at least three instances".
const DefaultReplicas = 3

// Contribution is a peer's claim that a replica of a subject exists, or should.
type Contribution struct {
	// Contributor is who is offering it.
	Contributor string
	// SubjectID is the object being preserved. §6a.9's "content object".
	SubjectID string
	// SubjectShare is the SUBJECT OWNER's share state, as this instance knows it.
	//
	// It is a field on the contribution rather than a lookup because the local instance's
	// record is the authority: a peer asserting a subject's consent is a peer asking to be
	// believed about somebody else's decision, which is the whole failure mode.
	SubjectShare string
	// ReplicaCount is how many replicas the contributor is claiming to hold.
	ReplicaCount int
}

// Policy is the preservation policy for one object -- §6a.9's "preservation policy".
type Policy struct {
	SubjectID string
	// Replicas is the number of instances the object should be hosted on. DefaultReplicas is
	// the floor and a contribution cannot lower it.
	Replicas int
	// Holders are the instances that have accepted a replica.
	//
	// COPIED ON THE WAY OUT, and the reason is worth recording because the hazard is easy to
	// dismiss. Holders is only ever appended to one element at a time from a nil slice, so
	// len(Holders) == cap(Holders) always -- and a mutation removing the copy in PolicyFor
	// SURVIVED a dedicated test, because with no spare capacity Go's append reallocates and the
	// caller's slice is untouched either way. The copy is what makes the invariant hold if that
	// ever stops being true (a bulk-accept, a restore from JSON, a future field), so it stays,
	// with the test below pinning the intent rather than the mechanism.
	Holders []string
	// Access is here to be false and empty: preservation grants no access (§6a.9, §6a.10).
	Access     bool
	TrustLevel string
}

// Preservation is the protocol's local half.
type Preservation struct {
	now func() time.Time

	// policies is keyed by subject. A subject that was refused has NO entry -- that absence is
	// the enforcement, so there is no way to look up a policy for an opted-out subject.
	policies map[string]*Policy

	// refusals are instances that must not hold a replica (§6a.9: "an instance that must not
	// hold a replica refuses it").
	refusals map[string]bool
}

// NewPreservation returns an empty protocol.
func NewPreservation() *Preservation {
	t := TestNow
	return &Preservation{
		now:      func() time.Time { return t },
		policies: map[string]*Policy{},
		refusals: map[string]bool{},
	}
}

// withClock is a test seam.
func (p *Preservation) withClock(fn func() time.Time) *Preservation { p.now = fn; return p }

// withRefusals marks instances as unable or unwilling to hold replicas.
func (p *Preservation) withRefusals(ids ...string) *Preservation {
	for _, id := range ids {
		p.refusals[id] = true
	}
	return p
}

// OptedIn is the ONLY string that opts in.
//
// Exact match, no trimming, no case folding. §6a.21's rule is that an opted-out entity is
// neither indexed nor reachable, and this package's whole posture is that the safe default is
// the narrow one: a peer speaking a different vocabulary, or a query string that picked up a
// trailing space, must widen the surface to nothing rather than to everything. A `strings.EqualFold`
// here would accept "OPTED-IN" and turn a case difference into a consent decision.
const OptedIn = "opted-in"

// Contribute registers a preservation contribution.
//
// THE ORDER MATTERS, and it is the substance of §6a.9. Validation of the contribution's own
// shape comes first (a malformed contribution is a client bug), then the SUBJECT's consent
// (the owner's decision, which no contribution can override), and only then is anything written.
func (p *Preservation) Contribute(c Contribution) error {
	if strings.TrimSpace(c.Contributor) == "" {
		return fmt.Errorf("ecosystem: a contribution needs a contributor")
	}
	if strings.TrimSpace(c.SubjectID) == "" {
		return fmt.Errorf("ecosystem: a contribution needs a subject")
	}
	if c.ReplicaCount < 1 {
		return fmt.Errorf("ecosystem: a contribution claims %d replicas; a claim of none is not a "+
			"contribution", c.ReplicaCount)
	}

	// §6a.9's hard constraint. Before anything is registered, and regardless of ReplicaCount:
	// a high replica count and a large bounty do not make an opted-out scene a subject.
	if c.SubjectShare != OptedIn {
		return fmt.Errorf("ecosystem: %q is not a replication subject: its owner's share state is "+
			"%q, not %q. §6a.9: no amount of mesh popularity or a preservation bounty changes that",
			c.SubjectID, c.SubjectShare, OptedIn)
	}

	pol, ok := p.policies[c.SubjectID]
	if !ok {
		// DefaultReplicas as a FLOOR. A contribution claiming one replica does not get to
		// declare the object adequately preserved on one instance.
		pol = &Policy{
			SubjectID: c.SubjectID,
			Replicas:  DefaultReplicas,
		}
		p.policies[c.SubjectID] = pol
	}
	return nil
}

// PolicyFor returns a subject's policy.
//
// Errors for an unknown subject rather than returning a zero policy, for the same reason
// RankingOf does: "no policy" and "a policy of zero replicas" are different facts.
func (p *Preservation) PolicyFor(subjectID string) (Policy, error) {
	pol, ok := p.policies[subjectID]
	if !ok {
		return Policy{}, fmt.Errorf("ecosystem: no preservation policy for %q", subjectID)
	}
	out := *pol
	out.Holders = append([]string(nil), pol.Holders...)
	return out, nil
}

// AcceptReplica records that an instance is holding a replica.
//
// An instance that must not hold one refuses, and the refusal is TOTAL (§6a.9: "refusing is not
// a partial success") -- so this returns an error and registers nothing.
func (p *Preservation) AcceptReplica(subjectID, holderID string) error {
	if strings.TrimSpace(holderID) == "" {
		return fmt.Errorf("ecosystem: no holder named")
	}
	pol, ok := p.policies[subjectID]
	if !ok {
		return fmt.Errorf("ecosystem: %q is not a replication subject", subjectID)
	}
	if p.refusals[holderID] {
		return fmt.Errorf("ecosystem: %q must not hold a replica of %q, and refusing is not a "+
			"partial success", holderID, subjectID)
	}
	for _, h := range pol.Holders {
		if h == holderID {
			return nil // already holding; idempotent
		}
	}
	pol.Holders = append(pol.Holders, holderID)
	return nil
}

// HoldingReplica reports whether an instance holds a replica.
func (p *Preservation) HoldingReplica(subjectID, holderID string) (bool, error) {
	pol, ok := p.policies[subjectID]
	if !ok {
		return false, fmt.Errorf("ecosystem: %q is not a replication subject", subjectID)
	}
	if p.refusals[holderID] {
		// A refusal is remembered, not merely not-yet-accepted. Otherwise "refused" and
		// "never asked" are the same state, and a caller that retried would see a different
		// answer than one that read the policy.
		return false, fmt.Errorf("ecosystem: %q refused to hold a replica of %q", holderID, subjectID)
	}
	for _, h := range pol.Holders {
		if h == holderID {
			return true, nil
		}
	}
	return false, nil
}
