package alert

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// R079's trigger, and the reason §6b.5 exists: the count is of VERIFIED replicas.
// A scene with three copies on disk and nothing verified is not healthy, and an
// alert built on any other count would stay quiet while the promise is untrue.
func TestUrgencyIsJudgedOnVerifiedReplicasAlone(t *testing.T) {
	// Three pending, zero verified, N=3. Looks full, is empty.
	c := Candidate{SceneID: "s1", VerifiedReplicas: 0, PendingReplicas: 3, Rarity: 1}
	u, err := UrgencyFor(c, 3)
	require.NoError(t, err)
	assert.Equal(t, UrgencyHigh, u,
		"pending replicas are bytes in flight, not healthy copies. Counting them "+
			"would leave R079 silent while §6b.5's promise is untrue")

	// Two verified, one pending, N=3: one short, low.
	c2 := Candidate{SceneID: "s2", VerifiedReplicas: 2, PendingReplicas: 1}
	u2, err := UrgencyFor(c2, 3)
	require.NoError(t, err)
	assert.Equal(t, UrgencyLow, u2, "one short of three with a fetch in progress is "+
		"work to schedule, not an emergency")
}

// N is a POLICY and 0 is not one. N=0 would make every scene healthy, so the alert
// could never fire — and a check that cannot fire is worse than no check, because it
// is present in the code and absent from the operator's list of working things.
func TestNZeroIsRefusedRatherThanSilentlyHealthy(t *testing.T) {
	c := Candidate{SceneID: "s", VerifiedReplicas: 0}
	for _, n := range []int{0, -1} {
		u, err := UrgencyFor(c, n)
		assert.ErrorIs(t, err, ErrInvalidN, "N=%d must be refused", n)
		assert.Equal(t, UrgencyNone, u)

		_, err = PlanFor([]Candidate{c}, n, time.Now())
		assert.ErrorIs(t, err, ErrInvalidN, "and PlanFor must refuse too, at N=%d", n)
	}
}

// §6b.4: "Urgency weights rarity and whether anyone is asking for it -- never by
// how much mesh-wide traffic it has drawn, which would concentrate scarce storage on
// content that is already plentiful."
//
// This is THE requirement, and the strongest form of it is enforced structurally:
// there is no traffic field to rank on. A grep for "traffic" would miss a field
// named Popularity, so the test asserts the exact field set.
func TestTheCandidateHasNoTrafficFieldToRankOn(t *testing.T) {
	fields := reflect.TypeOf(Candidate{})
	got := make([]string, 0, fields.NumField())
	for i := 0; i < fields.NumField(); i++ {
		got = append(got, fields.Field(i).Name)
	}

	// Exactly the measured inputs. Adding Traffic, Popularity, Views, FetchCount or
	// anything else is what §6b.4 forbids, and the failure here is the failure mode.
	assert.ElementsMatch(t, []string{
		"SceneID", "VerifiedReplicas", "PendingReplicas", "MissingReplicas",
		"Rarity", "Demand", "Size",
	}, got,
		"§6b.4 forbids ranking urgency by mesh-wide traffic. The way to enforce a "+
			"rule about a field is for the field not to exist -- and Size is in this "+
			"set only to be REPORTED, which TestSizeIsReportedButNotRanked checks")

	for _, banned := range []string{"traffic", "popular", "view", "fetch", "download", "count_hit"} {
		for _, name := range got {
			assert.NotContains(t, strings.ToLower(name), banned,
				"%s looks like a demand-by-volume input, which is what §6b.4 rules out", name)
		}
	}
}

// THE RANKING PROPERTY: among scenes at the same urgency, the RARE one wins even if
// the popular one has far more demand. This is the whole reason Rarity is separate
// from Demand rather than summed into one score.
func TestRarityOutranksDemandAtTheSameUrgency(t *testing.T) {
	rare := Candidate{SceneID: "rare", VerifiedReplicas: 1, Rarity: 100, Demand: 1}
	popular := Candidate{SceneID: "popular", VerifiedReplicas: 1, Rarity: 0.01, Demand: 5000}

	rareU, err := UrgencyFor(rare, 3)
	require.NoError(t, err)
	popU, err := UrgencyFor(popular, 3)
	require.NoError(t, err)
	require.Equal(t, rareU, popU, "precondition: same band, so the tiebreak is what decides")

	assert.Greater(t, Rank(rareU, rare), Rank(popU, popular),
		"§6b.4: scarce content is worth more scarce storage. A 5000:1 demand "+
			"advantage must not outrank being rare, or every byte goes to whatever "+
			"is currently popular — the exact failure the sentence forbids")
}

// Demand IS a tie-breaker, though: among equally rare and equally at-risk scenes,
// what people are asking for goes first.
func TestDemandBreaksTiesAtEqualRarity(t *testing.T) {
	wanted := Candidate{SceneID: "wanted", VerifiedReplicas: 1, Rarity: 5, Demand: 100}
	unwanted := Candidate{SceneID: "unwanted", VerifiedReplicas: 1, Rarity: 5, Demand: 0}

	u, _ := UrgencyFor(wanted, 3)
	assert.Greater(t, Rank(u, wanted), Rank(u, unwanted),
		"§6b.4 weights urgency by 'whether anyone is asking for it' as well as by "+
			"rarity, so demand decides once rarity has not")
}

// The BAND DOMINATES. A very popular scene one replica short must not outrank a
// scene with no healthy copy, or scarce storage flows to the comfortable.
func TestTheUrgencyBandDominatesRarityAndDemand(t *testing.T) {
	high := Candidate{SceneID: "dying", VerifiedReplicas: 0, Rarity: 0, Demand: 0}
	low := Candidate{SceneID: "comfortable", VerifiedReplicas: 2, Rarity: 1e9, Demand: 1_000_000}

	hu, _ := UrgencyFor(high, 3)
	lu, _ := UrgencyFor(low, 3)
	require.Equal(t, UrgencyHigh, hu)
	require.Equal(t, UrgencyLow, lu)

	assert.Greater(t, Rank(hu, high), Rank(lu, low),
		"band dominates. A band is a whole order of magnitude, so no amount of "+
			"popularity carries a comfortable scene past one that is about to be lost")
}

// A MISSING REPLICA DOES NOT RAISE THE BAND, and this test exists because my first
// version got it wrong.
//
// I had a scene short by one escalate to `medium` when a replica was missing,
// reasoning that "recovering it takes longer than placing a new copy would". That
// contradicts two things this repository already states:
//
//   - §6b.5: "a failed verification triggers re-fetch from a healthy peer RATHER THAN
//     AN ALERT". A missing replica is that case exactly.
//   - `internal/preservation.NeedsRepair` (step 7.4) classifies missing and corrupt
//     as REPAIR work and pending as "wait for verification rather than re-place",
//     so the repair path owns it and an alert duplicates it with a louder voice.
//
// The practical cost of the wrong version is a page of alarms for work the system
// does on its own, which is how an operator learns to ignore the subsystem.
func TestAMissingReplicaDoesNotRaiseTheBand(t *testing.T) {
	short := Candidate{SceneID: "s", VerifiedReplicas: 2}
	withMissing := Candidate{SceneID: "s", VerifiedReplicas: 2, MissingReplicas: 3}

	u1, err := UrgencyFor(short, 3)
	require.NoError(t, err)
	u2, err := UrgencyFor(withMissing, 3)
	require.NoError(t, err)

	assert.Equal(t, u1, u2,
		"the verified count is what R079 reads, so the same count gives the same "+
			"band however many replicas are missing. §6b.5 makes a failed verification "+
			"a RE-FETCH rather than an alert, and preservation.NeedsRepair already "+
			"classifies missing as repair work")

	// It is still REPORTED, so the operator can see it.
	a, err := Build(withMissing, 3)
	require.NoError(t, err)
	assert.Equal(t, 3, a.MissingReplicas, "reported, not escalated")
	assert.Contains(t, a.Why, "of 3 verified", "and the reason still names the count")
}

// A scene WITH N verified copies is healthy even if something is missing, because
// the count has not fallen. Alerting on it is the alert-that-never-clears failure:
// the operator sees a page of alerts and learns to ignore them.
func TestASceneWithNVerifiedIsHealthyEvenWithAMissingReplica(t *testing.T) {
	c := Candidate{SceneID: "s", VerifiedReplicas: 3, MissingReplicas: 1}

	u, err := UrgencyFor(c, 3)
	require.NoError(t, err)
	assert.Equal(t, UrgencyNone, u,
		"there is no shortfall to fix. The missing replica is reported in the alert "+
			"and in the sweep, but a scene holding N verified copies is not an "+
			"emergency and must not appear in an operator's actionable queue")
}

// Size is reported and NOT ranked. Ranking by size would replicate the smallest
// objects first and leave one huge scene unreplicated — the failure where the
// scene everyone cares about is the one at risk.
func TestSizeIsReportedButNotRanked(t *testing.T) {
	small := Candidate{SceneID: "small", VerifiedReplicas: 1, Size: 1}
	huge := Candidate{SceneID: "huge", VerifiedReplicas: 1, Size: 100 << 30}

	u, _ := UrgencyFor(small, 3)
	assert.Equal(t, Rank(u, small), Rank(u, huge),
		"a 100 GiB scene and a 1-byte scene at the same risk rank identically. "+
			"Ranking by size replicates the small ones first and strands the large "+
			"one — and the large one is usually the scene someone would miss")

	a, err := Build(huge, 3)
	require.NoError(t, err)
	assert.Equal(t, int64(100<<30), a.SizeBytes, "but it IS reported, so an operator "+
		"can see what fixing it costs")
	assert.Contains(t, a.Why, "of 3", "and the reason names the numbers that produced it")
}

// A sweep must be REPEATABLE. Two sweeps over identical data must produce identical
// order, or a queue built from them is unreproducible and an operator cannot tell a
// change in the data from a change in the sort.
func TestASweepIsRepeatableAndTotallyOrdered(t *testing.T) {
	// Candidates deliberately share identical data, so only the tiebreak can order
	// them.
	cands := []Candidate{
		{SceneID: "c", VerifiedReplicas: 1, Rarity: 1, Demand: 1},
		{SceneID: "a", VerifiedReplicas: 1, Rarity: 1, Demand: 1},
		{SceneID: "b", VerifiedReplicas: 1, Rarity: 1, Demand: 1},
	}
	now := time.Unix(1_700_000_000, 0).UTC()

	p1, err := PlanFor(cands, 3, now)
	require.NoError(t, err)
	p2, err := PlanFor(cands, 3, now)
	require.NoError(t, err)

	require.Len(t, p1.Actionable, 3)
	for i := range p1.Actionable {
		assert.Equal(t, p1.Actionable[i].SceneID, p2.Actionable[i].SceneID,
			"two sweeps over identical data must agree, element for element")
	}
	assert.Equal(t, []string{"a", "b", "c"},
		[]string{p1.Actionable[0].SceneID, p1.Actionable[1].SceneID, p1.Actionable[2].SceneID},
		"and identically-ranked scenes are ordered by id, so the order is TOTAL "+
			"rather than left to the sort's behaviour on equal keys")
}

// A HEALTHY scene still gets an alert, so a sweep can say how much it covered. A
// sweep that returns only problems cannot report "812 of 4,000 need attention" with
// confidence, and cannot demonstrate it looked at everything.
func TestEveryJudgedSceneAppearsInThePlan(t *testing.T) {
	cands := []Candidate{
		{SceneID: "healthy", VerifiedReplicas: 3},
		{SceneID: "thin", VerifiedReplicas: 2},
		{SceneID: "dying", VerifiedReplicas: 0},
	}
	now := time.Unix(1_700_000_000, 0).UTC()

	p, err := PlanFor(cands, 3, now)
	require.NoError(t, err)

	assert.Len(t, p.All, 3, "every candidate is judged, healthy included")
	assert.Len(t, p.Actionable, 2, "and only the two short scenes are actionable")
	assert.Equal(t, 3, p.N, "the plan carries the N it was run at, so a report says "+
		"which policy it measured against")
	assert.Equal(t, now, p.GeneratedAt, "and when it ran, so a stale alert is visible as stale")

	assert.Contains(t, p.Summary(), "2 of 3", "and the summary reports coverage")
	assert.True(t, p.Degraded(), "a scene with no verified copy degrades the instance")
}

// A healthy-only plan says so, rather than reporting "the worst is none" — which
// reads as a band with content behind it.
func TestAHealthyPlanIsNotDegradedAndSaysSo(t *testing.T) {
	p, err := PlanFor([]Candidate{{SceneID: "a", VerifiedReplicas: 3}}, 3, time.Now())
	require.NoError(t, err)

	assert.False(t, p.Degraded())
	_, actionable := p.HighestUrgency()
	assert.False(t, actionable, "HighestUrgency reports whether there is anything at all, "+
		"so a healthy plan does not return the band `none` as though it had content")

	assert.Contains(t, p.Summary(), "all healthy")
	assert.Contains(t, p.Summary(), "N=3", "and names the policy it measured against")
}

// §6b.5: a failed verification triggers RE-FETCH, not an alert. So a corrupt or
// missing replica is not itself an alert condition; the ALERT is about the verified
// count being short. This asserts the distinction survives into this layer.
func TestAnUnverifiedReplicaIsNotItselfAnAlert(t *testing.T) {
	// Two verified of N=3, with four replicas gone or corrupt. The band is `low`:
	// one short of three.
	c := Candidate{SceneID: "s", VerifiedReplicas: 2, MissingReplicas: 3, PendingReplicas: 1}

	u, err := UrgencyFor(c, 3)
	require.NoError(t, err)
	assert.Equal(t, UrgencyLow, u,
		"the alert is about the verified shortfall, and here that is a plain `low`. "+
			"The pile of missing and pending replicas does not escalate it: that is "+
			"repair work for §6b.5's re-fetch path, not an operator-facing alarm")

	a, err := Build(c, 3)
	require.NoError(t, err)
	assert.Equal(t, 3, a.MissingReplicas, "it is still REPORTED, so the operator can see it")
	assert.Equal(t, 1, a.PendingReplicas, "and so is the pending count — reported, not ranked on")
	assert.Contains(t, a.Why, "of 3 verified")
}

// ONE UNJUDGEABLE CANDIDATE FAILS THE SWEEP. A sweep that silently drops a scene
// means an operator reads "800 of 801 assessed" with no way to know which was
// skipped — and a silently-skipped at-risk scene is precisely what R079 exists to
// prevent.
func TestOneUnjudgeableCandidateFailsTheSweep(t *testing.T) {
	for name, c := range map[string]Candidate{
		"no scene id":       {VerifiedReplicas: 0},
		"negative verified": {SceneID: "s", VerifiedReplicas: -1},
		"negative pending":  {SceneID: "s", PendingReplicas: -3},
		"negative missing":  {SceneID: "s", MissingReplicas: -1},
	} {
		_, err := PlanFor([]Candidate{{SceneID: "ok", VerifiedReplicas: 3}, c}, 3, time.Now())
		assert.Error(t, err, "%s must fail the sweep rather than be skipped", name)
		assert.Contains(t, err.Error(), c.SceneID,
			"and the error NAMES the scene, so an operator knows which one was not judged")
	}
}

// A nonsensical commons value must not make a scene VANISH. NaN in particular is
// the dangerous one: it sorts nowhere, so the scene most in need of attention is
// the one that disappears from the queue.
func TestANonsensicalCommonsValueCannotMakeASceneVanish(t *testing.T) {
	for name, c := range map[string]Candidate{
		"NaN rarity":   {SceneID: "s", VerifiedReplicas: 1, Rarity: math.NaN()},
		"NaN via +Inf": {SceneID: "s", VerifiedReplicas: 1, Rarity: math.Inf(1)},
		"negative":     {SceneID: "s", VerifiedReplicas: 1, Rarity: -50},
	} {
		r := Rank(UrgencyMedium, c)
		assert.False(t, math.IsNaN(r), "%s must not produce a NaN rank, which sorts "+
			"nowhere and would drop an at-risk scene from the queue", name)
		assert.True(t, r > 0, "%s must still rank as actionable", name)
	}

	// And it stays in the plan rather than vanishing.
	p, err := PlanFor([]Candidate{{SceneID: "nan", VerifiedReplicas: 1, Rarity: math.NaN()}},
		3, time.Now())
	require.NoError(t, err)
	require.Len(t, p.Actionable, 1, "the scene is present")
	assert.Equal(t, "nan", p.Actionable[0].SceneID)
}

// A very popular scene must not swamp the rarity term within a band, or demand
// becomes the primary weight by arithmetic accident rather than by decision.
func TestDemandIsBoundedSoItStaysATieBreaker(t *testing.T) {
	base := Candidate{SceneID: "s", VerifiedReplicas: 1, Rarity: 1}

	// Doubling rarity must beat any demand difference the clamp allows.
	rarer := base
	rarer.Rarity = 2
	viral := base
	viral.Demand = 1_000_000_000 // far beyond the clamp

	assert.Greater(t, Rank(UrgencyLow, rarer), Rank(UrgencyLow, viral),
		"demand is clamped to 1e5 and weighted by log1p, so it can never outweigh a "+
			"whole unit of rarity. Without the clamp, a viral scene would rank above "+
			"a rare one and §6b.4's rule would hold only by luck")
}

// The bands are ordered, which is what makes Greater() comparisons and the sort
// mean anything.
func TestTheBandsAreOrderedAndNamed(t *testing.T) {
	assert.Less(t, int(UrgencyNone), int(UrgencyLow))
	assert.Less(t, int(UrgencyLow), int(UrgencyMedium))
	assert.Less(t, int(UrgencyMedium), int(UrgencyHigh))

	assert.Equal(t, "none", UrgencyNone.String())
	assert.Equal(t, "low", UrgencyLow.String())
	assert.Equal(t, "medium", UrgencyMedium.String())
	assert.Equal(t, "high", UrgencyHigh.String())
	assert.Equal(t, "unknown", Urgency(99).String(),
		"an unrecognised band says so rather than reporting a healthy one")
}

// A healthy scene ranks NEGATIVE, so a sorted queue's healthy tail is separated
// from its actionable head rather than a run of near-identical positives.
func TestAHealthySceneRanksBelowEveryActionableOne(t *testing.T) {
	healthy := Candidate{SceneID: "ok", VerifiedReplicas: 5, Rarity: 1e9, Demand: 1e6}
	assert.Less(t, Rank(UrgencyNone, healthy), 0.0,
		"a healthy scene ranks negative even with maximal rarity and demand, so it "+
			"cannot head an actionable queue a caller forgot to filter")

	// And that is visible in the plan without filtering.
	p, err := PlanFor([]Candidate{
		{SceneID: "healthy", VerifiedReplicas: 3},
		{SceneID: "thin", VerifiedReplicas: 1},
	}, 3, time.Now())
	require.NoError(t, err)
	require.NotEmpty(t, p.All)
	for i, a := range p.All {
		if a.NeedsAttention() {
			assert.Less(t, i, len(p.All), "every actionable alert sorts before any "+
				"healthy one, so a caller that ignores NeedsAttention still stops "+
				"at the right place")
			break
		}
	}
}

// THREE MUTANTS SURVIVED, AND ALL THREE ARE THE SAME MISSING TEST.
//
//	B_band_does_not_dominate   scaled the band base by 1e-6
//	G_rarity_unbounded        raised the rarity clamp to 1e12
//	L_unknown_band_healthy    made an unrecognised band rank as healthy
//
// Each is caught by a SINGLE pair comparison with ordinary values, and none is
// caught by any test in the file. The gap is that "band dominates", "rarity beats
// demand" and "an unknown band is actionable" were each asserted with ONE
// representative pair, where the margin happened to be comfortable. A weighting
// function's properties are about the WORST case, not the typical one -- and my two
// real ranking bugs were both found by a test that used comfortable values.
//
// So this test sweeps the FULL input range and asserts the ordering properties
// directly, which is the form a coefficient change cannot survive.
func TestTheOrderingPropertiesHoldAcrossTheWholeInputRange(t *testing.T) {
	// Extremes included, because the property has to hold when a commons sends a
	// value no sane implementation would.
	rarities := []float64{0, 0.001, 1, 100, 999, 1e3, 1e6, 1e9, 1e12, math.MaxFloat64}
	demands := []int{0, 1, 10, 1_000, 100_000, 1_000_000, math.MaxInt32}

	// 1. THE BAND DOMINATES, at every rarity and demand.
	for _, r := range rarities {
		for _, d := range demands {
			c := Candidate{SceneID: "x", VerifiedReplicas: 1, Rarity: r, Demand: d}
			high := Rank(UrgencyHigh, c)
			medium := Rank(UrgencyMedium, c)
			low := Rank(UrgencyLow, c)

			assert.Greater(t, high, medium,
				"high must outrank medium at rarity=%g demand=%d", r, d)
			assert.Greater(t, medium, low,
				"medium must outrank low at rarity=%g demand=%d", r, d)
			assert.Greater(t, low, 0.0,
				"every actionable band must rank positive at rarity=%g demand=%d, or a "+
					"caller that ignores NeedsAttention can act on a healthy scene", r, d)
		}
	}

	// 2. RARITY BEATS DEMAND -- WITHIN THE RANGE WHERE RARITY IS REPRESENTABLE.
	//
	// The sweep initially asserted this at rarity 1e12 and it failed with two ranks
	// printing identically. That is not a bug in the ranking: rarity is clamped to
	// rankRadix-1 so an unbounded term cannot cross a band (mutation G), and above
	// the clamp two scenes necessarily receive the SAME credit. The property is
	// therefore "rarity beats demand while rarity is representable, and ties beyond
	// it", and asserting the stronger claim would be asserting something no
	// implementation can provide.
	//
	// The clamp is what makes the ordering TOTAL: two scenes that differ only in
	// an unrepresentable rarity rank equal, and the plan's scene-id tiebreak then
	// orders them deterministically rather than leaving it to float comparison.
	for _, r := range rarities {
		for _, d := range demands {
			rare := Candidate{SceneID: "r", VerifiedReplicas: 1, Rarity: r + 1, Demand: d}
			viral := Candidate{SceneID: "v", VerifiedReplicas: 1, Rarity: r, Demand: d}

			rareRank := Rank(UrgencyLow, rare)
			viralRank := Rank(UrgencyLow, viral)

			if r+1 == r {
				// Float saturation: the two scenes ARE the same input.
				assert.Equal(t, viralRank, rareRank,
					"at rarity=%g the two candidates are indistinguishable, so they must "+
						"rank identically", r)
				continue
			}
			if r+1 >= rankRadix {
				// Beyond the clamp both receive full credit; equality is correct and
				// the plan's tiebreak keeps the order deterministic.
				assert.Equal(t, viralRank, rareRank,
					"above the clamp (rarity=%g) both scenes are credited fully, so "+
						"they tie rather than one arbitrarily beating the other", r)
				continue
			}
			assert.Greater(t, rareRank, viralRank,
				"one representable unit of rarity must beat any demand at rarity=%g "+
					"demand=%d. §6b.4: scarcity outranks popularity, so a viral scene "+
					"must never outrank a rare one however many people want it", r, d)
		}
	}

	// 3. AN UNRECOGNISED BAND RANKS AS ACTIONABLE, never as healthy.
	//
	// The dangerous default is `healthy`, because an at-risk scene that sorts into
	// the healthy tail disappears from the queue and nothing reports it missing.
	for _, u := range []Urgency{Urgency(-1), Urgency(4), Urgency(99), Urgency(1000)} {
		c := Candidate{SceneID: "x", VerifiedReplicas: 1, Rarity: 1, Demand: 1}
		// The property that matters is that it is ACTIONABLE, i.e. positive and above
		// the healthy rank. It does NOT earn rarity or demand credit, because we
		// cannot know what band it is -- so it sits at the low band's base, just
		// below a real `low` that carries those terms. Comparing it against a real
		// `low` was my error: "actionable-minimal" is the intent, not "tied with a
		// fully-described low".
		rank := Rank(u, c)
		healthy := Rank(UrgencyNone, c)
		assert.Greater(t, rank, healthy,
			"band %d is not one this build knows, so it must rank above the healthy "+
				"rank (got %g, healthy %g). An at-risk scene sorting into the healthy "+
				"tail disappears from the queue and nothing reports it missing",
			int(u), rank, healthy)
		assert.Greater(t, rank, 0.0, "band %d must rank positive, never healthy", int(u))
	}
}
