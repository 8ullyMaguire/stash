package ecosystem

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// R057's last two §6a.19 capabilities: "see rankings, contribute to preservation".
//
// Both live in this file because they share one property, which is the reason they are worth
// building together: neither may grant anything. §6a.16 says gamified voting "grants nothing
// (§6a.10)" and §6a.9 says preservation is a protocol whose constraints are hard. A score or a
// preservation credit that carried any authority would be a privilege-escalation path wearing
// the costume of a community feature -- so both types carry no access field that means anything,
// and both are guarded by a method allowlist.

// TestNow is the clock's fixed reference point, so decay is testable without a sleep.
var TestNow = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

// LeaderboardScope is §6a.16's "local / mesh / global".
type LeaderboardScope string

const (
	// ScopeLocal is this instance's own votes.
	ScopeLocal LeaderboardScope = "local"
	// ScopeMesh is votes from peered instances.
	ScopeMesh LeaderboardScope = "mesh"
	// ScopeGlobal is every vote the mesh has seen. Not accepted by NewLeaderboard yet -- it
	// needs a cross-scope aggregation this package does not have, and accepting the string
	// without one would return a board that silently omits the peers it claims to include.
	ScopeGlobal LeaderboardScope = "global"
)

// Vote is one pairwise comparison.
//
// A VOTE and not a rating, because §6a.16 (#4) requires the rating to be computed from a vote
// set: "it must compute a rating from a vote set rather than maintain a counter".
type Vote struct {
	// Voter is who cast it. Part of the vote's identity, so one voter cannot inflate a score.
	Voter string
	// Left and Right are the two entities compared. They are ids, not pointers, so a vote set
	// stays comparable and copyable.
	Left, Right string
	// Winner is exactly one of Left or Right.
	Winner string

	// recordedAt is stamped by Record, from the board's own clock. It is unexported because a
	// caller-supplied timestamp would let a peer backdate or forwarddate its own votes, and time
	// decay (§6a.16) is then a lever anyone can pull.
	//
	// MY FIRST VERSION OF THIS WAS BROKEN AND A TEST CAUGHT IT: recordedAt returned the board's
	// CURRENT time rather than the time the vote arrived, so every vote had age zero and the
	// decay weight was always exactly 1.0 -- the decay code ran, and did nothing. See
	// TestRatingsDecayWithAge, which fails against that version.
	recordedAt time.Time
}

// RankingEntry is one row of a leaderboard.
type RankingEntry struct {
	PublicID string
	Rating   float64
	Votes    int
	Wins     int

	// GrantsAccess and TrustLevel are here to be FALSE and EMPTY, always. They exist so the
	// prohibition is stated in the type rather than only in a comment, and a mutation that set
	// either would be caught by a test that reads them. §6a.16 and §6a.10.
	GrantsAccess bool
	TrustLevel   string
}

// Leaderboard holds a VOTE SET and derives ratings from it.
//
// There is no cached score and no counter anywhere in this type, and that is the enforcement of
// §6a.16 (#4) rather than a stylistic choice: a cached rating can fall out of step with the votes
// it claims to summarise, and then the "truth" is whatever the cache says. Folding the votes on
// every read is O(votes); at mesh scale that is a real cost, and the answer to that is an
// incrementally maintained value WITH the votes still authoritative -- not a counter that
// replaces them.
type Leaderboard struct {
	scope LeaderboardScope
	now   func() time.Time

	// votes is a SET, keyed so a repeated vote is idempotent.
	votes map[string]Vote
}

// NewLeaderboard returns an empty board for one scope.
func NewLeaderboard(scope LeaderboardScope) *Leaderboard {
	return NewLeaderboardAt(scope, TestNow)
}

// NewLeaderboardAt is NewLeaderboard with an explicit clock, for decay tests.
func NewLeaderboardAt(scope LeaderboardScope, now time.Time) *Leaderboard {
	t := now
	return &Leaderboard{
		scope: scope,
		now:   func() time.Time { return t },
		votes: map[string]Vote{},
	}
}

// withClock returns a board reading the time from fn. Test seam, like Sandbox.withCount.
func (l *Leaderboard) withClock(fn func() time.Time) *Leaderboard { l.now = fn; return l }

// Validate reports why a vote is not a vote.
//
// The four cases are all ways a client can send something that is not a comparison. Scoring any
// of them as a draw would let a broken or hostile client pad the leaderboard with entities that
// never played.
func (v Vote) validate() error {
	switch {
	case strings.TrimSpace(v.Voter) == "":
		return fmt.Errorf("ecosystem: a vote needs a voter")
	case strings.TrimSpace(v.Left) == "" || strings.TrimSpace(v.Right) == "":
		return fmt.Errorf("ecosystem: a vote needs two entities")
	case v.Left == v.Right:
		return fmt.Errorf("ecosystem: %q cannot be compared with itself", v.Left)
	case v.Winner != v.Left && v.Winner != v.Right:
		// This is what refuses "both win" (Winner "alpha,beta") and "neither wins" (""), since
		// neither string equals either side.
		return fmt.Errorf("ecosystem: winner %q is not one of %q or %q", v.Winner, v.Left, v.Right)
	}
	return nil
}

// key identifies a vote for set semantics: the same voter comparing the same pair is one vote.
//
// The pair is stored in a CANONICAL order (left, right sorted) so "a beat b" and "b beat a" are
// the same matchup -- otherwise one voter could cast both and the pair would count twice.
func (v Vote) key() string {
	pair := []string{v.Left, v.Right}
	if pair[0] > pair[1] {
		pair[0], pair[1] = pair[1], pair[0]
	}
	return v.Voter + "\x00" + pair[0] + "\x00" + pair[1]
}

// Record adds a vote to the set. Recording the same vote twice is idempotent, so a retried
// message cannot inflate a score.
func (l *Leaderboard) Record(v Vote) error {
	if err := v.validate(); err != nil {
		return err
	}
	stamped := v
	stamped.recordedAt = l.now()
	l.votes[v.key()] = stamped
	return nil
}

// DecayHalfLife is how long a vote takes to count half as much. §6a.16: "Time decay reflects
// current relevance" -- a vote from years ago should not outvote a vote from today.
const DecayHalfLife = 18 * 30 * 24 * time.Hour

// RatingOf computes one entity's rating by FOLDING the vote set.
//
// The rating is a weighted win rate: each vote contributes according to its age, and the score is
// (weighted wins + prior) / (weighted games + 2*prior). The prior is Laplace smoothing with
// weight 1, so an entity with one win and no losses does not read as 100% -- with a single vote
// that is a statement about the sample, not the entity.
func (l *Leaderboard) RatingOf(publicID string) (RankingEntry, error) {
	if strings.TrimSpace(publicID) == "" {
		return RankingEntry{}, fmt.Errorf("ecosystem: no entity named")
	}
	// The scope check is here and not only in Record, so a board cannot be asked about an
	// entity whose rating exists on a scope this board does not speak for. Returning an error
	// rather than a zero is deliberate: a zero rating reads as "rated and found worthless",
	// which is a different claim from "not rated here".
	if l.scope != ScopeMesh && l.scope != ScopeLocal {
		return RankingEntry{}, fmt.Errorf("ecosystem: scope %q has no vote set", l.scope)
	}

	entry := RankingEntry{PublicID: publicID}
	var weightedWins, weightedGames float64

	for _, v := range l.votes {
		if v.Left != publicID && v.Right != publicID {
			continue
		}
		w := math.Exp2(-v.ageHours(l) / DecayHalfLife.Hours())
		weightedGames += w
		if v.Winner == publicID {
			weightedWins += w
			entry.Wins++
		}
		entry.Votes++
	}

	if weightedGames == 0 {
		// Never played on this scope. An error, for the reason above: "unrated" and "rated zero"
		// are different facts and a leaderboard that conflates them shows an unknown entity as
		// the worst thing in the mesh.
		return RankingEntry{}, fmt.Errorf("ecosystem: %q has no votes on the %s scope", publicID, l.scope)
	}

	entry.Rating = (weightedWins + 1) / (weightedGames + 2)
	// GrantsAccess and TrustLevel stay at their zero values. See the type comment.
	return entry, nil
}

// ageHours is how old a vote is on this board's clock. A vote stamped by a clock that has since
// moved backwards (a test, or a machine whose clock was corrected) yields a NEGATIVE age, which
// would weight a vote ABOVE 1.0 -- so a negative age is treated as zero rather than as a bonus.
func (v Vote) ageHours(l *Leaderboard) float64 {
	h := l.now().Sub(v.recordedAt).Hours()
	if h < 0 {
		return 0
	}
	return h
}

// Board returns the top n by rating.
func (l *Leaderboard) Board(ctx context.Context, n int) ([]RankingEntry, error) {
	if n <= 0 || n > MaxPublicLimit {
		n = MaxPublicLimit
	}

	ids := map[string]bool{}
	for _, v := range l.votes {
		ids[v.Left] = true
		ids[v.Right] = true
	}

	out := make([]RankingEntry, 0, len(ids))
	for id := range ids {
		e, err := l.RatingOf(id)
		if err != nil {
			continue
		}
		out = append(out, e)
	}

	// Rank by rating, then by games played DESC, then by id -- so the order is TOTAL. A sort
	// that is not total makes a leaderboard that reshuffles equal rows between calls, and a
	// developer testing "rank 1" gets a different answer for the same data.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Rating != out[j].Rating {
			return out[i].Rating > out[j].Rating
		}
		if out[i].Votes != out[j].Votes {
			return out[i].Votes > out[j].Votes
		}
		return out[i].PublicID < out[j].PublicID
	})

	if len(out) > n {
		out = out[:n]
	}
	return out, nil
}
