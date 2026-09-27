package collab

import (
	"math"
	"sort"
)

// Reputation-weighted voting, adopted from Commons §8.2 in milestone M2b.
//
// # The problem this solves, stated precisely
//
// M2's arithmetic is Σ(+1) − Σ(−1) ≥ threshold, gated on distinct voters. The
// gate stops one account with three votes, and nothing else. Three accounts
// created in the same minute by the same person cross it exactly as easily as
// three people who have been here for a year. MinVoters is a speed bump, not a
// defence, and the spec is right that reputation weighting is the answer.
//
// # What weight is a function of
//
// Not volume. An account that has proposed a thousand times has not earned a
// thousand times the weight — it has demonstrated the same thing a hundred
// times. Reputation is agreement with *settled outcomes*, so weight tracks being
// right over time rather than being busy.
//
// # The two adjustments, and why they are not the same thing
//
// Decay: a user whose proposals are repeatedly rejected loses weight. This
// targets one identified user and is therefore a judgement, so it moves slowly
// and is visible in the user's own reputation number.
//
// Sybil damping: many accounts voting the same way are discounted. This
// targets a *pattern* and is not about any individual being wrong, so the spec
// requires coordinated patterns to be *flagged* rather than silently punished.
// Damping here reduces the weight those ballots carry; a separate mechanism
// raises the flag, and the difference matters — a user who can see they were
// damped can ask why, and a user silently punished for a pattern they did not
// create cannot.

// WeightBasis is one voter's contribution to a tally.
type WeightBasis struct {
	// UserID identifies the voter. Used to group correlated votes.
	UserID int

	// Value is +1 or −1. Kept as the caller supplied it rather than as a sign
	// derived from a magnitude, because the magnitude IS the weight and folding
	// them together loses the ability to explain a tally.
	Value int

	// Reputation is the voter's earned reputation on this field, from
	// ReputationSource. Per field, deliberately: agreeing about titles says
	// nothing about tags, and weight earned in one area must not be cashed in
	// to overrule another.
	Reputation int

	// FieldRejections is how many of this voter's proposals on this field have
	// been rejected. Drives decay.
	FieldRejections int
}

// ReputationSource supplies a voter's reputation for a field.
//
// An interface rather than a function because the real implementation reads
// per-field reputation from the database and the tests must be able to
// construct a tally without one. The distinction is the same one the rest of
// this package makes: governance logic stays pure, persistence lives outside.
type ReputationSource interface {
	// Reputation returns the voter's reputation on (targetType, field), and how
	// many of their proposals on that field have been rejected.
	//
	// An unknown user is 0 reputation, not an error: a new account is the
	// normal case and must be able to vote with a small weight rather than
	// being refused.
	Reputation(voterID int, targetType, field string) (reputation int, rejections int, err error)
}

// ReputationFunc adapts a function to ReputationSource.
type ReputationFunc func(voterID int, targetType, field string) (int, int, error)

func (f ReputationFunc) Reputation(voterID int, targetType, field string) (int, int, error) {
	return f(voterID, targetType, field)
}

// DecayPolicy is how weight falls on repeated rejection.
type DecayPolicy struct {
	// Enabled turns decay on. Off by default so that a deployment which has not
	// chosen a decay rate behaves as it did in M2.
	Enabled bool

	// Factor is the multiplier applied per rejection, in (0, 1). 0.5 halves a
	// voter's weight with each rejection of theirs.
	//
	// Values outside (0,1) are clamped rather than honoured: a Factor of 0
	// would strip a user's vote entirely, and a Factor above 1 would turn
	// rejection into a reward. Both are the sort of value that arrives from a
	// typo in a config file, and neither is ever what an operator meant.
	Factor float64

	// MinWeight is the floor on a single voter's weight, in basis points of a
	// fresh voter. It exists so that decay cannot reach zero and quietly
	// disenfranchise a user, which would make "they are being ignored" and
	// "they were silenced" indistinguishable from inside.
	MinWeight int
}

// DefaultDecay returns the proposed decay: halve per rejection, floor at 10%.
//
// The floor is 10% rather than 0 because a user's own reputation number is
// visible to them, and a weight that has decayed to nothing looks identical to a
// bug. A floor keeps the signal legible and keeps a determined user in the
// process.
func DefaultDecay() DecayPolicy {
	return DecayPolicy{Enabled: true, Factor: 0.5, MinWeight: 1000}
}

// Normalised clamps a decay policy into usable values.
func (d DecayPolicy) Normalised() DecayPolicy {
	out := d
	if out.Factor <= 0 || out.Factor >= 1 || math.IsNaN(out.Factor) {
		out.Factor = 0.5
	}
	if out.MinWeight < 0 {
		out.MinWeight = 0
	}
	return out
}

// SybilPolicy damps correlated voting.
type SybilPolicy struct {
	// Enabled turns damping on.
	Enabled bool

	// Diminishing returns the k-th identical ballot in a correlation group is
	// worth, as a fraction of the first. Expressed in basis points.
	//
	// This is the whole mechanism, and it is deliberately gentle: a group of
	// n accounts agreeing perfectly is not proof of Sybil — a community can
	// genuinely agree — so the k-th vote is worth less than the first, not
	// nothing. A hard cap would be a claim the data cannot support.
	DiminishingReturns int

	// GroupWindow is how many ballots are grouped together, in the order they
	// were cast. Correlation is computed over a window rather than over all
	// history because a group of ten accounts created years apart is not a
	// coordinated group, and lumping them together would punish an old
	// disagreement that has since been resolved.
	GroupWindow int
}

// DefaultSybil returns the proposed damping: the k-th identical ballot in a
// 10-vote window is worth 80% of the one before it.
//
// 80% rather than something harsher because a single gentle step is
// indistinguishable from noise, and the flagging mechanism is what actually
// surfaces a coordinated pattern. Damping exists to make one cheap without
// making it decisive.
func DefaultSybil() SybilPolicy {
	return SybilPolicy{Enabled: true, DiminishingReturns: 8000, GroupWindow: 10}
}

// Normalised clamps a Sybil policy into usable values.
func (s SybilPolicy) Normalised() SybilPolicy {
	out := s
	if out.DiminishingReturns <= 0 || out.DiminishingReturns > 10000 {
		out.DiminishingReturns = 10000
	}
	if out.GroupWindow < 2 {
		out.GroupWindow = 2
	}
	return out
}

// Weight is one voter's computed contribution.
//
// Kept as a struct rather than a bare int so that a tally can explain itself:
// "your ballot was worth 0.4 because your reputation on titles is 12 and you
// have been rejected twice here" is a sentence a moderator can say, and an int
// cannot.
type Weight struct {
	// UserID is the voter this weight belongs to. Carried on the weight rather
	// than kept alongside it, because the two are one fact: a weight without its
	// owner cannot be counted as a distinct voter, and a caller that has to
	// keep them in step is a caller that will eventually not.
	UserID int

	// Basis is the voter's weight in basis points, before Sybil damping. 10000
	// is a fresh voter with no reputation.
	Basis int

	// SybilFactor is the damping applied to this ballot, in basis points.
	// 10000 is undamped. Reported separately so a damped vote is visibly damped
	// rather than mysteriously small.
	SybilFactor int

	// Group is the correlation group this ballot was placed in, and Position is
	// its 1-based position within it. Both zero when damping is off.
	Group    int
	Position int

	// Decayed is true when the decay policy reduced this voter's basis.
	Decayed bool
}

// Effective is the ballot's final weight in basis points.
func (w Weight) Effective() int {
	return w.Basis * w.SybilFactor / 10000
}

// ReputationFloor is the weight of a voter with zero reputation, in basis
// points.
//
// A brand-new account can vote, and its vote counts — just less than everyone
// else's. The newcomer problem (#743) is that a new account should not be
// *ignored*; a floor rather than a zero is what distinguishes "not yet trusted"
// from "silenced", and those are different messages to a user.
const ReputationFloor = 2500

// ReputationToBasis converts reputation into a ballot weight in basis points.
//
// Sub-linear on purpose. Doubling someone's reputation should not double their
// vote, or weight becomes a lever rather than a signal and the top account
// decides every election. The square root gives early gains — where the newcomer
// problem lives — and diminishing later ones.
func ReputationToBasis(reputation int) int {
	if reputation < 0 {
		reputation = 0
	}
	// The scale is chosen so that reputation 100 — the reputation the spec
	// treats as "an ordinary established voter" — is exactly 10000, i.e. one
	// fresh voter's full weight. That makes the basis-point scale directly
	// comparable to the old net-vote count, so a quorum threshold carries over
	// without rescaling.
	//
	// sqrt(1e6 * 100) = 10000, which is where 1e6 comes from. The first version
	// used 10240 and the tests caught it: reputation 100 returned the floor
	// (2500) rather than 10000, so every voter looked like a newcomer and the
	// weighting did nothing at all. A magic constant that is nearly right is
	// worse than one that is obviously wrong, because the failure is silent.
	//
	// Computed in integer math via math.Sqrt on a float64, then floored. The
	// input is at most 1e6*1e9, well inside the exact integer range of float64,
	// so this is bit-identical on every platform — a ballot weight that differed
	// by one basis point between two machines would be a tally that could not be
	// reproduced from its own audit row.
	root := int(math.Sqrt(float64(reputation) * 1e6))
	if root < ReputationFloor {
		return ReputationFloor
	}
	// Cap, so one account cannot become a supermajority on its own. Ten times a
	// fresh voter is already a lot of weight for one account.
	const maxBasis = 100000
	if root > maxBasis {
		return maxBasis
	}
	return root
}

// ComputeWeights turns ballots into weights, applying decay and then damping.
//
// Order matters and is not interchangeable: decay is a property of one voter
// and is applied first; damping is a property of a group of voters and must see
// the decayed values, or a rejected voter in a coordinated group would keep
// voting at full weight because the group was counted before the penalty.
func ComputeWeights(basis []WeightBasis, decay DecayPolicy, sybil SybilPolicy) []Weight {
	decay = decay.Normalised()
	sybil = sybil.Normalised()

	out := make([]Weight, len(basis))

	// Pass 1: per-voter decay.
	for i, b := range basis {
		w := Weight{
			UserID:      b.UserID,
			Basis:       ReputationToBasis(b.Reputation),
			SybilFactor: 10000,
		}

		if decay.Enabled && b.FieldRejections > 0 {
			// Integer exponentiation rather than math.Pow so the result is
			// bit-identical everywhere. math.Pow(0.5, 3) is 0.125 exactly, but
			// math.Pow(0.9, 7) is not, and a ballot weight that differs by one
			// basis point between two machines is a tally that cannot be
			// reproduced from the audit row.
			reduced := w.Basis
			for n := 0; n < b.FieldRejections; n++ {
				reduced = reduced * int(decay.Factor*10000) / 10000
			}
			if reduced < decay.MinWeight {
				reduced = decay.MinWeight
			}
			if reduced != w.Basis {
				w.Decayed = true
				w.Basis = reduced
			}
		}

		out[i] = w
	}

	// Pass 2: Sybil damping over correlation groups.
	//
	// Groups are formed by identical vote VALUE within a sliding window, not by
	// user: a correlation group is "people who voted the same way", and grouping
	// by user would be a no-op that looked like it was doing something.
	if !sybil.Enabled {
		return out
	}

	group := 0
	position := 0
	for i := range basis {
		// A change of vote value starts a new group. Same value continues the
		// current one, within the window.
		if i > 0 && basis[i].Value == basis[i-1].Value && position < sybil.GroupWindow {
			position++
		} else {
			group++
			position = 1
		}

		out[i].Group = group
		out[i].Position = position

		// k-th ballot is worth DiminishingReturns^(k-1) of the first.
		//
		// A run of identical values is one correlation group, so position > 1
		// means this ballot agrees with the ones before it and is damped. A
		// ballot that changes the value starts a new group at position 1 and is
		// not damped at all.
		//
		// Damping the minority direction would be a serious bug: a bloc of five
		// voting for and one against would have its dissent discounted, which
		// makes a coordinated attack CHEAPER, not more expensive.
		out[i].SybilFactor = sybilFactorFor(position, sybil)
	}

	return out
}

// sybilFactorFor returns the basis-point weight of the position-th ballot in a
// group.
func sybilFactorFor(position int, sybil SybilPolicy) int {
	if position <= 1 {
		return 10000
	}
	factor := 10000
	for n := 1; n < position; n++ {
		factor = factor * sybil.DiminishingReturns / 10000
	}
	return factor
}

// WeightedTally is the result of weighting a set of ballots.
type WeightedTally struct {
	// Net is Σ(weight × value) in basis points, signed.
	//
	// Signed and weighted, so a strongly-supported "no" counts against a
	// weakly-supported "yes" — which is the entire reason to weight rather than
	// count.
	Net int

	// For and Against are the unsigned basis-point totals, kept separately
	// because the quorum gate needs them individually and a difference of two
	// large numbers is not the same fact as either of them.
	For     int
	Against int

	// Weight is the unsigned total, so a caller can express agreement as a
	// fraction without recomputing it from Net.
	Weight int

	// Voters is the count of DISTINCT users who voted. Unweighted and
	// undecayed, because it is the Sybil check: it answers "how many people",
	// which damping deliberately does not change the answer to.
	Voters int

	// Damped is how many ballots had their weight reduced by Sybil damping.
	// Surfaced so a moderation view can show that a pattern was noticed, which
	// is the difference between damping and silent punishment.
	Damped int
}

// Tally computes the weighted tally for a set of ballots.
//
// Takes the weights rather than the ballots because computing weights needs the
// policy and the grouping, and a caller that tallies without them would be
// reimplementing the decision function — the one thing this package exists to
// have in exactly one place.
func Tally(weights []Weight, values []int) WeightedTally {
	var t WeightedTally

	seen := map[int]struct{}{}
	for i, w := range weights {
		if i >= len(values) {
			// A weight with no matching ballot is a caller error. Skipping is
			// right rather than panicking: a tally that drops the excess is
			// visibly wrong, and a panic in the request path is worse.
			break
		}
		v := values[i]
		seen[w.UserID] = struct{}{}

		eff := w.Effective()
		if eff == 0 {
			// A ballot worth nothing still counts as a voter: somebody turned
			// up and said something. Excluding them from Voters would let an
			// instance with a heavy floor refuse to count its least-privileged
			// users at all, which is not what the floor is for.
			continue
		}
		if v < 0 {
			t.Against += eff
		} else {
			t.For += eff
		}
		if w.SybilFactor < 10000 {
			t.Damped++
		}
	}

	t.Net = t.For - t.Against
	t.Weight = t.For + t.Against
	t.Voters = len(seen)
	return t
}

// FlagCorrelation reports ballot indices that look coordinated.
//
// The spec is explicit that coordinated patterns are flagged, not silently
// punished, and this is the flag. It is separate from Tally on purpose: a caller
// may want the tally without a moderation alert, and a caller that wants an
// alert should not have to re-derive the pattern.
//
// The signal is a correlation group in which every ballot shares one value and
// none of the voters share an established reputation on the field — many
// accounts, no history, same answer.
func FlagCorrelation(weights []Weight, bases []WeightBasis, threshold int) []int {
	if threshold <= 0 {
		threshold = 5
	}

	byGroup := map[int][]int{}
	for i, w := range weights {
		if w.Group > 0 {
			byGroup[w.Group] = append(byGroup[w.Group], i)
		}
	}

	var flagged []int
	for _, idx := range byGroup {
		if len(idx) < threshold {
			continue
		}
		// Every member's ballot must be damped, which means they were all in the
		// same-value run, AND none of them may have reputation on the field.
		// A group of established users agreeing is consensus, not a pattern.
		allInexperienced := true
		for _, i := range idx {
			if i < len(bases) && bases[i].Reputation > 0 {
				allInexperienced = false
				break
			}
		}
		if allInexperienced {
			flagged = append(flagged, idx...)
		}
	}
	sort.Ints(flagged)
	return flagged
}
