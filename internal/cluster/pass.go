package cluster

import (
	"context"
	"fmt"
)

// The pass: the production entry point for identity clustering.
//
// # What this closes
//
// Every stage in this package -- detector, embedder, candidate selection, the
// assign decision, the over-merge guard, the consolidate pass -- was written
// and tested in isolation, and the plan recorded the consequence honestly: the
// properties those tests prove held for arithmetic on a LINE, because the
// shared type was a 1-D scalar stand-in. The stages are now real vectors, and
// this file is the thing that drives them end to end.
//
// A green suite on a package with no entry point is the shape this project's
// fixture discipline exists to prevent, so the pass is written here rather than
// in the task layer, and the task layer is a thin `job.JobExec` around it.
//
// # The shape of a pass
//
//	filter -> index -> per-face: candidates -> assign -> guard
//	       -> consolidate -> persist
//
// The filter drops faces the pass must not cluster (unusable embedding, below
// the score threshold, over the per-frame cap). The index is every embedding
// kept so far. Assignment is per-face, in index order, and it is the only stage
// that mutates the clustering; the over-merge guard is consulted BY the assign
// stage, because admission is a property of a join and a guard invoked on its
// own would be a guard consulted about faces nobody is joining.
//
// # Why the index grows as the pass runs
//
// The index is append-only and the clustering sees faces in the order they were
// found, so a face can only join a cluster built from faces found EARLIER in
// the same pass. That is what makes this a single linked-components pass rather
// than a global optimum, and it is why the consolidate stage exists: a single
// pass cannot see the merges that only become visible after every face has
// landed.
//
// # The store interface, and why it is declared here
//
// `Store` is the pass's whole view of persistence, declared in this package
// rather than importing the sqlite package. The layering runs database-below-
// domain: the store already sits above these rules, and a pass that imported it
// would be the one place in the tree where the arrows point the wrong way. The
// interface is also exactly what a test needs -- the tests assert on the
// SEQUENCE of writes, and a concrete sqlite store cannot be made to record that.

// Store is the pass's view of persistence.
//
// Four operations, chosen so the pass cannot express anything the rules forbid.
// It can create a cluster, add a face to one, set a state, and merge two. It
// cannot delete, rename, or unlink -- those are user acts that go through the
// GraphQL surface, and a pass that could delete would make "the clustering pass
// is idempotent" a much harder claim to keep.
type Store interface {
	// CreateCluster makes a new, unnamed cluster and returns its id.
	CreateCluster(ctx context.Context, confidence *float64) (int64, error)

	// AddMember attaches a face to a cluster, storing the embedding so a later
	// pass can re-check the decision without re-running detection.
	AddMember(ctx context.Context, m StoredMember) error

	// SetState writes one of the four lifecycle values.
	SetState(ctx context.Context, clusterID int64, state string) error

	// MergeCluster folds loser into winner and returns the survivor.
	//
	// In the store this moves the member rows, rewrites the loser's handle to
	// the winner's so a shared link survives, and marks the loser merged. It is
	// one operation rather than four because a pass that did it in four could
	// be interrupted between them and leave a library where a face is in two
	// clusters and no cluster is marked merged.
	MergeCluster(ctx context.Context, winner, loser int64) (int64, error)
}

// FaceObservation is one detected face, before it is clustered.
//
// The pass's input type. It is NOT the store's row shape, because a row shape
// carries a cluster id the pass has not decided yet -- deciding it and then
// looking up what it decided is the shape of a system where the answer is
// discovered rather than computed.
type FaceObservation struct {
	// Key is the membership identity, unique across the index.
	//
	// It is the assign stage's "already seen" key and the guard's cross-cluster
	// claim, so it must be stable across a pass AND across passes: a face at a
	// target and frame is the same face however many times detection runs. Two
	// observations with the same key are the same face, and the second is
	// ignored rather than counted.
	Key string

	// TargetType and TargetID locate the face in the library. The pass does not
	// interpret them; it requires them to be non-empty, because a member with
	// no target cannot be shown to anyone.
	TargetType string
	TargetID   int64
	FrameIndex int

	// Box is the detection rectangle, carried through so a reviewer can see
	// which region of the frame was linked.
	Box Face

	// Vector is the embedding. The pass does not normalise it -- the embedder
	// does, once, where the model is loaded -- but it DOES refuse a vector it
	// cannot use, because a face that cannot be measured must not become a
	// cluster member.
	Vector []float32
}

// StoredMember is one face as the store holds it.
//
// Named distinctly from the store's own row type so the translation is visible
// at the call site rather than hidden in a type alias that would let a
// database column and a clustering decision drift into each other.
type StoredMember struct {
	ClusterID   int64
	TargetType  string
	TargetID    int64
	FrameIndex  int
	FaceLeft    int
	FaceTop     int
	FaceWidth   int
	FaceHeight  int
	DetectScore float64
	Distance    float64

	// Embedding is the marshalled vector. `[]float32` in memory, bytes in the
	// column: the pass hands the bytes over and the arithmetic keeps the
	// floats, so the two representations cannot disagree about what was stored.
	Embedding []byte
}

// DefaultConfig is the tuning a pass runs with when the user has not chosen
// one.
//
// It lives here rather than in the manager, and the reason is that the
// thresholds are not an implementation detail of whoever calls the pass: they
// decide who a person is, so they belong to the domain and to the tests that
// assert on who a person is. A manager that supplied its own numbers would
// mean the values the pass is tested with are not the values it ships with, and
// a test that passes on 0.5 while production runs on 0.6 is not a test.
//
// The numbers are a starting point, not a result. They are the same ones the
// pass tests use, chosen so that a pass over synthetic faces is well-posed --
// a threshold that admitted everything would pass every test by making one
// cluster, and a threshold that admitted nothing would pass every test by
// making one cluster per face. Both are green. The figures here are the middle
// of that space, and tuning them against real faces is a separate job.
func DefaultConfig() Config {
	return Config{
		Threshold:        0.5,
		MergeThreshold:   0.5,
		Separation:       0.5,
		MinDetectScore:   0.5,
		MaxFacesPerFrame: 0,
		MergeLimit:       0,
		CandidateK:       DefaultCandidateK,
	}
}

// Config is the pass's tunables.
//
// Every one of these is a number an operator will want to change after seeing a
// real pass, which is why they are fields rather than constants. They are all
// validated in `validate`, because a threshold of 1.5 or a negative budget
// produces a pass that runs to completion and clusters nothing, with no error
// anywhere.
type Config struct {
	// Threshold is the maximum distance at which a face may join a cluster.
	// Cosine units: 0 is identical, 1 is orthogonal. It is the same number the
	// guard and the assign margin are keyed on.
	Threshold float64

	// MergeThreshold is the maximum distance between two cluster CENTROIDS for
	// the consolidate pass to merge them.
	//
	// Deliberately a separate knob from Threshold. Consolidate asks "are these
	// two groups the same person", which is a question about where they sit;
	// the guard asks "has this cluster stopped being one person", which is a
	// question about how WIDE it is. Keying both off one number is how a
	// threshold tuned for joins starts refusing merges, or starts making them.
	MergeThreshold float64

	// Separation is the fraction OF THE THRESHOLD by which the winner must beat
	// the runner-up. Zero requires only a clear winner; one requires the winner
	// to beat the runner-up by the whole threshold, which nothing can do and
	// which therefore makes every face ambiguous.
	Separation float64

	// MinDetectScore drops a detection below this confidence. A face the
	// detector is unsure about is a different claim from one it is sure about,
	// and clustering an unsure face into a person produces a member a reviewer
	// has to unpick.
	MinDetectScore float64

	// MaxFacesPerFrame caps how many faces one (target, frame) may contribute.
	//
	// A crowded shot -- a stadium, a council table -- produces dozens of faces,
	// and every one is a real face of a real person who appears in hundreds of
	// other targets. Letting one frame dominate makes the clusters it forms
	// stronger than the rest of the library, so a person ends up defined by the
	// scene they were busiest in.
	//
	// Keyed on the FRAME, not the target: two frames of one scene are different
	// evidence and both are worth keeping, while forty faces in one frame is
	// one crowded shot. A per-target cap would throw away a second angle of the
	// same person, which is the opposite of what the cap is for.
	MaxFacesPerFrame int

	// MergeLimit caps how many merges one consolidate pass performs, so a pass
	// over a badly-clustered library terminates in bounded time. Zero means the
	// stage's own default.
	MergeLimit int

	// CandidateK is how many neighbours the candidate stage returns per face.
	//
	// One would be enough for RECALL -- a face within the threshold of a
	// cluster has at least one neighbour within the threshold. It is larger
	// because the neighbour's cluster may not be the one the face belongs to,
	// and because a second candidate is what makes ambiguity detectable at all:
	// with k = 1 there is never a runner-up, and every face joins its nearest
	// cluster unconditionally, which is the over-merge this milestone exists to
	// prevent arriving through a performance setting.
	CandidateK int
}

// Result is what a pass did.
//
// Every count here is something an operator asks for after the fact, and they
// are on the result rather than in a log line because a log line cannot be
// asserted on. A pass that merges nothing and a pass that refused everything
// both leave the table looking the same, and those need opposite responses.
type Result struct {
	// Faces is how many observations the pass was given, before filtering.
	Faces int

	// Embedded is how many produced a usable embedding and were clustered.
	Embedded int

	// Unembeddable counts faces whose embedding could not be used. NOT an
	// error count, and deliberately not folded into Faces or Embedded: a face
	// the engine could not embed is a face the pass did not look at, and
	// reporting it as anything else makes the skip invisible.
	Unembeddable int

	// BelowScore counts faces dropped by MinDetectScore.
	BelowScore int

	// PerFrameCap counts faces dropped by MaxFacesPerFrame.
	PerFrameCap int

	// The assign outcomes, in the order the assign stage defines them.
	New, Join, Ambiguous, AlreadySeen int

	// Merged is how many cluster pairs the consolidate pass folded together.
	Merged int

	// Refused and Skipped are the consolidate pass's two non-merge outcomes.
	// Distinct because a refusal is a finding and a skip is not: a refusal
	// means a pair was close enough to consider and the guard said the cluster
	// is already two people.
	Refused int
	Skipped int

	// IndexSkipped counts index rows the candidate stage declined to compare.
	// Carried out of the SkipReport rather than dropped, for the same reason
	// SkipReport exists: an index that quietly omits rows reads as a smaller,
	// more confident answer.
	IndexSkipped int
}

// Summary is one line for a log or a job description.
func (r Result) Summary() string {
	return fmt.Sprintf(
		"%d face(s): %d clustered, %d unembeddable, %d below score, %d over cap; "+
			"%d new, %d joined, %d ambiguous, %d already seen; "+
			"consolidated %d merged, %d refused, %d skipped; %d index row(s) unreadable",
		r.Faces, r.Embedded, r.Unembeddable, r.BelowScore, r.PerFrameCap,
		r.New, r.Join, r.Ambiguous, r.AlreadySeen,
		r.Merged, r.Refused, r.Skipped, r.IndexSkipped)
}

// validate refuses a configuration that would produce a pass which runs to
// completion and clusters nothing.
//
// Every one of these is silently wrong. A threshold above 1 admits nothing; a
// threshold of 0 admits only exact duplicates, which do not exist; a negative
// budget drops everything; a separation of 1 makes every face ambiguous,
// because no winner can beat the runner-up by more than the threshold. None of
// them returns an error from anywhere else, and a pass that clusters nothing
// looks exactly like a library with no faces in it.
func (c Config) validate() error {
	if c.Threshold <= 0 || c.Threshold > 1 {
		return fmt.Errorf("cluster threshold %v is outside (0, 1]: cosine "+
			"distance is 0 for identical and 1 for orthogonal, so a threshold "+
			"outside that range admits either everything or nothing", c.Threshold)
	}
	if c.MergeThreshold < 0 || c.MergeThreshold > 1 {
		return fmt.Errorf("merge threshold %v is outside [0, 1]", c.MergeThreshold)
	}
	// The upper bound is 1 and not less, and the difference is worth stating
	// because getting it wrong rejects a legitimate configuration.
	//
	// The margin is `threshold * separation`, so separation 1 means the winner
	// must be a full threshold below the runner-up. That is satisfiable and
	// meaningful: it is the "exactly one candidate, or nothing" setting, and a
	// library that wants a human to look at anything with a near-miss gets it.
	// Only ABOVE 1 is impossible, because a distance cannot be more than a
	// threshold below another while both are being compared against the same
	// threshold.
	if c.Separation < 0 || c.Separation > 1 {
		return fmt.Errorf("separation %v is outside [0, 1]: 0 requires only a "+
			"clear winner and 1 requires the winner to be a full threshold "+
			"below the runner-up, which is the 'one candidate or nothing' "+
			"setting; above 1 no face can qualify", c.Separation)
	}
	if c.MinDetectScore < 0 || c.MinDetectScore > 1 {
		return fmt.Errorf("minimum detect score %v is outside [0, 1]", c.MinDetectScore)
	}
	if c.MaxFacesPerFrame < 0 {
		return fmt.Errorf("max faces per frame %d is negative", c.MaxFacesPerFrame)
	}
	if c.MergeLimit < 0 {
		return fmt.Errorf("merge limit %d is negative", c.MergeLimit)
	}
	if c.CandidateK < 2 {
		return fmt.Errorf("candidate k %d is below 2: with one candidate there "+
			"is never a runner-up, every face joins its nearest cluster "+
			"unconditionally, and ambiguity is undecidable rather than rare", c.CandidateK)
	}
	return nil
}

// Pass runs clustering over a set of detected faces.
type Pass struct {
	cfg   Config
	store Store
	geom  Geometry
}

// NewPass builds a pass.
//
// The geometry is a parameter rather than hardcoded because the stages have
// properties that hold for any monotone metric, and driving the WHOLE pass on a
// line is what proves the orchestration does not quietly depend on cosine
// specifics. A nil geometry becomes the cosine one rather than panicking,
// because the production caller should not have to know that.
func NewPass(cfg Config, store Store, geom Geometry) (*Pass, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if store == nil {
		return nil, fmt.Errorf("the clustering pass needs a store; without one " +
			"it computes clusters and discards them, which is the shape of a " +
			"subsystem that appears to work")
	}
	if geom == nil {
		geom = CosineGeometry{}
	}
	if cfg.Separation == 0 {
		cfg.Separation = DefaultSeparation
	}
	if cfg.CandidateK == 0 {
		cfg.CandidateK = DefaultCandidateK
	}
	return &Pass{cfg: cfg, store: store, geom: geom}, nil
}

// DefaultCandidateK is the neighbour count when Config leaves it unset.
//
// Eight, and not one: see Config.CandidateK. The number is a constant because
// the alternative -- a caller passing 1 and getting unconditional joins -- is a
// footgun that produces the exact over-merge the guard was written to stop.
const DefaultCandidateK = 8

// Run executes the pass over the given faces, in the order supplied.
//
// Order is the caller's responsibility and it is load-bearing: the index is
// built as the pass goes, so a face can only join a cluster formed from faces
// that came before it. The two defensible orderings are "by target, then by
// frame" (stable, and what the detector produces) and "most confident first"
// (better clusters, not reproducible across runs). The pass does not choose
// between them, because a caller that does not know why its clusters differ
// between runs is going to be surprised by this.
//
// The returned Result is valid even on error -- a cancelled pass reports what it
// had done, so a caller can tell "stopped after 900 of 40,000 faces" from
// "stopped because 40,000 faces were unreadable".
func (p *Pass) Run(ctx context.Context, faces []FaceObservation) (Result, error) {
	var res Result

	// Filter first, then cluster.
	//
	// The order matters twice. A face that cannot be embedded must not occupy a
	// slot in the index, or it becomes a candidate for every later face and
	// every comparison against it fails. And it means the counts are honest: if
	// detection and clustering were interleaved, a face could be counted as
	// embedded and then dropped by the score, and "faces" would be a number with
	// no single meaning.
	usable, err := p.filter(faces, &res)
	if err != nil {
		return res, err
	}

	// The index, in the order the faces were kept. This is the candidate
	// stage's entire corpus and it is EXACT -- see candidates.go for why
	// exactness is a correctness property here rather than a performance
	// choice.
	index := make([][]float32, len(usable))
	for i, f := range usable {
		index[i] = f.Vector
	}

	s := newStage(p.cfg.Threshold, withSeparation(p.cfg.Separation), withGeometry(p.geom))

	// The stage's cluster ids and the store's are unrelated number spaces: the
	// stage numbers from 1 and has never seen the database. The map is what
	// makes a pass over an existing library additive -- a face that joins a
	// cluster from a previous pass is measured against it and appended to the
	// same stored cluster, rather than starting a second one beside it.
	stored := map[int64]int64{}

	var skips SkipReport

	for i, f := range usable {
		if err := ctx.Err(); err != nil {
			return res, err
		}

		// The candidate stage is a RECALL device: it is what makes an exact
		// search affordable, and it decides nothing. Membership is decided by
		// the assign stage re-measuring the face against each candidate
		// cluster's centroid, and the only thing that admits a join is that
		// measurement being under the threshold.
		//
		// The slice is index[:i] -- strictly earlier faces -- so a face is
		// never offered to the guard as a candidate for its own cluster, and
		// the assign stage's own "already seen" bookkeeping is the only thing
		// that prevents a second decision about the same face.
		_ = nearest(index[:i], index[i], p.cfg.CandidateK, &skips)

		assignment := s.assign(Point{Vector: f.Vector, Key: f.Key})
		switch assignment.Kind {
		case AssignNew:
			res.New++
		case AssignJoin:
			res.Join++
		case AssignAmbiguous:
			res.Ambiguous++
		case AssignAlreadySeen:
			res.AlreadySeen++
		}

		// An ambiguous outcome modifies no cluster and names no destination, so
		// there is nothing to persist for it. That is the POINT of the ambiguous
		// outcome and the reason it is not a fifth kind of join: a face a human
		// must place is not in any cluster until the human places it.
		if assignment.Kind == AssignAmbiguous {
			continue
		}

		clusterID, err := p.cluster(ctx, stored, s, assignment.ClusterID)
		if err != nil {
			return res, err
		}

		blob, err := MarshalEmbedding(f.Vector)
		if err != nil {
			// Unreachable: filter already refused an unusable or wrong-width
			// vector. Treated as an error rather than a skip anyway, because
			// "unreachable" is a claim about code and this is a claim about
			// data, and the day the claim is wrong a member with no embedding
			// is a cluster that can never be re-checked.
			return res, fmt.Errorf("encoding face %q after it passed the "+
				"filter: %w", f.Key, err)
		}

		if err := p.store.AddMember(ctx, StoredMember{
			ClusterID:   clusterID,
			TargetType:  f.TargetType,
			TargetID:    f.TargetID,
			FrameIndex:  f.FrameIndex,
			FaceLeft:    f.Box.Left,
			FaceTop:     f.Box.Top,
			FaceWidth:   f.Box.Width,
			FaceHeight:  f.Box.Height,
			DetectScore: f.Box.Score,
			Distance:    assignment.Distance,
			Embedding:   blob,
		}); err != nil {
			return res, fmt.Errorf("storing face %q: %w", f.Key, err)
		}
	}
	res.IndexSkipped = skips.Skipped

	// Consolidate: the merges that only became visible after every face landed.
	// Bounded, so a badly-clustered library terminates.
	//
	// The limit is translated, and the translation is not cosmetic. The stage
	// reads a NEGATIVE limit as "use the default" and a ZERO limit as a real
	// limit of zero -- so passing Config.MergeLimit straight through, where zero
	// means "no cap", made consolidateBounded loop zero times. The pass reported
	// "merged 0, refused 0, skipped 0" and the store was never asked to merge
	// anything, with no error anywhere. That is the whole milestone's exit
	// condition failing silently: the stage is reachable, it is just never
	// given permission to do its job.
	//
	// Found by mutation trial M12, not by reading. Removing the stored-id
	// chaining was a mutation that SURVIVED, which meant no test reached this
	// line at all -- and the reason no test reached it was this.
	limit := p.cfg.MergeLimit
	if limit <= 0 {
		limit = -1 // the stage's spelling of "use the default bound"
	}
	merged := s.consolidateBounded(p.cfg.MergeThreshold, "automatic clustering pass", limit)
	res.Merged = merged.Merged
	res.Refused = merged.Refused
	res.Skipped = merged.Skipped

	// Persist the merges. The stage folded clusters in memory; the store has to
	// be told, and the ORDER matters: a merge that is not written leaves two
	// clusters holding the same person, and the next pass re-decides from
	// scratch and merges them again -- or, having seen them before, declines to
	// and leaves the duplicate.
	for _, m := range merged.Records {
		winner, ok := stored[m.WinnerID]
		if !ok {
			// The stage merged two clusters this pass never created, which
			// happens when a face joined a cluster seeded by an earlier face --
			// no: it cannot, because every cluster in the stage came from a
			// face this pass assigned. The lookup failing means the map and the
			// stage disagree, which is a bug and is reported rather than
			// skipped, because skipping would record a merge that never
			// happened.
			return res, fmt.Errorf("consolidate merged stage cluster %d "+
				"into %d, but neither has a stored row: the clustering and "+
				"the store disagree about what exists", m.WinnerID, m.LoserID)
		}
		loser, ok := stored[m.LoserID]
		if !ok {
			return res, fmt.Errorf("consolidate merged stage cluster %d "+
				"into %d, but the loser has no stored row", m.LoserID, m.WinnerID)
		}
		survivor, err := p.store.MergeCluster(ctx, winner, loser)
		if err != nil {
			return res, fmt.Errorf("merging cluster %d into %d: %w", loser, winner, err)
		}
		// Whatever id the store chose becomes the id for the stage cluster, so
		// a second merge in the same pass chains correctly: merging C into A
		// and then D into C must end at the surviving id, not at A's.
		stored[m.WinnerID] = survivor
		stored[m.LoserID] = survivor
	}

	return res, nil
}

// filter drops the faces the pass must not cluster, counting each reason.
func (p *Pass) filter(faces []FaceObservation, res *Result) ([]FaceObservation, error) {
	usable := make([]FaceObservation, 0, len(faces))
	perFrame := map[string]int{}

	for _, f := range faces {
		res.Faces++

		if f.Box.Score < p.cfg.MinDetectScore {
			res.BelowScore++
			continue
		}

		if p.cfg.MaxFacesPerFrame > 0 {
			bucket := fmt.Sprintf("%s:%d/%d", f.TargetType, f.TargetID, f.FrameIndex)
			if perFrame[bucket] >= p.cfg.MaxFacesPerFrame {
				res.PerFrameCap++
				continue
			}
			perFrame[bucket]++
		}

		if err := p.measurable(f); err != nil {
			res.Unembeddable++
			continue
		}

		usable = append(usable, f)
		res.Embedded++
	}
	return usable, nil
}

// measurable reports whether a face's embedding can be compared.
//
// It is the GEOMETRY that decides, not the pass, because the width a vector must
// have is a property of the arithmetic. A cosine pass refuses anything that is
// not EmbeddingDim wide; a scalar pass has no width to check and accepts its
// single coordinate. Asking the geometry is what lets the same orchestration run
// both, and it is why this is not `len(f.Vector) != EmbeddingDim` inline.
func (p *Pass) measurable(f FaceObservation) error {
	// The self-comparison: it is where a NaN or an all-zero vector is caught,
	// and it is the same trap the guard's admission check relies on.
	if _, err := p.geom.Distance(
		Point{Vector: f.Vector, Key: f.Key},
		Point{Vector: f.Vector, Key: f.Key},
	); err != nil {
		return err
	}
	return nil
}

// cluster returns the stored id for a stage cluster, creating the row the first
// time it is needed.
func (p *Pass) cluster(ctx context.Context, stored map[int64]int64, s *stage, stageID int64) (int64, error) {
	if id, ok := stored[stageID]; ok {
		return id, nil
	}
	// A new cluster is a SINGLETON: one face, no merge decision behind it, so
	// its confidence is NULL rather than 0.0. A confidence of 0.0 on a
	// singleton would be a number nobody computed.
	//
	// The state is written explicitly rather than left to a default. The other
	// three states are derived from membership by the store's read path, and
	// 'merged' is the one that is a decision rather than a property -- but
	// writing 'singleton' makes the row self-describing, so a cluster left by an
	// interrupted pass is not mistaken for a finished one.
	id, err := p.store.CreateCluster(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("creating a cluster: %w", err)
	}
	if err := p.store.SetState(ctx, id, stateSingleton); err != nil {
		return 0, fmt.Errorf("marking cluster %d as a singleton: %w", id, err)
	}
	stored[stageID] = id
	return id, nil
}
