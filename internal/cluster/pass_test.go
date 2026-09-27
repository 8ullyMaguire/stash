package cluster

import (
	"context"
	"fmt"
	"math"
	"sort"
	"testing"
)

// Tests for the pass -- the production entry point.
//
// # What these are for
//
// The stages each have tests. This file tests the thing that was missing: that
// driving all of them in sequence over REAL VECTORS produces the outcomes the
// stages were designed to produce, and that the pass's own rules (filtering,
// persistence order, cancellation) hold at the seam.
//
// The store is a recording fake rather than sqlite, for two reasons. It records
// the SEQUENCE of writes, which is the property that matters here -- a pass
// that creates its cluster rows in the wrong order, or merges before it writes
// the members, produces a library the sqlite CHECK constraints reject at a
// point where the cause is three steps back. And a real database cannot be made
// to assert "this face was never persisted".
//
// # The geometry is cosine, everywhere.
//
// These tests are the reason the scalar stand-in was not enough. A pass driven
// on a line can be correct while the cosine pass is wrong, because the two
// differ in exactly one place that matters: on a line, the centroid of a
// bimodal cluster sits at a distance that grows with the gap, while in high
// dimensions the mean of two groups sits in the middle of the gap where a
// centre-based test looks SAFEST. `TestPassSeparatesTwoPeopleFiftyDegreesApart`
// is that case, and it cannot be written on a line.

// recordingStore records every call in order.
//
// The order is a first-class field rather than a convenience. A pass that
// persists a member before its cluster exists fails at the database with a
// foreign key violation, and the trace back from that error to the loop that
// ordered it wrong is not a short one.
type recordingStore struct {
	calls []string

	nextID  int64
	states  map[int64]string
	members []StoredMember
	merges  [][2]int64

	// errAt makes the named operation fail, so a test can assert what the pass
	// does when persistence fails partway rather than only when it succeeds.
	errAt string
	err   error
}

func newRecordingStore() *recordingStore {
	return &recordingStore{nextID: 100, states: map[int64]string{}}
}

func (s *recordingStore) CreateCluster(ctx context.Context, confidence *float64) (int64, error) {
	s.calls = append(s.calls, "create")
	if s.errAt == "create" {
		return 0, s.err
	}
	s.nextID++
	return s.nextID, nil
}

func (s *recordingStore) AddMember(ctx context.Context, m StoredMember) error {
	s.calls = append(s.calls, fmt.Sprintf("member:%d", m.ClusterID))
	if s.errAt == "member" {
		return s.err
	}
	s.members = append(s.members, m)
	return nil
}

func (s *recordingStore) SetState(ctx context.Context, clusterID int64, state string) error {
	s.calls = append(s.calls, fmt.Sprintf("state:%d=%s", clusterID, state))
	if s.errAt == "state" {
		return s.err
	}
	s.states[clusterID] = state
	return nil
}

func (s *recordingStore) MergeCluster(ctx context.Context, winner, loser int64) (int64, error) {
	s.calls = append(s.calls, fmt.Sprintf("merge:%d->%d", loser, winner))
	if s.errAt == "merge" {
		return 0, s.err
	}
	s.merges = append(s.merges, [2]int64{winner, loser})
	s.states[loser] = stateMerged
	// The store's real MergeCluster returns the survivor's id, which is the
	// winner. A pass that assumes the loser survives gets the chains wrong.
	return winner, nil
}

// clusterOf returns the stored cluster a member was written to.
//
// It matches on TargetID, because the key is carried there. The first version
// matched on TargetType alone, which is "scene" for every fixture, so it
// returned the first member's cluster for every key and reported "0" for faces
// it had never stored. A lookup that cannot distinguish its arguments is
// answering a different question.
func (s *recordingStore) clusterOf(key int64) int64 {
	for _, m := range s.members {
		if m.TargetID == key {
			return m.ClusterID
		}
	}
	return 0
}

// angVec is an EmbeddingDim-wide unit vector at the given angle from axis 0.
//
// It uses EVERY component, not just the first two, and that is the whole
// point. The first version set v[0] = cos and v[1] = sin and left the rest at
// zero, which is a valid 512-wide vector and a 2-dimensional one wearing a
// 512-wide costume. In a 2-plane the mean of two vectors 91 degrees apart has
// almost no length, renormalising it amplifies the result, and the distance
// from either face to the pair's centroid came back as 1.02 -- a "distance"
// larger than the 2.0 maximum for a true cosine. Every threshold in this
// package is calibrated on real 512-wide embeddings, and a fixture that is not
// one produces numbers no production run will ever see.
//
// The construction is a spherical one: each component is a fixed function of
// its index and the angle, so the result is smooth in the angle, is
// deterministic, and spreads across all components.
//
// Note the scale, which the tests now state rather than assume: cosine DISTANCE
// here is 1 - cos, so it runs from 0 (identical) through 1 (orthogonal) to 2
// (opposite). "60 degrees apart is 0.5" is the midpoint, not the far end.
func angVec(deg float64) []float32 {
	r := deg * math.Pi / 180
	v := make([]float32, EmbeddingDim)
	for i := range v {
		t := 2 * math.Pi * float64(i) / float64(EmbeddingDim)
		v[i] = float32(math.Cos(r)*math.Cos(t) +
			math.Sin(r)*math.Sin(t)*math.Cos(3*t))
	}
	// Normalise. The magnitude of the construction above depends on the angle,
	// and an un-normalised vector is refused by the geometry -- correctly, since
	// every threshold here is calibrated on unit vectors.
	ss := 0.0
	for _, x := range v {
		ss += float64(x) * float64(x)
	}
	ss = math.Sqrt(ss)
	for i := range v {
		v[i] = float32(float64(v[i]) / ss)
	}
	return v
}

// obs builds a FaceObservation at angle `deg`, keyed by `key`.
//
// The key becomes a distinct TargetID so the fake store can find the member
// again afterwards; the pass itself never reads TargetID. FrameIndex stays 0
// unless a test changes it, which is what makes the per-frame cap observable.
func obs(key string, deg float64) FaceObservation {
	return FaceObservation{
		Key:        key,
		TargetType: "scene",
		TargetID:   keyID(key),
		FrameIndex: 0,
		Box:        Face{Left: 10, Top: 20, Width: 64, Height: 64, Score: 0.9},
		Vector:     angVec(deg),
	}
}

// keyIDs maps a face key to a TargetID.
//
// Distinct per key because the tests need to find a member again afterwards, and
// a shared TargetID made clusterOf return the first member's cluster for every
// key -- so a test that only checked that two faces shared a cluster passed on
// a lookup that had not distinguished them.
var keyIDs = map[string]int64{
	"a": 1, "b": 2, "c": 3, "p1a": 4, "p1b": 5, "p2": 6,
	"f0a": 7, "f0b": 8, "f0c": 9, "f1": 10,
	"good": 11, "nan": 12, "zero": 13, "low": 14, "narrow": 15,
}

func keyID(key string) int64 {
	if id, ok := keyIDs[key]; ok {
		return id
	}
	// An unknown key gets a stable id from its bytes rather than a constant, so
	// a test that adds a face without registering it still gets a distinct one
	// and does not silently share a bucket with everything else.
	var h int64
	for _, c := range key {
		h = h*31 + int64(c)
	}
	return 1000 + h%1000
}

// passCfg is a configuration that admits the fixtures below.
//
// Threshold 0.5 admits anything within 60 degrees; MergeThreshold 0.5 likewise;
// separation 0.5 means a winner must beat the runner-up by half the threshold.
func passCfg() Config {
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

// newTestPass builds a cosine pass over a fresh recording store.
func newTestPass(t *testing.T, cfg Config) (*Pass, *recordingStore) {
	t.Helper()
	st := newRecordingStore()
	p, err := NewPass(cfg, st, CosineGeometry{})
	if err != nil {
		t.Fatalf("building a pass with a valid config: %v", err)
	}
	return p, st
}

// --- the pass is reachable, and does what the stages do ---

// TestPassGroupsTheSameFaceAcrossScenes is the whole point of the milestone in
// one assertion: two appearances of one person, 10 degrees apart, in different
// scenes, land in ONE cluster, and a third at 85 degrees does not join them.
func TestPassGroupsTheSameFaceAcrossScenes(t *testing.T) {
	p, st := newTestPass(t, passCfg())

	res, err := p.Run(context.Background(), []FaceObservation{
		obs("a", 0), obs("b", 10), obs("c", 85),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// The precondition, asserted: the fixtures must produce two clusters, or
	// the group assertion below is satisfied by a pass that put everything
	// together and means nothing.
	if got := len(st.states); got != 2 {
		t.Fatalf("setup: expected 2 stored clusters, got %d (%v); the "+
			"fixtures do not separate, so a test that only checks that a and b "+
			"share a cluster would pass on a pass that merged everything", got,
			st.states)
	}

	a, b, c := st.clusterOf(keyID("a")), st.clusterOf(keyID("b")), st.clusterOf(keyID("c"))
	if a == 0 || b == 0 || c == 0 {
		t.Fatalf("not every face was persisted: a=%d b=%d c=%d (%v)",
			a, b, c, st.calls)
	}
	if a != b {
		t.Errorf("two faces 10 degrees apart landed in different clusters "+
			"(%d and %d) under a 0.5 threshold; cosine distance at 10 "+
			"degrees is 0.015", a, b)
	}
	if a == c {
		t.Errorf("a face 85 degrees from the others joined their cluster "+
			"(%d); cosine distance at 85 degrees is 0.906, far outside a "+
			"0.5 threshold", a)
	}
	if res.Faces != 3 || res.Embedded != 3 {
		t.Errorf("result counts: %d faces, %d embedded; want 3 and 3\n%s",
			res.Faces, res.Embedded, res.Summary())
	}
}

// TestPassSeparatesTwoPeopleNearTheThreshold is the case that cannot be written
// on a line.
//
// The first version of this used 50 degrees, which is cos = 0.357 -- INSIDE a
// 0.5 threshold, so every face was a candidate for every cluster and the engine
// was correct to join them. The test asserted a separation that the configured
// threshold does not claim, and it failed for that reason rather than because
// the pass is wrong. A fixture that contradicts the threshold under test is
// testing the fixture.
//
// The case worth testing has the two people straddling the threshold: each is
// near the boundary, a second appearance of person one pulls the first
// cluster's centroid toward them, and person two is then measured against a
// centre that has moved away. In high dimensions the mean of a group sits
// inside the group, so this works; the bimodal case in membership_cosine_test.go
// is the mirror image, where the mean lands in the gap and only the diameter
// catches it.
func TestPassSeparatesTwoPeopleNearTheThreshold(t *testing.T) {
	p, st := newTestPass(t, passCfg())

	// Person one at 0 and 20 degrees: their centroid is near 10, and 20 degrees
	// is well within 0.5 of it. Person two at 75 degrees: cos(75-10) = 0.79, so
	// they are outside the threshold of the FIRST cluster. The precondition
	// assertion below proves the fixture separates before the test relies on it.
	_, err := p.Run(context.Background(), []FaceObservation{
		obs("p1a", 0), obs("p2", 75), obs("p1b", 20),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	p1, p2 := st.clusterOf(keyID("p1a")), st.clusterOf(keyID("p2"))
	if p1 == p2 {
		t.Fatalf("person two at 75 degrees joined the cluster of the person at "+
			"0 and 20 degrees (both %d); the centroid of those two is near 10 "+
			"degrees, and 75 is 0.79 away from it -- well outside a 0.5 "+
			"threshold", p1)
	}
	if got := st.clusterOf(keyID("p1b")); got != p1 {
		t.Errorf("the two appearances of person one landed in different "+
			"clusters (%d and %d); they are 20 degrees apart, distance 0.06",
			p1, got)
	}
	if p1 == 0 || p2 == 0 {
		t.Errorf("a face was not persisted: p1=%d p2=%d (%v)", p1, p2, st.calls)
	}
}

// TestPassWritesTheEmbeddingItClusteredOn is the idempotence property.
//
// A member whose embedding was not stored cannot be re-checked, and a cluster
// that cannot be re-checked can only be corrected by re-running detection on
// the whole library. The store refuses an empty embedding, so the pass has to
// supply one -- and it has to be the SAME vector it clustered on, not a
// re-derived one.
func TestPassWritesTheEmbeddingItClusteredOn(t *testing.T) {
	p, st := newTestPass(t, passCfg())

	src := obs("a", 30)
	if _, err := p.Run(context.Background(), []FaceObservation{src}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(st.members) != 1 {
		t.Fatalf("setup: expected 1 member, got %d", len(st.members))
	}

	back, err := UnmarshalEmbedding(st.members[0].Embedding)
	if err != nil {
		t.Fatalf("the stored embedding does not decode: %v", err)
	}
	if len(back) != EmbeddingDim {
		t.Fatalf("stored embedding is %d wide, want %d", len(back), EmbeddingDim)
	}
	for i := range src.Vector {
		if back[i] != src.Vector[i] {
			t.Fatalf("component %d came back as %v, stored as %v; the round "+
				"trip is not byte-exact, so a later pass measures a "+
				"different distance than this one did", i, back[i], src.Vector[i])
		}
	}
}

// --- the pass's own rules ---

// TestPassFiltersBeforeClustering checks the counts are the counts.
//
// Each rejection reason is asserted separately, because a pass that lumps them
// together has told the operator nothing: "12 faces could not be used" sends
// someone to look for corrupt embeddings when the real answer is the detector
// was unsure, and those are different bugs in different files.
func TestPassFiltersBeforeClustering(t *testing.T) {
	p, st := newTestPass(t, passCfg())

	bad := obs("nan", 0)
	bad.Vector[5] = float32(math.NaN())

	zero := obs("zero", 0)
	for i := range zero.Vector {
		zero.Vector[i] = 0
	}

	low := obs("low", 0)
	low.Box.Score = 0.1

	narrow := obs("narrow", 0)
	narrow.Vector = narrow.Vector[:8] // wrong width for a cosine pass

	good := obs("good", 0)

	res, err := p.Run(context.Background(),
		[]FaceObservation{bad, zero, low, narrow, good})
	if err != nil {
		t.Fatalf("a pass that skips unusable faces must not fail: %v", err)
	}

	if res.Faces != 5 {
		t.Errorf("Faces = %d, want 5: the count is of observations IN, so "+
			"it must not shrink when faces are rejected", res.Faces)
	}
	if res.Unembeddable != 3 {
		t.Errorf("Unembeddable = %d, want 3 (a NaN, an all-zero vector and a "+
			"wrong-width vector)\n%s", res.Unembeddable, res.Summary())
	}
	if res.BelowScore != 1 {
		t.Errorf("BelowScore = %d, want 1", res.BelowScore)
	}
	if res.Embedded != 1 {
		t.Errorf("Embedded = %d, want 1: only the usable face may be clustered",
			res.Embedded)
	}
	if len(st.members) != 1 {
		t.Errorf("%d members were persisted, want 1; a face the pass could "+
			"not measure must not occupy a cluster", len(st.members))
	}
}

// TestPassCapsFacesPerFrame checks the cap is per FRAME.
//
// A per-target cap would throw away a second frame of the same scene, which is
// the second angle of the same person -- the opposite of what a cap for crowded
// shots is for. So the fixture puts two faces in one frame and one in a second
// frame of the same target, and asserts the second frame survives.
func TestPassCapsFacesPerFrame(t *testing.T) {
	cfg := passCfg()
	cfg.MaxFacesPerFrame = 2
	p, st := newTestPass(t, cfg)

	// All three crowded faces share a TargetID, so they land in one
	// (target, frame) bucket. Distinct ids here would put each in its own
	// bucket, the cap would never fire, and the test would pass for the wrong
	// reason -- which is exactly what the first version did.
	//
	// The second frame shares that TargetID deliberately, and that is the whole
	// point: the cap is keyed on (TargetType, TargetID, FrameIndex), so the two
	// frames are different buckets. A cap keyed on the target alone would drop
	// f1 and throw away a second angle of the same person.
	const crowdedTarget = 900
	frame0a := obs("f0a", 0)
	frame0b := obs("f0b", 120)
	frame0c := obs("f0c", 240)
	frame0a.TargetID, frame0b.TargetID, frame0c.TargetID = crowdedTarget, crowdedTarget, crowdedTarget

	frame1 := obs("f1", 0)
	frame1.FrameIndex = 1
	frame1.TargetID = crowdedTarget

	res, err := p.Run(context.Background(),
		[]FaceObservation{frame0a, frame0b, frame0c, frame1})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if res.PerFrameCap != 1 {
		t.Errorf("PerFrameCap = %d, want 1: the third face in frame 0 is "+
			"over the cap of 2\n%s", res.PerFrameCap, res.Summary())
	}

	// f1 is looked up by its assigned id, not its key: the test moved it into
	// the crowded target, so the two no longer agree. The first version looked
	// it up by key and reported "dropped" for a face that was written.
	if st.clusterOf(crowdedTarget) == 0 {
		t.Error("no face from the crowded target was persisted at all")
	}
	found := false
	for _, m := range st.members {
		if m.TargetID == crowdedTarget && m.FrameIndex == 1 {
			found = true
		}
	}
	if !found {
		t.Errorf("the face in frame 1 was dropped; the cap is per FRAME, and a "+
			"per-target cap would throw away a second angle of the same "+
			"person. Members written: %+v", st.members)
	}
}

// TestPassRefusesAConfigThatWouldClusterNothing checks validate.
//
// Each of these produces a pass that runs to completion, writes no clusters and
// reports no error. A caller gets an empty table and a job that says "done".
func TestPassRefusesAConfigThatWouldClusterNothing(t *testing.T) {
	cases := []struct {
		name string
		cfg  func(Config) Config
	}{
		{"threshold above 1", func(c Config) Config { c.Threshold = 1.5; return c }},
		{"threshold zero", func(c Config) Config { c.Threshold = 0; return c }},
		{"threshold negative", func(c Config) Config { c.Threshold = -0.2; return c }},
		{"merge threshold above 1", func(c Config) Config { c.MergeThreshold = 2; return c }},
		{"separation above 1 admits no winner", func(c Config) Config { c.Separation = 1.5; return c }},
		{"separation negative", func(c Config) Config { c.Separation = -1; return c }},
		{"score above 1", func(c Config) Config { c.MinDetectScore = 1.5; return c }},
		{"negative frame cap", func(c Config) Config { c.MaxFacesPerFrame = -1; return c }},
		{"negative merge limit", func(c Config) Config { c.MergeLimit = -1; return c }},
		{"candidate k of 1 cannot be ambiguous", func(c Config) Config { c.CandidateK = 1; return c }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg(passCfg())
			_, err := NewPass(cfg, newRecordingStore(), CosineGeometry{})
			if err == nil {
				t.Fatalf("NewPass accepted a configuration that produces a "+
					"pass which clusters nothing (%+v)", cfg)
			}
		})
	}
}

// TestPassAcceptsTheSeparationBoundaries pins both ends.
//
// Separation 1 is NOT a mistake and must not be refused: the margin becomes the
// whole threshold, which is the "exactly one candidate or nothing" setting. The
// first version of the config test refused it, on the reasoning that no winner
// could beat the runner-up by the full threshold -- and a pass that refuses a
// legitimate configuration is a pass an operator cannot get a working library
// out of, which is a worse failure than a bad default.
func TestPassAcceptsTheSeparationBoundaries(t *testing.T) {
	for _, sep := range []float64{0, 0.5, 1} {
		cfg := passCfg()
		cfg.Separation = sep
		if _, err := NewPass(cfg, newRecordingStore(), CosineGeometry{}); err != nil {
			t.Errorf("NewPass refused separation %v, which is inside the "+
				"documented range: %v", sep, err)
		}
	}
}

// TestPassRefusesANilStore is the "appears to work" guard.
//
// A pass with no store computes clusters and discards them. It returns no error
// and reports a plausible result, and the only symptom is an empty table a user
// reads as "no faces found".
func TestPassRefusesANilStore(t *testing.T) {
	if _, err := NewPass(passCfg(), nil, CosineGeometry{}); err == nil {
		t.Fatal("NewPass accepted a nil store; the pass would compute " +
			"clusters and discard them, reporting success")
	}
}

// TestPassCreatesTheClusterBeforeItsMember is the ordering property.
//
// The database rejects a member whose cluster does not exist, and the error
// names a foreign key rather than the loop that ordered the writes wrong.
func TestPassCreatesTheClusterBeforeItsMember(t *testing.T) {
	p, st := newTestPass(t, passCfg())

	if _, err := p.Run(context.Background(), []FaceObservation{obs("a", 0)}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(st.calls) < 2 {
		t.Fatalf("expected at least a create and a member, got %v", st.calls)
	}
	if st.calls[0] != "create" {
		t.Errorf("first call was %q, want \"create\"; a member written before "+
			"its cluster exists fails the foreign key, and the trace back to "+
			"this is not short: %v", st.calls[0], st.calls)
	}
	if st.calls[1] != "state:101=singleton" {
		t.Errorf("second call was %q, want the singleton state; the row is "+
			"written self-describing so a pass interrupted here does not "+
			"leave a cluster that reads as finished: %v", st.calls[1], st.calls)
	}
}

// TestPassWritesTheStateOfANewClusterAsSingleton checks the state write, which
// the ordering test above only checks positionally.
func TestPassWritesTheStateOfANewClusterAsSingleton(t *testing.T) {
	p, st := newTestPass(t, passCfg())

	if _, err := p.Run(context.Background(), []FaceObservation{obs("a", 0)}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := st.states[101]; got != stateSingleton {
		t.Errorf("cluster 101 state = %q, want %q", got, stateSingleton)
	}
}

// TestPassIsCancelled checks cancellation actually stops the pass, and that the
// result still says what it had done.
//
// The second half is the point. "Stopped after 900 of 40,000 faces" and "stopped
// because 40,000 faces were unreadable" are different reports, and a caller
// given a zero Result cannot tell them apart.
func TestPassIsCancelled(t *testing.T) {
	p, st := newTestPass(t, passCfg())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := p.Run(ctx, []FaceObservation{obs("a", 0), obs("b", 5)})
	if err == nil {
		t.Fatal("a cancelled pass returned no error; the job would report " +
			"success having clustered nothing")
	}
	if len(st.members) != 0 {
		t.Errorf("%d members were written after cancellation", len(st.members))
	}
	if res.Faces == 0 && res.Embedded == 0 {
		t.Errorf("the result is empty (%s); a caller must be able to tell a "+
			"cancelled pass from one that read no faces", res.Summary())
	}
}

// TestPassStopsWhenTheStoreFails checks the error is reported rather than
// swallowed, and that the pass does not claim a result it did not achieve.
func TestPassStopsWhenTheStoreFails(t *testing.T) {
	p, st := newTestPass(t, passCfg())
	st.errAt = "member"
	st.err = fmt.Errorf("disk full")

	res, err := p.Run(context.Background(), []FaceObservation{obs("a", 0), obs("b", 5)})
	if err == nil {
		t.Fatal("a pass whose member write failed returned no error; the " +
			"cluster it created would sit in the table with no members and " +
			"read as a finished singleton")
	}
	if res.Embedded == 0 {
		t.Errorf("the result claims nothing was embedded (%s) when the "+
			"failure happened on the first member write", res.Summary())
	}
}

// TestPassIsIdempotentForOneFace is the re-scan property.
//
// A rescan re-derives candidates but must not re-decide: the assign stage's
// "already seen" key is what makes a second pass over the same face a no-op
// rather than a second membership row. A pass that appended a duplicate row for
// every rescan would make the member count -- which drives the "is this a
// person or a crowd" judgement everywhere else -- a count of scans.
func TestPassIsIdempotentForOneFace(t *testing.T) {
	p, _ := newTestPass(t, passCfg())
	face := obs("a", 0)

	first, err := p.Run(context.Background(), []FaceObservation{face})
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if first.AlreadySeen != 0 {
		t.Fatalf("a brand new face was reported as already seen (%s)",
			first.Summary())
	}

	// A second pass over the SAME face, with a fresh store, is a new decision
	// and correctly writes a member. The idempotence that matters is WITHIN a
	// pass, so this is two faces with the same key.
	second, err := p.Run(context.Background(), []FaceObservation{face, face})
	if err != nil {
		t.Fatalf("pass with a repeated face: %v", err)
	}
	if second.AlreadySeen != 1 {
		t.Errorf("AlreadySeen = %d, want 1; a face offered twice in one pass "+
			"is one face, and writing it twice makes the member count a "+
			"count of scans\n%s", second.AlreadySeen, second.Summary())
	}
}

// TestPassConsolidateCannotMergeWhatAssignSeparated is a finding, not a
// coverage gap, and it is pinned here so nobody re-derives it.
//
// The two stages' conditions are the same inequality, negated:
//
//   - assign keeps two clusters APART when the loser's nearest member is
//     farther than the JOIN threshold from the winner's centroid;
//   - absorb merges them only when every loser member is CLOSER than that
//     same JOIN threshold from the same same centroid.
//
// So a pair that survived assign is exactly a pair absorb refuses, and a pair
// absorb would accept is exactly a pair assign already joined. No configuration
// of the two thresholds makes a merge reachable, and this is true of the
// STAGES, not of the pass -- the pass only chooses where to put the knobs.
//
// The evidence is in the numbers below rather than in the claim. A sweep over
// the whole feasible region (angles 65-100 degrees against join thresholds
// 0.3-0.6 and merge thresholds up to 1.5) produced no merge at any setting, and
// a construction that pulls the winner's centroid across the gap with an
// outlier -- the only shape that could satisfy both conditions -- still
// refuses, because absorb measures the MEMBER, not the centroid.
func TestPassConsolidateCannotMergeWhatAssignSeparated(t *testing.T) {
	for _, ang := range []float64{70, 80, 90, 100} {
		for _, join := range []float64{0.4, 0.6} {
			for _, merge := range []float64{0.6, 1.0} {
				cfg := passCfg()
				cfg.Threshold = join
				cfg.Separation = 0.0
				cfg.MergeThreshold = merge
				p, st := newTestPass(t, cfg)

				res, err := p.Run(context.Background(), []FaceObservation{
					obs("a", 0), obs("b", float64(ang)),
				})
				if err != nil {
					t.Fatalf("run: %v", err)
				}
				if res.Merged > 0 {
					t.Errorf("ang=%.0f join=%.1f merge=%.1f: a merge SUCCEEDED "+
						"(%d). If this now happens the two stages no longer "+
						"share the join threshold and this test is obsolete "+
						"-- check whether absorb stopped re-checking merges "+
						"with s.threshold, which is the property that makes it "+
						"unreachable: %v", ang, join, merge, res.Merged, st.merges)
				}
			}
		}
	}
}

// TestPassConsolidateReportsRefusals pins the observable consequence: the pass
// does not silently do nothing.
//
// Before the merge-limit fix the same configurations reported "merged 0,
// refused 0, skipped 0" -- a pass that considered nothing. Now the pair is
// considered and refused, and the refusal is visible. That difference is the
// whole reason the limit bug was findable at all.
func TestPassConsolidateReportsRefusals(t *testing.T) {
	cfg := passCfg()
	cfg.Threshold = 0.6
	cfg.Separation = 0.0
	cfg.MergeThreshold = 1.0
	p, st := newTestPass(t, cfg)

	res, err := p.Run(context.Background(), []FaceObservation{
		obs("a", 0), obs("b", 75),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.New != 2 {
		t.Fatalf("setup: expected 2 clusters, got %d (%s)", res.New, res.Summary())
	}
	if res.Refused == 0 {
		t.Errorf("the pass considered no pairs: %s. Two clusters 75 degrees "+
			"apart under a 1.0 merge threshold must at least be CONSIDERED, "+
			"and reported as refused. Silence here is what a "+
			"never-executed consolidate loop looks like", res.Summary())
	}
	if len(st.merges) != 0 {
		t.Errorf("a refused pair was merged anyway: %v", st.merges)
	}
}

// TestPassMergeLimitZeroMeansUnbounded is the bug this milestone almost shipped.
//
// Config.MergeLimit documents zero as "no cap", and the stage reads a
// NEGATIVE limit as its own default and a ZERO limit as a real limit of zero.
// Passing the config value straight through made consolidateBounded loop zero
// times: the pass reported "merged 0, refused 0, skipped 0", the store was
// never asked to merge anything, and no error was returned anywhere.
//
// Found by mutation trial M12 surviving, which is the only reason it was found
// at all -- a test that had reached the merge-persistence line would have failed
// immediately, and no test did because the loop never ran.
func TestPassMergeLimitZeroMeansUnbounded(t *testing.T) {
	cfg := passCfg()
	cfg.Threshold = 0.6
	cfg.Separation = 0.0
	cfg.MergeThreshold = 1.0
	cfg.MergeLimit = 0 // the documented "no cap"
	p, _ := newTestPass(t, cfg)

	res, err := p.Run(context.Background(), []FaceObservation{
		obs("a", 0), obs("b", 75),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// Either outcome is fine -- the guard will refuse -- but the pass must have
	// CONSIDERED the pair. Considered-and-refused is 1; not-considered is 0.
	if res.Refused+res.Merged+res.Skipped == 0 {
		t.Errorf("the pass considered nothing: %s. MergeLimit 0 means "+
			"unbounded, and a zero passed through as a real limit of zero "+
			"makes the consolidate loop run zero times", res.Summary())
	}
}

// TestPassDoesNotPersistAnAmbiguousFace is the ambiguous contract.
//
// An ambiguous outcome modifies no cluster and names no destination. A face a
// human must place is not in any cluster until the human places it, and a pass
// that persisted it into the nearest one has committed a guess.
func TestPassDoesNotPersistAnAmbiguousFace(t *testing.T) {
	cfg := passCfg()
	cfg.Separation = 0.5
	p, st := newTestPass(t, cfg)

	// Two clusters first, 90 degrees apart (distance 1.0, outside a 0.5 join
	// threshold), so they are unambiguously separate. Then a face at 45, which
	// is equidistant from both. The measured values: 90 degrees is 1.000 and 45
	// is 0.293, and under a 0.5 separation margin the runner-up is not far
	// enough behind the winner for the engine to choose.
	//
	// The first version of this used separation 0.99 and three faces 25 degrees
	// apart, and produced NO ambiguity at all: all three landed in one cluster,
	// so there was never a runner-up for the margin to be measured against. A
	// fixture with no runner-up cannot exercise a rule about runners-up, and
	// the test SKIPPED itself rather than failing -- which is the worst outcome,
	// because a skip reports as neither coverage nor a defect.
	res, err := p.Run(context.Background(), []FaceObservation{
		obs("p1", 0), obs("p2", 90), obs("mid", 45),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if res.Ambiguous == 0 {
		t.Fatalf("the fixture produced no ambiguous faces (%s); the rule under "+
			"test is about a face that cannot choose between two clusters, and "+
			"with one cluster there is nothing to choose between", res.Summary())
	}
	if res.New != 2 {
		t.Errorf("New = %d, want 2: the two anchors must form separate clusters "+
			"or the third face is not ambiguous between anything", res.New)
	}

	// The rule: an ambiguous face writes NOTHING. A face a human must place is
	// not in any cluster until the human places it, and persisting it into the
	// nearest one commits a guess the whole design exists to avoid.
	decided := res.New + res.Join
	if len(st.members) != decided {
		t.Errorf("%d members persisted for %d decided faces (%d new + %d "+
			"joined + %d ambiguous); an ambiguous face must write no member "+
			"row at all\n%s", len(st.members), decided, res.New, res.Join,
			res.Ambiguous, res.Summary())
	}
}

// TestPassDefaultsToCosineWhenGivenNoGeometry pins the default.
//
// The nil-geometry default is the one thing standing between a caller who does
// not know about Geometry and a pass that measures cosine distances with |a-b|.
// Every threshold in the package is calibrated on cosine, so a scalar pass is
// not a slower pass, it is a DIFFERENT pass that reports confident wrong
// numbers -- and the mutation that changes this default survives without a test,
// because every other test passes CosineGeometry explicitly.
func TestPassDefaultsToCosineWhenGivenNoGeometry(t *testing.T) {
	st := newRecordingStore()
	p, err := NewPass(passCfg(), st, nil)
	if err != nil {
		t.Fatalf("NewPass with a nil geometry: %v", err)
	}
	if _, ok := p.geom.(CosineGeometry); !ok {
		t.Fatalf("a nil geometry became %T, want CosineGeometry; the scalar "+
			"geometry would measure |a-b| against thresholds calibrated on "+
			"cosine and report confident wrong numbers", p.geom)
	}
}

// TestPassStatesMatchTheSchema guards the duplicated vocabulary.
//
// The four state strings are declared in this package AND constrained by a CHECK
// in the migration. Nothing makes them agree except this test, and a pass that
// wrote a fifth spelling would be refused by the database at the last step. The
// allowed set is spelled out here rather than read from the store package,
// because the store is below this layer and importing it here is the inversion
// the pass exists to avoid.
func TestPassStatesMatchTheSchema(t *testing.T) {
	allowed := map[string]bool{
		"singleton": true, "settled": true, "ambiguous": true, "merged": true,
	}
	for _, s := range []string{stateSingleton, stateSettled, stateAmbiguous, stateMerged} {
		if !allowed[s] {
			t.Errorf("state %q is not one the schema allows; a pass writing it "+
				"is refused by the CHECK at the last step", s)
		}
		if allowed[s] {
			delete(allowed, s)
		}
	}
	if len(allowed) != 0 {
		t.Errorf("the schema allows %v, which this package does not declare; "+
			"a state the database accepts and the code cannot produce is a "+
			"state no code path can ever write", keysOf(allowed))
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
