package cluster

import (
	"fmt"
	"sort"
)

// Step 2.4b.4: the consolidate pass. Commons §7.1 step 4 and §7.2.
//
// Merges clusters that are close enough to be one person, and records every
// merge it performs.
//
// # Why a record at all
//
// A merge is reversible and a split is a correction. Neither is answerable from
// the current state alone: "why are these two people the same person" and "why
// was this face taken out" are the questions a user asks second. The record is
// the same reasoning as `collab_audit` in M2 and migration 92's refusal to
// store a tally -- a decision's justification is a record, not a derivation.
//
// # Why a refused merge leaves NOTHING
//
// The tempting implementation writes the record, discovers the guard refuses
// the join, and rolls the membership back. The record survives the rollback.
// The review queue then shows a merge that did not happen, and the user spends
// time on a phantom -- which is precisely how a queue stops being trusted.
//
// # Why the loser is marked and not deleted
//
// Migration 98 has no ON DELETE CASCADE on loser_id, and that is why a cluster
// named by a merge record cannot be hard-deleted. The state 'merged' is the
// supported way to retire one, and it is also what makes the pass idempotent:
// a second pass excludes anything already marked.

// MergeRecord is one merge, as written to person_cluster_merges.
//
// The field names mirror the migration so a reviewer can read one against the
// other. It is not a struct scan target -- the store row is -- because the
// cluster package has no business knowing column names.
type MergeRecord struct {
	WinnerID  int64
	LoserID   int64
	Kind      string
	Reason    string
	DecidedBy int64
}

// ConsolidateResult reports what a pass did.
//
// Merged and len(Records) are deliberately both present and required to agree.
// A caller that surfaces the count without checking the records can put a
// phantom in the queue, and the disagreement is the only evidence of it.
type ConsolidateResult struct {
	Merged  int
	Records []MergeRecord

	// Skipped counts clusters the pass declined to consider -- already merged,
	// or too large for the guard. It is NOT an error count. Kept separate so
	// "nothing to merge" can never be reported as something went wrong.
	Skipped int

	// Refused counts candidate pairs the over-merge guard rejected. Distinct
	// from Skipped because a refusal is a real finding: the pair was close
	// enough to consider and the guard said the cluster is already two people.
	Refused int
}

func (r ConsolidateResult) Summary() string {
	return fmt.Sprintf("merged %d cluster(s), refused %d candidate pair(s), skipped %d",
		r.Merged, r.Refused, r.Skipped)
}

// cluster state values, matching the CHECK in migration 96.
const (
	stateSingleton = "singleton"
	stateSettled   = "settled"
	stateAmbiguous = "ambiguous"
	stateMerged    = "merged"
)

// consolidate merges clusters closer than mergeThreshold.
//
// reason is written to every record it produces. An empty reason is refused
// below rather than stored, because a merge with no stated reason is a
// moderation queue item nobody can rule on.
func (s *stage) consolidate(mergeThreshold float64, reason string) ConsolidateResult {
	return s.consolidateBounded(mergeThreshold, reason, -1)
}

// MaxMergesPerPass bounds one pass.
//
// It exists so "abandoned partway" is a reachable state rather than a thought
// experiment, and the state-ordering guarantee below is therefore testable. A
// batch job that is cancelled, a user who interrupts a long pass, and a guard
// that refuses the tenth pair all leave the same question: has anything been
// told to the queue yet?
const MaxMergesPerPass = 100

// consolidateBounded performs a merge pass, stopping after at most limit merges.
// A limit of -1 means MaxMergesPerPass.
//
// # The ordering guarantee
//
// Every decision is made first and every state transition is written after. An
// abandoned pass therefore leaves the state it found -- not a partial set of
// 'settled' clusters that were never actually reconciled.
//
// The mutation that writes state inside the loop produces an identical final
// state on a pass that completes, which is why asserting the final state cannot
// see it. The difference is only observable when the pass stops early, which is
// what the limit makes possible.
func (s *stage) consolidateBounded(mergeThreshold float64, reason string, limit int) ConsolidateResult {
	var res ConsolidateResult

	if limit < 0 {
		limit = MaxMergesPerPass
	}

	// A record with no reason is not storable (migration 98 makes reason NOT
	// NULL) and not useful. Refusing here means the caller finds out before
	// any state has changed, rather than after a half-applied pass.
	if reason == "" {
		return res
	}

	ids := s.liveClusterIDs()
	res.Skipped = len(s.clusters) - len(ids)

	// Deterministic order. Go randomises map iteration, so without this the
	// winner of a merge depends on chance -- and which cluster survives a merge
	// decides its id, which a user sees and a reversal points at.
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	// One pass, not a fixpoint loop. A fixpoint would merge further than any
	// single pairwise decision justified -- which is the over-merge the guard
	// exists to prevent, reintroduced one layer up. Pairs are evaluated against
	// the state as it was when the pass began.
	absorbed := map[int64]bool{}

	for i := 0; i < len(ids) && res.Merged < limit; i++ {
		winner := ids[i]
		if absorbed[winner] {
			continue
		}
		for j := i + 1; j < len(ids); j++ {
			loser := ids[j]
			if absorbed[loser] {
				continue
			}

			// A pair whose centres cannot be compared is SKIPPED, and counted
			// separately from a refusal.
			//
			// Skipping rather than failing: one corrupt member in a
			// 40,000-face index must not stop the pass. But not silently either,
			// because "this pass found no mergeable pairs" and "this pass could
			// not measure half of them" are different reports and an operator
			// told the first will go looking for a threshold that is fine.
			//
			// Skipping is also the safe direction: not merging leaves two
			// clusters a human can join, while merging a pair on a guess
			// produces a wrong identity.
			d, err := s.centroidDistance(winner, loser)
			if err != nil {
				res.Skipped++
				continue
			}
			if d > mergeThreshold {
				continue
			}

			if err := s.absorb(winner, loser); err != nil {
				// Refused, and deliberately silent: no record, no state change.
				// A refusal is reported in the count so an operator can see the
				// pass is finding pairs the guard rejects, which is different
				// from a pass that found nothing.
				res.Refused++
				continue
			}
			absorbed[loser] = true
			res.Merged++
			res.Records = append(res.Records, MergeRecord{
				WinnerID: winner,
				LoserID:  loser,
				Kind:     "merge",
				Reason:   reason,
			})
		}
	}

	// State transitions, applied AFTER every decision is made.
	//
	// Writing state inside absorb() would mean a pass that is later abandoned
	// has already told the queue a cluster is settled. Deciding first and
	// writing last keeps the two consistent: either the whole pass happened or
	// none of it did.
	for _, rec := range res.Records {
		s.state[rec.LoserID] = stateMerged
		s.state[rec.WinnerID] = stateSettled
	}

	return res
}

// liveClusterIDs returns the clusters a pass may consider: everything not
// already marked 'merged'.
//
// The exclusion is what makes the pass idempotent. Without it a second pass
// re-merges the same pair, which is the retried-job case migration 98's partial
// unique index is there to catch -- and a database constraint is the wrong place
// to discover that a pass is not idempotent.
func (s *stage) liveClusterIDs() []int64 {
	var ids []int64
	for id := range s.clusters {
		if s.stateOf(id) == stateMerged {
			continue
		}
		if len(s.clusters[id]) == 0 {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

func (s *stage) stateOf(id int64) string {
	if st, ok := s.state[id]; ok {
		return st
	}
	// A cluster with one face is a singleton; with more, it is settled. Derived
	// rather than stored so a freshly seeded cluster does not need a state
	// written before it can be asked about.
	if len(s.clusters[id]) > 1 {
		return stateSettled
	}
	return stateSingleton
}

// centroidDistance is the distance between two clusters' centroids.
//
// The CONSOLIDATE threshold is keyed on this and not on the diameter. That is a
// real distinction and it is the reason both measurements exist: consolidate
// asks "are these two groups the same person", which is a question about where
// they sit, while the over-merge guard asks "has this cluster stopped being one
// person", which is a question about how wide it is. Using the diameter for
// consolidate would refuse merges that the guard considers healthy, and using
// the centroid for the guard would admit the bimodal cluster the twins in
// membership_cosine_test.go exist to catch.
//
// An error means one of the two centres is unmeasurable, and the caller SKIPS
// the pair: a corrupt member must not stop every other pair from being
// considered, and refusing to merge is the safe direction when in doubt.
func (s *stage) centroidDistance(a, b int64) (float64, error) {
	ca, err := s.geom.Centroid(s.clusters[a])
	if err != nil {
		return 0, err
	}
	cb, err := s.geom.Centroid(s.clusters[b])
	if err != nil {
		return 0, err
	}
	return s.geom.Distance(ca, cb)
}

// absorb moves every face from loser into winner, refusing if the over-merge
// guard will not allow it.
//
// The refusal is total: the guard is consulted BEFORE anything moves, so there
// is no partial state to roll back and therefore nothing that could survive a
// rollback by accident. That is the structural reason a refused merge cannot
// leave a trace -- not a discipline about cleaning up, but an ordering.
func (s *stage) absorb(winner, loser int64) error {
	// A cluster cannot be merged into itself.
	//
	// The port removed this. It had been implicit in the scalar guard -- the
	// second line of the j-loop only ever reached a different id -- and moving
	// to Membership made it explicit-by-accident instead: forceCluster
	// pre-loads the winner's members AND their keys, so every one of the loser's
	// (identical) members is then already claimed and addMember returns nil for
	// all of them. The merge "succeeded" and doubled the membership.
	//
	// Migration 98's CHECK catches it in the database, which is the last line of
	// defence rather than the first. The refusal belongs here, where the
	// mistake is made, and the test that pins it is
	// TestAbsorb_RefusesToMergeAClusterIntoItself.
	if winner == loser {
		return fmt.Errorf("cluster %d cannot be merged into itself", winner)
	}

	members := s.clusters[loser]
	if len(members) == 0 {
		return fmt.Errorf("cluster %d has no members to merge", loser)
	}

	// Try every face. A partial absorption would be worse than none: some faces
	// in the winner and some in a cluster marked 'merged' is a state the UI
	// cannot render and a reversal cannot reconstruct.
	prospective := append(append([]Point{}, s.clusters[winner]...), members...)

	// The over-merge guard, run over the PROSPECTIVE membership. Reusing
	// Membership.addMember rather than reimplementing the check is deliberate:
	// the rules that decide whether a cluster may grow are the rules in
	// guard.go, and a second implementation of them in this file is a rule that
	// will drift from the first.
	//
	// It is the PORTED guard, over the stage's own geometry. The previous
	// version constructed the scalar guard inline, which meant the consolidate
	// path checked merges with |a - b| while the assign path checked them with
	// cosine -- the same merge, two arithmetic systems, and a pair that
	// consolidate accepted could be one that assign's own guard would refuse.
	//
	// Only the WINNER's existing members are pre-loaded. Pre-loading the loser's
	// as well and then adding them was the first version, and it failed with
	// "face is already a member of another cluster" -- the guard correctly
	// refuses a face the index says belongs elsewhere, and forceCluster had just
	// claimed it. The prospective membership is the QUESTION being asked; the
	// index's existing claims are the ANSWER being checked against, and mixing
	// the two makes the check answer itself.
	pg := newMembershipWith(s.threshold, s.geom)
	pg.forceCluster(append([]Point{}, s.clusters[winner]...))

	for _, m := range members {
		if err := pg.addMember(1, m); err != nil {
			return err
		}
	}

	// Committed. Update both the guard's bookkeeping and the stage's own.
	for _, m := range members {
		if m.Key != "" {
			if owner, ok := s.keys[m.Key]; ok && owner == loser {
				s.keys[m.Key] = winner
			}
		}
	}
	s.clusters[winner] = prospective

	// The loser's membership is CLEARED, not left in place.
	//
	// The first version left it, and the consequences were all invisible to the
	// idempotence test: a face was a member of two clusters at once, the loser's
	// centroid still existed and could be compared against, and a reversal would
	// have to reconstruct which copy was authoritative. Marking the loser
	// 'merged' does not retire it -- the state is a label, not a lock -- so
	// anything iterating clusters rather than reading their state sees the
	// stale copy.
	//
	// It also hid the S mutation below: with the loser still holding members,
	// liveClusterIDs returned both and a second pass had a candidate pair to
	// consider, which is a different reason for idempotence than the one the
	// test believed it was checking.
	s.clusters[loser] = nil
	return nil
}
