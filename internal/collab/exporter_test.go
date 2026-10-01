package collab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The exporter's tests. M3 step 3.2.
//
// The load-bearing one is TestExport_ExcludesFilenamesAndPaths, which asserts
// over the MARSHALLED JSON rather than the struct. That is not pedantry: a
// struct-level assertion cannot see a field that was added to the payload after
// the test was written, and "a field was added to the payload" is precisely the
// failure this milestone cannot have.

// fakeSource serves a fixed set of scenes.
type fakeSource struct {
	scenes     map[int64]map[string]string
	fps        map[int64]map[string][]string
	ids        []int64
	idsErr     error
	fieldsErr  error
	fpsErr     error
	scopeCalls int
	// lastScope records the library the last SceneIDs call asked for, so a test
	// can prove the scope was applied rather than assumed.
	lastScope LibraryRef
}

func (f *fakeSource) SceneIDs(ctx context.Context, lib LibraryRef) ([]int64, error) {
	f.scopeCalls++
	f.lastScope = lib
	if f.idsErr != nil {
		return nil, f.idsErr
	}
	return f.ids, nil
}

func (f *fakeSource) SceneFields(ctx context.Context, sceneID int64) (map[string]string, error) {
	if f.fieldsErr != nil {
		return nil, f.fieldsErr
	}
	m, ok := f.scenes[sceneID]
	if !ok {
		return nil, fmt.Errorf("no scene %d", sceneID)
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out, nil
}

func (f *fakeSource) SceneFingerprints(ctx context.Context, sceneID int64) (map[string][]string, error) {
	if f.fpsErr != nil {
		return nil, f.fpsErr
	}
	return f.fps[sceneID], nil
}

func testLib() LibraryRef { return LibraryRef{ID: 3, Name: "Main", OwnerID: 7} }

// TestBuildPayload_IsADryRunByDefault pins the spec §6.3 rule: the first sync
// after consent writes to disk and sends nothing. A default of false here would
// mean a user's first export is a real publish, which is the irreversible
// mistake the whole milestone is about.
func TestBuildPayload_IsADryRunByDefault(t *testing.T) {
	src := &fakeSource{ids: []int64{1}, scenes: map[int64]map[string]string{1: {"title": "A"}}}
	p, err := BuildPayload(context.Background(), src, "inst", testLib())
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	if !p.DryRun {
		t.Fatal("the first export must be a dry run: a default of false publishes a user's library without them asking")
	}

	// And it must be visible in the serialised form, not only in the struct --
	// an artefact that does not say it is a dry run is one somebody will treat
	// as a real publish.
	raw, err := p.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"dry_run":true`) {
		t.Fatalf("payload JSON does not record that it is a dry run: %s", raw)
	}
}

// TestBuildPayload_ScopesToTheLibrary proves the library filter is applied by
// the exporter rather than assumed of the caller. If SceneIDs returned
// everything, a private library's rows would reach the payload and the only
// defence would be a filter some future caller forgot.
func TestBuildPayload_ScopesToTheLibrary(t *testing.T) {
	src := &fakeSource{ids: []int64{1, 2}, scenes: map[int64]map[string]string{
		1: {"title": "One"},
		2: {"title": "Two"},
	}}
	lib := LibraryRef{ID: 42, Name: "Private", OwnerID: 7}
	p, err := BuildPayload(context.Background(), src, "inst", lib)
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	if src.scopeCalls != 1 {
		t.Fatalf("SceneIDs called %d times, want 1", src.scopeCalls)
	}
	if src.lastScope.ID != 42 {
		t.Fatalf("SceneIDs was asked for library %d, want 42 -- the scope the caller passed", src.lastScope.ID)
	}
	if len(p.Entries) != 2 {
		t.Fatalf("%d entries, want the 2 the library reported", len(p.Entries))
	}
}

// TestExport_DropsUndisclosedFields is the defence-in-depth case: a source that
// returns more than the disclosure allows must not have it reach the payload.
func TestExport_DropsUndisclosedFields(t *testing.T) {
	src := &fakeSource{
		ids: []int64{1},
		scenes: map[int64]map[string]string{1: {
			"title":   "Fine",
			"details": "Fine too",
			// None of these three are disclosed. A path is the one that matters.
			"path":        "/home/alv/videos/secret.mkv",
			"basename":    "secret.mkv",
			"studio_host": "files.example.com",
		}},
	}
	p, err := BuildPayload(context.Background(), src, "inst", testLib())
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	for _, forbidden := range []string{"path", "basename", "studio_host"} {
		if _, present := p.Entries[0].Fields[forbidden]; present {
			t.Errorf("undisclosed field %q reached the payload", forbidden)
		}
	}
	if p.Entries[0].Fields["title"] != "Fine" {
		t.Error("a disclosed field was dropped")
	}
}

// TestExport_ExcludesFilenamesAndPaths is the test the plan asks for by name,
// and the one that has to be right.
//
// It walks the MARSHALLED JSON -- every key and every value -- and asserts that
// nothing looks like a path, a filename, a hostname or an IP. A struct-level
// assertion cannot do this: it would not see a field added to ExportEntry
// later, and adding a field to the payload is the exact way this goes wrong.
//
// The fixture deliberately contains values that LOOK like paths, hostnames and
// IPs, in fields that are dropped, and asserts they are absent from the bytes.
// A test with clean fixtures would pass whether or not the filter worked.
func TestExport_ExcludesFilenamesAndPaths(t *testing.T) {
	hostile := map[string]string{
		"title": "A perfectly ordinary title",
		// Every one of these is a plausible mistake in a query.
		"path":          "/home/alv/media/2024/clip.mp4",
		"file_path":     "C:\\Users\\alv\\media\\clip.mp4",
		"basename":      "clip.mp4",
		"folder":        "../../etc/passwd",
		"host":          "stash.example.com",
		"url":           "http://192.168.1.50:9999/graphql",
		"ip":            "10.0.0.7",
		"studio_url":    "https://studio.example.org",
		"size":          "123456789",
		"last_accessed": "2026-09-27 10:00:00",
	}

	src := &fakeSource{
		ids:    []int64{1, 2},
		scenes: map[int64]map[string]string{1: hostile, 2: {"title": "Second"}},
		fps: map[int64]map[string][]string{
			1: {"phash": {"abc123"}},
		},
	}

	p, err := BuildPayload(context.Background(), src, "inst.example", testLib())
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}

	raw, err := p.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	body := string(raw)

	// The literal hostile values, if any of them survived, would be in the bytes.
	for _, leak := range []string{
		"/home/alv/media", "clip.mp4", "C:\\Users", "etc/passwd",
		"stash.example.com", "192.168.1.50", "10.0.0.7", "studio.example.org",
	} {
		if strings.Contains(body, leak) {
			t.Errorf("payload contains %q -- a value that must never be published", leak)
		}
	}

	// And the shape check, which is what catches a value nobody thought to
	// enumerate: the field NAMES that carry these are not disclosed, so even a
	// differently-valued leak would be named as a shape.
	if bad := findDisallowed(body); bad != "" {
		t.Errorf("payload matches %s; the disclosed-field filter did not hold", bad)
	}

	// The runtime guard agrees, and says why.
	if err := AssertNoDisallowedValues(p); err != nil {
		t.Errorf("AssertNoDisallowedValues rejected a clean payload: %v", err)
	}

	// Positive control: the guard MUST fire on a payload that does carry a path.
	// Without this the test above could be passing because the guard never works.
	dirty := Payload{Instance: "inst", Entries: []ExportEntry{{
		TargetType: "scene",
		TargetID:   1,
		Fields:     map[string]string{"title": "/home/alv/videos/x.mp4"},
	}}}
	if err := AssertNoDisallowedValues(dirty); err == nil {
		t.Fatal("AssertNoDisallowedValues accepted a payload containing an absolute path: the guard does not work")
	}
}

// TestAssertNoDisallowedValues_AcceptsRealTitles is the other half of the
// positive control. A guard too eager is not a safe guard: it gets disabled, and
// then it protects nothing. These are values that MUST pass.
// TestAssertNoDisallowedValues_AcceptsRealTitles covers the values the guard must
// NOT refuse. A guard that fires on ordinary content is a guard that gets
// disabled, and a disabled guard protects nothing.
func TestAssertNoDisallowedValues_AcceptsRealTitles(t *testing.T) {
	for _, title := range []string{
		"A Scene Title",
		"Scene (2019) [1080p]",
		"Winter 2024, part 2",
		"Meet me at 10.0",
		"Episode 1.2.3",
		"1920x1080 recording",
		"Directed by A. Director",
		"tags: outdoor, hiking",
	} {
		p := Payload{Instance: "inst", Entries: []ExportEntry{{
			TargetType: "scene", TargetID: 1,
			Fields: map[string]string{"title": title},
		}}}
		if err := AssertNoDisallowedValues(p); err != nil {
			t.Errorf("guard refused a legitimate title %q: %v", title, err)
		}
	}
}

// TestAssertNoDisallowedValues_CannotTellAPathFromATitleThatIsAPath records the
// guard's actual limit, because a limit written down is a design decision and a
// limit discovered in production is a bug.
//
// There is no way to tell "C:/Users/Movies/x.mp4" as a TITLE from the same
// string as a leaked path. The bytes are identical. So the guard refuses both,
// which means a user who titles a scene with a path has their export blocked.
//
// That trade is deliberate and it is the right way round: the alternative --
// letting path-shaped strings through because a title might look like one --
// makes the guard useless for the case it exists for. The cost is a rare,
// visible failure the user can fix by renaming the scene, rather than a silent
// one. The primary defence is upstream of this guard anyway: restrictToDisclosed
// drops any field that is not in PublishedFields, so the ambiguity is only
// reachable by a user typing a path into a title they are allowed to publish.
func TestAssertNoDisallowedValues_CannotTellAPathFromATitleThatIsAPath(t *testing.T) {
	for _, ambiguous := range []string{
		"C:/Users/Movies/filmed-on-windows.mp4",
		"www.example.com is my studio",
		"/home/alv/videos/x.mp4",
	} {
		p := Payload{Instance: "inst", Entries: []ExportEntry{{
			TargetType: "scene", TargetID: 1,
			Fields: map[string]string{"title": ambiguous},
		}}}
		if err := AssertNoDisallowedValues(p); err == nil {
			t.Errorf("guard accepted path-shaped value %q: it is indistinguishable from a leak, and refusing is the safe direction", ambiguous)
		}
	}

	// A disclosed field carrying ordinary user text must still pass -- the
	// guard is not simply refusing every entry.
	p, err := BuildPayload(context.Background(), &fakeSource{
		ids:    []int64{1},
		scenes: map[int64]map[string]string{1: {"title": "A title", "path": "/home/alv/x.mp4"}},
	}, "inst", testLib())
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	if err := AssertNoDisallowedValues(p); err != nil {
		t.Errorf("a disclosed field carrying user text tripped the guard: %v", err)
	}
}

// TestBuildPayload_FingerprintWhitelist pins that only content-derived hashes
// are published, and that the values are sorted so two runs produce identical
// bytes.
func TestBuildPayload_FingerprintWhitelist(t *testing.T) {
	src := &fakeSource{
		ids:    []int64{1},
		scenes: map[int64]map[string]string{1: {"title": "T"}},
		fps: map[int64]map[string][]string{1: {
			"phash":      {"zzz", "aaa", "mmm"},
			"oshash":     {"deadbeef"},
			"file_path":  {"/home/alv/x.mp4"}, // not content-derived: must be dropped
			"local_path": {"/srv/media/y.mp4"},
		}},
	}
	p, err := BuildPayload(context.Background(), src, "inst", testLib())
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	fp := p.Entries[0].Fingerprints
	if _, present := fp["file_path"]; present {
		t.Error("a path-derived fingerprint reached the payload")
	}
	if _, present := fp["local_path"]; present {
		t.Error("a path-derived fingerprint reached the payload")
	}
	if got := fp["phash"]; len(got) != 3 || got[0] != "aaa" || got[2] != "zzz" {
		t.Errorf("phash = %v, want it sorted so the payload is byte-stable", got)
	}
}

// TestSubmissionID_IsContentAddressedAndStable checks the property federation
// depends on: the same content must produce the same id, regardless of row
// order, and different content must not.
func TestSubmissionID_IsContentAddressedAndStable(t *testing.T) {
	a := []ExportEntry{
		{TargetType: "scene", TargetID: 2, Fields: map[string]string{"title": "B"}},
		{TargetType: "scene", TargetID: 1, Fields: map[string]string{"title": "A"}},
	}
	b := []ExportEntry{
		{TargetType: "scene", TargetID: 1, Fields: map[string]string{"title": "A"}},
		{TargetType: "scene", TargetID: 2, Fields: map[string]string{"title": "B"}},
	}
	if SubmissionID("inst", 3, a) != SubmissionID("inst", 3, b) {
		t.Error("submission id depends on row order: two peers with identical content would disagree about whether they already have it")
	}

	changed := []ExportEntry{{TargetType: "scene", TargetID: 1, Fields: map[string]string{"title": "A DIFFERENT"}}}
	if SubmissionID("inst", 3, a) == SubmissionID("inst", 3, changed) {
		t.Error("different content produced the same submission id")
	}
	if SubmissionID("other", 3, a) == SubmissionID("inst", 3, a) {
		t.Error("two instances produced the same submission id: an id must be scoped to the instance that produced it")
	}
}

// TestBuildPayload_IsDeterministic pins byte-stability of the whole payload,
// which is what makes the submission id meaningful.
func TestBuildPayload_IsDeterministic(t *testing.T) {
	mk := func() *fakeSource {
		return &fakeSource{
			ids:    []int64{3, 1, 2},
			scenes: map[int64]map[string]string{1: {"title": "A"}, 2: {"title": "B"}, 3: {"title": "C"}},
		}
	}
	p1, err := BuildPayload(context.Background(), mk(), "inst", testLib())
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	p2, err := BuildPayload(context.Background(), mk(), "inst", testLib())
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}
	if p1.SubmissionID != p2.SubmissionID {
		t.Error("two runs over the same data produced different submission ids")
	}
	// Entries follow the ids the source reported; determinism of the ID is what
	// matters, not the order of the slice.
	if len(p1.Entries) != 3 {
		t.Fatalf("%d entries, want 3", len(p1.Entries))
	}
}

// TestBuildPayload_PropagatesSourceErrors checks a read failure is not turned
// into a partial payload. A partial export that looks complete is worse than one
// that failed.
func TestBuildPayload_PropagatesSourceErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  *fakeSource
	}{
		{"listing fails", &fakeSource{idsErr: errors.New("disk gone")}},
		{"fields fail", &fakeSource{ids: []int64{1}, fieldsErr: errors.New("disk gone")}},
		{"fingerprints fail", &fakeSource{ids: []int64{1}, scenes: map[int64]map[string]string{1: {}}, fpsErr: errors.New("disk gone")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := BuildPayload(context.Background(), tc.src, "inst", testLib())
			if err == nil {
				t.Fatalf("BuildPayload returned (%+v, nil): a partial export that looks complete is worse than a failure", p)
			}
		})
	}
}

// TestPublish_RefusedWhenOptedOut is the milestone's headline test.
//
// It asserts THREE things, and the third is the one usually missed:
//  1. the publish is refused;
//  2. it is refused with the specific sentinel, not a generic error;
//  3. THE REFUSAL IS AUDITED, so an operator can see it happened.
func TestPublish_RefusedWhenOptedOut(t *testing.T) {
	sink := &fakeSink{}
	auditor := &fakeAuditor{}
	q := consentRow(string(ChoiceOptedOut), 1)
	p := NewPublisher("inst", &fakeSource{}, q, sink, auditor)

	payload := Payload{Instance: "inst", SubmissionID: "abc", Entries: []ExportEntry{{TargetID: 1}}}
	err := p.Publish(context.Background(), payload, 7)

	if err == nil {
		t.Fatal("Publish succeeded for a user who opted out")
	}
	if !errors.Is(err, ErrConsentOptedOut) {
		t.Fatalf("Publish returned %v, want ErrConsentOptedOut so a caller can tell a refusal from a failure", err)
	}
	if !IsPublishRefusal(err) {
		t.Error("IsPublishRefusal did not recognise the refusal it exists to recognise")
	}
	if sink.writes != 0 {
		t.Fatalf("the payload was written %d times despite the refusal", sink.writes)
	}

	// The audit row. A refusal nobody can see is indistinguishable from a
	// refusal that was never configured.
	if auditor.calls != 1 {
		t.Fatalf("auditor called %d times, want exactly 1: a refusal must leave a trace", auditor.calls)
	}
	if auditor.lastAction != PublishRefusedOptOut {
		t.Errorf("audit action = %q, want %q", auditor.lastAction, PublishRefusedOptOut)
	}
	if auditor.lastUser != 7 {
		t.Errorf("audit recorded user %d, want 7", auditor.lastUser)
	}
}

// TestPublish_ProceedsWhenOptedIn is the other direction, because a guard that
// refuses everything is not a guard, it is an outage.
func TestPublish_ProceedsWhenOptedIn(t *testing.T) {
	for _, tc := range []struct {
		name string
		q    *fakeQueryer
	}{
		{"explicitly opted in", consentRow(string(ChoiceOptedIn), 1)},
		{"never asked, which defaults to opted in", noRow()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &fakeSink{}
			auditor := &fakeAuditor{}
			p := NewPublisher("inst", &fakeSource{}, tc.q, sink, auditor)

			if err := p.Publish(context.Background(), Payload{Instance: "inst"}, 7); err != nil {
				t.Fatalf("Publish refused an opted-in user: %v", err)
			}
			if sink.writes != 1 {
				t.Errorf("payload written %d times, want 1", sink.writes)
			}
			if auditor.calls != 0 {
				t.Errorf("a successful publish wrote %d audit rows, want 0", auditor.calls)
			}
		})
	}
}

// TestPublish_AFailedConsentReadDoesNotPublish: an unreachable database is not
// consent. Both directions of "we could not ask" must refuse.
func TestPublish_AFailedConsentReadDoesNotPublish(t *testing.T) {
	sink := &fakeSink{}
	q := &fakeQueryer{qErr: errors.New("disk gone")}
	p := NewPublisher("inst", &fakeSource{}, q, sink, &fakeAuditor{})

	err := p.Publish(context.Background(), Payload{Instance: "inst"}, 7)
	if err == nil {
		t.Fatal("Publish proceeded when the consent row could not be read: a database that cannot answer is not a yes")
	}
	if errors.Is(err, ErrConsentOptedOut) {
		t.Error("an unreadable consent row was reported as a refusal; it is a failure, and the operator needs to know which")
	}
	if sink.writes != 0 {
		t.Fatalf("the payload was written %d times despite the failed consent read", sink.writes)
	}
}

// TestPublish_RefusesWithoutAConsentReader: a publisher that cannot read consent
// must fail, not assume. This is the configuration that would otherwise ship.
func TestPublish_RefusesWithoutAConsentReader(t *testing.T) {
	sink := &fakeSink{}
	p := NewPublisher("inst", &fakeSource{}, nil, sink, &fakeAuditor{})

	err := p.Publish(context.Background(), Payload{Instance: "inst"}, 7)
	if err == nil {
		t.Fatal("a publisher with no consent reader published: the one configuration that must not exist")
	}
	if sink.writes != 0 {
		t.Fatalf("the payload was written %d times with no consent reader", sink.writes)
	}
}

// TestPublish_StillRefusesWhenTheAuditWriteFails: the refusal is the safe
// outcome, so a failure to record it must not become a failure to refuse.
func TestPublish_StillRefusesWhenTheAuditWriteFails(t *testing.T) {
	sink := &fakeSink{}
	auditor := &fakeAuditor{err: errors.New("audit table locked")}
	p := NewPublisher("inst", &fakeSource{}, consentRow(string(ChoiceOptedOut), 1), sink, auditor)

	err := p.Publish(context.Background(), Payload{Instance: "inst"}, 7)
	if !errors.Is(err, ErrConsentOptedOut) {
		t.Fatalf("Publish returned %v: a failed audit write must not turn a refusal into a publish", err)
	}
	if sink.writes != 0 {
		t.Fatal("the payload was written despite the refusal")
	}
}

// TestPayload_JSONShape pins the wire format, because a peer parses it and a
// renamed key is a broken peer.
func TestPayload_JSONShape(t *testing.T) {
	p := Payload{
		Instance:     "inst",
		SubmissionID: "abc",
		EmittedAt:    time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC),
		Entries: []ExportEntry{{
			TargetType:   "scene",
			TargetID:     1,
			Fields:       map[string]string{"title": "T"},
			Fingerprints: map[string][]string{"phash": {"a"}},
		}},
	}
	raw, err := p.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	for _, key := range []string{"instance", "submission_id", "entries", "emitted_at", "dry_run"} {
		if _, present := got[key]; !present {
			t.Errorf("payload JSON is missing the top-level key %q", key)
		}
	}
	entries, ok := got["entries"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("entries = %v, want one entry", got["entries"])
	}
	entry := entries[0].(map[string]any)
	for _, key := range []string{"target_type", "target_id", "fields"} {
		if _, present := entry[key]; !present {
			t.Errorf("entry JSON is missing the key %q", key)
		}
	}
}

// fakeSink records writes.
type fakeSink struct {
	writes int
	err    error
}

func (f *fakeSink) Write(ctx context.Context, p Payload) error {
	f.writes++
	return f.err
}

// fakeAuditor records refusals.
type fakeAuditor struct {
	calls      int
	lastAction string
	lastUser   int64
	err        error
}

func (f *fakeAuditor) AppendPublishRefusal(ctx context.Context, userID int64, reason string, detail map[string]any) error {
	f.calls++
	f.lastAction = reason
	f.lastUser = userID
	return f.err
}
