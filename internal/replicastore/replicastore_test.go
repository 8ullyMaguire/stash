package replicastore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/stashapp/stash/internal/manifest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE PROMISE §6b.5 MAKES: "a replica counts as healthy only after a manifest
// verification against a content hash, never after a peer says it accepted the
// bytes."
//
// The test records a replica, asserts it is pending, then asserts it does NOT count
// toward N before verification. A store that counted rows regardless of health would
// pass a test that only checked SatisfiesN after verifying, so this checks the
// negative first.
func TestAPendingReplicaDoesNotCountTowardN(t *testing.T) {
	root := tmpRoot(t)

	s := newFakeStore()
	r := mustReconciler(t, s, nil, root)

	content := writeScene(t, root, []byte("the scene's bytes"))
	m := manifest.Manifest{Hash: sha256Hex(content), Size: int64(len(content)), SceneID: "42"}

	_, err := r.Record(context.Background(), 42, "relay-a", defaultReplicaRel, m)
	require.NoError(t, err)

	ok, err := r.SatisfiesN(context.Background(), 42, 3)
	require.NoError(t, err)
	assert.False(t, ok, "a pending replica is an intention. Counting it would make "+
		"the >=N promise true on paper the first time a peer said the bytes were on "+
		"its disk, which is precisely what §6b.5 forbids")

	c, err := r.CountsFor(context.Background(), 42)
	require.NoError(t, err)
	assert.Equal(t, Counts{Pending: 1}, c)
	assert.Equal(t, 1, c.Total())
	assert.False(t, c.SatisfiesN(1), "even N=1 is not satisfied by a pending replica")

	// Now verify, and it counts.
	h, err := r.Verify(context.Background(), 42, "relay-a", root)
	require.NoError(t, err)
	assert.Equal(t, Verified, h)

	ok, err = r.SatisfiesN(context.Background(), 42, 1)
	require.NoError(t, err)
	assert.True(t, ok, "and now it does. The transition is the point: it happens by "+
		"hashing this instance's own bytes")
}

// THE ONLY ROUTE TO 'verified' IS Verify, and this asserts the shape rather than
// the behaviour: a peer reporting "I have it, verified" must have nowhere to put
// that. Record has no health parameter and no expected-hash parameter.
func TestThereIsNoRouteToVerifiedExceptVerification(t *testing.T) {
	rt := reflect.TypeOf(&Reconciler{})

	for _, name := range []string{"Record", "Verify"} {
		m, ok := rt.MethodByName(name)
		require.True(t, ok, "%s should exist", name)
		_ = m
	}

	// No method anywhere on the type takes a free Health, which is what a caller
	// would need to assert a state.
	for i := 0; i < rt.NumMethod(); i++ {
		m := rt.Method(i)
		ft := m.Type
		// Skip the receiver.
		for j := 1; j < ft.NumIn(); j++ {
			assert.NotEqual(t, reflect.TypeOf(Health("")), ft.In(j),
				"%s takes a Health argument, so a caller could assert a state "+
					"instead of having it verified. #14 exists to close that route", m.Name)
		}
	}

	// And Settable is the rule, with verified and corrupt NOT settable.
	assert.True(t, Settable(Pending))
	assert.True(t, Settable(Missing))
	assert.False(t, Settable(Verified), "verified is the RESULT of hashing, not an input")
	assert.False(t, Settable(Corrupt), "and so is corrupt")
	assert.False(t, Settable(Health("anything-else")))
}

// A peer claiming success changes nothing, because the claim is not a path any
// caller can take. This is the test that would fail if someone added a
// trust-the-peer shortcut, and it states the shortcut's exact shape so a future
// reader knows what not to add.
func TestAPeerCannotAssertAReplicaIsVerified(t *testing.T) {
	root := tmpRoot(t)

	s := newFakeStore()
	r := mustReconciler(t, s, nil, root)

	content := writeScene(t, root, []byte("never actually written"))
	m := manifest.Manifest{Hash: sha256Hex(content), Size: int64(len(content)), SceneID: "1"}
	_, err := r.Record(context.Background(), 1, "relay-b", "replicas/x", m)
	require.NoError(t, err)

	// The peer said it is verified and the content does not exist on disk. The only
	// way to learn that is to verify, which fails.
	h, err := r.Verify(context.Background(), 1, "relay-b", root)
	require.Error(t, err, "verification reads the file and it is not there")
	assert.Equal(t, Missing, h,
		"a file that is NOT THERE is 'missing', not 'corrupt'. §6a.9 detects a peer "+
			"going offline as a missing replica, and conflating the two makes a sweep "+
			"re-fetch something that does not exist while telling the operator the "+
			"bytes are wrong")

	c, _ := r.CountsFor(context.Background(), 1)
	assert.Equal(t, 0, c.Verified, "so it does not count, however confidently the peer reported")
	assert.Equal(t, 1, c.Missing)
}

// §6b.5: a failed verification triggers RE-FETCH from a healthy peer rather than an
// alert. So the failure is recorded as corrupt AND returned as an error — the caller
// needs both facts to choose the response.
func TestAFailedVerificationIsRecordedAndReported(t *testing.T) {
	root := tmpRoot(t)

	s := newFakeStore()
	r := mustReconciler(t, s, nil, root)

	good := []byte("the correct bytes")
	m := manifest.Manifest{Hash: sha256Hex(good), Size: int64(len(good)), SceneID: "7"}
	_, err := r.Record(context.Background(), 7, "relay-c", defaultReplicaRel, m)
	require.NoError(t, err)

	// Write the WRONG bytes where the manifest says the right ones are.
	writeScene(t, root, []byte("the WRONG bytes"))

	h, err := r.Verify(context.Background(), 7, "relay-c", root)
	require.ErrorIs(t, err, ErrNotVerified)
	assert.Equal(t, Corrupt, h)

	c, _ := r.CountsFor(context.Background(), 7)
	assert.Equal(t, 1, c.Corrupt, "the row records it, so a sweep can find it")
	assert.Equal(t, 0, c.Verified)
}

// NON-NEGOTIABLE #4, twice: no stored tally, so the count cannot drift. Asserted on
// the package's SURFACE — there is no field or method that could hold a tally.
func TestTheCountIsComputedNotStored(t *testing.T) {
	// No method returns a number assembled from anything but verified rows.
	cs := reflect.TypeOf(Counts{})
	for i := 0; i < cs.NumField(); i++ {
		f := cs.Field(i)
		if f.Type.Kind() != reflect.Int {
			continue
		}
		assert.NotContains(t, strings.ToLower(f.Name), "total",
			"a field named Total next to Verified invites a caller to compare "+
				"against it, and Total includes unverified rows")
	}

	// SatisfiesN compares against Verified only. Proved by behaviour rather than by
	// reading the source, since a source reading is how the original internal/api
	// guard went stale.
	c := Counts{Verified: 2, Pending: 10, Corrupt: 5, Missing: 5}
	assert.False(t, c.SatisfiesN(3), "22 rows but 2 verified: pending, corrupt and "+
		"missing replicas must not pad the count")
	assert.True(t, c.SatisfiesN(2))
	// N=0 and negative N are refused by Reconciler rather than counted; the Counts
	// method is a pure comparison and does not second-guess its argument.
	assert.True(t, c.SatisfiesN(0), "Counts is a pure comparison; rejecting N is "+
		"Reconciler's job so there is one place that decides")
}

// N is a policy and 0 is not one. Satisfied-vacuously would make every scene pass
// every preservation check, which is a check that never fires.
func TestNZeroIsRefusedRatherThanSatisfiedVacuously(t *testing.T) {
	r := mustReconciler(t, newFakeStore(), nil, tmpRoot(t))

	for _, n := range []int{0, -1} {
		ok, err := r.SatisfiesN(context.Background(), 1, n)
		assert.Error(t, err, "N=%d must be refused", n)
		assert.False(t, ok, "and must not report satisfied")
	}
}

// #7 EXTENDED TO THREE PATHS, one of which is replication: a denied object is never
// a replication subject, and a preservation bounty never overrides it.
func TestADeniedSceneMayNotHoldAReplica(t *testing.T) {
	denied := &fakeDenials{deny: map[int]bool{9: true}}
	r := mustReconciler(t, newFakeStore(), denied, tmpRoot(t))

	content := []byte("bytes of a denied thing")
	m := manifest.Manifest{Hash: sha256Hex(content), Size: int64(len(content)), SceneID: "9"}

	_, err := r.Record(context.Background(), 9, "relay-d", "replicas/z", m)
	require.ErrorIs(t, err, ErrBlockedByDenial,
		"§6b.4: replication is a publish path, so a denied object is never a "+
			"replication subject and no preservation bounty overrides it")

	c, _ := r.CountsFor(context.Background(), 9)
	assert.Equal(t, Counts{}, c, "and nothing was recorded")
}

// An unanswerable denial question is REFUSED. Assuming "not denied" because the
// checker errored would turn a transient fault into a published copy of something a
// user denied — the failure mode a fail-closed check exists to prevent.
func TestAnUnanswerableDenialQuestionIsRefusedNotAssumed(t *testing.T) {
	broken := &fakeDenials{err: errors.New("consent database unavailable")}
	r := mustReconciler(t, newFakeStore(), broken, tmpRoot(t))

	content := []byte("bytes")
	m := manifest.Manifest{Hash: sha256Hex(content), Size: int64(len(content)), SceneID: "3"}
	_, err := r.Record(context.Background(), 3, "relay-e", "replicas/w", m)

	require.Error(t, err, "the record must not proceed on an unanswerable question")
	assert.NotErrorIs(t, err, ErrBlockedByDenial,
		"and the error must not claim a denial, because none was established")
	assert.Contains(t, err.Error(), "cannot determine")
}

// #13: a path from a stranger is the one input that can name a file outside the
// library. Checked at write AND at read, because Verify opens whatever it is given.
func TestAReplicaPathFromAStrangerCannotEscapeTheStorageRoot(t *testing.T) {
	root := tmpRoot(t)

	s := newFakeStore()
	r := mustReconciler(t, s, nil, root)

	content := []byte("x")
	m := manifest.Manifest{Hash: sha256Hex(content), Size: 1, SceneID: "1"}

	for _, bad := range []string{
		"/etc/passwd",
		"../../etc/passwd",
		"replicas/../../../etc/passwd",
		"   ",
	} {
		_, err := r.Record(context.Background(), 1, "relay", bad, m)
		assert.Error(t, err, "path %q must be refused before it is ever written", bad)
	}

	assert.Empty(t, s.rows, "and no bad path reached the store")
}

// The read-time check exists because a row can predate the check or be edited
// directly in the database. Without it, Verify would open whatever it was given.
func TestVerificationRefusesAStoredPathThatEscapesTheRoot(t *testing.T) {
	root := tmpRoot(t)

	s := newFakeStore()
	// Insert a row DIRECTLY into the store, bypassing Record's check entirely --
	// which is what a row written before this check existed looks like.
	content := []byte("secret")
	m := manifest.Manifest{Hash: sha256Hex(content), Size: int64(len(content)), SceneID: "1"}
	s.rows["1|relay"] = Replica{ID: 1, SceneID: 1, SourceEndpoint: "relay",
		ReplicaPath: "../../../etc/passwd", ManifestHash: m, Health: Pending}

	r := mustReconciler(t, s, nil, root)

	_, err := r.Verify(context.Background(), 1, "relay", root)
	require.Error(t, err, "a stored path that escapes must be refused at read time "+
		"even though nothing wrote it through Record")
	// Assert the RULE rather than one phrasing of it: checkPath rejects the ".."
	// and resolve independently rejects a join that escapes, and they say
	// different things. Pinning the exact wording would make the test fail on a
	// reworded message while the check still works.
	assert.Contains(t, err.Error(), "replica path",
		"and the error must say what was refused, not merely that something was")

	// And it did NOT become verified.
	assert.Equal(t, Pending, s.rows["1|relay"].Health,
		"a refused path leaves the row as it was, rather than half-processed")
}

// A storage root is REQUIRED, because without one there is no boundary and the path
// check accepts everything — a guard that passes because it has nothing to compare
// against.
func TestAReconcilerWithNoStorageRootCannotBeBuilt(t *testing.T) {
	_, err := New(newFakeStore(), nil, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "storage root",
		"the error names WHY, because the alternative is a check that passes vacuously")

	_, err = New(newFakeStore(), nil, "   ")
	assert.Error(t, err, "whitespace is not a root")

	_, err = New(nil, nil, "/srv")
	assert.Error(t, err, "and no store means nothing can be recorded")
}

// Namespacing (§6a.6): a peer's scene 412 is not this instance's scene 412. Two
// peers serving the same local scene must be two rows, or the mesh believes it
// holds a replica it does not have.
func TestTwoPeersForOneSceneAreTwoReplicas(t *testing.T) {
	root := tmpRoot(t)

	s := newFakeStore()
	r := mustReconciler(t, s, nil, root)

	content := []byte("shared scene")
	m := manifest.Manifest{Hash: sha256Hex(content), Size: int64(len(content)), SceneID: "412"}

	_, err := r.Record(context.Background(), 412, "relay-a", "replicas/a", m)
	require.NoError(t, err)
	_, err = r.Record(context.Background(), 412, "relay-b", "replicas/b", m)
	require.NoError(t, err)

	assert.Len(t, s.rows, 2, "the source endpoint is part of the identity. Two peers "+
		"serving one local scene id collide into a single row without it, and the "+
		"mesh then believes it holds a copy it does not have — a missing copy that "+
		"health checks pass")

	c, _ := r.CountsFor(context.Background(), 412)
	assert.Equal(t, 2, c.Pending)
}

// A pending replica is WORK, not a FAULT. §6b.5: a failed verification triggers
// re-fetch rather than an alert, so the sweep returns pending rows and says so.
func TestAPendingReplicaIsWorkRatherThanAnAlert(t *testing.T) {
	root := tmpRoot(t)

	s := newFakeStore()
	r := mustReconciler(t, s, nil, root)

	content := []byte("pending bytes")
	m := manifest.Manifest{Hash: sha256Hex(content), Size: int64(len(content)), SceneID: "5"}
	_, err := r.Record(context.Background(), 5, "relay-f", "replicas/p", m)
	require.NoError(t, err)

	needs, err := r.NeedsVerification(context.Background())
	require.NoError(t, err)
	require.Len(t, needs, 1)
	assert.Equal(t, 5, needs[0].SceneID)

	// Marking one missing takes it OUT of the verification sweep: it is gone, not
	// waiting.
	require.NoError(t, r.MarkMissing(context.Background(), 5, "relay-f"))
	needs, err = r.NeedsVerification(context.Background())
	require.NoError(t, err)
	assert.Empty(t, needs, "a missing replica has nothing to verify")
}

// The sweep returns PENDING rows and nothing else.
//
// Mutation `M` widened this filter to include corrupt rows and every test still
// passed, because no test ever left a CORRUPT row in the store before asking for
// the sweep. The two states need different responses -- §6b.5 has a failed
// verification trigger RE-FETCH, while a pending replica is simply unexamined --
// so a sweep that returns them together makes the caller choose, and it will
// eventually choose the wrong one.
func TestTheVerificationSweepReturnsPendingAndNothingElse(t *testing.T) {
	root := tmpRoot(t)
	s := newFakeStore()
	r := mustReconciler(t, s, nil, root)

	content := []byte("bytes")
	m := manifest.Manifest{Hash: sha256Hex(content), Size: int64(len(content)), SceneID: "1"}
	_, err := r.Record(context.Background(), 1, "relay-pending", defaultReplicaRel, m)
	require.NoError(t, err)

	// A VERIFIED row needs a file whose bytes match its manifest.
	good := []byte("verified bytes")
	writeScene(t, root, good)
	vm := manifest.Manifest{Hash: sha256Hex(good), Size: int64(len(good)), SceneID: "2"}
	_, err = r.Record(context.Background(), 2, "relay-verified", defaultReplicaRel, vm)
	require.NoError(t, err)
	_, err = r.Verify(context.Background(), 2, "relay-verified", root)
	require.NoError(t, err)

	// A CORRUPT row needs a file whose bytes do NOT match its manifest: the
	// manifest names "what it claims", the file will hold something else.
	claimed := []byte("what it claims")
	bm := manifest.Manifest{Hash: sha256Hex(claimed), Size: int64(len(claimed)), SceneID: "3"}
	_, err = r.Record(context.Background(), 3, "relay-corrupt", defaultReplicaRel, bm)
	require.NoError(t, err)
	writeScene(t, root, []byte("the WRONG bytes"))

	corruptOutcome, verr := r.Verify(context.Background(), 3, "relay-corrupt", root)
	require.Error(t, verr)
	require.Equal(t, Corrupt, corruptOutcome, "precondition: this row is corrupt")

	needs, err := r.NeedsVerification(context.Background())
	require.NoError(t, err)

	require.Len(t, needs, 1, "exactly the pending replica")
	assert.Equal(t, 1, needs[0].SceneID)
	for _, rep := range needs {
		assert.Equal(t, Pending, rep.Health,
			"a corrupt replica is NOT work waiting to be done -- it is a failed "+
				"verification with its own response. Including it here would make the "+
				"sweep re-fetch rows that already failed and report them as new")
	}
}

// A store must refuse to be asked to assert an unverified health. Settable is the
// rule; an implementation that ignores it would let #14 reopen through the storage
// layer rather than through this package.
func TestTheStoreRefusesToBeAskedToAssertAnUnverifiedHealth(t *testing.T) {
	root := tmpRoot(t)

	s := newFakeStore()
	r := mustReconciler(t, s, nil, root)

	content := []byte("x")
	m := manifest.Manifest{Hash: sha256Hex(content), Size: 1, SceneID: "1"}
	_, err := r.Record(context.Background(), 1, "relay", defaultReplicaRel, m)
	require.NoError(t, err)

	// Going through Reconcilor.mark refuses non-settable states.
	err = r.mark(context.Background(), 1, "relay", Verified)
	require.ErrorIs(t, err, ErrUnverifiedTransition,
		"the reconciler refuses, naming the rule")

	// And a settable one works through the same route.
	require.NoError(t, r.MarkMissing(context.Background(), 1, "relay"))
	assert.Equal(t, Missing, s.rows["1|relay"].Health)

	// The store rejects a state that does not exist, so a typo or a stale constant
	// cannot be written as though it were real.
	err = s.SetSettableHealth(context.Background(), 1, "relay", Health("probably-fine"))
	assert.Error(t, err,
		"an unknown state is refused. The store accepts any KNOWN state -- including "+
			"the verifier's outcomes, which are results rather than assertions -- but "+
			"not a value that is not in the enum")

	// WHY ENFORCEMENT LIVES HERE AND NOT IN THE STORE. The first version of this
	// test demanded that the store ALSO enforce Settable, which contradicted the
	// contract and had to be relaxed: if the store refuses verified, the verifier
	// cannot record its own outcome. A peer never calls the store directly -- it
	// goes through Reconciler.Verify, which hashes this instance's bytes -- so the
	// reconciler is where a caller's assertion is refused, and that is where #14
	// is actually closed.
}

// A malformed manifest from a peer is refused before a row is created, because a
// row that cannot be verified later is a row that counts for nothing and blocks the
// scene from ever reaching N.
func TestAnUnverifiableManifestIsRefusedBeforeARowExists(t *testing.T) {
	root := tmpRoot(t)

	s := newFakeStore()
	r := mustReconciler(t, s, nil, root)

	for name, m := range map[string]manifest.Manifest{
		"empty hash":  {Hash: "", Size: 10, SceneID: "1"},
		"zero size":   {Hash: strings.Repeat("a", 64), Size: 0, SceneID: "1"},
		"short hash":  {Hash: "abcd", Size: 10, SceneID: "1"},
		"no scene id": {Hash: strings.Repeat("a", 64), Size: 10, SceneID: ""},
	} {
		_, err := r.Record(context.Background(), 1, "relay", "replicas/z", m)
		assert.Error(t, err, "%s must be refused", name)
	}
	assert.Empty(t, s.rows, "no row was created for any of them")
}

// --- fakes ---------------------------------------------------------------

func mustReconciler(t *testing.T, s Store, d DenialChecker, root string) *Reconciler {
	t.Helper()
	r, err := New(s, d, root)
	require.NoError(t, err)
	return r
}

// fakeStore records what it was asked to do, and enforces the Settable rule itself
// because a second implementation cannot be trusted to.
type fakeStore struct {
	rows map[string]Replica
	next int
}

func newFakeStore() *fakeStore { return &fakeStore{rows: map[string]Replica{}} }

func key(sceneID int, ep string) string { return itoa(sceneID) + "|" + ep }

func (f *fakeStore) Create(_ context.Context, r Replica) (int, error) {
	// Settable is enforced by the RECONCILER, not here. The first version of this
	// fake refused verified/corrupt on Create, which made it stricter than the real
	// store's contract and left Verify unable to record its own outcome -- so the
	// suite failed on a rule the fake invented rather than one the code states.
	if !Known(r.Health) {
		return 0, ErrUnverifiedTransition
	}
	f.next++
	r.ID = f.next
	f.rows[key(r.SceneID, r.SourceEndpoint)] = r
	return r.ID, nil
}

func (f *fakeStore) ByKey(_ context.Context, sceneID int, ep string) (Replica, error) {
	r, ok := f.rows[key(sceneID, ep)]
	if !ok {
		return Replica{}, ErrNoReplica
	}
	return r, nil
}

func (f *fakeStore) ForScene(_ context.Context, sceneID int) ([]Replica, error) {
	var out []Replica
	for _, r := range f.rows {
		if r.SceneID == sceneID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStore) Unverified(_ context.Context) ([]Replica, error) {
	var out []Replica
	for _, r := range f.rows {
		if r.Health != Verified {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStore) SetSettableHealth(_ context.Context, sceneID int, ep string, h Health) error {
	// Accepts any KNOWN state, including the verifier's outcomes. The caller-route
	// restriction lives in Reconciler.mark and is asserted by
	// TestTheStoreRefusesToBeAskedToAssertAnUnverifiedHealth.
	if !Known(h) {
		return ErrUnverifiedTransition
	}
	r, ok := f.rows[key(sceneID, ep)]
	if !ok {
		return ErrNoReplica
	}
	r.Health = h
	f.rows[key(sceneID, ep)] = r
	return nil
}

type fakeDenials struct {
	deny map[int]bool
	err  error
}

func (d *fakeDenials) ReplicationDenied(_ context.Context, sceneID int) (bool, error) {
	if d.err != nil {
		return false, d.err
	}
	return d.deny[sceneID], nil
}

// --- helpers ---------------------------------------------------------------

// writeScene writes content under <root>/replicas/scene and returns it.
//
// The root is always a t.TempDir(). The first version of these tests passed the
// LITERAL "/srv/storage", so three of them failed on `mkdir /srv/storage:
// permission denied` -- which is not a failure of the code under test and reads,
// in a CI log, exactly like one. A test that fails for a reason unrelated to its
// subject trains its reader to look past the message.
// writeSceneAt writes content at <root>/<rel>, creating the parent directory.
//
// TAKES THE RELATIVE PATH THE TEST RECORDED. The first version always wrote to
// "replicas/scene" while the tests recorded paths like "replicas/abc", so Verify
// was asked to open a file that had never been written -- and then correctly
// reported it missing. A test that exercises the wrong path is not testing the
// thing it names; the reconciler was right and the test was wrong.
func writeSceneAt(t *testing.T, root, rel string, content []byte) []byte {
	t.Helper()
	full := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, content, 0o600))
	return content
}

// writeScene writes to the default path used by most tests.
func writeScene(t *testing.T, root string, content []byte) []byte {
	t.Helper()
	return writeSceneAt(t, root, defaultReplicaRel, content)
}

// defaultReplicaRel is the recorded path most tests use, so writeScene and the
// Record calls agree on one name.
const defaultReplicaRel = "replicas/scene"

// tmpRoot is a writable storage root for a test.
func tmpRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	// The reconciler refuses a root that is only whitespace and requires one it can
	// compare a path against; a temp dir satisfies both.
	return root
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// itoa formats an int.
//
// The first version of this helper HASHED the rune instead of formatting the
// number, which meant two scene ids could collide in the fake's map key -- silently
// merging two replicas' rows. That is precisely the §6a.6 bug
// TestTwoPeersForOneSceneAreTwoReplicas exists to catch, hiding inside the fake
// that test uses. A fake that cannot faithfully represent the data under test is
// worse than no fake.
func itoa(n int) string { return strconv.Itoa(n) }

// THE VERIFIER'S BYPASS MUST STAY NARROW. Mutation `N` added Pending to
// markVerifiedOutcome's allowed set and every test still passed, because the method
// has no test asserting its own boundary -- it is only ever called with an outcome.
//
// It IS one call site wide by design, since the point of the bypass is that
// Settable restricts callers and verified/corrupt are results rather than
// assertions. A bypass with no boundary test is a bypass that widens silently.
func TestTheVerifiersBypassCoversOutcomesAndNothingElse(t *testing.T) {
	root := tmpRoot(t)
	s := newFakeStore()
	r := mustReconciler(t, s, nil, root)

	// Record a row first. The first version of this test called the bypass with no
	// replica in existence, so the store answered ErrNoReplica -- a real answer to
	// a question the test had not set up, and not the rule under test.
	content := []byte("bytes")
	m := manifest.Manifest{Hash: sha256Hex(content), Size: int64(len(content)), SceneID: "1"}
	_, err := r.Record(context.Background(), 1, "relay", defaultReplicaRel, m)
	require.NoError(t, err)

	// Outcomes are accepted: these are what Verify passes.
	for _, h := range []Health{Verified, Corrupt, Missing} {
		err := r.markVerifiedOutcome(context.Background(), 1, "relay", h)
		assert.NoError(t, err, "%s is a verification outcome and must be recordable", h)
	}

	// Anything else is refused -- and Pending is the case that matters, because it
	// is the one a CALLER may set, so accepting it here would collapse the
	// distinction the method exists to keep.
	err = r.markVerifiedOutcome(context.Background(), 1, "relay", Pending)
	require.ErrorIs(t, err, ErrUnverifiedTransition,
		"pending is a CALLER-settable observation, not a verification outcome. "+
			"Accepting it here would give the verifier a route to write a state that "+
			"should have gone through mark, and the two routes enforce different rules")

	err = r.markVerifiedOutcome(context.Background(), 1, "relay", Health("invented"))
	assert.Error(t, err, "an unknown state is not an outcome either")
}
