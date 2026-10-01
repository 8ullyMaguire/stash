package collab

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
)

// Peer federation. M3 step 3.3, spec §6.5.
//
// Three separate capabilities, and the spec is explicit that they are separable:
// an instance may PUBLISH to a peer, CONSUME a peer's commons for identification
// (the way stash uses stash-box today), or both. Folding "receive" into "send"
// would mean that accepting an inbound submission from someone also opts you into
// publishing to them, which is not what either side asked for.
//
// So a Peer has two independent flags and four combinations, and
// TestFederation_PublishAndConsumeAreIndependent walks all four.

// Peer is a configured federation partner.
//
// Per-peer, not global: the spec says federation is opt-in per peer, and a
// global switch would make "I share with Alice but not Bob" inexpressible.
type Peer struct {
	ID       int64
	Name     string
	Endpoint string
	// Key is the shared secret used to sign and verify submissions. Never the
	// peer's API key and never a user credential: it authorises THIS pairing and
	// nothing else, so revoking the peer revokes exactly this relationship.
	Key []byte

	// PublishTo is whether this instance sends its commons to the peer. False
	// for a peer you consume from only.
	PublishTo bool

	// ConsumeFrom is whether this instance reads the peer's commons. False for
	// a peer you publish to only.
	ConsumeFrom bool
}

// Errors a federation call can return. Each is a sentinel so a caller can tell a
// refusal (a policy decision) from a failure (something broke), which is the
// distinction an operator needs when a sync stops.
var (
	// ErrPeerPublishNotEnabled: this instance does not publish to that peer.
	ErrPeerPublishNotEnabled = errors.New("federation: publishing to this peer is not enabled")

	// ErrPeerConsumeNotEnabled: this instance does not consume from that peer.
	ErrPeerConsumeNotEnabled = errors.New("federation: consuming from this peer is not enabled")

	// ErrBadSignature: the submission's signature did not verify. TAMPERING, not
	// a malformed request, and deliberately a distinct error so the audit row
	// can say so.
	ErrBadSignature = errors.New("federation: submission signature does not verify")

	// ErrUnsignedSubmission: a submission arrived with no signature at all.
	// Distinct from ErrBadSignature because "unsigned" and "forged" are
	// different events and an operator investigating a breach needs to know
	// which one happened.
	ErrUnsignedSubmission = errors.New("federation: submission is unsigned")
)

// SignSubmission produces the signature for a payload sent to a peer.
//
// HMAC-SHA256 over the submission id, keyed by the per-peer secret. The
// submission id is already a content hash, so signing the id rather than the
// body means the signature covers the content transitively -- there is no way
// for the body to change while the id stays the same, because the id is derived
// from the body.
//
// Keyed per peer, so a submission to Alice cannot be replayed at Bob: the
// verification key differs and the MAC will not match.
func SignSubmission(submissionID string, key []byte) string {
	mac := hmac.New(sha256.New, key)
	// Domain-separated. Without the prefix, a MAC produced for some other
	// purpose with the same key would verify here, and the key is a per-peer
	// secret that other code in the instance also holds.
	mac.Write([]byte("stashforge/federation/v1"))
	mac.Write([]byte{0})
	mac.Write([]byte(submissionID))
	return hex.EncodeToString(mac.Sum(nil))
}

// Submission is a payload in transit, with its signature.
type Submission struct {
	Payload   Payload `json:"payload"`
	Signature string  `json:"signature"`
	PeerKeyID string  `json:"peer_key_id,omitempty"`
}

// SignedSubmission pairs a payload with the signature and the peer it is for.
type SignedSubmission struct {
	Payload   Payload
	Signature string
	PeerID    int64
}

// MakeSubmission signs a payload for a peer.
//
// Refuses when the peer is not enabled for publishing. The check is HERE, in the
// function that produces the signed artefact, rather than in each caller: a
// signed submission is the thing that leaves the host, and a submission that
// exists is a submission that can be sent.
func MakeSubmission(p Payload, peer Peer) (SignedSubmission, error) {
	if !peer.PublishTo {
		return SignedSubmission{}, fmt.Errorf("%w: peer %d (%s)", ErrPeerPublishNotEnabled, peer.ID, peer.Name)
	}
	if len(peer.Key) == 0 {
		// A peer with no key cannot sign, and an unsigned submission is refused
		// on the receiving end anyway -- so failing here gives the operator a
		// useful message instead of a rejection from a peer they cannot debug.
		return SignedSubmission{}, fmt.Errorf("federation: peer %d (%s) has no key configured", peer.ID, peer.Name)
	}
	if p.SubmissionID == "" {
		return SignedSubmission{}, fmt.Errorf("federation: payload has no submission id to sign")
	}
	return SignedSubmission{
		Payload:   p,
		Signature: SignSubmission(p.SubmissionID, peer.Key),
		PeerID:    peer.ID,
	}, nil
}

// VerifySubmission checks an inbound submission against a peer's key.
//
// The three failure modes are kept apart on purpose, because they mean different
// things to whoever is looking at the log: unsigned (misconfigured sender),
// bad signature (tampering or a wrong key), and a payload whose id does not
// match its own content (a sender that signed one thing and sent another).
func VerifySubmission(p Payload, signature string, peer Peer) error {
	if !peer.ConsumeFrom {
		return fmt.Errorf("%w: peer %d (%s)", ErrPeerConsumeNotEnabled, peer.ID, peer.Name)
	}
	if signature == "" {
		return ErrUnsignedSubmission
	}
	if len(peer.Key) == 0 {
		return fmt.Errorf("federation: peer %d (%s) has no key configured, so nothing can be verified", peer.ID, peer.Name)
	}

	want := SignSubmission(p.SubmissionID, peer.Key)
	// Constant-time. A byte-by-byte compare leaks the position of the first
	// differing byte, which is enough to forge a MAC one byte at a time.
	if !hmac.Equal([]byte(want), []byte(signature)) {
		return fmt.Errorf("%w: peer %d (%s)", ErrBadSignature, peer.ID, peer.Name)
	}
	return nil
}

// VerifySubmissionContent re-derives the submission id from the payload's own
// entries and checks it against the id that was signed.
//
// This is the check that makes the signature mean something. Signing the id is
// only safe because the id is derived from the content -- so the content has to
// be re-hashed here, or a peer could sign an id for an EMPTY payload and then
// send a full one, and the signature would still verify. Without this, Verify is
// a MAC check and nothing more.
//
// libraryID is a PARAMETER, not something read out of the payload, and that is
// deliberate. The id hashes the library id, so verification needs it -- and the
// only trustworthy source for "which library is this submission for" is the
// receiver's own request context, not a field the sender chose. Reading it from
// the payload would make the check circular: the sender would supply the very
// value the hash is supposed to bind.
//
// It also catches a peer whose exporter is buggy rather than malicious, which
// is the likelier cause and the one that would otherwise be invisible.
func VerifySubmissionContent(p Payload, libraryID int64) error {
	derived := SubmissionID(p.Instance, libraryID, p.Entries)
	if derived != p.SubmissionID {
		return fmt.Errorf("federation: submission id %s does not match its content (derived %s); "+
			"the sender signed one payload and sent another",
			short(p.SubmissionID), short(derived))
	}
	return nil
}

func short(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12] + "..."
}

// PeerRegistry holds the configured peers.
//
// An interface so the federation logic is testable without a database, and so
// the set of operations is visible: adding a read the send path needs becomes a
// compile error rather than a silent nil.
type PeerRegistry interface {
	// PeersForPublish returns peers this instance may send to.
	PeersForPublish(ctx context.Context) ([]Peer, error)
	// PeerByID returns one peer, for verification.
	PeerByID(ctx context.Context, id int64) (Peer, bool, error)
	// PeersForConsume returns peers this instance may read from.
	PeersForConsume(ctx context.Context) ([]Peer, error)
}

// FedPublisher fans a payload out to every peer enabled for publishing.
//
// The order is sorted by peer id so a run is reproducible; and a failure to one
// peer does not stop the others, because "Alice is down" should not prevent
// publishing to Bob. Every failure is collected and returned together, because
// an operator needs to know that two of three peers were missed, not that
// "something went wrong".
type FedPublisher struct {
	Registry PeerRegistry
	// Send delivers one signed submission. Returning an error is recorded and
	// does not abort the run.
	Send func(ctx context.Context, peer Peer, s SignedSubmission) error
}

type FedResult struct {
	Sent   []int64
	Failed map[int64]error
	// Skipped lists peers that were not enabled for publishing. Counted rather
	// than ignored: "published to 1 of 5 peers" is a fact an operator wants.
	Skipped int
}

func (f *FedPublisher) PublishToPeers(ctx context.Context, p Payload) (FedResult, error) {
	peers, err := f.Registry.PeersForPublish(ctx)
	if err != nil {
		return FedResult{}, fmt.Errorf("listing peers for publish: %w", err)
	}

	sort.Slice(peers, func(i, j int) bool { return peers[i].ID < peers[j].ID })

	res := FedResult{Failed: map[int64]error{}}
	for _, peer := range peers {
		if !peer.PublishTo {
			res.Skipped++
			continue
		}
		sub, err := MakeSubmission(p, peer)
		if err != nil {
			// A peer that cannot be signed to is a failure for that peer, not
			// for the run. Recorded with its reason so a missing key is
			// distinguishable from a network error.
			res.Failed[peer.ID] = err
			continue
		}
		if err := f.Send(ctx, peer, sub); err != nil {
			res.Failed[peer.ID] = err
			continue
		}
		res.Sent = append(res.Sent, peer.ID)
	}
	return res, nil
}

// FedReceiver accepts submissions from peers enabled for consuming.
type FedReceiver struct {
	Registry PeerRegistry
	// Accept stores a verified payload. Called ONLY after both the signature and
	// the content-address check have passed.
	Accept func(ctx context.Context, peer Peer, p Payload) error
	// Audit records a rejection, so a refused or forged submission leaves a
	// trace. A rejection that is invisible is an attack that succeeded twice.
	Audit func(ctx context.Context, peer Peer, reason string, err error) error
}

// Receive verifies and accepts one inbound submission.
//
// VERIFICATION IS COMPLETE BEFORE ACCEPTANCE. Both checks run, and both must
// pass, before Accept is called: the signature (this is from who it claims) and
// the content address (this is what it claims). A receiver that accepted on
// signature alone would be trusting a sender's own claim about its own payload.
func (r *FedReceiver) Receive(ctx context.Context, peerID, libraryID int64, p Payload, signature string) error {
	peer, found, err := r.Registry.PeerByID(ctx, peerID)
	if err != nil {
		return fmt.Errorf("looking up peer %d: %w", peerID, err)
	}
	if !found {
		// An unknown peer is refused BEFORE any signature work, and reported as
		// "not enabled" rather than "bad signature": a peer that is not
		// configured is a configuration question, not an attack, and conflating
		// the two sends an operator hunting for a breach that did not happen.
		return fmt.Errorf("%w: peer %d is not configured", ErrPeerConsumeNotEnabled, peerID)
	}

	if err := VerifySubmission(p, signature, peer); err != nil {
		r.audit(ctx, peer, "federation_rejected_signature", err)
		return err
	}
	if err := VerifySubmissionContent(p, libraryID); err != nil {
		r.audit(ctx, peer, "federation_rejected_content_mismatch", err)
		return err
	}
	if r.Accept == nil {
		return fmt.Errorf("federation: receiver has no Accept function; refusing an unverified-by-storage submission")
	}
	return r.Accept(ctx, peer, p)
}

func (r *FedReceiver) audit(ctx context.Context, peer Peer, reason string, cause error) {
	if r.Audit == nil {
		return
	}
	_ = r.Audit(ctx, peer, reason, cause)
}

// CommonsRead is the authenticated read endpoint over a payload (§6.3).
//
// "Authenticated" and "not disclosed" are the same requirement here, which is
// why the unauthenticated case is 404 and not 403: a 403 confirms the resource
// exists, and for a private library that confirmation is itself a disclosure
// (§6.4).
type CommonsRead struct {
	// Lookup finds a payload by instance and library. A miss is a miss, not an
	// error, and the caller must not be able to tell the two apart either.
	Lookup func(ctx context.Context, instance string, libraryID int64) (Payload, bool, error)
	// Authorize reports whether this caller may read that payload.
	Authorize func(ctx context.Context, callerID int64, instance string, libraryID int64) (bool, error)
}

// ErrCommonsNotFound is the 404. Returned for BOTH "no such payload" and "you
// are not allowed", and that conflation is the point.
var ErrCommonsNotFound = errors.New("commons: not found")

// Read returns the payload, or ErrCommonsNotFound.
//
// The order is: authorize BEFORE lookup's result is disclosed. Both failures
// return the same error and the same information, so a caller cannot probe for
// which libraries exist by watching which requests 404 differently.
func (c *CommonsRead) Read(ctx context.Context, callerID int64, instance string, libraryID int64) (Payload, error) {
	allowed := false
	if c.Authorize != nil {
		var err error
		allowed, err = c.Authorize(ctx, callerID, instance, libraryID)
		if err != nil {
			// An authorization failure is treated as "not found" rather than
			// surfaced, because a distinct error here tells the caller their
			// credentials are valid but their grant is not, which is more
			// information than the 404 is meant to withhold.
			return Payload{}, ErrCommonsNotFound
		}
	}
	if !allowed {
		return Payload{}, ErrCommonsNotFound
	}

	if c.Lookup == nil {
		return Payload{}, ErrCommonsNotFound
	}
	p, found, err := c.Lookup(ctx, instance, libraryID)
	if err != nil || !found {
		// Same error for a lookup failure and a genuine miss. A database error
		// that surfaces as 503 tells an unauthenticated prober that this
		// instance has a database problem, which is a small amount of free
		// information about the host.
		return Payload{}, ErrCommonsNotFound
	}
	return p, nil
}

// FederationSummary describes the configured peers, for a settings screen.
//
// A read model rather than a computed string at the call site, so the "what
// exactly is shared with whom" question has ONE answer in the tree. Consent that
// can only be understood by reading the code is not consent.
type FederationSummary struct {
	PeerID   int64
	Name     string
	Endpoint string
	// Direction reads "outbound", "inbound", "both" or "none" -- a human
	// description, because "PublishTo: true, ConsumeFrom: false" is not an
	// answer an operator can act on.
	Direction string
	// KeyConfigured is reported separately from Direction: a peer marked
	// outbound with no key is a misconfiguration, and hiding that behind a
	// cheerful "outbound" would be the kind of thing that is discovered during
	// an incident.
	KeyConfigured bool
}

func SummarizePeers(peers []Peer) []FederationSummary {
	out := make([]FederationSummary, 0, len(peers))
	for _, p := range peers {
		var dir string
		switch {
		case p.PublishTo && p.ConsumeFrom:
			dir = "both"
		case p.PublishTo:
			dir = "outbound"
		case p.ConsumeFrom:
			dir = "inbound"
		default:
			dir = "none"
		}
		out = append(out, FederationSummary{
			PeerID:        p.ID,
			Name:          p.Name,
			Endpoint:      p.Endpoint,
			Direction:     dir,
			KeyConfigured: len(p.Key) > 0,
		})
	}
	return out
}

// RedactPeerKey is what a settings screen or an API response may show.
//
// A key is shown as present/absent and never in any part, because a peer key
// that has been rendered once into a GraphQL response or a log line is a peer key
// that has to be rotated.
func RedactPeerKey(p Peer) string {
	if len(p.Key) == 0 {
		return ""
	}
	return fmt.Sprintf("[%d bytes, configured]", len(p.Key))
}
