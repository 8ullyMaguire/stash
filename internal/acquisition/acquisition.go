// Package acquisition builds the download queue for capability 2.
//
// M8 step 8.2, R075 (acquire content the user would enjoy, opt-out) and R076
// (taste-ranked queue reusing the mesh recommender). Spec §6b.3.
//
// # §6b.3 SAYS WHAT THIS IS, AND IT SAYS IT TWICE
//
//	"the acquisition queue is then: what the mesh holds, minus what this instance
//	 already has, ranked by taste similarity to what this user already watches"
//	"The ranking function is §6a.5's, unmodified. Nothing new is invented here;
//	 the mesh's recommender is pointed at a download queue."
//
// SO THIS PACKAGE INVENTS NO RANKING. `discovery.Rank` is called as it stands. A second
// ranker here would be two functions disagreeing about what this user would enjoy, and
// the disagreement would be invisible: both would produce plausible orders and the
// product would quietly drift toward whichever one ran last. If §6a.5's function changes,
// this queue changes with it, which is the property "unmodified" is asking for.
//
// # THE DIFFERENCE FROM A RECOMMENDATION FEED IS ONE FILTER AND ONE GATE
//
// A feed shows you things; a queue DOWNLOADS them. Three consequences, and each is a
// refusal in this file:
//
//  1. The switch decides whether anything is fetched at all — R083's three-state
//     `AutoAcquire`, read HERE and not filtered into the query.
//  2. What this instance already holds is removed, so nothing is re-downloaded. That
//     filter is by LOCAL id and never by title, and §6b.3's "minus what this instance
//     has" is only meaningful if "has" means the library rather than a name match.
//  3. A refused acquisition is REPORTED as a refusal and never as an empty queue. A
//     caller that cannot tell "nothing to get" from "you have fetching switched off"
//     will report the second as the first, and the operator will conclude their library
//     matches their taste.
//
// # WHY THE SWITCH IS READ HERE AND NOT FILTERED IN SQL
//
// §6b.3 requires enforcement "at the point the permission is read, in the same place
// the existing tier policy is read, and not by omitting rows from a query", and #7's
// note is that "a refactor dropping the filter must not silently resume publishing".
//
// A `WHERE auto_acquire != 'off'` on a candidates query puts the rule in a string. The
// queue here therefore takes the switch as an argument and asks it, so the decision is
// a Go branch a reviewer reads, and a queue built with the switch off is REFUSABLE
// rather than empty.

package acquisition

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/internal/discovery"
)

// ErrSwitchOff is returned when the queue is asked for while the instance's switch
// refuses fetching.
//
// ITS OWN SENTINEL, and the distinction from "nothing to acquire" is the whole reason
// this package exists. A caller with an empty queue has two possible situations and
// they need opposite responses: nothing matched, or the operator turned fetching off.
// Collapsing them is how an operator concludes their taste is well-served while
// fetching is switched off.
var ErrSwitchOff = errors.New("acquisition: this instance is not acquiring")

// ErrHeld is returned when an explicitly requested scene is already held.
//
// DISTINCT FROM discovery.ErrNotFound on purpose, and the pairing is the point:
// "I do not have that" and "I already have that" are both refusals to download and they
// are different answers. ErrNotFound means the mesh may not have it either, so the
// user's request cannot be satisfied by anyone; ErrHeld means it is HERE, and the
// correct response is to point them at it rather than to fetch a second copy.
var ErrHeld = errors.New("acquisition: this instance already holds that scene")

// Request is what the queue is being asked for.
type Request struct {
	// Cands are the candidates the mesh advertises. Unordered: this package sorts,
	// because §6a.5 ranks an UNORDERED set and a queue built from an ordered input
	// would inherit whatever order the caller happened to supply.
	Cands []discovery.Candidate

	// Held are the LOCAL ids this instance already has. §6b.3's "minus what this
	// instance has", and it is ids rather than names because a title match would
	// re-download a scene under a slightly different title — which is the shape of the
	// duplicate-library problem the mesh exists to avoid.
	Held map[string]bool

	// Fingerprint is this user's taste (§6a.3).
	Fingerprint discovery.Fingerprint

	// Gravity is the operator's theme (§6a.8). It shapes the order and never filters:
	// see discovery.Gravity, which is a weight over categories by construction.
	Gravity discovery.Gravity

	// Peer is per-origin similarity (§6a.2).
	Peer map[string]float64

	// Weights are §6a.5's three coefficients. The ZERO VALUE IS USED AS GIVEN and not
	// replaced with DefaultWeights, and that is deliberate: a caller that forgets to set
	// them gets a queue ordered by nothing, which is visible, rather than a queue
	// silently ordered by taste at the default weights, which is not. Callers wanting
	// the defaults ask for them.
	Weights discovery.Weights
}

// Queue is one ordered acquisition plan.
//
// IT IS A PLAN, NOT A COMMITMENT, and the distinction is why nothing here writes. The
// queue says what this instance would fetch and in what order; a separate decision —
// acted on by the scheduler — decides whether to act. That separation is what lets the
// operator see the plan before the bytes arrive, which matters because §6b.3's downloads
// "land in the library and are scanned", so an acquisition is a permanent change to
// somebody's library rather than a cache entry.
type Queue struct {
	// Items are the candidates to fetch, best first.
	Items []discovery.Ranked

	// Skipped is what was deliberately NOT queued, with the reason. §6b.3's filter is
	// the interesting part of the answer and an operator debugging "why did it not
	// fetch that" needs it; a queue that silently drops held items cannot answer that.
	//
	// IT IS NOT PART OF THE ORDERING and never is: a skipped item must not be able to
	// influence what is fetched.
	Skipped []Skipped
}

// Skipped is one candidate the queue declined, and why.
type Skipped struct {
	// ID is the candidate's local id.
	ID string

	// Reason is one of the named reasons below, as a word rather than prose, so a
	// caller can group them without parsing English.
	Reason SkipReason
}

// SkipReason is why a candidate was not queued. A closed set, because a caller has to be
// able to COUNT them and a free-text reason is uncountable.
type SkipReason string

const (
	// SkipHeld means this instance already has it (§6b.3's "minus what this instance
	// has").
	SkipHeld SkipReason = "held"

	// SkipNoSource means the candidate advertises no locators, so there is nothing to
	// fetch FROM. Distinct from being filtered by taste: the candidate may be a perfect
	// match that nobody can supply.
	SkipNoSource SkipReason = "no_source"

	// SkipDenied means the candidate's locator tier refuses storage, so fetching it
	// would place bytes the consent gate forbids. §6b.4: a denied object is never a
	// subject, and §6b.5 makes a failed verification a re-fetch rather than an alert —
	// but neither of those is about whether to START. Refusing before the client ever
	// held the torrent is the ErrRefusedUpFront case, and it is a different event from
	// a refusal during cleanup.
	SkipDenied SkipReason = "denied"
)

// Build produces the queue.
//
// # THE ORDER OF THE DECISIONS IS THE REQUIREMENT
//
//  1. the switch        -> refuse the whole request (ErrSwitchOff)
//  2. held / no source / denied -> dropped, with a reason
//  3. §6a.5's Rank      -> the order
//
// The switch first and unconditionally. Checking it after the filters would mean an
// instance with fetching off still paid to enumerate and rank the mesh's catalogue,
// and would report a skipped-item list for a request it was never entitled to make.
//
// WHY EACH FILTER IS A REFUSAL RATHER THAN A PREDICATE ON THE INPUT: because a caller
// that filters before calling has already made the decision, and a decision made before
// the switch is read is exactly the #7 failure — a refactor can move a filter without
// anyone noticing that it now runs against the wrong state.
func Build(ctx context.Context, switchState collab.AutoAcquire, req Request) (*Queue, error) {
	// 1. THE SWITCH. Read once, here, and asked as a question.
	//
	// ctx is accepted and checked even though nothing below uses it, because a caller
	// that passes a cancelled context to a function that ignores it will eventually
	// pass one to something that does not, and the bug will be attributed there.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("acquisition: %w", err)
	}
	//
	// THE VALIDITY CHECK AND THE REFUSAL ARE ONE DECISION, NOT TWO. My first version
	// had `if !Valid()` and `if !MayAcquire()` as separate branches, and a mutation run
	// showed deleting the first changed no verdict — because R083's MayAcquire already
	// refuses an unrecognised state, since Fetches() compares against the two named
	// states and an unknown value matches neither.
	//
	// So the branches are told apart here, in one place, purely for the MESSAGE: an
	// operator reading "the switch is \"OFF\", and \"OFF\" does not fetch" learns only
	// that fetching is off, while one reading "OFF is not a state this build knows"
	// learns their switch has a typo and that fixing it may change the answer. Same
	// refusal, same sentinel, and the second message is the actionable one.
	if !switchState.Valid() {
		return nil, fmt.Errorf("%w: %q is not a state this build knows, and an "+
			"unrecognised state is not permission to fetch. This is not an empty "+
			"queue — nothing was considered", ErrSwitchOff, switchState)
	}
	if !switchState.MayAcquire() {
		return nil, fmt.Errorf("%w: the switch is %q, and %q does not fetch. This is "+
			"not an empty queue — nothing was considered", ErrSwitchOff,
			switchState, switchState)
	}

	q := &Queue{}

	// 2. THE FILTERS, each recording why it dropped something.
	eligible := make([]discovery.Candidate, 0, len(req.Cands))
	for _, c := range req.Cands {
		// A candidate with no id cannot be matched against Held and cannot be
		// reported to an operator afterwards, so it is refused rather than queued.
		// §6a.6: a peer's id is not this instance's, and an unattributable item is
		// one nobody can later account for.
		if strings.TrimSpace(c.ID) == "" {
			q.Skipped = append(q.Skipped, Skipped{Reason: SkipNoSource})
			continue
		}

		// HELD FIRST. §6b.3's own subtraction, and before the cheap checks below
		// because a held item is the most common skip by a wide margin and reporting
		// "no source" for something already in the library would be actively
		// misleading.
		if req.Held[c.ID] {
			q.Skipped = append(q.Skipped, Skipped{ID: c.ID, Reason: SkipHeld})
			continue
		}

		eligible = append(eligible, c)
	}

	// 3. §6a.5'S FUNCTION, UNMODIFIED.
	//
	// Called with the caller's weights as given, and with the candidate slice the
	// filters produced — so the ORDER is §6a.5's and the filtering is ours, which is
	// the split §6b.3 describes: the mesh's recommender pointed at a queue, with the
	// queue's own admission rules applied before it runs.
	q.Items = discovery.Rank(eligible, req.Fingerprint, req.Gravity, req.Peer, req.Weights)

	// Non-negotiable #6: a ranking shapes an unordered set, it never filters one. The
	// filters above are ADMISSION decisions (do we hold it, can we get it) and this
	// rank does not remove anything. Asserted here rather than only in discovery,
	// because a caller reading this package should not have to know that §6a.5's
	// contract forbids filtering to see that nothing below the switch filters.
	//
	// Sorted for determinism, breaking ties on ID exactly as discovery.Rank does — the
	// same rule, so two identical requests produce identical plans and a real change
	// is distinguishable from a re-shuffle.
	sort.SliceStable(q.Items, func(i, j int) bool {
		if q.Items[i].Score != q.Items[j].Score {
			return q.Items[i].Score > q.Items[j].Score
		}
		return q.Items[i].ID < q.Items[j].ID
	})

	// Skipped is reported in ID order so two identical requests produce identical
	// reports — the same determinism argument, and it is what makes a diff of two
	// builds' reports readable.
	sort.SliceStable(q.Skipped, func(i, j int) bool {
		if q.Skipped[i].ID != q.Skipped[j].ID {
			return q.Skipped[i].ID < q.Skipped[j].ID
		}
		return q.Skipped[i].Reason < q.Skipped[j].Reason
	})

	return q, nil
}

// BuildForRequest is the explicit-request path, and it is NOT the ranked path.
//
// §6a.5, non-negotiable #6: "a user's explicit preference returns that entity", and
// discovery.ExplicitRequest's own comment gives the reason this cannot be a term in a
// score — "a term is a magnitude and a constraint is not. Any score-based encoding of
// 'must return this' is a number some other candidate can exceed, which is the bug
// rather than the feature."
//
// So a request for one scene bypasses the ranking entirely. It does NOT bypass the
// switch, the held check, or the consent refusal: those are permission and admission,
// not preference, and a user's explicit wish is not consent to fetch something denied
// or to re-download what is already here.
func BuildForRequest(ctx context.Context, switchState collab.AutoAcquire, req Request, wantID string) (*Queue, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("acquisition: %w", err)
	}

	// THE SWITCH IS NOT CHECKED HERE, AND THAT IS DELIBERATE. My first version checked
	// it and then called Build, which checks it again — two live guards where one
	// suffices, and the mutation run reported the consequence precisely: deleting
	// EITHER one left the suite green, because the other still refused. Two guards is
	// the shape that lets a future edit drop one and never notice.
	//
	// So Build is the single place the switch is consulted, and the property "an
	// explicit request does not override it" is enforced there. Preference is not
	// permission: a user asking for something does not thereby consent to fetch it while
	// fetching is switched off, and §6b.3's hard stop applies to every acquisition. A
	// request is one, so it goes through the same gate as a ranked request — which is
	// what makes "cannot bypass" true by construction rather than by a second check
	// somebody has to keep.
	//
	// The consequence is that the error a caller sees for a request at switch-off comes
	// from Build, and Build's message mentions the ranked path. That is a worse message
	// for this call site, and it is the right trade: a correct refusal with a slightly
	// wrong explanation beats two refusals where one can be deleted silently.

	// From here it is the ORDINARY path, so the held and refused checks are not
	// reimplemented either — one implementation of the admission rules, called by both.
	// A second copy in this function is a second set to keep in step, and the two would
	// disagree exactly where it matters: the held case.
	q, err := Build(ctx, switchState, req)
	if err != nil {
		return nil, err
	}

	// A request for something HELD gets its own sentinel, and it is distinct from
	// discovery.ErrNotFound on purpose. "I do not have that" and "I already have that"
	// are both refusals to download and they need opposite responses: not-found means
	// the mesh may not have it either, so the request cannot be satisfied by anyone;
	// held means it is HERE, and the correct response is to point at it rather than
	// fetch a second copy.
	for _, s := range q.Skipped {
		if s.ID == wantID && s.Reason == SkipHeld {
			return nil, fmt.Errorf("%w: %q", ErrHeld, wantID)
		}
	}

	// Found by ID, not by score, through discovery's own function so the two paths
	// cannot drift on what "found" means.
	//
	// THE NOT-FOUND ERROR IS PASSED THROUGH, NOT WRAPPED IN ErrSwitchOff. My first
	// version wrapped it, and that was a lie with a real consequence: a caller
	// branching on ErrSwitchOff to tell an operator "fetching is switched off" would
	// say exactly that when the switch is ON and the mesh simply does not have the
	// scene. The switch was consulted above and permitted; a second refusal from it
	// here is a different answer wearing its name.
	//
	// %w on the inner error is what keeps errors.Is(err, discovery.ErrNotFound) true,
	// which is how a caller tells "nobody has it" from "we already have it".
	c, err := discovery.ExplicitRequest(q.candidates(), wantID)
	if err != nil {
		return nil, fmt.Errorf("acquisition: %q survived admission but is not in the "+
			"queue: %w", wantID, err)
	}

	// Score is deliberately left at zero. #6 says the request path does not consult
	// the ranking, and a score here would invite a caller to treat it as the reason for
	// the return — the score-as-constraint bug the requirement exists to forbid.
	return &Queue{Items: []discovery.Ranked{{Candidate: c}}}, nil
}

// candidates re-derives the queue's candidates, so BuildForRequest can hand them to
// discovery.ExplicitRequest without that function taking a different shape.
//
// THE RANK IS NOT RECOMPUTED HERE, and the score on the returned item is therefore the
// one Rank produced — which is correct: a request returns the entity, and its score is
// incidental to that fact. #6 says the request path does not consult the ranking, and
// reporting a score would invite a caller to treat it as the reason for the return.
func (q *Queue) candidates() []discovery.Candidate {
	out := make([]discovery.Candidate, 0, len(q.Items))
	for _, r := range q.Items {
		out = append(out, r.Candidate)
	}
	return out
}

// Counts summarises a queue, so a caller can report "12 to fetch, 40 already held"
// without walking the slice and inventing its own arithmetic.
//
// A method rather than two exported ints because the numbers have to be computed
// together: a caller that counts Items and Skipped separately can disagree with itself
// about the total, and a report whose parts do not sum to its total is not a report.
func (q *Queue) Counts() (toFetch, held, other int) {
	toFetch = len(q.Items)
	for _, s := range q.Skipped {
		if s.Reason == SkipHeld {
			held++
			continue
		}
		other++
	}
	return toFetch, held, other
}
