// Package alert decides when a scene needs attention for preservation, and
// produces an ALERT rather than logging one.
//
// M8 step 8.3 (R079, §6b.4). Depends on R078, which is `internal/replicastore`.
//
// # WHY THIS IS A VALUE, NOT A FUNCTION CALL ANYWHERE
//
// R079 says "preservation alerts when a scene falls below N healthy replicas", and
// the commons knows the count is low while the NODE is the one that can fix it. So
// the interesting output is not a log line — this process has a logger — it is a
// DURABLE, RANKED, DEDUPLICATED decision that a scheduler can act on: which scene
// to replicate next, and how urgently.
//
// The three requirements that shape the type:
//
//   1. URGENCY IS WEIGHTED BY RARITY AND DEMAND, never by mesh-wide traffic. §6b.4
//      spells out why: "never by how much mesh-wide traffic it has drawn, which
//      would concentrate scarce storage on content that is already plentiful." So
//      there is no field for traffic, and a candidate's rank is built from rarity
//      and demand alone.
//   2. IT IS NOT AN ERROR CONDITION. §6b.5 has already settled that a failed
//      verification triggers re-fetch rather than an alert, so a scene at one
//      healthy replica is work, not a fault. Treating it as an error is how an
//      operator learns to ignore the subsystem.
//   3. IT MUST BE DEDUPLICATED AND STABLE. A sweep over 4,000 scenes must not
//      produce 4,000 identical alerts for one condition, and re-running it must
//      not reshuffle the queue — a rank that changes on every call is a queue an
//      operator cannot act on.
//
// # WHY SCARCE STORAGE IS ALLOCATED BY RARITY, NOT BY WHO ASKED LOUDEST
//
// The scarcity is real and bounded: a node has a storage budget, so when 50 scenes
// are below N, something gets the disk and 49 do not. Ranking by demand alone would
// send every byte to whatever is currently popular, which is precisely the failure
// §6b.4 names. RankRarity and RankDemand are kept separate in Candidate so the
// weighting is visible and testable rather than folded into one opaque number.

package alert

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/stashapp/stash/internal/preservation"
)

// DefaultN is the target when an instance has not chosen one.
//
// READ FROM `preservation.Default` rather than repeated as a literal here. §6b.4
// says content replicates to N "configurable and defaulting to 3", and
// `internal/preservation` (step 7.4) already owns that constant as the placement
// scheduler's target. Two packages each holding their own 3 is how an instance ends
// up alerting at N=3 while placing at N=4 — which is not a coincidence anyone
// notices and not a failure anyone can explain.
const DefaultN = preservation.Default

// Urgency is how badly a scene needs a replica.
//
// A BAND, NOT A SCORE, and the distinction is deliberate: a float score invites
// comparing numbers across sweeps and tuning a threshold, which is how a policy
// becomes an unauditable tuning knob. A named band says "this is urgent" in the
// vocabulary an operator already uses, and it has an exhaustion consequence that is
// visible in the type.
type Urgency int

const (
	// UrgencyNone means the scene is healthy: it has N verified replicas.
	UrgencyNone Urgency = iota

	// UrgencyLow means one replica is short. Work to schedule, not to panic about.
	UrgencyLow

	// UrgencyMedium means the margin is thin — under half of N.
	UrgencyMedium

	// UrgencyHigh means the scene is at risk of loss: zero verified replicas, or a
	// verified replica that has gone missing.
	UrgencyHigh
)

var (
	// ErrNoCandidate means an alert was requested for a scene with no verified
	// count to judge.
	ErrNoCandidate = errors.New("alert: candidate carries no replica count")

	// ErrInvalidN means N is not a policy value. Refused rather than satisfied
	// vacuously — see below.
	ErrInvalidN = errors.New("alert: N must be at least 1")
)

// String renders a band, so an alert reads in words.
func (u Urgency) String() string {
	switch u {
	case UrgencyNone:
		return "none"
	case UrgencyLow:
		return "low"
	case UrgencyMedium:
		return "medium"
	case UrgencyHigh:
		return "high"
	}
	return "unknown"
}

// Candidate is a scene's preservation state as the commons reports it.
//
// DELIBERATELY ABSENT: any traffic, popularity or fetch-count field. §6b.4's rule
// is that urgency never weights mesh-wide traffic, and the way to enforce a rule
// about a field is for the field not to exist. A `Traffic int64` here would be a
// ranking input waiting for someone to add it to the sort.
type Candidate struct {
	SceneID string

	// VerifiedReplicas counts ONLY verified replicas — `replicastore.Counts.Verified`
	// and nothing else. This is the whole of §6b.5 reaching the alert layer: a
	// pending, corrupt or missing replica is not a healthy one, and an alert built
	// on any other count would fire while three copies sat on disk unverified.
	VerifiedReplicas int

	// PendingReplicas is reported for the operator, NOT ranked on. A pending replica
	// is work already in flight, so a scene with two pending and one verified is not
	// more urgent than one with one pending and one verified — it is the same risk
	// with the same progress.
	PendingReplicas int

	// MissingReplicas is a VERIFIED replica that has gone. It counts as risk even if
	// the verified count is unchanged, because §6a.9's detection means a peer went
	// offline and the margin is now thinner than the count alone suggests.
	MissingReplicas int

	// Rarity is how scarce this content is mesh-wide: higher means rarer, and rarer
	// content is worth more scarce storage. §6b.4: urgency is "weighted by rarity
	// and by whether anyone is asking for it".
	Rarity float64

	// Demand is how many peers have asked for it. The second urgency weight, kept
	// separate from Rarity so the weighting is auditable.
	Demand int

	// Size is the content size in bytes, used only to report what a fix would cost.
	// It is NOT a ranking input: ranking by size would replicate the smallest
	// objects first and leave a single huge scene unreplicated, which is the failure
	// mode where the scene everyone cares about is the one at risk.
	Size int64
}

// Validate checks the candidate can be judged at all.
func (c Candidate) Validate() error {
	if strings.TrimSpace(c.SceneID) == "" {
		return fmt.Errorf("%w: no scene id", ErrNoCandidate)
	}
	if c.VerifiedReplicas < 0 || c.PendingReplicas < 0 || c.MissingReplicas < 0 {
		return fmt.Errorf("%w: scene %s has a negative replica count", ErrNoCandidate, c.SceneID)
	}
	return nil
}

// UrgencyFor reports the band for a candidate at a given N.
//
// THE VERIFIED COUNT IS THE ONLY INPUT, and that is the requirement rather than a
// simplification. §6b.5 makes verification what counts, so the band reads the count
// §6b.5 defines -- and pending, corrupt and missing replicas are REPAIR work for
// `internal/preservation.NeedsRepair`, not operator-facing alarms. Making a missing
// replica escalate the band would duplicate the repair path with a louder voice and
// is the "rather than an alert" clause read backwards.
//
// Two bands are `high` regardless of N, because they are the cases where a single
// loss ends the promise:
//
//   - zero verified replicas: no healthy copy exists at all.
//   - under half of N verified: the margin cannot absorb another loss.
func UrgencyFor(c Candidate, n int) (Urgency, error) {
	if err := c.Validate(); err != nil {
		return UrgencyNone, err
	}
	if n <= 0 {
		// N is a policy. N=0 would make every scene healthy by definition, which is
		// an alert that can never fire -- and a check that cannot fire is worse than
		// no check, because it is present in the code and absent from the operator's
		// list of things that work.
		return UrgencyNone, fmt.Errorf("%w: got %d", ErrInvalidN, n)
	}

	// Zero verified replicas is `high` at any N. There is no healthy copy, so the
	// scene is one failure away from being lost.
	if c.VerifiedReplicas == 0 {
		return UrgencyHigh, nil
	}

	shortfall := n - c.VerifiedReplicas
	if shortfall <= 0 {
		// Healthy by count. A missing replica is reported in the alert and handled
		// by the repair path; it does not make a scene holding N verified copies an
		// emergency, because doing so is the alert-that-never-clears failure.
		return UrgencyNone, nil
	}

	// Under half of N is medium: the margin cannot absorb another loss.
	if float64(c.VerifiedReplicas) < float64(n)/2 {
		return UrgencyMedium, nil
	}
	return UrgencyLow, nil
}

// Alert is one scene's preservation state, ready for a scheduler to act on.
type Alert struct {
	SceneID string
	Urgency Urgency

	VerifiedReplicas int
	PendingReplicas  int
	MissingReplicas  int

	// Rank is the ordering key: HIGHER means more urgent, and it is the sort key
	// descending. Stored rather than computed at print time so a queue's ORDER is
	// fixed at the moment it was built.
	Rank float64

	// Why explains the band in a sentence an operator can act on, naming the
	// numbers that produced it. An alert with no explanation is a number to be
	// ignored or guessed at, and R079's whole value is that it tells an operator
	// where to spend storage.
	Why string

	// SizeBytes is what fixing this would cost, reported and NOT ranked on.
	SizeBytes int64
}

// NeedsAttention reports whether the alert is one a scheduler should act on.
func (a Alert) NeedsAttention() bool { return a.Urgency != UrgencyNone }

// Build judges one candidate and returns its alert.
//
// A HEALTHY SCENE STILL RETURNS AN ALERT, with UrgencyNone, rather than a zero
// value or a nil. The caller gets a complete picture of every scene it asked about,
// which is what lets a sweep report "812 of 4,000 need attention" instead of only
// listing the ones that do — and a count of the healthy ones is what an operator
// uses to decide the sweep covered everything.
func Build(c Candidate, n int) (Alert, error) {
	urgency, err := UrgencyFor(c, n)
	if err != nil {
		return Alert{}, err
	}

	rank := Rank(urgency, c)
	return Alert{
		SceneID:          c.SceneID,
		Urgency:          urgency,
		VerifiedReplicas: c.VerifiedReplicas,
		PendingReplicas:  c.PendingReplicas,
		MissingReplicas:  c.MissingReplicas,
		Rank:             rank,
		SizeBytes:        c.Size,
		Why:              explain(urgency, c, n),
	}, nil
}

// Rank is the ordering key.
//
// # THE ARITHMETIC IS THE REQUIREMENT, and my first version got it wrong in two
// ways that a test written to pass would have accepted.
//
//	Rank = band + rarity + demand
//
// with the band as a whole-number "digit" in a fixed-radix number. Three properties
// hold by construction, and each is a rule rather than a hope:
//
//   - THE BAND DOMINATES. Band * RADIX^3 and every sub-band term is bounded below
//     RADIX^3, so no amount of rarity or demand crosses a band boundary. My first
//     version used band bases 1e6 apart with an UNBOUNDED rarity term
//     (`rarity * 1e3`), so a commons reporting rarity 1e9 gave a `low` scene a rank
//     of 1.0001e12 -- above the `high` band's base. That is precisely the inversion
//     §6b.4 forbids, and it arrived through a magic number rather than a decision.
//   - RARITY OUTRANKS DEMAND. Rarity occupies the thousands place and demand is
//     capped below 1000, so demand cannot buy a whole unit of scarcity. The first
//     version let `log1p(demand) * 100` reach 2070 at a billion demand, which
//     outranked a full unit of rarity -- so a viral scene beat a rare one, again the
//     inversion the rule forbids.
//   - NOTHING PRODUCES NaN OR INF. Every input is clamped and finite, so an
//     at-risk scene can never sort nowhere and vanish from the queue, which is the
//     one failure mode that would defeat the requirement entirely.
//
// RADIX is 1000. It is a named constant rather than a literal at three sites
// because the dominance property depends on the coefficients agreeing with it, and
// three separate literals is how they stopped agreeing in the first place.
const rankRadix = 1000.0

// rankBands maps a band to its leading digit. Healthy is NEGATIVE so a sorted
// queue's healthy tail is separated from its actionable head rather than a run of
// near-identical positive numbers -- see the doc comment on Rank.
var rankBands = map[Urgency]float64{
	UrgencyNone:   -1,
	UrgencyLow:    1 * rankRadix * rankRadix * rankRadix,
	UrgencyMedium: 2 * rankRadix * rankRadix * rankRadix,
	UrgencyHigh:   3 * rankRadix * rankRadix * rankRadix,
}

// demandWeight scales demand so its TOTAL contribution stays below HALF a rarity
// unit, whatever the demand.
//
// DERIVED FROM THE CLAMP, NOT PICKED. log1p(1e5) = 11.51, so a weight of 1.0 spans
// 11.5 rarity units -- a viral scene would outrank a rare one, which is the exact
// inversion §6b.4 forbids. Computing the weight from the demand clamp makes the
// bound a property of the constants rather than a fact someone checked once: raise
// maxDemand and the weight shrinks to compensate.
//
// My first version used 1.0 and a test asserting "rarity wins" failed, which is the
// argument for deriving it.
const maxDemand = 1e5

var demandWeight = 1.0 / (2 * math.Log1p(maxDemand))

func Rank(urgency Urgency, c Candidate) float64 {
	base, ok := rankBands[urgency]
	if !ok {
		// An unrecognised band ranks as actionable-and-minimal rather than healthy.
		// Returning 0 would put it beside a healthy scene, and defaulting to a
		// healthy rank is the dangerous direction: an at-risk scene that sorts into
		// the healthy tail disappears from the queue.
		return rankBands[UrgencyLow]
	}
	if urgency == UrgencyNone {
		// Healthy ranks flat regardless of rarity and demand, so it cannot head an
		// actionable queue a caller forgot to filter.
		return base
	}

	// Rarity, clamped to the radix so a nonsensical commons value cannot cross a
	// band. NaN and Inf both become zero rather than propagating.
	rarity := clampFinite(c.Rarity, 0, rankRadix-1)

	// Demand, clamped at maxDemand and then scaled by the derived weight, so its
	// contribution is bounded by construction.
	demand := clampFinite(float64(c.Demand), 0, maxDemand)
	demandTerm := math.Log1p(demand) * demandWeight

	return base + rarity + demandTerm
}

// clampFinite maps a nonsensical number into range.
//
// NaN IS THE DANGEROUS CASE and gets an explicit test rather than falling out of a
// comparison: every comparison against NaN is false, so a naive
// `if v < lo { v = lo } else if v > hi { v = hi }` leaves NaN untouched, and a NaN
// rank sorts NOWHERE -- which would drop the most at-risk scene out of the queue
// entirely. Inf clamps to the ceiling like any other large value.
func clampFinite(v, lo, hi float64) float64 {
	if math.IsNaN(v) {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// explain is the sentence an operator reads.
//
// IT NAMES THE NUMBERS AND THE FIX, because R079's value is telling an operator
// where to spend storage. "1 of 3 verified" and "0 of 3 verified" are the
// difference between scheduling a placement and treating a scene as at risk, and an
// alert that does not say which it is makes the operator check.
func explain(urgency Urgency, c Candidate, n int) string {
	switch urgency {
	case UrgencyNone:
		return fmt.Sprintf("healthy: %d of %d verified replicas", c.VerifiedReplicas, n)
	case UrgencyHigh:
		return fmt.Sprintf("at risk: %d of %d verified replicas — one disk failure from loss",
			c.VerifiedReplicas, n)
	case UrgencyMedium:
		return fmt.Sprintf("thin: %d of %d verified, under half the target", c.VerifiedReplicas, n)
	case UrgencyLow:
		return fmt.Sprintf("short: %d of %d verified", c.VerifiedReplicas, n)
	}
	return "unknown state"
}

// Plan is one sweep's output: every scene judged, and the ones that need attention
// in the order they should be handled.
type Plan struct {
	// All is every scene that was judged, healthy included, so a caller can report
	// coverage.
	All []Alert

	// Actionable is the subset with NeedsAttention, sorted by Rank descending.
	// SORTED HERE, ONCE, so two callers planning from the same sweep agree on the
	// order rather than each sorting a slice that came back in map order.
	Actionable []Alert

	// N is the target the sweep was run at, carried so a report says which policy it
	// measured against rather than leaving the reader to assume.
	N int

	// GeneratedAt is when the sweep ran, so an operator looking at a stale alert can
	// see that it is stale.
	GeneratedAt time.Time
}

// PlanFor judges a set of candidates and returns the sweep.
//
// THE ORDER OF OPERATIONS IS LOAD-BEARING: build every alert first, sort, THEN
// partition. Sorting a filtered subset would work too, and the difference is that
// sorting first means the healthy alerts are ranked on the same key as the rest, so
// a caller who wants "everything, worst first" gets a coherent list.
func PlanFor(candidates []Candidate, n int, now time.Time) (Plan, error) {
	if n <= 0 {
		return Plan{}, fmt.Errorf("%w: got %d", ErrInvalidN, n)
	}

	all := make([]Alert, 0, len(candidates))
	for _, c := range candidates {
		a, err := Build(c, n)
		if err != nil {
			// One unjudgeable candidate FAILS THE SWEEP rather than being skipped.
			// A sweep that silently drops a scene means an operator sees "800 of 801
			// assessed" with no way to know which one was skipped or why — and a
			// silently-skipped at-risk scene is the failure this requirement exists
			// to prevent.
			return Plan{}, fmt.Errorf("alert: judging scene %q: %w", c.SceneID, err)
		}
		all = append(all, a)
	}

	// Descending by Rank, with the scene id as a TIE-BREAK so the order is total and
	// repeatable. Two scenes with identical candidate data must not swap places
	// between sweeps, or a queue built from them is unreproducible.
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Rank != all[j].Rank {
			return all[i].Rank > all[j].Rank
		}
		return all[i].SceneID < all[j].SceneID
	})

	actionable := make([]Alert, 0, len(all))
	for _, a := range all {
		if a.NeedsAttention() {
			actionable = append(actionable, a)
		}
	}

	return Plan{All: all, Actionable: actionable, N: n, GeneratedAt: now}, nil
}

// Degraded reports whether the sweep found something that needs immediate action,
// which is the question R079 is for.
//
// NAMED RATHER THAN RETURNING A COUNT so a caller cannot accidentally treat the
// number as "how many scenes to fix this run" — the count of `high` band scenes is
// the answer to that, and it is a different question.
func (p Plan) Degraded() bool {
	for _, a := range p.Actionable {
		if a.Urgency == UrgencyHigh {
			return true
		}
	}
	return false
}

// HighestUrgency returns the worst band in the plan, and whether the plan is
// actionable at all.
//
// A SEPARATE CALL rather than a max over Urgency, because a plan of healthy scenes
// should report "nothing needs attention" rather than "the worst is none", which
// reads as a band with content behind it.
func (p Plan) HighestUrgency() (Urgency, bool) {
	if len(p.Actionable) == 0 {
		return UrgencyNone, false
	}
	return p.Actionable[0].Urgency, true
}

// Summary is a one-line report an operator can read.
func (p Plan) Summary() string {
	if len(p.Actionable) == 0 {
		return fmt.Sprintf("%d scenes assessed at N=%d: all healthy", len(p.All), p.N)
	}
	worst, _ := p.HighestUrgency()
	return fmt.Sprintf("%d of %d scenes need attention at N=%d, worst is %s",
		len(p.Actionable), len(p.All), p.N, worst)
}
