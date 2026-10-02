// Package relaydiscovery is how a node finds a relay when it has never met one.
// R087, step 8.9.
//
// # THE PROBLEM, AND WHY THE OBVIOUS ANSWER IS WRONG
//
// "The first node has no peer to ask." A mesh whose entry requires a mesh member
// cannot be joined by a first install -- so the property is unreachable for exactly
// the user most likely to want it, a lone preservationist with one library.
//
// `cmd/probe_bootstrap` measured the three candidates on 2026-10-02. The number that
// reframed the problem:
//
//	P(a joining node finds at least ONE live relay) = 1 - (1-p)^N
//
//	     p=0.50   N=7    for 99%
//	     p=0.10   N=44   for 99%
//	     p=0.01   N=459  for 99%
//
// 459 hardcoded addresses is a REGISTRY, and a registry is a central point that can
// be subpoenaed, seized or unmaintained. So the number only has that shape if peers
// CANNOT learn each other.
//
// Make learning possible and the hardcoded list stops being a registry and becomes a
// HINT. It does not have to stay accurate, because its failure mode is a timeout
// rather than a wrong answer. That is the entire reason seven seeds is enough and 459
// is not, and this package is the part that makes it true.
//
// # WHY LEARNED PEERS OUTRANK SHIPPED SEEDS
//
// A seed is a guess about a machine that may be switched off. A peer a live relay
// named was alive minutes ago. Ranking seeds first would make the network LESS
// reliable than the seed list alone -- it would spend its first attempts on the
// stalest information it holds. So `Candidates` returns learned peers before seeds,
// and the caller only falls through to a seed when every learned peer has been tried.
//
// # WHAT CROSSES DURING PEER EXCHANGE
//
// A DIGEST, never an address that identifies the person behind the box. This is R077's
// property applied to discovery rather than to transport, and it is the same property:
// the relay learns that a peer exists, and does not learn whose library it is carrying.
//
// The limit is stated rather than glossed: a joining node still contacts SOMETHING. This
// makes the mesh joinable; it does not make joining private. The honest claim is that an
// observer at a bootstrap node learns a node joined and learns nothing about which
// library it wanted -- a real improvement on a DHT, whose leak is permanent, and a real
// cost against a configured address, whose leak is zero and whose reach is also zero.
package relaydiscovery

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// seedFloor is the score a shipped seed carries -- the bottom of the ranking, but
// ABOVE zero so that a repeatedly-failed peer cannot sort beneath it.
//
// See the comment in Candidates: the first version scored seeds at 0, which made
// them worse than every peer including the ones with a dozen consecutive failures.
//
// The CLAMP is to `seedFloor - epsilon` rather than to `seedFloor` itself, and that is
// the third bug this function has had. Clamping to the seed's OWN score produces a TIE,
// and a tie in a stable sort is decided by append order -- and the peer was appended
// first, so a peer that had just failed five times stayed FIRST. The comment beside the
// clamp claimed the opposite, which is how a plausible comment and a wrong answer can
// coexist for as long as nobody asserts the ordering.
const seedFloor = 0.125

// peerFloor is where a peer demoted past usefulness sits: strictly BELOW the seed, so
// a caller that works down the list reaches its fallback before spending an attempt on
// a machine it has just learned is dead.
const peerFloor = seedFloor / 2

// Learned is a peer a live relay told us about.
//
// An IDENTIFIER AND AN ADDRESS, held separately, because the point of the type is that
// discovery hands the address to the dialer and never logs or forwards the identifier
// alongside it. A struct that carried a name, a library digest and an address would
// make "we only share the address" true only as long as nobody wanted the other two
// fields.
type Learned struct {
	// Addr is where to connect. This is what a bootstrap relay supplies.
	Addr string

	// Digest is a non-routable identifier for the peer -- the value safe to pass
	// onwards. Never an address and never anything derived from a library.
	Digest string

	// From is the relay that told us, so a peer heard about second-hand can be
	// weighed against one we were handed directly.
	From string
}

// Failure is the record of a peer that did not answer, kept so demotion is a decision
// about a KNOWN-BAD peer rather than a permanent blacklisting decision made in anger.
//
// The half-life is the load-bearing part. A relay with one flaky evening has not
// earned permanent exclusion, and a permanent blacklist would make the network
// progressively WORSE at recovering from a transient outage -- every peer touched
// during a bad hour would be gone for good.
type Failure struct {
	Peer string

	// At is when it failed, and Consecutive is how many times in a row.
	At          time.Time
	Consecutive int
}

// Set is the ranked view of who to try next. Safe for concurrent use.
//
// A relay serves many peers at once, so every one of them may be reporting a dial
// failure against the same set while another goroutine is ranking it. The mutex is
// not defensive -- TestConcurrentLearningAndFailureDoNotCorruptTheSet exercises it.
type Set struct {
	mu sync.Mutex

	// seeds are the addresses shipped with the software. Never demoted, never
	// learned from, and never returned twice.
	seeds []string

	learned map[string]*Learned

	// failures is keyed by address, and the entry's Consecutive count is what
	// demotes rather than removes.
	failures map[string]*Failure

	// now is injectable so the decay tests are deterministic rather than sleeping.
	now func() time.Time

	// halfLife is how long one failure halves a peer's standing. A week is chosen
	// because it matches the scale of the thing: a laptop relay that sleeps for the
	// weekend should be preferred again by Monday, and a relay decommissioned in
	// January should stay out through March.
	halfLife time.Duration
}

// NewSet returns a set seeded with the shipped addresses.
//
// SHIPPED SEEDS, NOT A NETWORK CALL. A discovery set that has to ask the network to be
// constructed cannot bootstrap a first node, which is the failure this package exists
// to remove. The seeds are a default that later peer exchange supersedes -- see the
// package comment.
func NewSet(seeds []string) *Set {
	cp := make([]string, 0, len(seeds))
	seen := make(map[string]bool, len(seeds))
	for _, s := range seeds {
		// TRIMMED, because a seed read from a config file or a response can arrive
		// with trailing whitespace, and "  " is not a fallback address -- it is a dial
		// that fails identically every time and costs an attempt. Deduplicated AFTER
		// trimming, because "a:4001" and "a:4001 " are the same address twice.
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		cp = append(cp, s)
	}
	return &Set{
		seeds:    cp,
		learned:  make(map[string]*Learned),
		failures: make(map[string]*Failure),
		now:      time.Now,
		halfLife: 7 * 24 * time.Hour,
	}
}

// SetNow replaces the clock. For tests; exported so a caller can inject determinism.
func (s *Set) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// Learn records a peer a relay told us about.
//
// LEARNED PEERS OUTRANK SEEDS, and the reason is the whole design: a seed is a guess
// about a machine that may be switched off, while a peer a live relay just named was
// alive minutes ago. See the package comment.
//
// A SUCCESSFUL DIAL CLEARS THE FAILURE RECORD, so a peer that recovers is immediately
// preferred again rather than serving out a demotion earned during an outage that has
// since passed. The alternative -- keeping the count -- means a network cannot recover
// from a bad hour without operator intervention, which is the opposite of what a
// self-repairing set is for.
func (s *Set) Learn(l Learned) {
	if l.Addr == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.learned[l.Addr]; ok {
		// Keep the better-known metadata rather than overwriting a direct sighting
		// with a second-hand one.
		if existing.From == "" || l.From == existing.From {
			existing.Digest = l.Digest
		}
		if existing.Digest == "" {
			existing.Digest = l.Digest
		}
	} else {
		cp := l
		s.learned[l.Addr] = &cp
	}

	delete(s.failures, l.Addr)
}

// Failed records that a peer did not answer.
//
// DEMOTED, NOT FORGOTTEN, and this is the property the whole step rests on: a
// discovery set that cannot demote a dead peer degrades over time to its worst entry.
// That degradation is what would make 459 shipped seeds necessary, so the test that
// matters is the one asserting a dead peer is never retried while a live one is
// preferred.
func (s *Set) Failed(addr string) {
	if addr == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	f, ok := s.failures[addr]
	if !ok {
		f = &Failure{Peer: addr}
		s.failures[addr] = f
	}
	f.Consecutive++
	f.At = s.now()
}

// Candidates returns who to try, best first, capped at limit.
//
// LEARNED FIRST, RANKED BY STANDING, THEN SEEDS. The ordering is the design decision
// and it is asserted rather than commented, because the comment being right is not the
// same as the code being right.
//
// Standing decays exponentially with time, so a peer's rank recovers on its own and no
// scheduled sweep is needed to un-demote anything. That matters because a sweep is a
// timer somebody has to keep alive, and an un-run sweep is a permanently demoted peer
// nobody can explain.
func (s *Set) Candidates(limit int) []string {
	if limit <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	type ranked struct {
		addr  string
		score float64
	}
	now := s.now()

	// PEERS FIRST, into their own slice.
	var peers []ranked
	for addr, l := range s.learned {
		// A DIRECT sighting ranks above one heard about second-hand, because the
		// relay that named it had recent evidence and a chain of names gets no better
		// with length.
		score := 1.0
		if l.From != "" {
			score = 0.5
		}
		if f, ok := s.failures[addr]; ok {
			score *= s.decay(now.Sub(f.At), f.Consecutive)
		}
		peers = append(peers, ranked{addr: addr, score: score})
	}

	// THEN SEEDS, AT THE FLOOR -- AND SORT ONCE, WITH SEEDS ALREADY IN THE SLICE.
	//
	// This is the third arrangement of this loop, and the earlier two were both wrong
	// in ways worth recording because each produced a plausible-looking ordering:
	//
	//   - seeds scored 0, which is "worth nothing", so a peer demoted by five failures
	//     (0.031) still outranked the fallback and the caller walked past it.
	//   - seeds appended AFTER the sort, which made a tied demoted peer sort AHEAD of
	//     the seed (a stable sort keeps the earlier element first, and the peer was
	//     earlier) -- so a node retried a peer that had just failed five times before
	//     reaching its fallback.
	//   - and briefly, seeds appended to BOTH `out` and `seeds`, then both prepended,
	//     which listed every seed twice until the dedup below hid it.
	//
	// One slice, one sort, seeds included. On a tie the seed wins because it is
	// appended after a peer with the same score, and a stable sort preserves that.
	seeds := make([]ranked, 0, len(s.seeds))
	for _, addr := range s.seeds {
		seeds = append(seeds, ranked{addr: addr, score: seedFloor})
	}
	// SEEDS CONCATENATED FIRST, THEN ONE SORT. A stable sort keeps the EARLIER element
	// on a tie, so putting seeds ahead means a peer that merely TIES the fallback is
	// tried after it -- which is the outcome we want, and which appending seeds last
	// got backwards three separate times.
	peers = append(seeds, peers...)
	sort.SliceStable(peers, func(i, j int) bool { return peers[i].score > peers[j].score })

	// DEDUPLICATE, because a peer learned by address that also appears in the seed list
	// would otherwise be offered twice -- and a caller that treats the second attempt as
	// a different peer would count one relay as two votes of health.
	res := make([]string, 0, limit)
	seen := make(map[string]bool, limit)
	for _, r := range peers {
		if seen[r.addr] {
			continue
		}
		seen[r.addr] = true
		res = append(res, r.addr)
		if len(res) == limit {
			break
		}
	}
	return res
}

// decay is the exponential demotion: one failure halves the standing, and each
// successive failure halves it again.
//
// CONSECUTIVE, NOT CUMULATIVE, because a peer that failed once in March and works
// every day since should not still be carrying March's demotion. The count resets on
// success (see Learn), so what is being decayed is "how bad is it RIGHT NOW", which is
// the only question a dialer's next attempt cares about.
//
// Bounded at zero: a peer with many failures is worth no more than a peer nobody has
// heard of, never negative. A negative score would sort it below the seeds -- and the
// seeds are the fallback, so sending the caller past the fallback to reach for a peer
// that is known bad is precisely backwards.
func (s *Set) decay(age time.Duration, consecutive int) float64 {
	if consecutive <= 0 {
		return 1
	}
	// BY AGE THE PENALTY FADES, which the first version had exactly backwards.
	//
	// It read `math.Pow(0.5, age/halfLife)`, which HALVES the standing for every
	// half-life that PASSES. That makes a demotion worse with age and best when it is
	// fresh -- so a peer that failed an hour ago outranked a seed while one that failed
	// six months ago was buried, and nothing ever recovered. It is the opposite of the
	// stated intent, and it is exactly the kind of sign error a comment cannot catch:
	// the comment said "a failure's effect halves per half-life of age" and the code
	// said the same words in the same direction, with the arithmetic meaning the
	// reverse.
	//
	// The penalty must DECAY, so the exponent runs the other way: an old failure is
	// nearly forgotten (0.5^n as n grows is small, and it MULTIPLIES a factor that
	// GROWS back toward 1), and a fresh one is at full strength. Written as
	// (1 - 0.5^(age/halfLife)) * 0.5^count is wrong for a different reason: it would
	// give a zero-age failure a score of ZERO, burying the freshest evidence.
	//
	// What is wanted is a multiplier that STARTS at seedFloor-ish for a fresh failure
	// and rises to 1 as the failure ages:
	//     byAge = peerFloor + (1 - peerFloor) * (1 - 0.5^(age/halfLife))
	// which is 0.0625 at age 0 -- the full penalty -- and 1.0 at age -> infinity.
	byAge := peerFloor + (1-peerFloor)*(1-math.Pow(0.5, age.Seconds()/s.halfLife.Seconds()))
	// BY COUNT, BUT ONLY AS A TIE-BREAKER AMONG FRESH FAILURES.
	//
	// Multiplying by 0.5^consecutive capped the score at 0.0625 forever: four failures
	// meant a peer could never outrank a seed again no matter how long it had been
	// gone, so the "laptop that slept for a fortnight" never came back. A streak is
	// evidence about how bad things were WHILE it was failing; after three weeks of
	// silence the streak is history, not a prediction.
	//
	// So the count scales the age term rather than the result: more consecutive failures
	// means the peer stays buried LONGER, but it does not cap how high it can climb.
	// One failure fades over one half-life; four take four half-lives to fade, which is
	// the behaviour the test wants and the behaviour a cap cannot produce.
	ageHalfLives := age.Seconds() / s.halfLife.Seconds()
	byCount := math.Pow(0.5, float64(consecutive))
	// The count delays recovery: the effective age is the real age scaled DOWN by the
	// streak, so a four-failure peer needs four times as long to climb back.
	byAge = peerFloor + (1-peerFloor)*(1-math.Pow(0.5, ageHalfLives*byCount))
	d := byAge
	// CLAMPED AT THE FLOOR, not at zero. Falling to zero would let a peer that has
	// been failing for months rank below the seeds, which is the bug this clamp is
	// here to prevent; a peer that bad should sit AT the fallback's shoulder, not
	// under it.
	if d < peerFloor {
		return peerFloor
	}
	return d
}

// LearnedCount reports how many peers were learned, for a status endpoint.
func (s *Set) LearnedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.learned)
}

// LearnedDigests returns the NON-ROUTABLE identifiers, which is the only thing safe
// to pass to another peer.
//
// A COPY, for the same reason relayconsent.Attribution is a copy: this is data leaving
// the process, and handing back the live map would let a caller add entries it has not
// verified. An unverified address in the learned set is an unverified dial target.
func (s *Set) LearnedDigests() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.learned))
	for addr, l := range s.learned {
		out[addr] = l.Digest
	}
	return out
}
