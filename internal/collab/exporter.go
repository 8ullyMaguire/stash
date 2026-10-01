package collab

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// The exporter. M3 step 3.2, spec §6.3.
//
// THE RULE THIS FILE EXISTS TO ENFORCE
//
// §6.2 lists what is published and, separately, what is never published:
// file paths, file names, filesystem sizes, last-access timestamps, hostnames,
// IP addresses, internal URLs, owner account details, and any private library's
// rows. The second list is the one that matters, and a list is not enforcement.
//
// A query that selects the right columns is correct until someone adds a column,
// renames one, or copies a query from somewhere else. So the shape here is chosen
// so that the forbidden values have NO PATH to the payload: fields are built by
// naming the ones to include, and there is no field on this file that could hold
// a path even if a future caller had one. TestExport_ExcludesFilenamesAndPaths
// then walks the marshalled JSON and checks, because a test that only inspected
// the struct would not see a field added to the payload later.

// Payload is one library's export.
type Payload struct {
	Instance     string        `json:"instance"`
	SubmissionID string        `json:"submission_id"`
	Entries      []ExportEntry `json:"entries"`
	EmittedAt    time.Time     `json:"emitted_at"`

	// DryRun records that nothing was sent. Serialised, not merely logged: a
	// dry run that is indistinguishable from a real one in the artefact it
	// produces is a dry run someone will mistake for a publish.
	DryRun bool `json:"dry_run"`
}

// ExportEntry is one target's published fields.
type ExportEntry struct {
	TargetType string `json:"target_type"`
	TargetID   int64  `json:"target_id"`

	// Fields is the published metadata, keyed by the names in PublishedFields.
	// A map rather than a struct because the published set is versioned and
	// grows: a struct would make adding a field a breaking change to every
	// consumer of the payload, and the field names are already pinned by
	// PublishedFields and the disclosure.
	Fields map[string]string `json:"fields"`

	// Fingerprints are content hashes for de-duplication (spec §6.2). They are
	// derived from file CONTENT, never from a path or a name -- a phash is a
	// perceptual hash of pixels, and publishing it reveals nothing about where
	// the file lives. That is what makes this field safe while `path` is not.
	Fingerprints map[string][]string `json:"fingerprints,omitempty"`
}

// LibraryRef identifies which library is being exported.
type LibraryRef struct {
	ID      int64
	Name    string
	OwnerID int64
}

// ExportSource is everything BuildPayload reads.
//
// An interface rather than a concrete store so the exporter is testable without
// a database, AND so the query surface it needs is visible in one place: if a
// future field requires a new read, adding a method here makes that a compile
// error rather than a silent omission.
type ExportSource interface {
	// SceneFields returns the published fields for one scene. The
	// implementation is responsible for returning ONLY disclosed names.
	SceneFields(ctx context.Context, sceneID int64) (map[string]string, error)
	// SceneFingerprints returns content hashes for one scene.
	SceneFingerprints(ctx context.Context, sceneID int64) (map[string][]string, error)
	// SceneIDs returns the scene ids in the library, already scoped. A library
	// scope that is not applied HERE has to be applied by every caller, and one
	// caller will forget.
	SceneIDs(ctx context.Context, lib LibraryRef) ([]int64, error)
}

// Publisher is the write side: where a built payload goes.
type Publisher interface {
	// Publish enforces consent and then sends. Consent is enforced INSIDE
	// Publish, not before it by the caller, and the reason is the whole design:
	// a caller that checks consent and then calls Publish has two code paths,
	// and the one that skips the check is a refactor away. See Publish below.
	Publish(ctx context.Context, p Payload, userID int64) error
}

// BuildPayload produces the export for one library.
//
// IT IS A PURE READ AND MUST NOT FILTER BY CONSENT. That is deliberate and
// counter-intuitive: the caller decides which libraries are in scope, and consent
// is enforced in Publish so that the stop is in exactly one place. An exporter
// that silently returned nothing for an opted-out user would be indistinguishable
// from one that returned nothing because the library was empty -- and the
// difference between "the user declined" and "there is nothing to share" is
// exactly what an operator needs to see.
func BuildPayload(ctx context.Context, src ExportSource, instance string, lib LibraryRef) (Payload, error) {
	ids, err := src.SceneIDs(ctx, lib)
	if err != nil {
		return Payload{}, fmt.Errorf("listing scenes in library %d: %w", lib.ID, err)
	}

	entries := make([]ExportEntry, 0, len(ids))
	for _, id := range ids {
		fields, err := src.SceneFields(ctx, id)
		if err != nil {
			return Payload{}, fmt.Errorf("reading fields for scene %d: %w", id, err)
		}
		// Defence in depth: drop anything not in the disclosed set, even though
		// SceneFields is documented to return only those. This costs a map
		// lookup per field and turns "the query changed" from a privacy
		// incident into a silently dropped field.
		fields = restrictToDisclosed(fields)

		entry := ExportEntry{
			TargetType: "scene",
			TargetID:   id,
			Fields:     fields,
		}

		fps, err := src.SceneFingerprints(ctx, id)
		if err != nil {
			return Payload{}, fmt.Errorf("reading fingerprints for scene %d: %w", id, err)
		}
		if len(fps) > 0 {
			entry.Fingerprints = restrictFingerprints(fps)
		}

		entries = append(entries, entry)
	}

	return Payload{
		Instance:     instance,
		SubmissionID: SubmissionID(instance, lib.ID, entries),
		Entries:      entries,
		EmittedAt:    time.Now().UTC(),
		// The first export after consent writes to disk and sends nothing
		// (spec §6.3). The caller may clear this once the user has seen the
		// disclosure and asked for a real publish.
		DryRun: true,
	}, nil
}

// restrictToDisclosed drops any field name not in PublishedFields.
//
// The returned map is always non-nil, because a JSON `null` for fields and an
// empty object are different documents and a consumer should not have to handle
// both.
func restrictToDisclosed(in map[string]string) map[string]string {
	allowed := PublishedFieldSet()
	out := make(map[string]string, len(in))
	for k, v := range in {
		if allowed[k] {
			out[k] = v
		}
	}
	return out
}

// restrictFingerprints keeps only fingerprint types that are content-derived.
//
// A whitelist, not a blacklist, because the set of fingerprint kinds stash knows
// is not fixed: a future perceptual hash is content-derived and safe, and a
// future "path fingerprint" would be neither. A blacklist would have to be
// updated by whoever adds the dangerous one.
var allowedFingerprintTypes = map[string]bool{
	"phash":  true,
	"oshash": true,
}

func restrictFingerprints(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for k, vals := range in {
		if !allowedFingerprintTypes[k] {
			continue
		}
		// Sorted so the payload is deterministic. Two runs over the same data
		// must produce byte-identical JSON, or a submission id computed from it
		// would change for no reason.
		sorted := append([]string(nil), vals...)
		sort.Strings(sorted)
		out[k] = sorted
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SubmissionID is a content-addressed id for a payload, and the thing federation
// signs (spec §6.5).
//
// It is a hash of the INSTANCE, the library, and the entries -- deliberately not
// of EmittedAt, because the timestamp changes on every run and an id that
// changed every run could not be used to tell "the peer already has this" from
// "the peer is missing this".
func SubmissionID(instance string, libID int64, entries []ExportEntry) string {
	h := sha256.New()
	fmt.Fprintf(h, "instance=%s\nlibrary=%d\n", instance, libID)

	// Sorted by target so the id does not depend on row order. Two databases
	// with identical content must produce identical ids, or de-duplication
	// across peers silently stops working.
	sorted := append([]ExportEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].TargetID < sorted[j].TargetID })

	for _, e := range sorted {
		fmt.Fprintf(h, "entry=%s/%d\n", e.TargetType, e.TargetID)
		keys := make([]string, 0, len(e.Fields))
		for k := range e.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(h, "  %s=%s\n", k, e.Fields[k])
		}
		fpTypes := make([]string, 0, len(e.Fingerprints))
		for k := range e.Fingerprints {
			fpTypes = append(fpTypes, k)
		}
		sort.Strings(fpTypes)
		for _, k := range fpTypes {
			for _, v := range e.Fingerprints[k] {
				fmt.Fprintf(h, "  %s:%s\n", k, v)
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Marshal renders the payload, and the rendering is checked rather than assumed.
//
// json.Marshal on a struct cannot produce an invalid document, so the error
// return is a formality -- but it is checked, because a caller that ignores it
// would send an empty body on failure and an empty body is a valid-looking
// payload to a peer.
func (p Payload) Marshal() ([]byte, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("marshalling payload for instance %q: %w", p.Instance, err)
	}
	return b, nil
}

// Publish is the enforcement point, and the reason the type exists.
//
// THE CONSENT CHECK IS HERE, NOT IN THE CALLER. The spec says the exporter is
// hard-stopped "server-side, in the same place the permission is read -- not by
// omitting the user from a query, which a future refactor could undo". A caller
// that checked consent and then called Publish would satisfy today's tests and
// be one refactor away from publishing a user who declined.
//
// The refusal is AUDITED before it is returned. A refusal that is only visible
// as an error to the caller is invisible to an operator, and an opt-out that
// leaves no trace looks exactly like an opt-out that was never configured.
type PublisherImpl struct {
	Source ExportSource

	// Consent is a SEPARATE field from Source, and deliberately so. The consent
	// row must be read through the same database handle -- and therefore the
	// same transaction -- as the publish it gates, or a publish could commit
	// against a consent row a concurrent writer revoked. Folding it into
	// ExportSource would have made that impossible to state, because
	// ExportSource's job is reading library content, not permissions.
	Consent Queryer

	Sink     PayloadSink
	Auditor  AuditWriter
	instance string
}

// PayloadSink receives a built payload.
type PayloadSink interface {
	// Write stores the payload. For a dry run this is a local file and the job
	// log, and nothing leaves the host.
	Write(ctx context.Context, p Payload) error
}

// AuditWriter records a refusal.
type AuditWriter interface {
	AppendPublishRefusal(ctx context.Context, userID int64, reason string, detail map[string]any) error
}

func NewPublisher(instance string, src ExportSource, consent Queryer, sink PayloadSink, auditor AuditWriter) *PublisherImpl {
	return &PublisherImpl{Source: src, Consent: consent, Sink: sink, Auditor: auditor, instance: instance}
}

// PublishRefusedOptOut is the audit action for a refusal. A named constant
// because the audit row is a thing an operator greps for, and a string literal
// at the call site is one typo away from a row nothing can find.
const PublishRefusedOptOut = "publish_refused_optout"

// Publish refuses when the user has opted out, and writes the refusal to the
// audit log before returning.
func (p *PublisherImpl) Publish(ctx context.Context, payload Payload, userID int64) error {
	if p.Consent == nil {
		// A publisher with no consent reader is a publisher that cannot enforce
		// consent, which is the one configuration that must not exist. Failing
		// here rather than treating nil as "opted in" is the difference between
		// a startup error and a privacy incident.
		return fmt.Errorf("publisher for instance %q has no consent reader; refusing to publish rather than assuming consent", p.instance)
	}
	optedIn, err := ShareOptedIn(ctx, p.Consent, userID)
	if err != nil {
		// An unreadable consent row is NOT consent. Returning the read error
		// rather than proceeding (or than defaulting) is the only safe answer.
		return fmt.Errorf("reading consent for user %d before publish: %w", userID, err)
	}
	if !optedIn {
		// Audit first, then return. If the audit write fails the publish still
		// does not happen -- the refusal is the safe outcome either way, so a
		// failure to record it must not become a failure to refuse.
		if p.Auditor != nil {
			_ = p.Auditor.AppendPublishRefusal(ctx, userID, PublishRefusedOptOut, map[string]any{
				"instance":      p.instance,
				"submission_id": payload.SubmissionID,
				"entries":       len(payload.Entries),
			})
		}
		return ErrConsentOptedOut
	}

	// A dry run writes locally and sends nothing.
	if payload.DryRun {
		return p.Sink.Write(ctx, payload)
	}
	return p.Sink.Write(ctx, payload)
}

// IsPublishRefusal reports whether an error is the opt-out refusal, so a caller
// can distinguish "declined" from "failed" without string matching.
func IsPublishRefusal(err error) bool {
	return err == ErrConsentOptedOut
}

// AssertNoDisallowedValues is the runtime half of TestExport_ExcludesFilenamesAndPaths.
//
// It is NOT the primary defence -- the primary defence is that no field on this
// file can hold a path. This is a cheap second line that runs in tests and can be
// run against a real payload before it is sent, and it is written to be
// embarrassingly obvious: a value that looks like a filesystem path, a hostname,
// or an IP is refused.
//
// Returning an error rather than logging is the point. A warning nobody reads is
// how this class of bug ships.
func AssertNoDisallowedValues(p Payload) error {
	// Walk the DECODED payload, not the encoded bytes.
	//
	// This started as a string scan over the marshalled JSON and it did not
	// work: a path embedded inside a longer string never satisfies HasPrefix, so
	// the positive control -- a payload carrying a real path -- passed the
	// guard. Scanning the blob only catches a value that IS the whole string.
	// Decoding first and examining every leaf value is what makes the check
	// match its own name, and the test that caught it is
	// TestExport_ExcludesFilenamesAndPaths' positive control.
	var doc any
	raw, err := p.Marshal()
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("payload for instance %q is not valid JSON: %w", p.Instance, err)
	}
	// The instance identifier is chosen by the operator, not derived from the
	// user's data, and it is how a peer addresses this instance at all. A
	// host-shaped instance name ("stash.example.com") is therefore legitimate
	// and must not trip the guard -- otherwise every operator with a DNS name
	// could not export. Same for the content-addressed submission id, which is
	// a hex digest by construction.
	//
	// Everything under entries[].fields is user data and is checked in full.
	if bad := findDisallowedInUserData(doc); bad != "" {
		return fmt.Errorf("payload for instance %q contains a value that must never be published (%s); "+
			"the export is refusing to send it", p.Instance, bad)
	}
	return nil
}

// findDisallowedInUserData checks only the part of the payload that came from
// the user: each entry's fields and fingerprints. The envelope is skipped.
//
// This is a judgement, and the reasoning is worth stating because it is the
// kind of carve-out that usually turns into a hole. The envelope holds
// (a) the instance name, which the operator chose and which a peer needs in
// order to address the instance at all, and (b) the submission id, a hex
// digest. Refusing a payload for containing either would make the feature
// unusable for a DNS-named instance without protecting anyone's data.
//
// The alternative -- checking everything -- refuses legitimate titles (see
// TestAssertNoDisallowedValues_AcceptsRealTitles), and a guard that fires on
// ordinary content gets switched off, which protects nothing.
func findDisallowedInUserData(doc any) string {
	root, ok := doc.(map[string]any)
	if !ok {
		return findDisallowedIn(doc)
	}
	entries, ok := root["entries"]
	if !ok {
		return ""
	}
	list, ok := entries.([]any)
	if !ok {
		return findDisallowedIn(entries)
	}
	for _, e := range list {
		entry, ok := e.(map[string]any)
		if !ok {
			return findDisallowedIn(e)
		}
		// Fields and fingerprints are the user's data. target_type and
		// target_id are ours.
		for _, key := range []string{"fields", "fingerprints"} {
			if v, present := entry[key]; present {
				if bad := findDisallowedIn(v); bad != "" {
					return bad
				}
			}
		}
	}
	return ""
}

// findDisallowedIn walks every string in a decoded document -- KEYS as well as
// values. A key named "path" is as much a disclosure of the schema's shape as a
// value, and a payload carrying such a key is a payload whose producer believed
// it was publishing one.
func findDisallowedIn(node any) string {
	switch v := node.(type) {
	case string:
		if bad := findDisallowed(v); bad != "" {
			return bad
		}
		// Also check the string for an EMBEDDED path or host, which is the case
		// a whole-string check misses.
		if bad := findEmbeddedDisallowed(v); bad != "" {
			return bad
		}
	case []any:
		for _, item := range v {
			if bad := findDisallowedIn(item); bad != "" {
				return bad
			}
		}
	case map[string]any:
		for k, item := range v {
			if bad := findDisallowedIn(k); bad != "" {
				return bad
			}
			if bad := findDisallowedIn(item); bad != "" {
				return bad
			}
		}
	}
	return ""
}

// forbiddenShapes are the value patterns that must never appear in a payload.
//
// Deliberately matched against the MARSHALLED JSON, not the struct: a struct-level
// check cannot see a field added later, which is the whole failure mode here.
var forbiddenShapes = []struct {
	name string
	test func(string) bool
}{
	{"a unix absolute path", func(v string) bool { return strings.HasPrefix(v, "/") && strings.Count(v, "/") >= 2 }},
	{"a windows path", func(v string) bool {
		return len(v) > 3 && v[1] == ':' && (v[2] == '\\' || v[2] == '/')
	}},
	{"a home-relative path", func(v string) bool {
		return strings.HasPrefix(v, "~/") || strings.HasPrefix(v, "..\\") || strings.HasPrefix(v, "../")
	}},
	{"a UNC path", func(v string) bool { return strings.HasPrefix(v, `\\`) }},
	{"a URL with a host", func(v string) bool {
		return strings.Contains(v, "://") || strings.HasPrefix(v, "www.")
	}},
	{"an IPv4 address", func(v string) bool { return looksLikeIPv4(v) }},
	{"a bare hostname with a dot and a tld", func(v string) bool {
		// Requires a plausible TLD so a sentence ending in a full stop is not
		// mistaken for a host. A check too eager here would refuse legitimate
		// titles, and a check that refuses legitimate titles trains people to
		// ignore it.
		for _, tld := range []string{".com", ".net", ".org", ".io", ".local", ".lan", ".example"} {
			if strings.HasSuffix(strings.ToLower(v), tld) {
				return true
			}
		}
		return false
	}},
}

func findDisallowed(payload string) string {
	for _, shape := range forbiddenShapes {
		if shape.test(payload) {
			return shape.name
		}
	}
	return ""
}

// findEmbeddedDisallowed catches a forbidden shape ANYWHERE inside a string,
// which is the case that matters: a value is rarely exactly a path, it is
// usually a sentence with one in it.
//
// Kept separate from findDisallowed because the whole-string rules must stay
// strict -- a bare value that IS a path is a different and more serious finding
// than one with a path buried in a title, and conflating them would make the
// error message lie about what was found.
func findEmbeddedDisallowed(v string) string {
	// Absolute unix path anywhere: "/" followed by a segment, then another "/".
	if i := strings.Index(v, "/"); i >= 0 && i < len(v)-1 {
		rest := v[i:]
		if strings.Count(rest, "/") >= 2 && !strings.HasPrefix(rest, "//") {
			return "an absolute path embedded in a value"
		}
	}
	// Windows drive path anywhere.
	for i := 0; i+2 < len(v); i++ {
		if v[i+1] == ':' && (v[i+2] == '\\' || v[i+2] == '/') && isDriveLetter(v[i]) {
			return "a windows path embedded in a value"
		}
	}
	// host:port anywhere -- the shape an internal URL takes.
	if i := strings.Index(v, "://"); i >= 0 {
		return "a URL embedded in a value"
	}
	return ""
}

func isDriveLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func looksLikeIPv4(v string) bool {
	parts := strings.Split(v, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if p == "" || len(p) > 3 {
			return false
		}
		n := 0
		for _, r := range p {
			if r < '0' || r > '9' {
				return false
			}
			n = n*10 + int(r-'0')
		}
		if n > 255 {
			return false
		}
	}
	return true
}
