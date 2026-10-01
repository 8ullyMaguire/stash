package autotag

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/autoproposal"
	"github.com/stashapp/stash/internal/collab"
)

// The tests in sink_test.go use a RECORDING sink, which is the right tool for "where
// did this match go" and the wrong tool for "what does DirectSink and ProposalSink
// actually write". Those two are the governance boundary, and the mutation gate caught
// both of them surviving: nothing here was watching.
//
// fakeTargets implements collab.TargetStore for the two methods the sinks call and
// panics on the rest, so a sink reaching for a method these tests do not model is a
// loud failure rather than a silent no-op.
type fakeTargets struct {
	mu sync.Mutex

	// field value per "type|id|field", and whether the row/field is present at all.
	// PRESENT AND NULL ARE DIFFERENT, and the difference is the whole of the
	// single-writer rule, so this fake keeps them apart exactly as the real store does.
	fields map[string]*string

	// the arguments the last WriteFieldIfChanged received, so a test can assert on the
	// `expected` it was handed and not only on the outcome.
	lastField   string
	lastExpect  *string
	lastValue   *string
	writeCalls  int
	linkCalls   int
	lastLinkKey string

	writeErr error
	linkErr  error
	// linkAdded mirrors the real store: true when a row was INSERTED, false when the
	// insert was ignored because the member was already there. DirectSink inverts it
	// into `already`, and a fake that got this backwards would have the test asserting
	// the inverse of the real behaviour -- which is exactly what happened the first
	// time round.
	linkAdded bool
}

func newFakeTargets() *fakeTargets {
	// linkAdded defaults TRUE, matching the ordinary case where the member is not yet
	// in the set -- which is the case every test but one wants.
	return &fakeTargets{fields: map[string]*string{}, linkAdded: true}
}

func fk(kind string, id int, field string) string {
	return kind + "|" + strconv.Itoa(id) + "|" + field
}

func (f *fakeTargets) WriteFieldIfChanged(_ context.Context, targetType string, targetID int, field string, expected, value *string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeCalls++
	f.lastField = field
	f.lastExpect = expected
	f.lastValue = value
	if f.writeErr != nil {
		return false, f.writeErr
	}

	k := fk(targetType, targetID, field)
	current, present := f.fields[k]

	// A nil `expected` means "only if currently NULL", which is the real store's rule
	// and the one DirectSink.SetStudio depends on. Modelling it here is what makes the
	// test able to catch an implementation that passes the wrong thing.
	if expected == nil {
		if present && current != nil {
			return false, nil
		}
	} else {
		if !present || current == nil || *current != *expected {
			return false, nil
		}
	}

	v := *value
	f.fields[k] = &v
	return true, nil
}

func (f *fakeTargets) AddLink(_ context.Context, targetType string, targetID int, kind collab.LinkKind, entityID int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.linkCalls++
	f.lastLinkKey = fk(targetType, targetID, string(kind))
	if f.linkErr != nil {
		return false, f.linkErr
	}
	return f.linkAdded, nil
}

// The remaining TargetStore methods are unmodelled on purpose: a sink that reaches for
// one is a design change, and a test that silently allowed it would be a lie.
func (f *fakeTargets) ReadField(context.Context, string, int, string) (*string, bool, error) {
	panic("ReadField is not modelled by this fake; a sink must not read before writing")
}

func (f *fakeTargets) MarkRejected(context.Context, int, int, string) error {
	panic("MarkRejected is not modelled by this fake")
}

func (f *fakeTargets) AppendAudit(context.Context, collab.AuditEntry) error {
	panic("AppendAudit is not modelled by this fake")
}

// recordingProposer is a collab.Proposer that keeps what it was asked to create.
type recordingProposer struct {
	mu      sync.Mutex
	created []collab.Proposal
	err     error
	nextID  int
}

func (p *recordingProposer) Create(_ context.Context, req collab.Proposal) (*collab.Proposal, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	p.nextID++
	p.created = append(p.created, req)
	out := req
	out.ID = p.nextID
	return &out, nil
}

func (p *recordingProposer) proposals() []collab.Proposal {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]collab.Proposal(nil), p.created...)
}

func testCurator(t *testing.T, p *recordingProposer) *autoproposal.Curator {
	t.Helper()
	curator, err := autoproposal.NewCurator(p,
		autoproposal.Attribution{
			Author: autoproposal.Author{UserID: 1, Name: "owner"},
			Source: "autotag",
		}, autoproposal.Policy{})
	require.NoError(t, err, "a curator with a proposer and a real author is buildable")
	return curator
}

// DIRECT APPLY FILLS AN EMPTY STUDIO FIELD.
//
// This is the basic case, and it is worth having plainly: after the sink split, the
// direct path writes a COLUMN through the store rather than calling the repository's
// UpdatePartial, and nothing else in the suite exercises that it still works at all.
func TestDirectSetStudioFillsAnEmptyField(t *testing.T) {
	targets := newFakeTargets()
	sink := DirectSink{Targets: targets}

	already, err := sink.SetStudio(context.Background(), "scene", 7, 42)
	require.NoError(t, err)

	assert.False(t, already, "filling an empty field is a CHANGE, so it is not `already`")
	assert.Equal(t, "42", *targets.fields[fk("scene", 7, "studio_id")],
		"the field must hold the studio id as written")
	assert.Equal(t, 1, targets.writeCalls)
}

// AND IT ASKS TO WRITE ONLY IF THE FIELD IS NULL -- the single-writer guarantee.
//
// This is the assertion the mutation gate asked for. Passing `expected = &value`
// instead of `nil` compiles, works on a first run, and turns a single-writer field
// into a last-writer-wins one: the second autotag run over a scene that already names
// a studio would overwrite it. That is the whole safety argument for a direct studio
// write, and no test asserted the argument, so the mutation survived.
func TestDirectSetStudioRefusesToOverwriteAnExistingStudio(t *testing.T) {
	targets := newFakeTargets()
	// The field already holds a DIFFERENT studio -- say a human set it deliberately.
	existing := "7"
	targets.fields[fk("scene", 7, "studio_id")] = &existing

	sink := DirectSink{Targets: targets}
	already, err := sink.SetStudio(context.Background(), "scene", 7, 42)
	require.NoError(t, err, "declining to overwrite is not an error")

	assert.True(t, already,
		"the field was already set, so nothing changed. Reporting a change here is "+
			"what would make a scan log a write it did not make")
	assert.Equal(t, "7", *targets.fields[fk("scene", 7, "studio_id")],
		"THE DELIBERATE CHOICE MUST SURVIVE. A machine's guess overwriting a value a "+
			"person entered is the failure a single-writer field exists to prevent")
}

// AND `expected` IS PASSED AS NIL, which is the mechanism -- asserted directly, so the
// reason is visible in the test rather than inferred from the outcome.
func TestDirectSetStudioAsksTheStoreToWriteOnlyIntoNull(t *testing.T) {
	targets := newFakeTargets()
	sink := DirectSink{Targets: targets}

	_, err := sink.SetStudio(context.Background(), "scene", 7, 42)
	require.NoError(t, err)

	assert.Nil(t, targets.lastExpect,
		"expected must be nil, which means 'only if currently NULL'. Any non-nil "+
			"value here converts the single-writer field into last-writer-wins")
	assert.Equal(t, "42", *targets.lastValue)
}

// A WRITE FAILURE PROPAGATES. The same no-fallback rule as AddMatch: a direct write
// that silently did nothing would leave the field empty and the scan reporting success.
func TestDirectSetStudioReturnsAWriteFailure(t *testing.T) {
	targets := newFakeTargets()
	targets.writeErr = assert.AnError

	_, err := DirectSink{Targets: targets}.SetStudio(context.Background(), "scene", 7, 42)
	assert.ErrorIs(t, err, assert.AnError,
		"a failed write must reach the caller; swallowing it makes an empty field "+
			"look like a scan that found nothing")
}

// AND THE LINK PATH STILL GOES THROUGH THE STORE, so the direct and governed paths
// share one implementation of "what a link is".
func TestDirectAddMatchWritesThroughTheStore(t *testing.T) {
	targets := newFakeTargets()
	sink := DirectSink{Targets: targets}

	already, err := sink.AddMatch(context.Background(), "scene", 7, collab.LinkScenePerformer, 42, "x")
	require.NoError(t, err)

	assert.False(t, already, "AddLink reported it added, so this was a change")
	assert.Equal(t, 1, targets.linkCalls)
	assert.Equal(t, fk("scene", 7, string(collab.LinkScenePerformer)), targets.lastLinkKey)
}

// A LINK THAT WAS ALREADY THERE REPORTS `already`, which is the signal the caller uses
// to skip a log line.
func TestDirectAddMatchReportsAnExistingLinkAsAlready(t *testing.T) {
	targets := newFakeTargets()
	targets.linkAdded = false
	sink := DirectSink{Targets: targets}

	already, err := sink.AddMatch(context.Background(), "scene", 7, collab.LinkScenePerformer, 42, "x")
	require.NoError(t, err)
	assert.True(t, already, "AddLink reported nothing was added, so nothing changed")
}

// THE GOVERNED PATH FILES A PROPOSAL AND WRITES NOTHING.
//
// The direct half of the pair, and the one that matters: with curation on, a studio
// claim must reach the proposal store and NOT the field. A test that only checked
// "a proposal was filed" would pass even if the field were also written, so this
// asserts the write count is zero.
func TestProposalSetStudioFilesAProposalAndWritesNothing(t *testing.T) {
	proposer := &recordingProposer{}
	targets := newFakeTargets()
	sink := ProposalSink{Curator: testCurator(t, proposer)}

	already, err := sink.SetStudio(context.Background(), "scene", 7, 42)
	require.NoError(t, err)

	assert.False(t, already,
		"A FILED PROPOSAL IS NOT `already`. The claim is pending, and reporting it as "+
			"already-correct would suppress the log line the operator needs to see it")
	assert.Equal(t, 0, targets.writeCalls,
		"THE GOVERNED PATH MUST NOT WRITE THE FIELD. A test asserting only that a "+
			"proposal was filed would pass even if the direct write also happened, "+
			"which is the exact bypass §6b.2 forbids")
	assert.Empty(t, targets.fields)

	filed := proposer.proposals()
	require.Len(t, filed, 1, "exactly one claim")
	assert.Equal(t, autoproposal.KindSceneStudio, filed[0].Field,
		"filed on the SINGLE-WRITER field, not as a join-table link. A studio is a "+
			"column where the last accepted writer wins, so a second claim to it has to "+
			"be visible as a COMPETING claim on one field rather than as a second member "+
			"of a set")
	assert.Equal(t, "scene", filed[0].TargetType)
	assert.Equal(t, 7, filed[0].TargetID)
}

// AND IT IS ATTRIBUTED, because §4.2's audit trail is the only way an operator later
// learns where a value came from.
func TestProposalSetStudioAttributesTheClaimToTheConfiguredUser(t *testing.T) {
	proposer := &recordingProposer{}
	sink := ProposalSink{Curator: testCurator(t, proposer)}

	_, err := sink.SetStudio(context.Background(), "scene", 7, 42)
	require.NoError(t, err)

	filed := proposer.proposals()
	require.Len(t, filed, 1)
	assert.NotZero(t, filed[0].AuthorID,
		"an automatic claim must name the user it is made on behalf of, or the audit "+
			"trail records an author nobody can name")
}

// A FILING FAILURE IS AN ERROR AND NEVER A DIRECT WRITE. The load-bearing assertion of
// the governed path, and the one the mutation gate found missing: returning `true`
// here would make a broken proposal store look like a clean no-op.
func TestProposalSetStudioReturnsAFilingFailure(t *testing.T) {
	proposer := &recordingProposer{err: fmt.Errorf("proposal store is down")}
	targets := newFakeTargets()
	sink := ProposalSink{Curator: testCurator(t, proposer)}

	already, err := sink.SetStudio(context.Background(), "scene", 7, 42)
	assert.Error(t, err,
		"a failed filing must reach the caller. Reporting `already` and swallowing it "+
			"would hide a broken proposal store behind a quiet, successful-looking scan")
	assert.False(t, already)
	assert.Equal(t, 0, targets.writeCalls,
		"and above all: a filing failure must NOT fall back to writing the field. "+
			"That fallback is how an automatic write sneaks past a broken audit trail "+
			"while looking like success, because the field would hold the right value")
}

// BOTH SINKS REFUSE TO WORK WITH NOTHING TO WRITE TO. The zero-value guard, for the
// two sinks rather than for the Tagger.
func TestBothSinksRefuseToRunWithNoStore(t *testing.T) {
	ctx := context.Background()

	_, err := DirectSink{}.SetStudio(ctx, "scene", 7, 42)
	assert.Error(t, err, "a direct studio write with no store would silently do nothing")

	_, err = DirectSink{}.AddMatch(ctx, "scene", 7, collab.LinkScenePerformer, 42, "x")
	assert.Error(t, err)

	_, err = ProposalSink{}.SetStudio(ctx, "scene", 7, 42)
	assert.Error(t, err, "a governed studio claim with no curator would file nothing")

	_, err = ProposalSink{}.AddMatch(ctx, "scene", 7, collab.LinkScenePerformer, 42, "x")
	assert.Error(t, err)
}
