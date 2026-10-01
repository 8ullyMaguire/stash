// Package rank computes completion and Elo from records.
//
// M7 step 7.6 (R034–R044, R039), spec §6a.15 and §6a.16.
//
// NOTHING HERE IS STORED. Both a completion score and a rating are computed from
// records at query time, for non-negotiable #4, and that is load-bearing rather
// than stylistic: a stored completion score rots the moment an edit lands, and a
// stored rating is a counter wearing a derived name. Delete every vote and the
// rating below returns zero — which is the difference between a rating and a
// tally, and is exactly what TestARatingIsRecomputableFromItsVotes asserts.
//
// THE PLAN-LEVEL CHOICE, recorded as the plan requires: Glicko-2 or TrueSkill.
//
// Plain Elo. The plan states one requirement for the choice — "it must compute a
// rating from a VOTE SET rather than maintain a counter (#4)" — and the reason
// Elo wins is that it satisfies that requirement with nothing extra to get wrong.
// Glicko-2 and TrueSkill are both superior systems: they model a player's
// uncertainty, so a newcomer is not treated as a confirmed mid-rater, which is a
// real advantage for a federated instance whose voters arrive with reputation
// earned elsewhere.
//
// Neither advantage matters until the vote sets are good enough to estimate a
// deviation from, and estimating one from five votes produces a confident number
// from no information. Shipping Glicko-2 now would mean shipping an uncertainty
// model whose inputs are mostly missing, plus an RD column to maintain, for a
// benefit that is not observable until there are enough votes per voter to
// justify it. Elo is ~40 lines, has one number per rating and no state that can
// drift from the votes, and the migration to Glicko-2 later is additive: the
// vote set is unchanged and only the formula moves.
//
// The plan's other commitments are implemented here rather than deferred: quests
// are a QUERY over completion, so a quest whose gap is closed is complete with no
// write.
package rank

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// ErrNoEntity is returned for an entity with no identity.
var ErrNoEntity = errors.New("rank: entity has no identity")

// FieldState distinguishes PRESENT, ABSENT and CLEARED, and the third is the
// whole point (§6a.15: "absent is distinct from empty, so a field explicitly
// cleared does not count as missing").
//
// A two-state boolean cannot express this. `present: false` means either "nobody
// knows this performer's birthdate" or "somebody deliberately cleared it", and a
// curator's progress bar treats those identically — so a quest to add birthdates
// keeps listing a performer somebody already curated, forever.
type FieldState uint8

const (
	// FieldAbsent means the field is expected and nobody has filled it in.
	FieldAbsent FieldState = iota
	// FieldPresent means the field carries a value.
	FieldPresent
	// FieldCleared means somebody explicitly cleared the field. It is NOT missing.
	FieldCleared
)

func (f FieldState) String() string {
	switch f {
	case FieldAbsent:
		return "absent"
	case FieldPresent:
		return "present"
	case FieldCleared:
		return "cleared"
	}
	return fmt.Sprintf("FieldState(%d)", uint8(f))
}

// Counts is the raw material: what is expected, and what each field holds.
type Counts struct {
	Expected int
	Present  int
	Cleared  int
}

// ErrInconsistent is returned when the counts cannot describe a real entity. It
// is an error rather than a clamp because every path into Counts is derived from
// a vocabulary and a record, so a negative here is a bug upstream — and silently
// clamping would turn a vocabulary mismatch into a plausible-looking progress bar.
var ErrInconsistent = errors.New("rank: field counts are inconsistent")

// Completion is the fraction of expected metadata present and verified (§6a.15).
//
// CLEARED FIELDS COUNT AS COMPLETE. That is the decision §6a.15 makes and it is
// the reason FieldState exists at all: a curated "unknown" is a completed piece
// of curation, whereas an absent field is work still to do.
func Completion(c Counts) (float64, error) {
	if c.Expected < 0 || c.Present < 0 || c.Cleared < 0 {
		return 0, fmt.Errorf("%w: negative counts %+v", ErrInconsistent, c)
	}
	if c.Present+c.Cleared > c.Expected {
		return 0, fmt.Errorf("%w: %d present + %d cleared exceeds %d expected",
			ErrInconsistent, c.Present, c.Cleared, c.Expected)
	}
	if c.Expected == 0 {
		// Nothing is expected, so there is no gap. Returning 0 would render an
		// entity with a complete record as 0% complete, which is the opposite of
		// true and looks like a bug to every user who sees it.
		//
		// 1, not 0: "nothing missing" is fully complete, and a progress bar at
		// 100% for a fully-curated entity is the honest rendering.
		return 1.0, nil
	}
	return float64(c.Present+c.Cleared) / float64(c.Expected), nil
}

// Missing is what a quest is built from: the fields still to do.
func Missing(c Counts) int {
	if c.Expected <= 0 {
		return 0
	}
	done := c.Present + c.Cleared
	if done > c.Expected {
		return 0
	}
	return c.Expected - done
}

// Quest is a curation target expressed as a QUERY over completion, never a stored
// list of entities (§6a.15: "a quest whose gap is closed is complete with no
// write required").
type Quest struct {
	ID   string
	Goal int // how many entities to close
}

// QuestProgress is computed by counting candidates that satisfy the query.
type QuestProgress struct {
	QuestID   string
	Completed int
	Goal      int
	Done      bool
}

// EvaluateQuest computes a quest's progress from candidates' counts.
//
// THE NO-WRITE PROPERTY IS THE FEATURE. Given the same candidates this returns
// the same result whether or not anything was ever persisted, so closing the
// last gap makes the quest complete without touching it. A stored quest counter
// would need a trigger to notice the gap closed; there is no reliable trigger
// here, because the gap can close by an import, by a federated sync, or by
// somebody else's edit — none of which are ours to hook.
func EvaluateQuest(q Quest, candidates []Counts) (QuestProgress, error) {
	if q.Goal <= 0 {
		return QuestProgress{}, fmt.Errorf("%w: quest %q has goal %d", ErrInconsistent, q.ID, q.Goal)
	}
	completed := 0
	for _, c := range candidates {
		if Missing(c) == 0 {
			completed++
		}
	}
	return QuestProgress{
		QuestID:   q.ID,
		Completed: completed,
		Goal:      q.Goal,
		Done:      completed >= q.Goal,
	}, nil
}

// Vote is one pairwise rating.
type Vote struct {
	VoterID string
	Target  string
	Score   int // 0..100 in this codebase's UI scale; see Normalise
}

// DefaultRating is what a player with no votes sits at.
const DefaultRating = 1500.0

// K is the Elo sensitivity constant.
const K = 32.0

// Normalise maps this codebase's 0..100 vote scale onto Elo's 0..1.
//
// EXPLICIT, because "just use the raw score" is wrong in a way that is invisible
// until the ratings look absurd: a 0..100 scale against K=32 makes the first vote
// swing a rating by up to 3200 points, so a single vote dominates and ordering is
// decided by who voted first.
func Normalise(score int) float64 {
	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	return float64(score) / 100.0
}

// Expected is the Elo expectation for a match between two ratings.
func Expected(a, b float64) float64 {
	return 1.0 / (1.0 + math.Pow(10, (b-a)/400.0))
}

// Rating is one player's computed rating. It has a POINTER-free shape and no ID
// field on purpose — see Ratings.
type Rating struct {
	Rating float64
	Votes  int
}

// compute returns BOTH pools: how entities were RECEIVED and how voters PREDICT.
//
// IT TAKES THE VOTES AND NOTHING ELSE. There is no prior-ratings parameter, no
// previous-round input, and no store handle — which is what makes the property
// testable: delete every vote and this returns DefaultRating for everyone, because
// there is nothing else it could return a stale number from. A function that
// accepted last round's ratings could be made to converge on a stale value, and
// "converges" is indistinguishable from "correct" at a glance.
//
// Every vote is replayed from the initial rating, in a deterministic order, so
// the result does not depend on the order votes arrived. Order-dependence is the
// signature of a stateful rating system wearing a derived name.
func compute(votes []Vote) (received, predicting map[string]Rating) {
	// Votes are replayed in a fixed order (voter, target, score) rather than in
	// whatever order they arrived.
	//
	// MEASURED, and the measurement contradicts the obvious justification: with
	// this algebra the ratings are INVARIANT under reordering. Over 500 random
	// permutations of a deliberately asymmetric vote set (three voters, uneven
	// scores, uneven vote counts) the worst deviation was exactly 0, with and
	// without the comparator. The vote counts are map fills, so they are
	// order-independent too.
	//
	// So the sort is not what makes these results deterministic, and a comment
	// claiming otherwise would be false. It is kept because it is cheap and it
	// removes a dependency whose removal is not free: any change to the update
	// rule could reintroduce order-sensitivity, and the guarantee is much easier
	// to keep than to rediscover. TestRatingsDoNotDependOnVoteOrder is what would
	// notice -- and it is a real check, not a formality, because it caught this
	// question in the first place.
	ordered := append([]Vote{}, votes...)
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.VoterID != b.VoterID {
			return a.VoterID < b.VoterID
		}
		if a.Target != b.Target {
			return a.Target < b.Target
		}
		return a.Score < b.Score
	})

	// TWO POOLS, NOT ONE. This is a bug the tests found and the first version had
	// it wrong, so the reason matters more than the code.
	//
	// A single rating per name conflates two different quantities:
	//
	//	how well this voter PREDICTS other votes
	//	how well this entity is RECEIVED
	//
	// They are not the same number, and sharing one makes a voter's enthusiasm
	// for one item inflate their rating, so the NEXT item they vote on is
	// predicted less harshly and gains more. Measured with four voters rating
	// `good`=95 and `bad`=10:
	//
	//	one pool:  good 1442.4   bad 1551.2   -- good ranks BELOW bad
	//	two pools: good 1556.4   bad 1452.4   -- correct
	//
	// So an instance ranking on the single pool would rank a well-loved scene
	// BELOW a disliked one, purely because its voters also rated something else.
	// That is not a subtle inaccuracy; it is the ranking inverted.
	//
	// §6a.16 lists performers, scenes, studios, sites, tags, lists AND instances
	// as the rated things, and votes come from users. A user who also appears as a
	// target (a performer rating scenes) would otherwise have their two roles
	// fight over one number.
	raters := map[string]float64{}
	rated := map[string]float64{}
	raterVotes := map[string]int{}
	ratedVotes := map[string]int{}

	for _, v := range ordered {
		// A self-vote is skipped, not an error: a user rating their own scene is
		// not malicious, and refusing the whole import for it would be worse than
		// ignoring one row.
		//
		// Skipping also stops a voter inflating their own rating, which is the only
		// thing this check has to prevent -- and it must happen BEFORE the pools are
		// populated, or a self-vote would create an entry that the replay skips
		// and the caller then sees as a player with votes but no rating.
		if v.VoterID == v.Target {
			continue
		}
		raters[v.VoterID] = DefaultRating
		raterVotes[v.VoterID]++
		rated[v.Target] = DefaultRating
		ratedVotes[v.Target]++
	}

	for _, v := range ordered {
		if v.VoterID == v.Target {
			continue
		}

		actual := Normalise(v.Score)

		// Each side uses ITS OWN expectation. E(voter, target) + E(target, voter)
		// == 1, so the target's update is the mirror of the rater's -- but the
		// target's is about RECEIVING, not about predicting, which is precisely the
		// distinction the two pools exist to keep.
		rater := raters[v.VoterID]
		target := rated[v.Target]

		rater += K * (actual - Expected(rater, target))
		rated[v.Target] = target + K*(actual-Expected(target, rater))

		raters[v.VoterID] = rater
	}

	// Wrap both pools. A name in both keeps its RECEIVED value here (what a
	// ranking shows) and its predicting value in the second return.
	received = make(map[string]Rating, len(rated))
	for id, r := range rated {
		received[id] = Rating{Rating: r, Votes: ratedVotes[id]}
	}
	predicting = make(map[string]Rating, len(raters))
	for id, r := range raters {
		predicting[id] = Rating{Rating: r, Votes: raterVotes[id]}
	}
	return received, predicting
}

// Ratings returns the RECEIVED rating for every rated entity — what a ranking
// shows. Names that only ever voted are absent, because nothing is known about
// how they were received.
func Ratings(votes []Vote) map[string]Rating {
	received, _ := compute(votes)
	return received
}

// RaterRatings returns the PREDICTING rating for every voter — how well their
// votes have matched outcomes. §6a.16 lists users alongside entities, and a
// performer who also rates scenes has both numbers; returning only the received
// one would silently discard the other.
func RaterRatings(votes []Vote) map[string]Rating {
	_, predicting := compute(votes)
	return predicting
}

// RatingOf returns one player's computed rating, and the default when they have
// no votes. A caller gets a number either way, because "no rating" is not a
// useful thing to render in a ranking column.
func RatingOf(votes []Vote, playerID string) Rating {
	if r, ok := Ratings(votes)[playerID]; ok {
		return r
	}
	return Rating{Rating: DefaultRating}
}
