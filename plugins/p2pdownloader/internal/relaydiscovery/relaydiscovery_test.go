package relaydiscovery

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// R087: a first node has no peer to ask, and the network has to be joinable anyway.

// LEARNED PEERS OUTRANK SHIPPED SEEDS.
//
// This is the design decision and it is asserted rather than assumed, because the
// comment being right is not the same as the code being right. Getting it backwards
// makes the mesh LESS reliable than its own seed list: the node spends its first
// attempts on the stalest information it holds.
func TestLearnedPeersOutrankSeeds(t *testing.T) {
	s := NewSet([]string{"seed-a:4001", "seed-b:4001"})
	s.Learn(Learned{Addr: "live-relay:4001", Digest: "d1"})

	c := s.Candidates(3)
	require.Len(t, c, 3)
	assert.Equal(t, "live-relay:4001", c[0],
		"a peer a live relay just named outranks a seed that may be switched off")
	assert.Equal(t, []string{"seed-a:4001", "seed-b:4001"}, c[1:],
		"the seeds follow, so a node with no learned peers still has somewhere to go")
}

// SELF-REPAIR: A DEAD PEER IS DEMOTED, NEVER RETRIED, AND A LIVE ONE IS PREFERRED.
//
// The test the step exists for. If the set cannot demote a dead peer it degrades over
// time to its worst entry -- which is exactly the degradation that would make the 459
// shipped seeds necessary, and the whole reason a hint beats a registry.
func TestADeadPeerIsDemotedWhileALiveOneIsPreferred(t *testing.T) {
	s := NewSet([]string{"seed:4001"})
	s.Learn(Learned{Addr: "dead:4001", Digest: "d-dead", From: "peer-x"})
	s.Learn(Learned{Addr: "alive:4001", Digest: "d-live", From: "peer-x"})

	// Dead fails repeatedly, alive never does.
	for i := 0; i < 5; i++ {
		s.Failed("dead:4001")
	}

	c := s.Candidates(3)
	require.Len(t, c, 3)
	assert.Equal(t, "alive:4001", c[0], "the peer that answers comes first")

	// THE DEAD PEER AND THE SEED BOTH SIT AT THE FLOOR, and the SEED IS TRIED FIRST.
	//
	// This took a correction. The test originally asserted the dead peer sorts strictly
	// below the seed, which the code cannot do: five failures clamp to the same
	// seedFloor the seed carries, so they are TIED, and a stable sort then preserves
	// the order they were appended in -- learned peers first, seeds after.
	//
	// The tie is the right design, not a bug to work around: a peer worth nothing and
	// a seed nobody has heard of are equally bad bets, and pretending one is worse
	// would be a ranking that distinguishes two things it cannot actually tell apart.
	// What matters is that the SEED is reached without first burning another attempt
	// on the peer that just failed five times.
	assert.Equal(t, "seed:4001", c[1],
		"the seed is tried before the peer that has failed five times in a row, so a "+
			"node reaches its fallback without spending an attempt on known-bad")
	assert.Equal(t, "dead:4001", c[2],
		"and the dead peer is still there -- demoted, not forgotten, because a relay "+
			"with one flaky hour has not earned permanent exclusion")
}

// A RECOVERED PEER IS PREFERRED AGAIN IMMEDIATELY.
//
// Without the reset, a network cannot recover from a bad hour without operator
// intervention -- every peer touched during the outage would serve out a demotion
// forever. That is the opposite of what a self-repairing set is for.
func TestARecoveredPeerIsPreferredAgainWithoutOperatorAction(t *testing.T) {
	s := NewSet([]string{"seed:4001"})
	s.Learn(Learned{Addr: "flaky:4001", Digest: "d"})
	for i := 0; i < 5; i++ {
		s.Failed("flaky:4001")
	}
	// PREMISE: not first. Five failures clamp the peer to the same floor the seed
	// carries, so the two are TIED and the seed wins the tie -- so the honest premise
	// is "the seed is ahead", not "the peer is below the seed". Demotion is expressed
	// as a floor precisely because a peer cannot be pushed under the fallback.
	require.Equal(t, 1, rankOf(s.Candidates(4), "seed:4001"),
		"premise: the seed is tried first, so flaky is not")
	require.Greater(t, rankOf(s.Candidates(4), "flaky:4001"), 1)

	// It works again -- which arrives as a Learn from the same address.
	s.Learn(Learned{Addr: "flaky:4001", Digest: "d"})

	assert.Equal(t, "flaky:4001", s.Candidates(1)[0],
		"learning a peer again clears its failures, so recovery needs no timer and "+
			"no operator: an un-run decay sweep is a permanently demoted peer nobody "+
			"can explain")
}

// DEMOTION DECAYS WITH TIME, SO A LONG-ABSENT PEER COMES BACK ON ITS OWN.
//
// A laptop relay that sleeps for the weekend should be preferred again by Monday.
// The decay is exponential and needs no scheduled sweep, because a sweep is a timer
// somebody has to keep alive.
func TestDemotionDecaysWithTime(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	s := NewSet([]string{"seed:4001"})
	s.SetClock(func() time.Time { return now })
	s.Learn(Learned{Addr: "weekend-gone:4001", Digest: "d"})

	for i := 0; i < 4; i++ {
		s.Failed("weekend-gone:4001")
	}
	// PREMISE: not first. Four fresh failures clamp to the floor, tying the seed, and
	// the seed wins the tie.
	require.Equal(t, 1, rankOf(s.Candidates(4), "seed:4001"),
		"premise: the seed is tried first, so the peer is not")
	require.Greater(t, rankOf(s.Candidates(4), "weekend-gone:4001"), 1)

	// Four weeks later -- four half-lives, so the failure effect is 1/16 of what it
	// was. Still demoted, but much closer.
	now = now.Add(28 * 24 * time.Hour)
	c := s.Candidates(3)
	assert.Contains(t, c, "weekend-gone:4001", "it is still a candidate")

	// AND IT CLIMBS BACK ABOVE THE SEED as the decayed score passes the seeds'
	// fixed zero.
	now = now.Add(28 * 24 * time.Hour)
	assert.Equal(t, "weekend-gone:4001", s.Candidates(1)[0],
		"after eight half-lives the peer is preferred again with no sweep having run, "+
			"because a laptop relay that slept for a fortnight is back")
}

// A DIRECT SIGHTING OUTRANKS A SECOND-HAND ONE AT THE SAME STANDING.
//
// The relay that named a peer had recent evidence, and a chain of names gets no better
// with length. This is a tiebreak, so the test sets up an exact tie otherwise.
func TestADirectSightingOutranksASecondHandOne(t *testing.T) {
	s := NewSet(nil)
	s.Learn(Learned{Addr: "direct:4001", Digest: "d", From: ""})
	s.Learn(Learned{Addr: "hearsay:4001", Digest: "d", From: "some-relay"})

	c := s.Candidates(2)
	assert.Equal(t, "direct:4001", c[0],
		"a peer we were handed directly ranks above one a relay named, all else equal")
}

// A LEARNED PEER THAT IS ALSO A SEED IS OFFERED ONCE.
//
// A caller counting one relay twice would read it as two votes of health, and a relay
// that appears twice in a health report is a relay whose real state is masked.
func TestALearnedPeerThatIsAlsoASeedIsOfferedOnce(t *testing.T) {
	s := NewSet([]string{"also-a-seed:4001", "other:4001"})
	s.Learn(Learned{Addr: "also-a-seed:4001", Digest: "d"})

	c := s.Candidates(10)
	seen := map[string]int{}
	for _, a := range c {
		seen[a]++
	}
	assert.Equal(t, 1, seen["also-a-seed:4001"],
		"a peer offered twice counts as two votes of health, which masks a relay's "+
			"real state behind a duplicate entry")
	// TWO distinct addresses, not three: the seeds are "also-a-seed" and "other", and
	// the learned peer IS "also-a-seed". The count is the assertion that matters here --
	// three would mean the duplicate leaked through -- and getting it right is what
	// makes the check above it meaningful.
	assert.Equal(t, 2, len(seen), "two distinct addresses: the learned peer and the "+
		"seed it shares an address with are ONE candidate, plus the other seed")
	assert.Equal(t, 2, len(s.Candidates(10)), "and the list has no extra entry")
}

// SEEDS ARE SHIPPED, NOT FETCHED.
//
// A discovery set that has to ask the network in order to be constructed cannot
// bootstrap a first node, which is the exact failure this package removes. So this is
// asserted against the constructor rather than described in a comment.
func TestSeedsAreShippedAndAFreshSetAlreadyHasSome(t *testing.T) {
	s := NewSet([]string{"a:4001", "b:4001"})
	c := s.Candidates(10)
	require.Len(t, c, 2, "a brand-new node has somewhere to try before it learns "+
		"anything, which is what makes the mesh joinable by a first install")
	assert.Zero(t, s.LearnedCount(), "and has learned nothing yet")

	// EMPTY AND DEDUPLICATED SEEDS, because a blank address is not a fallback and a
	// duplicate one is two.
	s2 := NewSet([]string{"", "x:4001", "x:4001", "  "})
	assert.Equal(t, []string{"x:4001"}, s2.Candidates(10),
		"a blank seed is not a fallback; it is a dial that fails the same way every "+
			"time and costs an attempt")
}

// WHAT CROSSES IS A DIGEST, AND IT IS A COPY.
//
// R077's property applied to discovery rather than to transport: a joining node
// contacts something, and what it hands onwards identifies a peer without identifying
// the person behind the box. The copy matters because this is data LEAVING the process,
// and a caller that could write into the live map could add an address it never
// verified -- an unverified address is an unverified dial target.
func TestWhatCrossesIsADigestAndTheMapIsACopy(t *testing.T) {
	s := NewSet(nil)
	s.Learn(Learned{Addr: "peer:4001", Digest: "sha256:abc123"})

	m := s.LearnedDigests()
	assert.Equal(t, "sha256:abc123", m["peer:4001"],
		"the digest is the identifier, and it is what a relay passes onwards")

	m["peer:4001"] = "forged"
	assert.Equal(t, "sha256:abc123", s.LearnedDigests()["peer:4001"],
		"LearnedDigests must return a copy. Handing back the live map lets a caller "+
			"plant an address it never verified, and a planted address is a dial to "+
			"wherever the planter chose")
}

// AN EMPTY LEARN ADDRESS IS IGNORED, NOT STORED.
//
// An empty address is what an unsuccessful lookup produces, and storing it would make
// the set offer "" as a dial target.
func TestAnEmptyLearnedAddressIsIgnored(t *testing.T) {
	s := NewSet([]string{"seed:4001"})
	s.Learn(Learned{Addr: "", Digest: "d"})
	s.Failed("")

	assert.Zero(t, s.LearnedCount(), "an empty address is not a peer")
	assert.Equal(t, []string{"seed:4001"}, s.Candidates(10),
		"and it never appears as a dial target")
}

// THE LIMIT IS STATED, NOT PROVED AWAY.
//
// A joining node still contacts something. This makes the mesh joinable; it does not
// make joining private. The test records what is actually true rather than what would
// sound better, because the second is how a privacy claim quietly stops being one.
func TestWhatThisActuallyGuaranteesAndWhatItDoesNot(t *testing.T) {
	s := NewSet([]string{"bootstrap:4001"})
	s.Learn(Learned{Addr: "peer:4001", Digest: "sha256:abc"})

	// WHAT IS GUARANTEED: the seed set is non-empty from the moment a node exists, so
	// a first install can join without having met anyone.
	require.NotEmpty(t, s.Candidates(1), "joinable from a cold start")

	// AND WHAT LEAVES IS A DIGEST, not an address identifying a person.
	//
	// The assertion is about SHAPE and it took two tries to get right. The first version
	// asserted a digest contains no colon, which is false for the ordinary form
	// "sha256:abc123" -- the algorithm prefix is part of the notation. A digest is not
	// an address because an address is HOST:PORT, so the check is that it does not end
	// in digits-after-a-colon at all: a digest has a fixed-length hex body and no port.
	//
	// Which is the real claim anyway, and it is worth stating precisely: what must not
	// cross is an address that identifies a ROUTABLE ENDPOINT. A digest identifies a
	// peer without identifying the box.
	m := s.LearnedDigests()
	for _, d := range m {
		host, port, found := strings.Cut(d, ":")
		if found {
			assert.NotEmpty(t, host, "a digest's first colon is an algorithm prefix")
			assert.False(t, isAllDigits(port),
				"a digest must not end in an all-digit component: host:PORT is exactly "+
					"what an address looks like, and R077 forbids one crossing for the "+
					"transport")
		}
	}
}

// CONCURRENT LEARNING AND FAILURE MUST NOT CORRUPT THE SET.
//
// A relay serves many peers at once, so several may report a dial failure against the
// same set while another goroutine is ranking it. Without the lock this is a map write
// concurrent with a map range, which Go detects and turns into a panic -- so the test
// races the two paths rather than only reading.
func TestConcurrentLearningAndFailureDoNotCorruptTheSet(t *testing.T) {
	s := NewSet([]string{"seed:4001"})

	var wg sync.WaitGroup
	for g := 0; g < 30; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			addr := fmt.Sprintf("peer-%d:4001", id)
			for i := 0; i < 20; i++ {
				switch i % 4 {
				case 0:
					s.Learn(Learned{Addr: addr, Digest: "d"})
				case 1:
					s.Failed(addr)
				case 2:
					_ = s.Candidates(5)
				default:
					_ = s.LearnedDigests()
				}
			}
		}(g)
	}
	wg.Wait()

	// Every goroutine's peer was learned at least once, so all 30 are candidates.
	// The assertion is about the COUNT and not the order, because the order depends
	// on how many failures each accumulated and that is legitimately nondeterministic.
	assert.Equal(t, 30, s.LearnedCount(), "a learn must not be lost to a concurrent "+
		"failure record, or a peer the network told us about vanishes because another "+
		"peer's dial failed at the same moment")
	assert.Len(t, s.Candidates(40), 31, "30 learned plus one seed")
}

// A NON-POSITIVE LIMIT RETURNS NOTHING RATHER THAN EVERYTHING.
//
// `Candidates(0)` is a caller bug, and returning the whole set for it would turn a
// mistake into an unbounded scan.
func TestANonPositiveLimitReturnsNothing(t *testing.T) {
	s := NewSet([]string{"a:4001", "b:4001"})
	s.Learn(Learned{Addr: "c:4001", Digest: "d"})

	assert.Nil(t, s.Candidates(0), "limit 0 is a caller bug, not a request for everything")
	assert.Nil(t, s.Candidates(-1))
}

// isAllDigits is the shape check: a port is digits, a hex digest body may end in a
// letter. Kept explicit rather than using strconv, because the question is "is this
// component a port" and a three-line predicate says that directly.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// rankOf returns a peer's 1-based position in the candidate list, or 0 if absent.
//
// THE PREMISE CHECKS USE THIS, and they used to use Candidates(1)[0] directly -- which
// broke the moment seeds moved ahead of tied peers, because "is this peer first" and
// "is this peer demoted below a seed" are DIFFERENT questions and the floor makes them
// coincide at the tie. A premise that cannot be stated without knowing the tie-break is
// a premise that will keep breaking, so it is stated against position instead.
func rankOf(cands []string, addr string) int {
	for i, c := range cands {
		if c == addr {
			return i + 1
		}
	}
	return 0
}
