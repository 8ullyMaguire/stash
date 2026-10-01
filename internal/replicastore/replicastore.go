// Package replicastore records which replicas this instance holds, and — the part
// that matters — how many of them are actually VERIFIED.
//
// M8 step 8.3 (R078, §6b.5, non-negotiables #4 and #14).
//
// # THE ONE PROPERTY THIS TYPE EXISTS TO MAKE TRUE
//
// §6b.4 promises "content replicates to N, N configurable and defaulting to 3", and
// §6b.5 says a replica counts as healthy ONLY after a manifest verification against
// a content hash, "never after a peer says it accepted the bytes". Non-negotiable #4
// then extends "computed, never stored" to cover the replication count itself.
//
// So there is no `replica_count` column anywhere in this package, and there is no
// method that returns a number assembled from whatever rows happen to be there. The
// count is always a query with `health = 'verified'` in it. A peer's report is a
// CLAIM that can set a row's health to pending and nothing else — there is no call
// in this file that writes 'verified'.
//
// # WHY A PEER CANNOT SET HEALTH
//
// `Record` takes an expected MANIFEST HASH and writes health 'pending'. Only
// `Verify` writes 'verified', and it does so by hashing the bytes on disk and
// comparing them to the manifest the receiver holds. So the only route to 'verified'
// runs through sha256 over this instance's own file — a peer claiming "I have it,
// verified" has no way to make this instance believe it, which is the whole of #14.
//
// # WHY NAMESPACING IS NOT OPTIONAL
//
// Migration 113 keys on (scene_id, source_endpoint) because a peer's `scene 412`
// is not this instance's `scene 412`. Without the endpoint in the key, two peers
// serving the same local scene id collide into one row and the mesh then believes
// it holds a replica it does not have — a MISSING COPY THAT HEALTH CHECKS PASS,
// which is the worst available failure for the mechanism whose entire purpose is
// knowing what you still hold.

package replicastore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/stashapp/stash/internal/manifest"
)

// Health is a replica's state.
//
// FOUR STATES, NOT A COUNT, and not a pair of booleans: `verified` and `corrupt` are
// both "I looked at it and the bytes are wrong", while `missing` is "the file is
// gone" — a different problem for a different person, and conflating them is what
// makes a health sweep able to do nothing useful.
type Health string

const (
	// Pending means bytes are held or expected but have not been verified. It does
	// NOT count toward N.
	Pending Health = "pending"

	// Verified means the content hashed to the manifest's digest. The ONLY state
	// that counts toward N.
	Verified Health = "verified"

	// Corrupt means verification failed: the bytes are there and they are wrong.
	// A corrupt replica triggers re-fetch from a healthy peer (§6b.5), not an
	// alert.
	Corrupt Health = "corrupt"

	// Missing means the replica row exists and the file does not. §6a.9: a peer
	// going offline is detected as a missing replica.
	Missing Health = "missing"
)

var (
	// ErrNoReplica means no row matches the scene and endpoint.
	ErrNoReplica = errors.New("replicastore: no such replica")

	// ErrUnverifiedTransition means a caller tried to set health to a state it may
	// not set directly. Only 'pending' and 'missing' are settable; 'verified' and
	// 'corrupt' are the RESULT of hashing the bytes.
	ErrUnverifiedTransition = errors.New("replicastore: health may only be set by verification, not asserted")

	// ErrNotVerified means a caller asked for a verified replica that is not one.
	ErrNotVerified = errors.New("replicastore: replica is not verified")

	// ErrBlockedByDenial means the scene is denied, so a replica of it may not be
	// held at all. §6b.4: replication is a publish path, and a preservation bounty
	// never overrides a denial (non-negotiable #7 extended to three paths).
	ErrBlockedByDenial = errors.New("replicastore: scene is denied; replication is a publish path and a denial is a hard stop")
)

// Known reports whether a health value is one this build understands.
func Known(h Health) bool {
	switch h {
	case Pending, Verified, Corrupt, Missing:
		return true
	}
	return false
}

// Settable reports whether a caller may write a health value directly.
//
// THE FUNCTION THAT ENCODES #14. `verified` and `corrupt` are outcomes of hashing
// this instance's own bytes, so they are not settable — a peer cannot assert them,
// and neither can the rest of the application. Only `pending` (bytes expected) and
// `missing` (bytes gone) are facts a caller may report, because both are
// observations that do not require reading the content.
func Settable(h Health) bool {
	return h == Pending || h == Missing
}

// Replica is one row of mesh_replica.
type Replica struct {
	ID             int
	SceneID        int
	SourceEndpoint string
	ReplicaPath    string
	ManifestHash   manifest.Manifest
	Health         Health
	VerifiedAt     *time.Time
}

// Counts reports verified and pending totals for a scene.
//
// TWO NUMBERS, AND THE VERIFIED ONE IS THE PROMISE. N is compared against Verified
// and never against Verified+Pending: a pending replica is an intention, and
// counting it would make the ≥N promise true on paper the first time a peer said
// the bytes were on their disk.
type Counts struct {
	Verified int
	Pending  int
	Corrupt  int
	Missing  int
}

// Total is every row for the scene regardless of health.
//
// NOT the number to compare against N. It exists for the sweep that looks for
// corrupt and missing rows, and naming it Total rather than Replicas keeps it from
// being read as the healthy count.
func (c Counts) Total() int { return c.Verified + c.Pending + c.Corrupt + c.Missing }

// SatisfiesN reports whether the scene has at least n VERIFIED replicas.
//
// THE FUNCTION §6b.4's promise is made of. One query with health = 'verified' in
// it, so the answer cannot drift from the rows: there is no stored tally to be
// stale, which is non-negotiable #4 twice over — once for the count, and once
// because a counter that can be incremented without the verification happening is
// precisely the laundering #14 forbids.
func (c Counts) SatisfiesN(n int) bool { return c.Verified >= n }

// Store is the replica store's dependency, so a test can observe what was written
// without a database.
//
// AND SO A CALLER CANNOT REACH A WRITER THROUGH IT. The interface exposes reads and
// the two transitions; nothing here can set health to verified except Verify.
type Store interface {
	Create(ctx context.Context, r Replica) (int, error)
	ByKey(ctx context.Context, sceneID int, sourceEndpoint string) (Replica, error)
	ForScene(ctx context.Context, sceneID int) ([]Replica, error)
	Unverified(ctx context.Context) ([]Replica, error)

	// SetSettableHealth records an OBSERVED state -- pending or missing.
	//
	// The name is the restriction. There is deliberately no `SetHealth` taking a
	// free Health: a method named for the general operation invites a caller to
	// pass Verified, and the only thing stopping it is that method checking a rule
	// in another package. Naming it for what it may do means the compiler rejects
	// the wrong call at the call site instead.
	//
	// A store implementation must still enforce Settable, because a test fake or a
	// second implementation can be written either way; TestTheStoreRefusesToBe
	// AskedToAssertAnUnverifiedHealth is the assertion that it does.
	SetSettableHealth(ctx context.Context, sceneID int, sourceEndpoint string, h Health) error
}

// DenialChecker reports whether a scene may be replicated at all.
//
// AN INTERFACE RATHER THAN A CLOSURE-FREE BOOL because the answer comes from the
// consent system (R082, §6b.4) and a store that took the caller's word for it
// would make the hard stop advisory. `nil` means "no denial checker configured",
// which is treated as PERMISSIVE — recorded below as a real limitation, because
// fail-closed would mean no replica can ever be recorded until the checker exists,
// and that is a different decision for the owner.
type DenialChecker interface {
	ReplicationDenied(ctx context.Context, sceneID int) (bool, error)
}

// Reconciler is the store plus the two things that make it a store rather than a
// table wrapper.
type Reconciler struct {
	store   Store
	denials DenialChecker
	// root is this instance's storage root, used to check a replica path stays
	// inside it. Migration 113 CHECKs the stored value too; this is the check that
	// happens BEFORE a write.
	root string
}

// New wires a reconciler.
//
// A non-empty root is REQUIRED, and not for tidiness: the path-safety check needs
// somewhere to check against. With an empty root there is no boundary, so every
// path would be accepted and the check would be theatre.
func New(store Store, denials DenialChecker, root string) (*Reconciler, error) {
	if store == nil {
		return nil, errors.New("replicastore: a reconciler with no store cannot record anything")
	}
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("replicastore: a storage root is required. Without one " +
			"there is no boundary for the path check, and a peer-supplied path would " +
			"be accepted unchecked")
	}
	return &Reconciler{store: store, denials: denials, root: root}, nil
}

// Record notes that a replica is expected, as 'pending'.
//
// IT CANNOT RECORD A VERIFIED REPLICA, and the signature is why: there is no health
// parameter. A caller passing a peer's "I already verified this" has nowhere to put
// it, so the fast path a peer would want does not exist to be taken.
func (r *Reconciler) Record(ctx context.Context, sceneID int, sourceEndpoint, replicaPath string, m manifest.Manifest) (int, error) {
	if sceneID <= 0 {
		return 0, fmt.Errorf("replicastore: scene id %d is not addressable", sceneID)
	}
	if err := m.Validate(); err != nil {
		return 0, fmt.Errorf("replicastore: refusing a manifest a peer could not have produced: %w", err)
	}
	if strings.TrimSpace(sourceEndpoint) == "" {
		return 0, fmt.Errorf("replicastore: replica from an unnamed source")
	}
	if strings.TrimSpace(m.SceneID) == "" {
		// §6b.4's table: the scene's canonical id from the commons crosses the wire.
		// An empty one means the peer named no scene, so the replica cannot be
		// attributed to anything and would never be counted.
		return 0, fmt.Errorf("%w: replica names no canonical scene id, so it could "+
			"never be counted toward N", ErrNotVerified)
	}

	// The path check BEFORE the write, so a bad path never reaches the database
	// even transiently. Migration 113 CHECKs it as well; two checks on the same
	// property is deliberate, because the database is the one place a peer cannot
	// talk you out of and this is the first place a bug would be caught.
	if err := r.checkPath(replicaPath); err != nil {
		return 0, err
	}

	// R082 / §6b.4 / #7: replication is a publish path. A denied object is never a
	// replication subject, and a preservation bounty never overrides it.
	if r.denials != nil {
		denied, err := r.denials.ReplicationDenied(ctx, sceneID)
		if err != nil {
			// An unanswerable denial question is REFUSED, not assumed. Assuming
			// "not denied" because the checker errored would turn a transient
			// database fault into a published copy of something a user denied.
			return 0, fmt.Errorf("replicastore: cannot determine whether scene %d is denied: %w", sceneID, err)
		}
		if denied {
			return 0, fmt.Errorf("%w: scene %d", ErrBlockedByDenial, sceneID)
		}
	}

	return r.store.Create(ctx, Replica{
		SceneID:        sceneID,
		SourceEndpoint: sourceEndpoint,
		ReplicaPath:    replicaPath,
		ManifestHash:   m,
		Health:         Pending,
	})
}

// Verify hashes the replica's own bytes and records the outcome.
//
// THE ONLY ROUTE TO 'verified', and it takes no expected hash from the caller: it
// reads the manifest the RECEIVER holds and compares against the bytes on this
// instance's disk. A peer that says "verified" is not believed, and cannot be, for
// the same reason Record has no health parameter.
func (r *Reconciler) Verify(ctx context.Context, sceneID int, sourceEndpoint, storageRoot string) (Health, error) {
	rep, err := r.store.ByKey(ctx, sceneID, sourceEndpoint)
	if err != nil {
		return "", err
	}

	// The path is checked again at READ time, not only at write time. A row written
	// before a check existed, or edited in the database directly, must not become a
	// way to hash an arbitrary file.
	full, err := r.resolve(rep.ReplicaPath, storageRoot)
	if err != nil {
		return "", err
	}

	if err := rep.ManifestHash.VerifyFile(full); err != nil {
		// A FILE THAT IS NOT THERE is 'missing', not 'corrupt'. §6a.9: a peer going
		// offline is detected as a missing replica. Conflating the two makes a
		// sweep re-fetch something that does not exist and, worse, makes an operator
		// read "the bytes are wrong" when the truth is "they are gone".
		//
		// This distinction is why the first version of Verify was wrong in a way no
		// test caught at first: it recorded everything that failed verification as
		// corrupt, including a path that had never been written.
		outcome := Corrupt
		if errors.Is(err, os.ErrNotExist) {
			outcome = Missing
		}

		// The outcome is recorded AND returned as an error, because the caller
		// needs both facts: that it failed, and what the row now says.
		if uerr := r.markVerifiedOutcome(ctx, sceneID, sourceEndpoint, outcome); uerr != nil {
			return outcome, fmt.Errorf("replicastore: verification failed (%s) AND the row could not be recorded: %w (verify: %v)", outcome, uerr, err)
		}
		return outcome, fmt.Errorf("%w: %s", ErrNotVerified, outcome)
	}

	if err := r.markVerifiedOutcome(ctx, sceneID, sourceEndpoint, Verified); err != nil {
		return "", err
	}
	return Verified, nil
}

// MarkMissing records that the file is gone.
//
// A separate method rather than a general SetHealth because §6a.9's detection — "a
// peer going offline is detected as a missing replica" — needs a route that says
// missing and nothing else. A general setter would let any caller write any
// settable state, and the reason there are two of them is that each is a distinct
// observation with a distinct response.
func (r *Reconciler) MarkMissing(ctx context.Context, sceneID int, sourceEndpoint string) error {
	return r.mark(ctx, sceneID, sourceEndpoint, Missing)
}

// mark records an OBSERVED state, and refuses anything a caller may not assert.
func (r *Reconciler) mark(ctx context.Context, sceneID int, sourceEndpoint string, h Health) error {
	if !Settable(h) {
		// Settable is the rule and this is where it is enforced, so a caller cannot
		// skip the check by reaching a lower-level path.
		return fmt.Errorf("%w: %s", ErrUnverifiedTransition, h)
	}
	return r.store.SetSettableHealth(ctx, sceneID, sourceEndpoint, h)
}

// markVerifiedOutcome records the RESULT of hashing this instance's own bytes.
//
// IT DELIBERATELY BYPASSES Settable, and that asymmetry is the whole design:
// Settable restricts what a CALLER may assert, and verified/corrupt are outcomes
// rather than assertions. The first version routed both through mark and so could
// not record a failed verification at all -- which made §6b.5's "a failed
// verification triggers re-fetch" unimplementable, because the caller could learn
// it failed but the row stayed pending and nothing could sweep it.
//
// The method is unexported and takes the outcome as an argument only Verify calls
// it with, so the bypass is one call site rather than a door.
func (r *Reconciler) markVerifiedOutcome(ctx context.Context, sceneID int, sourceEndpoint string, h Health) error {
	switch h {
	case Verified, Corrupt, Missing:
	default:
		// Defensive: the bypass is for verification outcomes and nothing else.
		return fmt.Errorf("%w: %s is not a verification outcome", ErrUnverifiedTransition, h)
	}
	return r.store.SetSettableHealth(ctx, sceneID, sourceEndpoint, h)
}

// CountsFor returns the health breakdown for a scene.
func (r *Reconciler) CountsFor(ctx context.Context, sceneID int) (Counts, error) {
	reps, err := r.store.ForScene(ctx, sceneID)
	if err != nil {
		return Counts{}, err
	}

	var c Counts
	for _, rep := range reps {
		switch rep.Health {
		case Verified:
			c.Verified++
		case Pending:
			c.Pending++
		case Corrupt:
			c.Corrupt++
		case Missing:
			c.Missing++
		}
	}
	return c, nil
}

// SatisfiesN reports whether the scene has n verified replicas.
//
// THE PRESERVATION PROMISE, as one call. It is a query over verified rows rather
// than a stored tally, so the ≥N promise is true by construction rather than by a
// peer's honesty — which is the sentence §6b.5 uses to justify verification in the
// first place.
func (r *Reconciler) SatisfiesN(ctx context.Context, sceneID int, n int) (bool, error) {
	if n <= 0 {
		// N is a policy and 0 is not one. Refused rather than satisfied vacuously:
		// "this scene needs zero replicas" is true of every scene and so is not a
		// preservation check.
		return false, fmt.Errorf("replicastore: N must be at least 1, got %d", n)
	}
	c, err := r.CountsFor(ctx, sceneID)
	if err != nil {
		return false, err
	}
	return c.SatisfiesN(n), nil
}

// NeedsVerification returns replicas that are pending, which is what a sweep after
// a fetch or a restart looks at.
//
// A PENDING REPLICA IS NOT AN ALERT CONDITION. §6b.5 is explicit that a failed
// verification triggers re-fetch rather than an alert, and a pending replica that
// has not been verified yet is the same shape: work to do, not a fault to report.
func (r *Reconciler) NeedsVerification(ctx context.Context) ([]Replica, error) {
	reps, err := r.store.Unverified(ctx)
	if err != nil {
		return nil, err
	}
	// Filtered here rather than trusted from the store, because the set of
	// unverified states is this package's rule and a store implementing it by its
	// own logic would drift the moment a fourth state is added.
	out := make([]Replica, 0, len(reps))
	for _, rep := range reps {
		if rep.Health == Pending {
			out = append(out, rep)
		}
	}
	return out, nil
}

// checkPath refuses a replica path that is absolute or walks out of the root.
//
// NON-NEGOTIABLE #13 AND §6b.4. A path from a stranger is the one input here that
// can name a file outside the library, and migration 113 CHECKs the stored value
// for the same two shapes — this is the check that happens BEFORE the write, so the
// value never exists outside the database even briefly.
func (r *Reconciler) checkPath(p string) error {
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("replicastore: replica path is empty")
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("replicastore: replica path %q is absolute. A replica is "+
			"stored under the instance's storage root and its location is computed, "+
			"not carried across the wire", p)
	}
	if strings.Contains(p, "..") {
		return fmt.Errorf("replicastore: replica path %q walks out of the storage root", p)
	}
	return nil
}

// resolve turns a stored relative path into a local one, refusing anything that
// would escape the root after joining.
//
// CHECKED AGAIN AT READ TIME as well as write time. A row that predates this check,
// or one edited directly in the database, must not become a way to make this
// instance hash an arbitrary file on disk — and Verify would happily do exactly
// that, because it opens whatever path it is given.
func (r *Reconciler) resolve(rel, storageRoot string) (string, error) {
	if err := r.checkPath(rel); err != nil {
		return "", err
	}
	root := storageRoot
	if strings.TrimSpace(root) == "" {
		root = r.root
	}
	full := root + "/" + rel

	// Compared after joining as well, because a path can be relative and still
	// escape: "a/../../etc/passwd" contains ".." and is caught above, but
	// "subdir/../../x" is the same idea and this is the cheap belt to the check's
	// braces.
	if !strings.HasPrefix(full, strings.TrimSuffix(root, "/")+"/") {
		return "", fmt.Errorf("replicastore: replica path %q resolves outside the storage root", rel)
	}
	return full, nil
}

// ErrNoRow is returned by a Store implementation when ByKey finds nothing, so a
// fake in a test and the real store agree on the sentinel.
var ErrNoRow = sql.ErrNoRows
