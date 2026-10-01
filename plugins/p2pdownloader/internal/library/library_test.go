package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptedHost is a Host that answers from a script.
//
// The interface exists so this package's orchestration can be driven without an
// HTTP server, and the whole point of a scripted host is that a test can make
// the host answer in a specific ORDER — the ordering being what the tests are
// about. A host that answered correctly-but-differently would test nothing.
type scriptedHost struct {
	mu sync.Mutex

	// scanErr is what Scan returns, if set.
	scanErr error

	// sceneAfter is how many SceneForPath calls return nil before one returns a
	// scene. Zero means the first call returns a scene.
	sceneAfter int

	calls   int
	scans   [][]string
	queries []string

	// sceneErr, when set, is returned by every SceneForPath call. For the
	// transport-failure branch.
	sceneErr error

	// lastScene is what the successful call returns.
	lastScene *Scene

	// jobStatus is what JobStatus returns. Empty means "the host has no such
	// job", which the real host answers for a job it has forgotten.
	jobStatus string

	// jobErr, when set, is returned by every JobStatus call.
	jobErr error

	// jobQueries counts the job lookups, so a test can assert the wait stops
	// asking once the answer cannot change.
	jobQueries int
}

func (h *scriptedHost) JobStatus(ctx context.Context, jobID string) (Job, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.jobQueries++
	if h.jobErr != nil {
		return Job{}, h.jobErr
	}
	if h.jobStatus == "" {
		return Job{}, nil
	}
	return Job{ID: jobID, Status: h.jobStatus}, nil
}

func (h *scriptedHost) Scan(ctx context.Context, paths []string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.scans = append(h.scans, paths)
	if h.scanErr != nil {
		return "", h.scanErr
	}
	return "42", nil
}

func (h *scriptedHost) SceneForPath(ctx context.Context, path string) (*Scene, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.queries = append(h.queries, path)
	if h.sceneErr != nil {
		return nil, h.sceneErr
	}
	if h.calls < h.sceneAfter {
		h.calls++
		return nil, nil
	}
	h.calls++
	if h.lastScene != nil {
		return h.lastScene, nil
	}
	return &Scene{ID: "scene-1", Path: path}, nil
}

func fastIntegrator(h Host) *Integrator {
	i := NewIntegrator(h)
	i.PollInterval = time.Millisecond
	i.Timeout = 500 * time.Millisecond
	return i
}

// # THE THREE THINGS THE PACKAGE COMMENT SAYS ARE EASY TO GET WRONG
//
// Each has a test here, and each is named after the failure rather than the
// function.

// TestTheScanIsAlwaysAskedForByPath is the one that matters most.
//
// An empty `paths` in `metadataScan` is a FULL LIBRARY SCAN. Confirmed by
// reading `getScanPaths` (internal/manager/manager_tasks.go:56): an empty input
// list returns every configured stash path. The plugin is a background task, so
// that scan happens with nobody watching and is not what anybody asked for.
//
// The guard is tested from the direction that matters -- can a CALLER get one
// past it? -- and then from the direction that actually protects, which is the
// client refusing to send it.
func TestTheScanIsAlwaysAskedForByPath(t *testing.T) {
	// The client refuses, whatever the caller passes.
	g := NewHost(Conn{}).(*graphqlClient)
	for _, paths := range [][]string{nil, {}} {
		if _, err := g.Scan(context.Background(), paths); !errors.Is(err, ErrNotInLibrary) {
			t.Errorf("Scan(%v) returned %v, which is not ErrNotInLibrary. An "+
				"empty list means \"scan every configured library\"", paths, err)
		}
	}

	// And a caller with nothing to scan never gets as far as sending one.
	i := fastIntegrator(&scriptedHost{})
	if got := i.Call(context.Background(), ""); got.State != StateFailed {
		t.Errorf("Call(\"\") reported %q, expected a failure", got.State)
	}
	if got := i.Call(context.Background(), "relative/path.mp4"); got.State != StateFailed {
		t.Errorf("Call on a relative path reported %q, expected a failure. A "+
			"relative path would be resolved against the host's working directory",
			got.State)
	}
}

// TestADownloadOutsideTheLibraryIsRefusedRatherThanScanned covers the failure
// that looks like success.
//
// A path under no configured library: the host ACCEPTS the mutation, starts a
// job that scans nothing, and returns a job id. So a successful scan is not
// evidence the file was scanned, and the outcome has to say what is actually
// known.
//
// The plugin cannot check the configured library paths — that is the host's
// configuration, and the plugin must not carry a copy of it (see the
// containment-check comment in path.go). What it CAN do is refuse a path that
// fails the mechanical half, and never report a link for a file the host did
// not confirm.
func TestADownloadOutsideTheLibraryIsRefusedRatherThanScanned(t *testing.T) {
	// The host ACCEPTS the scan, runs a job, and finds nothing -- which is what a
	// path under no configured library looks like from here. It finishes
	// CLEANLY, because nothing went wrong: there was simply nothing to scan.
	host := &scriptedHost{sceneAfter: 1 << 30, jobStatus: JobFinished}
	i := fastIntegrator(host)

	out := i.Call(context.Background(), "/tmp/downloads/elsewhere.mp4")

	if out.State == StateLinked {
		t.Fatal("reported a link for a file the host never confirmed. A " +
			"successful scan of a path outside the library looks exactly like " +
			"success, and reporting it as a link invents a scene")
	}
	if out.State != StateScannedNoScene {
		t.Errorf("reported %q (%v), expected %q", out.State, out.Err, StateScannedNoScene)
	}
	// And the job id is on the outcome, so an operator CAN look at the host's
	// log and see the "not in the configured stash paths" warning that is the
	// only place the real reason is recorded.
	if out.JobID == "" {
		t.Error("no job id on the outcome. The host is the only thing that " +
			"knows why a scan of an unknown path found nothing, and the job id " +
			"is the handle to that explanation")
	}

}

// TestACompletedDownloadIsScannedAndLinked is the positive path, and it is
// what M5's exit criterion actually asks for.
func TestACompletedDownloadIsScannedAndLinked(t *testing.T) {
	host := &scriptedHost{sceneAfter: 3} // three polls of "not yet"
	i := fastIntegrator(host)

	out := i.Call(context.Background(), "/library/Scene (2019).mp4")

	if out.State != StateLinked {
		t.Fatalf("reported %q (%v), expected %q", out.State, out.Err, StateLinked)
	}
	if out.SceneID != "scene-1" {
		t.Errorf("linked scene %q, expected %q", out.SceneID, "scene-1")
	}
	if out.JobID != "42" {
		t.Errorf("job id %q, expected %q. A "+
			"\"nothing happened\" outcome with no job id cannot be followed up",
			out.JobID, "42")
	}
	// The scan was asked for by PATH, once, and the path is the one given.
	if len(host.scans) != 1 || len(host.scans[0]) != 1 {
		t.Fatalf("scans: %v, expected exactly one call with one path", host.scans)
	}
	if host.scans[0][0] != "/library/Scene (2019).mp4" {
		t.Errorf("scanned %q, expected the completed file's path", host.scans[0][0])
	}
}

// TestTheLinkIsConfirmedByAskingAboutTheFileAndNotByTheJobReturning is the
// ordering property.
//
// A completed job does not mean a scene: the host can finish having decided the
// file is not a video. So a single check after the job completes is a race, and
// this test pins the polling on the PATH.
func TestTheLinkIsConfirmedByAskingAboutTheFileAndNotByTheJobReturning(t *testing.T) {
	host := &scriptedHost{sceneAfter: 5}
	i := fastIntegrator(host)

	if got := i.Call(context.Background(), "/library/a.mp4"); got.State != StateLinked {
		t.Errorf("reported %q, expected %q. The scene appeared on the sixth "+
			"poll, so a design that checked once after the job returned would "+
			"report this as never linked", got.State, StateLinked)
	}
	// Six polls: five empty, one with the scene. One is not enough, and the
	// point is that more than one happened.
	if len(host.queries) < 2 {
		t.Errorf("asked the host %d time(s); a single check after the job "+
			"returns is the race this avoids", len(host.queries))
	}
}

// TestATimeoutIsNotARejection holds the line that makes a timeout reportable.
//
// A timeout says the host has not answered. `ErrNotLinked` says the host
// answered and said no. A caller distinguishing the two with errors.Is must get
// different answers, or every slow scan tells the operator their file is not a
// video.
func TestATimeoutIsNotARejection(t *testing.T) {
	// The job NEVER reaches a terminal state, so the only thing that can end the
	// wait is the deadline. That is the case being tested: an answer that is not
	// coming.
	host := &scriptedHost{sceneAfter: 1 << 30, jobStatus: "RUNNING"}
	i := NewIntegrator(host)
	i.PollInterval = time.Millisecond
	i.Timeout = 20 * time.Millisecond

	out := i.Call(context.Background(), "/library/slow.mp4")

	if out.State != StateFailed {
		t.Fatalf("reported %q, expected %q", out.State, StateFailed)
	}
	var timeout *TimeoutError
	if !errors.As(out.Err, &timeout) {
		t.Fatalf("the error is %T (%v), expected a *TimeoutError", out.Err, out.Err)
	}
	if errors.Is(out.Err, ErrNotLinked) {
		t.Error("a timeout matches ErrNotLinked. The host did not answer; it " +
			"did not say no, and an operator told \"not a video\" when the " +
			"scan is still running will re-download a file that is fine")
	}
	// And the job id is on the timeout, because that is the operator's next step.
	if timeout.JobID == "" || timeout.JobID == "unknown" {
		t.Errorf("the timeout carries the job id %q. Looking up the job is how "+
			"\"did it work\" gets answered", timeout.JobID)
	}
	if out.JobID == "" {
		t.Error("the outcome carries no job id, so a timeout cannot be followed up")
	}
}

// TestAHostThatCannotBeReachedStopsTheWaitImmediately is the transport branch.
//
// A poll loop that keeps going through a transport error is a download that
// hangs until the deadline for a host that has already gone away — and the
// deadline is 90 seconds, so every failed connection costs 90 seconds of a task
// that is going to fail anyway.
func TestAHostThatCannotBeReachedStopsTheWaitImmediately(t *testing.T) {
	host := &scriptedHost{sceneErr: fmt.Errorf("%w: dial tcp: refused", ErrUnreachable)}
	i := fastIntegrator(host)

	start := time.Now()
	out := i.Call(context.Background(), "/library/a.mp4")
	elapsed := time.Since(start)

	if out.State != StateFailed {
		t.Errorf("reported %q, expected %q", out.State, StateFailed)
	}
	if !errors.Is(out.Err, ErrUnreachable) {
		t.Errorf("the error is %v, which is not ErrUnreachable", out.Err)
	}
	// The timeout here is 500ms, and the loop polls every 1ms, so a retrying
	// implementation would make ~500 queries and return a TIMEOUT rather than
	// the transport error -- which is why the assertion below is on the error
	// AND the time.
	//
	// The `200ms` bound is 40x the poll interval, so a loop that yields
	// correctly is comfortably inside it while a `continue` that skips the
	// select is not. That second case was found the hard way: a mutation probe
	// replaced `return nil, err` with `continue`, the sweep was interrupted, and
	// the replacement was left applied. The test caught it -- but the spin took
	// 126 seconds to fail, and the outer timeout killed the run first. So the
	// assertion has to be FAST as well as correct, or it reports the bug only to
	// whoever has time to wait for it.
	if elapsed > 200*time.Millisecond {
		t.Errorf("the wait took %s. A transport error is terminal — the host "+
			"has already gone away, and waiting out the deadline is 90 seconds "+
			"of a task that was never going to succeed", elapsed)
	}
}

// TestAStopEndsTheWaitPromptly is the host's Stop, which does not wait.
func TestAStopEndsTheWaitPromptly(t *testing.T) {
	host := &scriptedHost{sceneAfter: 1 << 30}
	i := NewIntegrator(host)
	i.PollInterval = 10 * time.Millisecond
	i.Timeout = 30 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	out := i.Call(ctx, "/library/a.mp4")
	elapsed := time.Since(start)

	if out.State != StateFailed {
		t.Errorf("reported %q, expected %q", out.State, StateFailed)
	}
	if elapsed > 5*time.Second {
		t.Errorf("the wait took %s after a Stop. The host calls Stop and does "+
			"not wait for it, so a wait that ignores the context keeps a "+
			"downloader running until the host kills the process", elapsed)
	}
}

// TestAScanFailureIsNotALinkFailure keeps the two apart.
//
// The caller's next action differs: a transport error is worth a retry, and a
// host that refused is done. Collapsing them produces either a retry loop
// against a host that already answered or an operator told to re-download.
func TestAScanFailureIsNotALinkFailure(t *testing.T) {
	host := &scriptedHost{scanErr: fmt.Errorf("%w: 401", ErrRefused)}
	i := fastIntegrator(host)

	out := i.Call(context.Background(), "/library/a.mp4")

	if out.State != StateFailed {
		t.Errorf("reported %q, expected %q", out.State, StateFailed)
	}
	if !errors.Is(out.Err, ErrRefused) {
		t.Errorf("the error is %v, which is not ErrRefused", out.Err)
	}
	// Nothing was asked about a scene, because nothing was scanned.
	if len(host.queries) != 0 {
		t.Errorf("the host was asked about a scene %d time(s) after a failed "+
			"scan. There is nothing scanned to ask about", len(host.queries))
	}
}

// # THE REGEX, WHICH IS WHERE A PATH BECOMES A PATTERN
//
// These are the tests for the hazard the package comment describes: a path
// handed to `findScenesByPathRegex` unescaped is a PATTERN, and a pattern can
// match a file that is not the one that was downloaded.

func TestAPathWithRegexCharactersMatchesOnlyItself(t *testing.T) {
	// The dangerous one: unescaped, `.*` matches every .mp4 in the library, so
	// the plugin would report a link to a scene belonging to a different file
	// and the operator would believe a download was linked when it was not.
	for _, path := range []string{
		"/library/.*.mp4",
		"/library/Scene (2019).mp4",
		"/library/a|b.mp4",
		"/library/[a-z].mp4",
		"/library/+plus+.mp4",
		"/library/^caret$.mp4",
		"/library/back\\slash.mp4",
		"/library/{2}.mp4",
	} {
		pattern := ExactPathPattern(path)

		// It must match the path it was built from.
		matched, err := regexpMatch(pattern, path)
		if err != nil {
			t.Errorf("%s: the pattern %q does not compile: %v", path, pattern, err)
			continue
		}
		if !matched {
			t.Errorf("%s: the pattern %q does not match the path it was built "+
				"from, so a scanned file would be reported as never linked", path, pattern)
		}

		// And it must not match a DIFFERENT file that shares the prefix.
		for _, other := range []string{
			"/library/Secret Documentary.mp4",
			"/library/anything.mp4",
			"/library/a.mp4",
		} {
			if other == path {
				continue
			}
			hit, err := regexpMatch(pattern, other)
			if err == nil && hit {
				t.Errorf("%s: the pattern %q also matches %s. A regex that "+
					"matches another file makes the plugin report a link to "+
					"somebody else's scene", path, pattern, other)
			}
		}
	}
}

// TestAnUnanchoredPatternWouldBeAnchored is the property behind the anchors.
//
// It checks the anchors directly rather than through a case that happens to
// need them, so the test survives a change of input.
func TestAnUnanchoredPatternWouldBeAnchored(t *testing.T) {
	pattern := ExactPathPattern("/library/a.mp4")
	if !strings.HasPrefix(pattern, "^") || !strings.HasSuffix(pattern, "$") {
		t.Errorf("the pattern %q is not anchored. Without ^ and $ a prefix "+
			"of a longer path matches, and the host would answer about a "+
			"different file", pattern)
	}
}

// TestThePatternIsPortableRatherThanGoSpecific is a deliberate choice, and
// this is what holds it.
//
// `\A` and `\z` would work and are marginally more precise. `^` and `$` also
// work in every other regex dialect, which matters because this pattern shows up
// in the host's logs where somebody is trying to reproduce it.
func TestThePatternIsPortableRatherThanGoSpecific(t *testing.T) {
	pattern := ExactPathPattern("/library/a.mp4")
	if strings.Contains(pattern, `\A`) || strings.Contains(pattern, `\z`) {
		t.Errorf("the pattern %q uses a Go-specific anchor. The host's logs "+
			"show this string to somebody trying to reproduce the query, and "+
			"^/$ work in every dialect", pattern)
	}
}

// TestACleanedPathIsQueriedRatherThanTheRawOne matches what the host stores.
//
// The host stores cleaned, NFC-normalised paths (#4425). Querying the cleaned
// form of what was written is what makes the match work at all.
func TestACleanedPathIsQueriedRatherThanTheRawOne(t *testing.T) {
	if got := ExactPathPattern("/library//a/../b.mp4"); got != `^/library/b\.mp4$` {
		t.Errorf("the pattern for an uncleaned path is %q, expected the "+
			"cleaned form", got)
	}
}

// # THE HOST CLIENT, AGAINST A REAL HTTP SERVER
//
// The orchestration above is tested against a scripted host, which cannot catch
// a wrong endpoint, a missing cookie or a mis-typed JSON field. These tests use
// a real httptest server for that.

// TestTheGraphQLCallsHaveTheShapeTheHostExpects is the wire-level check.
//
// The plugin cannot import the host's types — that is the module boundary — so
// the wire format is the interface, and a field the host renames arrives as a
// zero value here and decodes without error. These assertions are the
// substitute for that compile-time check.
func TestTheGraphQLCallsHaveTheShapeTheHostExpects(t *testing.T) {
	// metadataScan is typed `ID!` and core's resolver returns
	// strconv.Itoa(jobID), so the job id is a STRING. Decoding it into an int
	// is the mistake, and it fails only at run time.
	var got struct {
		MetadataScan json.RawMessage `json:"metadataScan"`
	}
	if err := json.Unmarshal([]byte(`{"metadataScan":"42"}`), &got); err != nil {
		t.Fatalf("decoding a string job id: %v", err)
	}
	if string(got.MetadataScan) != `"42"` {
		t.Errorf("the job id decoded as %s, expected a quoted string. The "+
			"host returns strconv.Itoa, and a scan that decodes it as an int "+
			"fails on a type mismatch that names neither the plugin nor the field",
			got.MetadataScan)
	}

	// findScenesByPathRegex returns a RESULT TYPE wrapping findScene, not a
	// bare scene list. Asking for a different shape is an error from the host,
	// and the error names the field rather than the intent.
	var res struct {
		FindScenesByPathRegex struct {
			FindScene *Scene `json:"findScene"`
		} `json:"findScenesByPathRegex"`
	}
	raw := `{"findScenesByPathRegex":{"findScene":{"id":"7","path":"/a.mp4"}}}`
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("decoding the scene result type: %v", err)
	}
	if res.FindScenesByPathRegex.FindScene == nil {
		t.Error("the result type did not decode. findScenesByPathRegex wraps " +
			"findScene in a result object; asking for a bare list is a different " +
			"query and errors at the host")
	}
}

// TestAScanAskedForAJobIdThatIsNotAStringIsAnErrorNotASilentSuccess.
//
// The host accepted the scan and returned something unidentifiable. Reporting
// success there would mean a caller with a job id it cannot look up, and the
// operator's "did it work" has no handle.
func TestAScanAskedForAJobIdThatIsNotAStringIsAnErrorNotASilentSuccess(t *testing.T) {
	for _, body := range []string{
		`{"data":{"metadataScan":42}}`,
		`{"data":{"metadataScan":""}}`,
		`{"data":{}}`,
	} {
		srv := newFakeHost(t, body)
		g := NewHost(testConn(srv)).(*graphqlClient)

		id, err := g.Scan(context.Background(), []string{"/library/a.mp4"})
		if err == nil {
			t.Errorf("the host answered %s and Scan returned the job id %q "+
				"with no error", body, id)
		}
		srv.Close()
	}
}

// TestTheRequestCarriesTheSessionCookie is the 401 case, which is fixable by
// the operator and must not read as a refusal.
func TestTheRequestCarriesTheSessionCookie(t *testing.T) {
	var seen *http.Cookie
	srv := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("stash-auth"); err == nil {
			seen = c
		}
		w.Write([]byte(`{"data":{"metadataScan":"7"}}`))
	})
	g := NewHost(testConn(srv)).(*graphqlClient)

	if _, err := g.Scan(context.Background(), []string{"/library/a.mp4"}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if seen == nil || seen.Value != "test-session" {
		t.Errorf("the host received the cookie %v, expected one with the "+
			"session value. A request built by hand sets the wrong header and "+
			"comes back 401", seen)
	}
}

// TestAnUnauthorizedAnswerIsARefusalAndNotAnUnreachableHost.
//
// Both are failures, and the difference is where the operator looks: a 401 is
// fixed by re-authenticating, and reporting it as unreachable sends them
// looking at a network problem that does not exist.
func TestAnUnauthorizedAnswerIsARefusalAndNotAnUnreachableHost(t *testing.T) {
	srv := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	g := NewHost(testConn(srv)).(*graphqlClient)

	_, err := g.Scan(context.Background(), []string{"/library/a.mp4"})
	if !errors.Is(err, ErrRefused) {
		t.Errorf("a 401 produced %v, which is not ErrRefused", err)
	}
	if errors.Is(err, ErrUnreachable) {
		t.Error("a 401 is reported as unreachable. The operator's next step " +
			"is to re-authenticate, not to check the network")
	}
	if !strings.Contains(err.Error(), "cookie") {
		t.Errorf("the error %q does not mention the cookie, so it does not say "+
			"what the operator should do", err)
	}
}

// TestAGraphQLErrorIsARefusal is the other failure the host can report.
func TestAGraphQLErrorIsARefusal(t *testing.T) {
	srv := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"errors":[{"message":"input coercion failed"}]}`))
	})
	g := NewHost(testConn(srv)).(*graphqlClient)

	if _, err := g.Scan(context.Background(), []string{"/library/a.mp4"}); !errors.Is(err, ErrRefused) {
		t.Errorf("a GraphQL error produced %v, which is not ErrRefused", err)
	}
}

// TestAnUnreachableHostIsNotARefusal, which is the other direction.
//
// This is the distinction the whole `downloadWithGate` design in internal/rpc
// turns on: a host that did not answer has not refused anything, so there is no
// permission and the plugin must not report one.
func TestAnUnreachableHostIsNotARefusal(t *testing.T) {
	srv := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	conn := testConn(srv)
	srv.Close() // nothing is listening now

	g := NewHost(conn).(*graphqlClient)
	_, err := g.Scan(context.Background(), []string{"/library/a.mp4"})

	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("a dead host produced %v, which is not ErrUnreachable", err)
	}
	if errors.Is(err, ErrRefused) {
		t.Error("a dead host is reported as a refusal. A refusal is final and " +
			"not retryable; an unreachable host is the opposite, and conflating " +
			"them means a plugin that retries forever against a host that has " +
			"already answered")
	}
}

// TestSceneForPathReportsNoSceneAsNilAndNotAnError is the distinction the
// caller switches on.
//
// `nil, nil` is "asked and told no". A non-nil error is "could not ask". A
// caller that conflates them reports a host outage as a file the host did not
// want.
func TestSceneForPathReportsNoSceneAsNilAndNotAnError(t *testing.T) {
	srv := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"findScenesByPathRegex":{"findScene":null}}}`))
	})
	g := NewHost(testConn(srv)).(*graphqlClient)

	scene, err := g.SceneForPath(context.Background(), "/library/a.mp4")
	if err != nil {
		t.Fatalf("an empty result produced the error %v. \"No scene\" is a "+
			"real answer about the file, not a transport failure", err)
	}
	if scene != nil {
		t.Errorf("got the scene %+v for a path the host has no scene for", scene)
	}
}

// TestTheQuerySentForASceneAsksForThePathAndTheId is the projection check.
//
// The plugin holds no title, no performers and no studio graph. Every field it
// asks for has to earn its place against one question — did the file enter the
// library — and a projection that grows is how a plugin ends up holding a copy
// of the host's data model.
func TestTheQuerySentForASceneAsksForThePathAndTheId(t *testing.T) {
	if !strings.Contains(sceneQuery, "findScenesByPathRegex") {
		t.Error("the query does not use findScenesByPathRegex")
	}
	for _, want := range []string{"id", "path"} {
		if !strings.Contains(sceneQuery, want) {
			t.Errorf("the query does not ask for %q", want)
		}
	}
	// And nothing else.
	for _, unwanted := range []string{"title", "performer", "studio", "tags",
		"details", "organized", "o_id", "files"} {
		if strings.Contains(sceneQuery, unwanted) {
			t.Errorf("the query asks for %q. The plugin needs to know whether "+
				"the file entered the library, and a projection that grows is "+
				"how a plugin ends up holding a copy of the host's data model",
				unwanted)
		}
	}
}

// TestTheScanAsksForPhashes is the linking decision, asserted.
//
// Without `scanGeneratePhashes` the scan succeeds, finds the file, and links
// nothing — and at this layer that is indistinguishable from a file the host
// rejected. The plan's decision was to let the host's own fingerprint matcher
// do the linking, and the matcher needs phashes.
func TestTheScanAsksForPhashes(t *testing.T) {
	var body string
	srv := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Write([]byte(`{"data":{"metadataScan":"7"}}`))
	})
	g := NewHost(testConn(srv)).(*graphqlClient)

	if _, err := g.Scan(context.Background(), []string{"/library/a.mp4"}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !strings.Contains(body, "scanGeneratePhashes") {
		t.Errorf("the mutation does not ask for phashes (%s). The scan finds "+
			"the file and links nothing, and here that is indistinguishable "+
			"from a file the host rejected", body)
	}
	if !strings.Contains(body, "metadataScan") {
		t.Errorf("the mutation does not call metadataScan (%s)", body)
	}
}

// TestACleanFinishWithNoSceneDoesNotCostTheTimeout is the property that stopped
// the wait from being a timer.
//
// The first version polled until the deadline regardless of whether the scan had
// finished, so every download that correctly produced no scene -- a caption, a
// cover, a .nfo, a video the host declined -- burned the full 90 seconds before
// reporting a perfectly good outcome. In a library with 4,000 subtitle files
// that is a day of the downloader's time.
//
// The Integrator here has the DEFAULT timeout, deliberately: 90 seconds is long
// enough that a test which passes by finishing early cannot be passing by
// accident, and short enough to run.
func TestACleanFinishWithNoSceneDoesNotCostTheTimeout(t *testing.T) {
	host := &scriptedHost{sceneAfter: 1 << 30, jobStatus: JobFinished}
	i := NewIntegrator(host)
	i.PollInterval = time.Millisecond
	// Timeout left at the 90s default.

	start := time.Now()
	out := i.Call(context.Background(), "/library/caption.srt")
	elapsed := time.Since(start)

	if out.State != StateScannedNoScene {
		t.Fatalf("reported %q (%v), expected %q", out.State, out.Err, StateScannedNoScene)
	}
	if elapsed > 5*time.Second {
		t.Errorf("the wait took %s for a scan that had already finished. A "+
			"finished job with no scene is an ANSWER, and answering it after "+
			"the timeout is 90 seconds of nothing per non-video download",
			elapsed)
	}
}

// TestAFailedScanIsAFailureAndNotANoScene is the third ending, and the one the
// job status exists to find.
//
// A scan the host reports as FAILED produced no scene, exactly like a clean scan
// of a subtitle. Reporting both the same way means a scan that broke -- a
// vanished directory, a permission error, an unreadable file -- is reported as
// "this file is not a video", and the operator's conclusion is wrong.
func TestAFailedScanIsAFailureAndNotANoScene(t *testing.T) {
	host := &scriptedHost{
		sceneAfter: 1 << 30,
		jobStatus:  JobFailed,
		jobErr:     nil,
	}
	i := fastIntegrator(host)

	out := i.Call(context.Background(), "/library/a.mp4")

	if out.State != StateFailed {
		t.Fatalf("reported %q, expected %q. A scan the host reports as "+
			"FAILED is a broken scan, and reporting it as \"not a scene\" "+
			"sends the operator to look at the file instead of the error",
			out.State, StateFailed)
	}
	var failed *ScanFailedError
	if !errors.As(out.Err, &failed) {
		t.Errorf("the error is %T (%v), expected a *ScanFailedError", out.Err, out.Err)
	}
	if failed != nil && failed.Status != JobFailed {
		t.Errorf("the error carries the status %q, expected %q", failed.Status, JobFailed)
	}
	// And it is neither of the other two, because a caller switching on those
	// would handle a broken scan as either a rejection or an outage.
	if errors.Is(out.Err, ErrNotLinked) || errors.Is(out.Err, ErrUnreachable) {
		t.Error("a host-reported scan failure matches one of the other two " +
			"errors, so a caller cannot tell a broken scan from a rejection")
	}
}

// TestAScanThatIsStillRunningIsNotAVerdict is the last status branch.
//
// READY and RUNNING mean the scan has not finished, so "no scene yet" is not an
// answer. Reporting it as one — either as a link or as a clean no-scene — makes a
// claim about a scan that has not happened. It is reported as a timeout instead,
// which is the honest word for "still going".
func TestAScanThatIsStillRunningIsNotAVerdict(t *testing.T) {
	for _, status := range []string{"READY", "RUNNING", "STOPPING"} {
		host := &scriptedHost{sceneAfter: 1 << 30, jobStatus: status}
		i := fastIntegrator(host)

		out := i.Call(context.Background(), "/library/a.mp4")

		if out.State == StateLinked {
			t.Errorf("status %s: reported a link for a scan that has not "+
				"finished", status)
		}
		if out.State == StateScannedNoScene {
			t.Errorf("status %s: reported \"scanned, no scene\" for a scan "+
				"that has not finished. That is a claim about a scan that has "+
				"not happened", status)
		}
	}
}

// TestAJobTheHostHasForgottenIsNotTreatedAsAFinishedScan, because a missing job
// is not an answer.
//
// The job id came from the host, so a job it does not know about means it lost
// it — NOT that the scan finished. Treating "" as terminal would report a verdict
// on the strength of a job the host cannot find.
func TestAJobTheHostHasForgottenIsNotTreatedAsAFinishedScan(t *testing.T) {
	host := &scriptedHost{sceneAfter: 1 << 30} // jobStatus "" = host has no job
	i := NewIntegrator(host)
	i.PollInterval = time.Millisecond
	i.Timeout = 20 * time.Millisecond

	out := i.Call(context.Background(), "/library/a.mp4")

	if out.State == StateScannedNoScene {
		t.Error("a job the host does not have was treated as a finished scan " +
			"with no scene. The job id came FROM the host, so a job it cannot " +
			"find means it lost it, not that the scan completed")
	}
	if out.State != StateFailed {
		t.Errorf("reported %q, expected %q", out.State, StateFailed)
	}
}

// TestTheStatusesThisPackageMatchesOnAreTheOnesTheHostEmits guards the constants
// against a host that renames one.
//
// The host's enum is READY, RUNNING, FINISHED, STOPPING, CANCELLED, FAILED
// (graphql/schema/types/job.graphql). A constant that stopped matching turns a
// terminal job into a non-terminal one, and every affected download then waits
// out the full timeout instead of answering.
func TestTheStatusesThisPackageMatchesOnAreTheOnesTheHostEmits(t *testing.T) {
	terminal := []string{JobFinished, JobFailed, JobCancelled}
	for _, s := range terminal {
		if !isTerminal(s) {
			t.Errorf("%q is not treated as terminal, so a scan in that state "+
				"waits out the full timeout instead of answering", s)
		}
	}
	for _, s := range []string{"READY", "RUNNING", "STOPPING"} {
		if isTerminal(s) {
			t.Errorf("%q is treated as terminal. A scan that has not finished "+
				"cannot answer anything", s)
		}
	}
	// And the literals are the host's, not ours.
	if JobFinished != "FINISHED" || JobFailed != "FAILED" ||
		JobCancelled != "CANCELLED" {
		t.Error("the status constants do not match the host's enum values")
	}
}

// TestTheJobQueryAsksForTheStatusAndTheError is the projection check for Job.
//
// The error is the only place the host can say WHY a scan of an unknown path
// found nothing, and without it the out-of-library case — which looks exactly
// like a subtitle — is undiagnosable.
func TestTheJobQueryAsksForTheStatusAndTheError(t *testing.T) {
	if !strings.Contains(jobQuery, "findJob") {
		t.Error("the job query does not use findJob")
	}
	for _, want := range []string{"status", "error"} {
		if !strings.Contains(jobQuery, want) {
			t.Errorf("the job query does not ask for %q", want)
		}
	}
	// The host's shape is findJob(input: {id: $id}), and asking for a job LIST
	// filtered by id returns every job the host has ever run.
	if !strings.Contains(jobQuery, "input: { id: $id }") {
		t.Errorf("the job query does not use findJob(input: {id: $id}). The "+
			"host's shape takes a FindJobInput, and the list form is a different "+
			"query that returns every job the host has ever run: %s", jobQuery)
	}
}
