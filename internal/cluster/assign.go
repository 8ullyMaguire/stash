package cluster

import "sort"

// Step 2.4b.3: the assign decision.
//
// # The three outcomes
//
//	new cluster   nothing was in range. The normal outcome for a first face,
//	              and a face with no cluster is not a problem to report.
//	join          exactly one cluster was clearly nearer than the others.
//	ambiguous     two or more clusters were in range AND close enough to each
//	              other that the ranking between them is inside the noise.
//
// # Why 'ambiguous' rather than picking the nearest
//
// The distance that decides "nearer" is the same distance that says nothing
// about whether two clusters are the same person. Clusters at 0.10 and 0.14
// from a candidate whose threshold is 0.15 are both in range, and the 0.04
// between them is not a measurement of anything.
//
// The costs are asymmetric, which is the whole argument. An ambiguous state
// costs a reviewer seconds. A wrong join silently merges two people, and the
// error compounds: every later face assigned to the merged cluster inherits it,
// and the centroid moves toward the wrong answer, so the cluster attracts more
// wrong faces. One bad merge makes itself more likely to grow.

// AssignKind is the outcome of one assign decision.
type AssignKind int

const (
	// AssignNew means the face started a cluster.
	AssignNew AssignKind = iota
	// AssignJoin means it joined exactly one cluster, clearly.
	AssignJoin
	// AssignAmbiguous means the engine could not choose and a human must.
	AssignAmbiguous
	// AssignAlreadySeen means this exact face was already assigned. A rescan
	// re-derives candidates but must not re-decide.
	AssignAlreadySeen
)

func (k AssignKind) String() string {
	switch k {
	case AssignNew:
		return "new"
	case AssignJoin:
		return "join"
	case AssignAmbiguous:
		return "ambiguous"
	case AssignAlreadySeen:
		return "already-seen"
	}
	return "unknown"
}

// Candidate is one cluster the face could have joined, with the distance that
// put it in range.
type Candidate struct {
	ClusterID int64
	Distance  float64
}

// Assignment is the decision, including what it was weighed against.
//
// Recording the rejected candidates is not diagnostic sugar. The ambiguous
// path is a queue a human works through, and a reviewer who can see only one
// candidate has no way to know what the engine considered. It is also the
// evidence for a later claim: "the engine put these two together and showed me
// exactly what it compared" is a different conversation from "the engine linked
// them".
type Assignment struct {
	Kind AssignKind

	// ClusterID is the destination, or 0 for AssignAmbiguous. Naming a
	// destination on an ambiguous outcome is how a guess gets committed
	// downstream by a caller that trusts the field rather than the Kind.
	ClusterID int64

	// Distance is the distance to ClusterID, for AssignJoin.
	Distance float64

	// Candidates is every in-range cluster, nearest first, for AssignJoin and
	// AssignAmbiguous. Empty for AssignNew, where there were none.
	Candidates []Candidate
}

// DefaultSeparation is how much closer one candidate must be than the next,
// as a fraction of the threshold, before the engine will choose between them.
//
// It is a fraction of the THRESHOLD and not of the runner-up, and the
// distinction is the whole reason this value is defensible. The margin answers
// "is the ranking between these two distances meaningful?", which is a question
// about the engine's noise floor -- and the threshold is already the
// expression of that. A fraction of the runner-up inverts the behaviour
// nonsensically: it demands a smaller margin when the runner-up is FARTHER,
// so two clusters at 0.40 and 0.41 would need a margin of 0.0025 to be
// distinguishable while two at 0.10 and 0.14 would need 0.05. Two distances
// that are nearly equal are nearly equal regardless of how large they are, and
// the test at hand is exactly that.
const DefaultSeparation = 0.5

// stage holds the assign decision's state.
type stage struct {
	threshold float64
	// separation is the fraction OF THE THRESHOLD by which the winner must beat
	// the runner-up. 0.5 means the winner's distance must be at most half the
	// threshold below the runner-up's.
	separation float64

	// clusters maps id to the members, in insertion order.
	clusters map[int64][]*testFace
	// seen maps a membership key to the decision already made for it.
	seen map[string]Assignment
	next  int64
}

type stageOption func(*stage)

// withSeparation overrides the ranking margin. The default is
// DefaultSeparation; tests set it explicitly rather than relying on the default
// so a change to the default does not silently change what they prove.
func withSeparation(f float64) stageOption {
	return func(s *stage) { s.separation = f }
}

func newStage(threshold float64, opts ...stageOption) *stage {
	s := &stage{
		threshold:  threshold,
		separation: DefaultSeparation,
		clusters:   map[int64][]*testFace{},
		seen:       map[string]Assignment{},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// seedCluster creates a cluster at a given distance from the origin.
func (s *stage) seedCluster(distance float64) int64 {
	return s.forceClusterAt(distance)
}

func (s *stage) forceClusterAt(distance float64) int64 {
	s.next++
	id := s.next
	s.clusters[id] = []*testFace{{pos: distance, key: "seed"}}
	return id
}

func (s *stage) forceMembers(id int64, f ...*testFace) {
	s.clusters[id] = append(s.clusters[id], f...)
}

func (s *stage) size(id int64) int { return len(s.clusters[id]) }

// assign decides where a face goes.
func (s *stage) assign(f *testFace) Assignment {
	// A rescan re-derives the candidates so the caller can see the current
	// state, but it does not re-decide. Duplicated decisions are not harmless
	// here: each ambiguous outcome is a row in a review queue, and duplicates
	// of one conflict bury the real ones.
	if prev, ok := s.seen[f.key]; ok && f.key != "" {
		return Assignment{
			Kind:       AssignAlreadySeen,
			ClusterID:  prev.ClusterID,
			Distance:   prev.Distance,
			Candidates: s.candidatesFor(f),
		}
	}

	cands := s.candidatesFor(f)

	switch {
	case len(cands) == 0:
		// Nothing in range. This is the normal outcome for a first face, not a
		// failure, and it must not produce a warning.
		s.next++
		s.clusters[s.next] = []*testFace{f}
		out := Assignment{Kind: AssignNew, ClusterID: s.next}
		if f.key != "" {
			s.seen[f.key] = out
		}
		return out

	case len(cands) == 1:
		s.join(f, cands[0])
		out := Assignment{
			Kind:       AssignJoin,
			ClusterID:  cands[0].ClusterID,
			Distance:   cands[0].Distance,
			Candidates: cands,
		}
		if f.key != "" {
			s.seen[f.key] = out
		}
		return out
	}

	// Two or more in range. The question is whether the ranking is meaningful:
	// if the runner-up is within the separation margin, the ordering is inside
	// the noise and choosing is a guess.
	//
	// Note what is NOT consulted: the winner's confidence, and the winner's
	// size. Size is the tempting one and it is actively harmful -- a cluster is
	// bigger because more faces were assigned to it, including faces assigned
	// wrongly, so letting size break the tie makes the first wrong merge the
	// most attractive destination for the next wrong merge.
	best, second := cands[0], cands[1]
	margin := s.threshold * s.separation
	if second.Distance-best.Distance >= margin {
		s.join(f, best)
		out := Assignment{
			Kind:       AssignJoin,
			ClusterID:  best.ClusterID,
			Distance:   best.Distance,
			Candidates: cands,
		}
		if f.key != "" {
			s.seen[f.key] = out
		}
		return out
	}

	// Genuinely ambiguous. No cluster is modified: the engine records the
	// conflict and a human resolves it. A stage that moved the face into the
	// nearer cluster AND set the ambiguous flag has already made the decision
	// it says it cannot make, and the stored state is a lie about what
	// happened.
	return Assignment{Kind: AssignAmbiguous, Candidates: cands}
}

// candidatesFor returns every in-range cluster, nearest first.
//
// Sorted because the review UI lists them in this order and a reviewer's eye
// goes to the first. An unsorted list makes the engine's preference the
// default answer even while the state says "ambiguous".
func (s *stage) candidatesFor(f *testFace) []Candidate {
	var cands []Candidate
	for id, members := range s.clusters {
		if len(members) == 0 {
			continue
		}
		if d := s.distanceTo(f, members); d <= s.threshold {
			cands = append(cands, Candidate{ClusterID: id, Distance: d})
		}
	}
	// Map iteration order is randomised in Go, so without this the winner would
	// be arbitrary among equal distances -- and a test that passed by luck would
	// be passing by luck forever.
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].Distance != cands[j].Distance {
			return cands[i].Distance < cands[j].Distance
		}
		return cands[i].ClusterID < cands[j].ClusterID
	})
	return cands
}

// distanceTo is the distance from a face to a cluster: the mean embedding, the
// same arithmetic the over-merge guard uses, so the two agree by construction.
func (s *stage) distanceTo(f *testFace, members []*testFace) float64 {
	return centroidOf(members).distanceTo(f)
}

func (s *stage) join(f *testFace, c Candidate) {
	s.clusters[c.ClusterID] = append(s.clusters[c.ClusterID], f)
}

// centroidOf is the mean position. Shared with the over-merge guard so the two
// cannot drift: a centroid computed two different ways in the same package is
// a bug waiting for a threshold change to expose it.
func centroidOf(faces []*testFace) centroid {
	return meanOf(faces)
}
