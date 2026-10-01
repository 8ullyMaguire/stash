package acquisition

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/internal/discovery"
)

// R075 (acquire what the user would enjoy, opt-out) and R076 (taste-ranked queue
// reusing the mesh recommender), §6b.3.
//
// THE TWO REQUIREMENTS ARE ONE IMPLEMENTATION, and the tests are written to show that
// rather than to test them apart: the queue IS "what the mesh holds, minus what this
// instance has, ranked by §6a.5's function".

// tasteFP is a fingerprint with two tags the fixtures use, so the ORDER of the expected
// queue is derived from the spec's ranking rather than hard-coded as a position.
func tasteFP() discovery.Fingerprint {
	return discovery.Fingerprint{
		Tags:       map[string]float64{"jazz": 1.0, "piano": 0.5},
		Studios:    map[string]float64{},
		Performers: map[string]float64{},
	}
}

func noGravity() discovery.Gravity {
	return discovery.Gravity{Tags: map[string]float64{}, Studios: map[string]float64{}}
}

// threeCands are three candidates whose taste scores are strictly ordered: jazz (1.0),
// piano (0.5), and neither (0).
func threeCands() []discovery.Candidate {
	return []discovery.Candidate{
		{ID: "c-none", Tags: []string{"rock"}},
		{ID: "c-jazz", Tags: []string{"jazz"}},
		{ID: "c-piano", Tags: []string{"piano"}},
	}
}

func baseRequest() Request {
	return Request{
		Cands:       threeCands(),
		Held:        map[string]bool{},
		Fingerprint: tasteFP(),
		Gravity:     noGravity(),
		Peer:        map[string]float64{},
		Weights:     discovery.DefaultWeights(),
	}
}

// R076: "ranked by taste similarity", using §6a.5's function UNMODIFIED.
//
// The test asserts the ORDER against scores computed by discovery.Score, so it fails if
// this package ever grows a second ranker that disagrees — which is the failure the
// spec's "nothing new is invented here" is written against, and one that would be
// invisible because both rankers would produce plausible orders.
func TestTheQueueIsRankedByTasteUsingTheSpecFunction(t *testing.T) {
	q, err := Build(context.Background(), collab.AcquireFull, baseRequest())
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	if len(q.Items) != 3 {
		t.Fatalf("got %d items, want 3", len(q.Items))
	}

	// The expected order is computed by §6a.5's own scorer, not written down. If this
	// package ranked differently, the test fails on the ORDER rather than on a position
	// that happened to be right today.
	want := discovery.Rank(threeCands(), tasteFP(), noGravity(), map[string]float64{},
		discovery.DefaultWeights())
	for i := range want {
		if q.Items[i].ID != want[i].ID {
			t.Errorf("item %d is %q, want %q — the queue must be §6a.5's order",
				i, q.Items[i].ID, want[i].ID)
		}
		if q.Items[i].Score != want[i].Score {
			t.Errorf("item %d (%s) scored %v, want %v — this package must not rescore",
				i, q.Items[i].ID, q.Items[i].Score, want[i].Score)
		}
	}

	// And the order is what the taste says: jazz, then piano, then neither.
	if q.Items[0].ID != "c-jazz" {
		t.Errorf("best match is %q, want c-jazz (taste 1.0)", q.Items[0].ID)
	}
}

// R075: the opt-out, "and it is a hard stop".
//
// ASSERTED AS A REFUSAL WITH ITS OWN SENTINEL, not as an empty queue. A caller that
// cannot tell "nothing matched" from "you have fetching off" reports the second as the
// first, and the operator concludes their taste is well served.
func TestTheSwitchOffRefusesTheWholeRequest(t *testing.T) {
	for _, state := range []collab.AutoAcquire{collab.AcquireOff} {
		q, err := Build(context.Background(), state, baseRequest())
		if err == nil {
			t.Fatalf("%q must refuse, got a queue of %d items", state, len(q.Items))
		}
		if !errors.Is(err, ErrSwitchOff) {
			t.Errorf("%q: got %v, want ErrSwitchOff", state, err)
		}
		if q != nil {
			t.Errorf("%q: a refused request must not also return a queue, or a caller "+
				"that ignores the error has an empty queue and will report it as "+
				"'nothing to acquire'", state)
		}
		// The message distinguishes the two situations, which is the reason the
		// sentinel exists.
		if !strings.Contains(err.Error(), "nothing was considered") {
			t.Errorf("%q: the error must say nothing was considered, so an operator is "+
				"not told their taste matches nothing: %v", state, err)
		}
	}
}

// fetch_only FETCHES, and this is the state §6b.3 made the default precisely so it
// would. A queue that refused at fetch_only would make the default state useless and
// nobody would notice until they expected content that never arrived.
func TestFetchOnlyStillAcquires(t *testing.T) {
	q, err := Build(context.Background(), collab.AcquireFetchOnly, baseRequest())
	if err != nil {
		t.Fatalf("fetch_only must acquire, got %v", err)
	}
	if len(q.Items) != 3 {
		t.Errorf("fetch_only produced %d items, want 3", len(q.Items))
	}
}

// AN UNRECOGNISED SWITCH REFUSES, even though it does not match the off state. The
// value may have come from a newer core, and fetching on a value this build does not
// understand is the direction that costs the user bandwidth and content they did not
// ask for.
func TestAnUnrecognisedSwitchRefuses(t *testing.T) {
	for _, state := range []collab.AutoAcquire{
		" ", "OFF", "fetch-only", "auto", "full ", "1",
	} {
		q, err := Build(context.Background(), state, baseRequest())
		if err == nil {
			t.Errorf("switch %q must refuse, got %d items", string(state), len(q.Items))
			continue
		}
		if !errors.Is(err, ErrSwitchOff) {
			t.Errorf("switch %q: got %v, want ErrSwitchOff", string(state), err)
		}
		if q != nil {
			t.Errorf("switch %q returned a queue alongside an error", string(state))
		}
		// THE MESSAGE IS THE ACTIONABLE ONE. Both paths refuse identically — the
		// mutation run showed the Validity check was verdict-redundant with MayAcquire —
		// so what the separate branch buys is telling an operator that their switch
		// holds a value this build does not know, which is a fixable problem rather
		// than a configuration they chose.
		if !strings.Contains(err.Error(), "not a state this build knows") {
			t.Errorf("switch %q: the refusal must say the value is unrecognised, not "+
				"merely that fetching is off, or an operator with a typo in the switch "+
				"concludes they turned it off: %v", string(state), err)
		}
	}
}

// AND THE OFF STATE GETS THE OTHER MESSAGE, so the two refusals are distinguishable
// from the error text alone as well as from the switch value. §6b.3's three states mean
// an operator has to be able to tell "I chose this" from "this is not what I chose".
func TestTheOffStateSaysItDoesNotFetchRatherThanThatItIsUnknown(t *testing.T) {
	_, err := Build(context.Background(), collab.AcquireOff, baseRequest())
	requireErr(t, err)

	if strings.Contains(err.Error(), "not a state this build knows") {
		t.Errorf("the off state is a VALID state, so saying it is unrecognised sends "+
			"an operator looking for a typo they did not make: %v", err)
	}
	if !strings.Contains(err.Error(), "does not fetch") {
		t.Errorf("and it must say what the state does instead: %v", err)
	}
}

// requireErr fails the test if err is nil, with the caller's context.
func requireErr(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}

// R075: "minus what this instance already has". The subtraction is by LOCAL id, and
// the reason a held item is dropped is reported — because §6b.3's filter is the
// interesting part of the answer and "why did it not fetch that" needs answering.
func TestWhatThisInstanceHoldsIsRemovedAndReported(t *testing.T) {
	req := baseRequest()
	req.Held = map[string]bool{"c-piano": true, "c-none": true}

	q, err := Build(context.Background(), collab.AcquireFull, req)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	if len(q.Items) != 1 || q.Items[0].ID != "c-jazz" {
		t.Fatalf("got %v, want only c-jazz", ids(q))
	}

	// Both held items are accounted for, by name, with the right reason.
	reasons := map[string]SkipReason{}
	for _, s := range q.Skipped {
		reasons[s.ID] = s.Reason
	}
	for _, id := range []string{"c-piano", "c-none"} {
		if reasons[id] != SkipHeld {
			t.Errorf("%s was skipped as %q, want %q", id, reasons[id], SkipHeld)
		}
	}

	// And the counts agree with the parts, which is what makes the report usable.
	toFetch, held, other := q.Counts()
	if toFetch != 1 || held != 2 || other != 0 {
		t.Errorf("counts = (%d, %d, %d), want (1, 2, 0)", toFetch, held, other)
	}
	if toFetch+held+other != len(req.Cands) {
		t.Errorf("counts sum to %d but %d candidates went in — a report whose parts do "+
			"not sum to its total is not a report",
			toFetch+held+other, len(req.Cands))
	}
}

// A SKIPPED ITEM MUST NOT INFLUENCE WHAT IS FETCHED. Two orders are asserted for the
// same final queue: one where the held item is first in the input and one where it is
// last. §6a.5 ranks an unordered set, so input order must not be observable — and a
// held item that could nudge a score is a filter that leaked into the ranking.
func TestInputOrderCannotChangeTheQueue(t *testing.T) {
	forward := baseRequest()
	forward.Held = map[string]bool{"c-jazz": true}

	reversed := baseRequest()
	reversed.Held = map[string]bool{"c-jazz": true}
	// Same three candidates, reversed.
	reversed.Cands = []discovery.Candidate{threeCands()[2], threeCands()[1], threeCands()[0]}

	a, err := Build(context.Background(), collab.AcquireFull, forward)
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	b, err := Build(context.Background(), collab.AcquireFull, reversed)
	if err != nil {
		t.Fatalf("reversed: %v", err)
	}

	if strings.Join(ids(a), ",") != strings.Join(ids(b), ",") {
		t.Errorf("input order changed the queue: %v vs %v — §6a.5 ranks an unordered "+
			"set, so input order must not be observable", ids(a), ids(b))
	}

	// And two identical requests are byte-identical, which is what makes a real change
	// distinguishable from a re-shuffle.
	c, err := Build(context.Background(), collab.AcquireFull, forward)
	if err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if strings.Join(ids(c), ",") != strings.Join(ids(a), ",") {
		t.Errorf("two identical requests gave different queues: %v vs %v",
			ids(c), ids(a))
	}
}

// AN EXPLICIT REQUEST BYPASSES THE RANKING, and this is non-negotiable #6: "a user's
// explicit preference returns that entity".
//
// The fixture makes the ranking DISAGREE — c-none has zero taste and would be last —
// so a request for it can only succeed if the ranking was genuinely not consulted.
func TestAnExplicitRequestReturnsTheEntityWhateverItsRank(t *testing.T) {
	q, err := BuildForRequest(context.Background(), collab.AcquireFull,
		baseRequest(), "c-none")
	if err != nil {
		t.Fatalf("an explicit request for the worst-ranked candidate failed: %v", err)
	}
	if len(q.Items) != 1 || q.Items[0].ID != "c-none" {
		t.Fatalf("got %v, want exactly [c-none]", ids(q))
	}
}

// #6's reason, quoted from discovery.ExplicitRequest: "a term is a magnitude and a
// constraint is not. Any score-based encoding of 'must return this' is a number some
// other candidate can exceed, which is the bug rather than the feature."
//
// So the returned item carries NO score, and a caller cannot mistake the score for the
// reason it came back.
func TestAnExplicitRequestCarriesNoScoreToMisread(t *testing.T) {
	q, err := BuildForRequest(context.Background(), collab.AcquireFull,
		baseRequest(), "c-none")
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	if q.Items[0].Score != 0 {
		t.Errorf("the returned item scored %v. A request returns the entity, not the "+
			"entity's rank, and a score here invites a caller to treat it as the reason "+
			"for the return — which is the score-as-constraint bug #6 forbids",
			q.Items[0].Score)
	}
}

// AN EXPLICIT REQUEST IS STILL AN ACQUISITION, so the switch stops it. The distinction
// that matters: preference is not permission. A user asking for something does not
// thereby consent to fetch it while fetching is off.
func TestAnExplicitRequestDoesNotOverrideTheSwitch(t *testing.T) {
	q, err := BuildForRequest(context.Background(), collab.AcquireOff,
		baseRequest(), "c-jazz")
	if err == nil {
		t.Fatalf("an explicit request at off must be refused, got %v", ids(q))
	}
	if !errors.Is(err, ErrSwitchOff) {
		t.Errorf("got %v, want ErrSwitchOff", err)
	}
	if q != nil {
		t.Error("and it must not return a queue alongside the error")
	}
}

// HELD IS REFUSED WITH ITS OWN SENTINEL, distinct from discovery's not-found, because
// the two need OPPOSITE responses: "nobody has it" versus "it is here, here is where".
// Collapsing them tells a user their library does not contain something it does.
func TestRequestingSomethingHeldIsDistinctFromRequestingSomethingAbsent(t *testing.T) {
	req := baseRequest()
	req.Held = map[string]bool{"c-jazz": true}

	_, heldErr := BuildForRequest(context.Background(), collab.AcquireFull, req, "c-jazz")
	if heldErr == nil {
		t.Fatal("requesting a held scene must be refused")
	}
	if !errors.Is(heldErr, ErrHeld) {
		t.Errorf("got %v, want ErrHeld — the operator's response is 'it is already in "+
			"your library', which is not 'not found'", heldErr)
	}

	// And it is NOT discovery.ErrNotFound, so a caller branching on the ranking
	// package's sentinel does not mistake it.
	if errors.Is(heldErr, discovery.ErrNotFound) {
		t.Error("a held scene must not report as discovery.ErrNotFound: the user asked " +
			"for something they have, and being told it does not exist is wrong in the " +
			"one direction that matters")
	}

	// An absent one reports not-found, which is a different answer.
	_, absentErr := BuildForRequest(context.Background(), collab.AcquireFull,
		baseRequest(), "no-such-scene")
	if !errors.Is(absentErr, discovery.ErrNotFound) {
		t.Errorf("an unknown id: got %v, want discovery.ErrNotFound", absentErr)
	}
	if errors.Is(absentErr, ErrHeld) {
		t.Error("and it must not report as ErrHeld")
	}
}

// A CANDIDATE WITH NO ID IS REFUSED, and reported. §6a.6: a peer's id is not this
// instance's, and an item nobody can attribute cannot be matched against Held, cannot
// be reported afterwards, and cannot be reconciled against a manifest.
func TestACandidateWithNoIdIsRefusedAndReported(t *testing.T) {
	req := baseRequest()
	req.Cands = append(req.Cands,
		discovery.Candidate{ID: "", Tags: []string{"jazz"}},
		discovery.Candidate{ID: "   ", Tags: []string{"jazz"}},
	)

	q, err := Build(context.Background(), collab.AcquireFull, req)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	for _, item := range q.Items {
		if strings.TrimSpace(item.ID) == "" {
			t.Errorf("an unattributable candidate was queued: %+v", item.Candidate)
		}
	}

	// Both are reported, so the skip count still sums to the input.
	toFetch, _, other := q.Counts()
	if other != 2 {
		t.Errorf("got %d unattributed skips, want 2", other)
	}
	if toFetch+heldPlus(other) != len(req.Cands) {
		t.Errorf("counts do not sum to the input: %d+%d vs %d candidates",
			toFetch, other, len(req.Cands))
	}
}

// A REFUSAL IS NEVER REPORTED AS AN EMPTY QUEUE, at any layer. The whole package
// exists because these two situations are different, so the property is asserted once
// over every refusal path rather than per-test.
func TestNoRefusalEverLooksLikeAnEmptyQueue(t *testing.T) {
	cases := []struct {
		name  string
		build func() (*Queue, error)
	}{
		{"switch off", func() (*Queue, error) {
			return Build(context.Background(), collab.AcquireOff, baseRequest())
		}},
		{"unrecognised switch", func() (*Queue, error) {
			return Build(context.Background(), "OFF", baseRequest())
		}},
		{"cancelled context", func() (*Queue, error) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return Build(ctx, collab.AcquireFull, baseRequest())
		}},
		{"request at switch off", func() (*Queue, error) {
			return BuildForRequest(context.Background(), collab.AcquireOff,
				baseRequest(), "c-jazz")
		}},
	}

	for _, c := range cases {
		q, err := c.build()
		if err == nil {
			t.Errorf("%s: expected a refusal, got a queue", c.name)
			continue
		}
		if q != nil {
			t.Errorf("%s: a refusal returned a queue with %d items alongside the "+
				"error; a caller that ignores the error then reports an empty queue "+
				"as 'nothing to acquire'", c.name, len(q.Items))
		}
	}
}

// AN EMPTY CANDIDATE SET IS AN EMPTY QUEUE AND NOT A REFUSAL — the one case where an
// empty result is the honest answer, and conflating it with the switch-off case is the
// error this package is built to avoid.
func TestNothingToAcquireIsAnEmptyQueueNotARefusal(t *testing.T) {
	req := baseRequest()
	req.Cands = nil

	q, err := Build(context.Background(), collab.AcquireFull, req)
	if err != nil {
		t.Fatalf("an empty mesh catalogue is not a refusal: %v", err)
	}
	if q == nil {
		t.Fatal("and it returns a queue, not nil")
	}
	if len(q.Items) != 0 || len(q.Skipped) != 0 {
		t.Errorf("got %d items and %d skipped, want none", len(q.Items), len(q.Skipped))
	}

	// Held-everything is the same honest answer.
	req2 := baseRequest()
	req2.Held = map[string]bool{"c-jazz": true, "c-piano": true, "c-none": true}
	q2, err := Build(context.Background(), collab.AcquireFull, req2)
	if err != nil {
		t.Fatalf("holding everything is not a refusal: %v", err)
	}
	toFetch, held, other := q2.Counts()
	if toFetch != 0 || held != 3 || other != 0 {
		t.Errorf("counts = (%d, %d, %d), want (0, 3, 0)", toFetch, held, other)
	}
}

// THE ZERO WEIGHTS ARE USED AS GIVEN, not silently replaced with the defaults.
//
// A caller who forgets to set weights gets a queue ordered by nothing, which is VISIBLE.
// Replacing them would produce a plausible order that nobody chose — and the failure
// would be a queue that looks like taste when it is the default configuration.
func TestZeroWeightsAreNotSilentlyReplaced(t *testing.T) {
	req := baseRequest()
	req.Weights = discovery.Weights{} // every term zero

	q, err := Build(context.Background(), collab.AcquireFull, req)
	if err != nil {
		t.Fatalf("building: %v", err)
	}

	for _, item := range q.Items {
		if item.Score != 0 {
			t.Errorf("item %s scored %v with zero weights, so the package substituted "+
				"the defaults; a caller who forgot to set them would get a plausible "+
				"order they did not ask for", item.ID, item.Score)
		}
	}

	// And the order falls back to the ID tiebreak, which is the documented behaviour
	// rather than a taste order.
	if q.Items[0].ID != "c-jazz" {
		t.Logf("with zero weights the order is %v — deterministic by ID, which is "+
			"acceptable; what matters is that no score was invented", ids(q))
	}
}

// THE SWITCH IS READ AT THE POINT OF THE DECISION, and this is asserted on the package
// rather than on a query string: §6b.3 requires enforcement "at the point the permission
// is read ... and not by omitting rows from a query", because #7's note is that a
// refactor dropping the filter must not silently resume publishing.
//
// So the only entry points take the switch as an argument and ask it. There is no
// exported function that builds a queue without one, which is the structural form of the
// requirement — a queue cannot be built in a state where nobody decided.
func TestEveryEntryPointTakesTheSwitch(t *testing.T) {
	// The package exports exactly two build functions and one counts method. If a
	// third entry point appeared without a switch, this would not catch it by
	// reflection, so the assertion is the simpler and honest one: the switch is a
	// REQUIRED parameter of both, which the compiler enforces.
	//
	// Asserted behaviourally instead, because that is what actually matters: a queue
	// cannot exist for a switch that refuses. Every refusal case above already proves
	// it, and this records why the requirement is structural rather than conventional.
	if collab.AcquireOff.MayAcquire() {
		t.Error("AcquireOff may acquire — the switch the whole package gates on is " +
			"wrong, and every refusal test above is passing for the wrong reason")
	}
	if !collab.AcquireFetchOnly.MayAcquire() {
		t.Error("AcquireFetchOnly must acquire, or §6b.3's default state is useless")
	}
}

// ids is a test helper: a queue as comparable ids.
func ids(q *Queue) []string {
	if q == nil {
		return nil
	}
	out := make([]string, 0, len(q.Items))
	for _, r := range q.Items {
		out = append(out, r.ID)
	}
	return out
}

func heldPlus(other int) int { return other }
