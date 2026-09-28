// Package library is the plugin's half of spec step 5.5: a finished download
// enters the library exactly the way a file somebody copied in by hand enters
// it.
//
// # WHAT 5.5 IS, AND WHY IT IS SMALLER THAN IT LOOKS
//
// The exit criterion for M5 is a plugin that "can fetch a file over BitTorrent
// or ed2k into a stash path and get it scanned and linked". So the work here is
// not a scanner, not a fingerprinter and not a linker. It is: put the file
// somewhere the host will look, tell the host to look there, and then find out
// whether it did.
//
// Three verbs, matching the three that exist:
//
//	metadataScan(paths: [...])   -> starts the host's own scanner on those paths
//	findScenesByPathRegex(...)  -> asks whether a scene now exists for the path
//	and nothing else.
//
// # WHY THE HOST'S SCANNER AND NOT A SCANNER OF OUR OWN
//
// Because the alternative is the failure this whole milestone exists to prevent.
// A downloader that maintains its own `files` rows is a plugin writing the
// core's database, and everything the module boundary in step 5.0 was built to
// prevent comes back through the front door. The plugin writes a file. The host
// decides what a file means. That is the entire separation of concerns, and it
// is why this package has no database code, no fingerprint code and no
// metadata-fetching code: those are the host's, and duplicating them would
// require the access this milestone is not allowed to ask for.
//
// # THE THREE THINGS THAT ARE ACTUALLY EASY TO GET WRONG
//
// # 1. AN EMPTY `paths` SCANS THE ENTIRE LIBRARY
//
// This is the one that matters, so it is asserted rather than documented.
//
// `getScanPaths` (internal/manager/manager_tasks.go:56) is:
//
//	func getScanPaths(inputPaths []string) []*config.StashConfig {
//	    stashPaths := config.GetInstance().GetStashPaths()
//	    if len(inputPaths) == 0 {
//	        return stashPaths
//	    }
//
// An empty list is not "scan nothing". It is "scan every configured library",
// and the plugin is a background task that the operator did not ask to reindex.
// A scanner running over a multi-terabyte library because a field serialised as
// `null` instead of `[]` is not a bug the operator can diagnose, and
// `TestTheScanIsAlwaysAskedForByPath` fails if anything can produce a call with
// no path in it.
//
// # 2. A PATH OUTSIDE THE LIBRARY IS REFUSED, SILENTLY-ISH
//
// `GetStashFromDirPath` returns nil for a path under no configured library, and
// the caller logs `is not in the configured stash paths` and moves on. So a
// download that lands outside the library is a download that is never scanned
// and never linked, and the plugin would report success having changed nothing.
//
// So the containment check is NOT optional here, and it is not the same check
// step 5.2 does. Step 5.2 asks "is this file name safe relative to the download
// root". This asks "is the download root itself inside a library the host knows
// about" -- and the plugin can only ask the host, because only the host knows
// the library paths. `TestADownloadOutsideTheLibraryIsRefusedRatherThanScanned`
// pins that, because the failure it prevents is a silently-successful run.
//
// # 3. SCANNING IS ASYNCHRONOUS, AND THE JOB ID IS THE ONLY HANDLE
//
// `metadataScan` returns a job ID as a STRING (`strconv.Itoa`), not an int, and
// the job runs on the host's task queue. A plugin that fires the mutation and
// returns "done" has finished nothing: the scan is queued, and whether the file
// is a scene is not yet decided.
//
// So `Scan` polls `findScenesByPathRegex` until the path resolves or the
// deadline passes. Polling the SCENE rather than the job is deliberate: the job
// can complete having decided the file is not a video, and a completed job with
// no scene is the answer "not a scene", not "not finished yet". Waiting on the
// job and then reporting success regardless would report a link that does not
// exist.
//
// # WHY THE PLUGIN DOES NOT LINK BY FINGERPRINT ITSELF
//
// The plan allows either: link by phasher/osher through the host's query
// surface, or let the scanner's own fingerprint match do it. The second is what
// happens, by not doing anything -- core generates phashes during the scan when
// `scanGeneratePhashes` is set, and its existing matching links the file. The
// first would need the host to expose a mutation this package should not assume
// exists, and inventing a requirement for the host is a real M5 finding rather
// than a convenience. So: ask for phashes, and let the host's own matcher link.
package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Conn is how the plugin reaches the host's API.
//
// REDECLARED, and it is the same struct `internal/rpc` uses -- deliberately not
// a shared one, because the two are separate modules once the plugin is
// installed and a plugin cannot import a package from inside the core's plugin
// tree. Two declarations of a four-field struct is the price of the boundary
// this milestone is built to prove, and `TestConnAndRPCAgreeOnTheWireFormat`
// keeps the two from drifting.
type Conn struct {
	Scheme string
	Host   string
	Port   int

	// Cookie authenticates to the host. A *http.Cookie on the wire, and
	// AddCookie rather than a raw header: the host reads a `Cookie` header, and
	// a request built by hand sets the wrong one and comes back 401.
	Cookie *http.Cookie
}

// Errors this package returns, each distinct because the caller's response to
// them differs. A caller that cannot tell "the host refused" from "the host did
// not answer" retries the first and pages someone for the second.
var (
	// ErrRefused means the host declined the request. Terminal: retrying a
	// refusal is a plugin hammering a host that already answered.
	ErrRefused = errors.New("the host refused the request")

	// ErrUnreachable means the host did not answer at all. Not a refusal, and
	// the distinction is the whole point: there is no decision to honour, so
	// the plugin must not report the file as scanned.
	ErrUnreachable = errors.New("the host did not answer")

	// ErrNotInLibrary means the completed file is not under a library path the
	// host knows about, so a scan of it would do nothing. Reported rather than
	// swallowed, because it means the operator's download path is configured
	// wrongly and every future download will do the same.
	ErrNotInLibrary = errors.New("the file is not inside a configured library path")

	// ErrNotLinked means the scan finished and no scene appeared for the path.
	// A real answer, not a timeout: the host has decided this file is not a
	// scene, and pretending otherwise would report a link that does not exist.
	ErrNotLinked = errors.New("the scan finished and found no scene for the file")
)

// Host is a client for the host's GraphQL API.
//
// An interface, for the same reason `rpc.Proposer` is: the integration tests
// drive the real code path against a scripted host without an HTTP server, and
// "what if the host is down" is one stub away rather than a firewall rule away.
type Host interface {
	// Scan asks the host to scan the given absolute paths.
	//
	// NEVER passes an empty slice. See the package comment: an empty `paths` is
	// a full-library scan, so a caller that cannot produce a path must fail
	// rather than send the empty list that means "everything".
	Scan(ctx context.Context, paths []string) (jobID string, err error)

	// SceneForPath asks whether a scene now exists for a path.
	SceneForPath(ctx context.Context, path string) (*Scene, error)

	// JobStatus asks the host what became of a scan job.
	//
	// NOT optional, and this is the fix for a distinction the first version of
	// this package could not make. "No scene appeared" is ambiguous on its own:
	// it is what a subtitle looks like, and it is ALSO what a path outside
	// every configured library looks like, because the host accepts the scan,
	// runs a job that scans nothing, and reports success.
	//
	// Those two want different answers. One is "correctly not a scene" and the
	// other is "your download path is misconfigured, and every future download
	// will do the same". Only the host knows which, because only the host knows
	// its library paths — so the job's own status and error is the signal, and
	// a JobStatus of "" means the host does not know the job either.
	JobStatus(ctx context.Context, jobID string) (Job, error)
}

// Job is the host's view of a scan job.
//
// A projection rather than the host's whole Job type, for the same reason Scene
// is one: this package needs to know whether the scan RAN and whether it
// FAILED, and `subTasks`/`progress`/`startTime` answer neither question. The
// error is kept because it is the only place a host can say WHY a scan of a path
// found nothing, and without it the out-of-library case is undiagnosable.
type Job struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error"`
}

// The statuses this package acts on.
//
// The full enum is READY, RUNNING, FINISHED, STOPPING, CANCELLED and FAILED.
// Named as constants because the decision is a switch on these values, and a
// literal in a comparison is a comparison that is wrong the day the host
// renames one. What the host emits and what this package matches on is checked
// by TestTheJobStatusesMatchTheSchema.
const (
	JobFinished  = "FINISHED"
	JobFailed    = "FAILED"
	JobCancelled = "CANCELLED"
)

// Scene is the host's answer for a path: enough to report a link, and no more.
//
// Deliberately a projection rather than the host's whole scene type. The plugin
// has no business holding a title, a performer list or a studio graph, and
// every field here has to earn its place against one question: did the file
// enter the library? The id and the path are the answer; the rest is the host's
// business and it can be fetched later through its own API.
type Scene struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// graphqlClient is the real Host, over HTTP.
type graphqlClient struct {
	conn Conn
	http *http.Client
}

// NewHost returns a Host pointed at the given connection.
//
// The timeout is 30 seconds, matching `internal/rpc`'s proposer, and for the
// same reason: a call that hangs holds the download, and a download that appears
// to do nothing is indistinguishable from one waiting on a peer. A slow host is
// surfaced as an error somebody can read.
func NewHost(conn Conn) Host {
	return &graphqlClient{conn: conn, http: &http.Client{Timeout: 30 * time.Second}}
}

// call is the one place a GraphQL request is made.
//
// `scan` and `query` in one method rather than two because the transport is
// identical and the difference is one field in the body -- and splitting them
// means two places to get the endpoint, the cookie and the error decoding right.
func (g *graphqlClient) call(ctx context.Context, query string, vars map[string]interface{}) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]interface{}{"query": query, "variables": vars})
	if err != nil {
		return nil, fmt.Errorf("encoding the request: %w", err)
	}

	endpoint := &url.URL{
		Scheme: g.conn.Scheme,
		Host:   fmt.Sprintf("%s:%d", g.conn.Host, g.conn.Port),
		Path:   "/graphql",
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("building the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if g.conn.Cookie != nil {
		req.AddCookie(g.conn.Cookie)
	}

	resp, err := g.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		// Its own error, because a 401 is fixable by the operator (log in
		// again, fix the cookie) and reporting it as a refusal sends them
		// looking at consent settings instead.
		return nil, fmt.Errorf("%w: the host answered 401, so the plugin's "+
			"session cookie is not accepted. Re-authenticate and retry", ErrRefused)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: the host answered %s", ErrRefused, resp.Status)
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("%w: decoding the host's answer: %v", ErrUnreachable, err)
	}
	if len(envelope.Errors) > 0 {
		return nil, fmt.Errorf("%w: the host reported %s", ErrRefused, envelope.Errors[0].Message)
	}
	return envelope.Data, nil
}

// Scan is the `metadataScan` mutation.
//
// `paths` is REQUIRED and must not be empty -- see the package comment for what
// an empty list does. The guard is here rather than only at the call site
// because this is the only function that can send the field, and a guard that
// lives with the caller is a guard the next caller does not have.
//
// `scanGeneratePhashes: true` because the plan's decision was to let the HOST's
// fingerprint matcher do the linking, and the matcher needs phashes. Asking for
// them is what turns "a file in a directory" into "a file linked to a scene" --
// without this the scan succeeds, finds the file, and links nothing, which is
// indistinguishable at this layer from a file the host rejected.
//
// `rescan: false` and the generate flags other than phashes left at their
// defaults, deliberately: the operator's configured scan options are the host's
// business, and a plugin that overrides them turns a background scan into a
// policy change made by a download.
func (g *graphqlClient) Scan(ctx context.Context, paths []string) (string, error) {
	if len(paths) == 0 {
		// Refused here rather than sent. This is the one call in the whole
		// plugin where a plausible-looking argument is a much bigger action than
		// the caller meant, and the caller cannot tell the difference from the
		// host's side -- it just runs for hours.
		return "", fmt.Errorf("%w: refusing to send a scan with no paths, "+
			"because an empty list means \"scan every configured library\"",
			ErrNotInLibrary)
	}

	data, err := g.call(ctx, `mutation ScanPaths($paths: [String!]!) {
		metadataScan(input: {
			paths: $paths
			scanGeneratePhashes: true
		})
	}`, map[string]interface{}{"paths": paths})
	if err != nil {
		return "", err
	}

	// The job id is a STRING on the wire. `metadataScan` is typed `ID!` in the
	// schema and core's resolver returns `strconv.Itoa(jobID)`, so a `"5"`
	// where an int was expected decodes as `5` from JSON but fails on re-encode
	// into an int field, and the error names a type rather than the endpoint.
	//
	// Decoded through a struct rather than `data["metadataScan"]`, and the
	// reason is worth recording: `data` is a `json.RawMessage`, which is a
	// `[]byte`. Indexing it with a string SLICES IT BY BYTES -- it does not
	// look up a key -- so that expression compiles, returns twelve arbitrary
	// bytes, and fails to unmarshal as a string for reasons that name neither
	// the field nor the endpoint.
	var payload struct {
		MetadataScan string `json:"metadataScan"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("decoding the scan response: %w", err)
	}
	id := payload.MetadataScan
	if id == "" {
		return "", fmt.Errorf("%w: the host accepted the scan and returned no "+
			"job id, so there is no handle to report", ErrUnreachable)
	}
	return id, nil
}

// jobQuery is the projection Job asks for.
//
// `findJob(input: {id: $id})` — the host's own shape, from schema.graphql:263
// and job.graphql's FindJobInput. Asking for a job list filtered by id instead
// would be a different query, and it would return every job the host has ever
// run rather than the one this download started.
const jobQuery = `query ScanJob($id: ID!) {
	findJob(input: { id: $id }) { id status error }
}`

// JobStatus is the `findJob` query.
func (g *graphqlClient) JobStatus(ctx context.Context, jobID string) (Job, error) {
	if jobID == "" {
		return Job{}, fmt.Errorf("%w: there is no job id to ask about, so "+
			"there is no way to find out whether the scan ran", ErrUnreachable)
	}
	data, err := g.call(ctx, jobQuery, map[string]interface{}{"id": jobID})
	if err != nil {
		return Job{}, err
	}
	var payload struct {
		FindJob *Job `json:"findJob"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return Job{}, fmt.Errorf("decoding the job: %w", err)
	}
	if payload.FindJob == nil {
		// The host has no such job. Not an error: the plugin's job id came from
		// the host, so this means the host has forgotten it, which is an answer
		// ("I do not have it") rather than a failure to ask.
		return Job{}, nil
	}
	return *payload.FindJob, nil
}

// sceneQuery is the projection Scene asks for.
//
// `path_regex` rather than an exact match: the host stores the path it found,
// which is the path it walked to, and asking for the exact string back is
// asking the host to echo back a value this package just sent it. A regex on
// the basename is the query the host's own UI uses to answer "what is at this
// path", and it survives the normalisation the host does when storing (#4425
// stores the NFC-normalised path, which is not always byte-identical to what
// was written to disk).
const sceneQuery = `query SceneAtPath($path_regex: String!) {
	findScenesByPathRegex(filter: { path: { regex: $path_regex } }) {
		findScene { id path }
	}
}`

// SceneForPath asks whether a scene now exists for a path.
//
// Returns `(nil, nil)` when the host has no scene for it -- which is the normal
// answer for a file that is not a video, and is NOT an error. The distinction
// matters: `nil, nil` means "asked and told no", and a non-nil error means "could
// not ask". A caller that conflates them reports a host outage as a file the
// host did not want, which is the wrong thing to tell somebody.
func (g *graphqlClient) SceneForPath(ctx context.Context, path string) (*Scene, error) {
	data, err := g.call(ctx, sceneQuery, map[string]interface{}{
		"path_regex": ExactPathPattern(path),
	})
	if err != nil {
		return nil, err
	}

	var envelope struct {
		FindScenesByPathRegex struct {
			FindScene *Scene `json:"findScene"`
		} `json:"findScenesByPathRegex"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("decoding the host's answer: %w", err)
	}
	return envelope.FindScenesByPathRegex.FindScene, nil
}
