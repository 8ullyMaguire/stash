package collab

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Consent, per spec §6.1.
//
// This file is deliberately small and deliberately paranoid. The rule it
// implements is one line in the spec — an opted-out user publishes nothing —
// and getting it wrong is the only mistake in this project that cannot be
// undone from the user's side, because a published private library cannot be
// unpublished from the copies already out there. Everything below follows from
// that.

// CurrentDisclosureVersion is the version of the published-field disclosure a
// user must have seen for their consent to cover what is actually published
// today.
//
// Bump this when PublishedFields changes. A field ADDED to the export without a
// bump is a disclosure that lies: the user agreed to share titles and tags, and
// is now also sharing a field they were never shown. That is the failure the
// version exists to prevent, and TestDisclosureVersionTracksPublishedFields is
// what keeps the bump from being forgotten.
const CurrentDisclosureVersion = 1

// ErrConsentOptedOut is returned by Publish when the user has declined sharing.
// It is a sentinel so a caller can distinguish "refused on purpose" from "the
// database was unreachable" -- both are errors, and a refusal must never be
// reported as a failure to decide.
var ErrConsentOptedOut = errors.New("user has opted out of metadata sharing")

// ErrDisclosureStale is returned where a decision is required before sharing
// can continue because the published set changed since the user answered.
var ErrDisclosureStale = errors.New("the disclosure has changed since the user answered")

// Consent is one user's sharing decision, as stored.
//
// MetadataShare is the answer. DisclosureVersion is what the user was shown
// when they gave it, and the two are independent on purpose: a user can be
// opted-out AND up to date on the disclosure, and that is the most common state
// for anyone who has declined.
type Consent struct {
	UserID            int64
	MetadataShare     ShareChoice
	DecidedAt         string
	DisclosureVersion int
}

// ShareChoice is the stored form. A string type rather than a bool because the
// column is CHECKed against exactly these two values, and a bool in the domain
// would make the third state (a row that failed to parse) unrepresentable in
// the type that is supposed to catch it.
type ShareChoice string

const (
	// ChoiceOptedIn is the default, per spec §6.1. Named Choice* rather than
	// ShareOptedIn because the exported FUNCTION of that name is the API this
	// package promises, and a constant sharing it would be unreachable.
	ChoiceOptedIn  ShareChoice = "opted-in"
	ChoiceOptedOut ShareChoice = "opted-out"
)

// Valid reports whether the choice is one the schema permits. Used when reading
// a row: an unknown value is an error, not a silent false, because treating an
// unrecognised value as opted-out would let a schema bug stop publishing and
// treating it as opted-in would publish.
func (s ShareChoice) Valid() bool {
	return s == ChoiceOptedIn || s == ChoiceOptedOut
}

// Queryer is the read surface consent needs. Deliberately one method: consent
// is read in the publish path on every export, and a wider interface here would
// tempt callers to reach for it to do something other than read consent.
//
// Accepting an interface this small also makes the publish-path test trivial to
// write, which is the point — the opt-out is the one guard in the project that
// must be provably hard to test around.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (Rows, error)
}

// Rows is the minimal result surface: enough to walk a result set and close it.
// database/sql's *sql.Rows satisfies it structurally, so the sqlite adapter
// needs no wrapper type at all — which is why this is an interface of methods
// rather than a declared struct, and why importing database/sql here would have
// been unnecessary.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

// ConsentStore reads and writes the decision.
//
// Split from the query-only path on purpose: ShareOptedIn takes a Queryer so it
// can be called with the in-flight transaction in Publish (consent must be read
// and enforced in the same transaction as the publish, or a publish could commit
// against a consent row that was concurrently revoked), while the setters take
// a writer.
type ConsentStore interface {
	Queryer

	// SetConsent records the user's decision, replacing any previous answer.
	// It is the ONLY writer: a store with an UpdateConsent and a SetConsent
	// would let a caller write an answer that skips decided_at.
	SetConsent(ctx context.Context, userID int64, choice ShareChoice) error
}

// ShareOptedIn reports whether the user has consented to metadata sharing.
//
// ABSENCE OF A ROW MEANS OPTED-IN (spec §6.1). That is the single most
// consequential default in the project and it is the reason this function is
// written the way it is: the sql.ErrNoRows path returns true, and every other
// path that cannot prove consent also fails CLOSED for the caller to handle
// separately. TestConsent_DefaultsToOptedIn pins the absent case, because a
// regression there publishes every user who has never been asked.
func ShareOptedIn(ctx context.Context, q Queryer, userID int64) (bool, error) {
	choice, found, err := ReadConsent(ctx, q, userID)
	if err != nil {
		return false, err
	}
	if !found {
		// No row. The default is opted-in. Deliberate, per spec §6.1, and
		// conspicuous to the user by the first-run disclosure rather than
		// silent at this layer.
		return true, nil
	}
	return choice == ChoiceOptedIn, nil
}

// ReadConsent returns the stored decision and whether a row existed.
//
// The two-value return exists so the absent case is visible to callers that care
// about the difference, which ShareOptedIn does not: the disclosure screen needs
// to know "never asked" apart from "asked and said yes", because only the
// former should be prompted.
//
// An unparseable `metadata_share` is an error rather than a default. The column
// is CHECKed, so this can only happen if the check was bypassed or the row was
// written by something other than this store — and silently choosing either
// value would make that a privacy decision made by a bug.
func ReadConsent(ctx context.Context, q Queryer, userID int64) (ShareChoice, bool, error) {
	row, err := q.QueryContext(ctx,
		`SELECT metadata_share, disclosure_version FROM consent_preferences WHERE user_id = ?`,
		userID)
	if err != nil {
		return "", false, fmt.Errorf("querying consent for user %d: %w", userID, err)
	}
	if row == nil {
		return ChoiceOptedIn, false, nil
	}
	defer func() { _ = row.Close() }()

	if !row.Next() {
		if err := row.Err(); err != nil {
			return "", false, fmt.Errorf("reading consent for user %d: %w", userID, err)
		}
		// No rows: never asked. The default, and the only case that means it.
		return ChoiceOptedIn, false, nil
	}

	var choice string
	var version int
	if err := row.Scan(&choice, &version); err != nil {
		return "", false, fmt.Errorf("scanning consent for user %d: %w", userID, err)
	}
	// A second row is impossible (user_id is the primary key) but a Scan that
	// stopped early because the caller expected fewer columns would leave
	// version zero; treat anything out of range as corrupt rather than defaulting.
	if version < 1 {
		return "", false, fmt.Errorf("consent for user %d has disclosure_version %d, want >= 1", userID, version)
	}

	parsed := ShareChoice(choice)
	if !parsed.Valid() {
		return "", false, fmt.Errorf("consent for user %d has unknown metadata_share %q", userID, choice)
	}
	return parsed, true, nil
}

// NeedsReprompt reports whether the user must be shown the disclosure again
// before their consent can be relied on.
//
// True when there is no row at all (never asked) or when the row records a
// disclosure version below the current one.
//
// IT IS FALSE FOR AN OPTED-OUT USER WHO IS BEHIND. This is the whole subtlety:
// an opted-out user being re-prompted is not a neutral act, because a re-prompt
// is a dialog with a "Share" button in it, and any implementation of the prompt
// that treats "user did not click Share" as consent has just published a user
// who declined. So the answer to "should we ask again" must not be "yes, if the
// text changed" for someone whose answer was no.
//
// A declined user is not re-prompted automatically at all. Changing what is
// published does not create a new reason to ask someone who already said no --
// it is the same no. If a later version wants a fresh answer, that is a
// deliberate, separate product decision, and it must not arrive as a side effect
// of a field being added to the exporter.
func NeedsReprompt(ctx context.Context, q Queryer, userID int64) (bool, error) {
	return needsRepromptAt(ctx, q, userID, CurrentDisclosureVersion)
}

// needsRepromptAt is NeedsReprompt with the current version supplied, so a test
// can exercise the stale/current boundary without a mutable package global. A
// test-only setter would leave a var that any code could write, which is a worse
// thing to have in a package whose whole job is a trustworthy default.
func needsRepromptAt(ctx context.Context, q Queryer, userID int64, current int) (bool, error) {
	choice, found, err := ReadConsent(ctx, q, userID)
	if err != nil {
		return false, err
	}
	if !found {
		return true, nil
	}
	if choice == ChoiceOptedOut {
		// Declined. Stays declined, and stays un-prompted, until a human says
		// otherwise. See the comment above.
		return false, nil
	}
	if stale, err := disclosureStaleAt(ctx, q, userID, current); err != nil {
		return false, err
	} else {
		return stale, nil
	}
}

// DisclosureStale reports whether the recorded answer predates the current
// published-field list.
//
// Only meaningful for a user who has opted IN: for an opted-out user the
// disclosure is irrelevant, because nothing is being published and so there is
// nothing to have agreed to.
func DisclosureStale(ctx context.Context, q Queryer, userID int64) (bool, error) {
	return disclosureStaleAt(ctx, q, userID, CurrentDisclosureVersion)
}

// disclosureStaleAt is DisclosureStale with the current version supplied. See
// needsRepromptAt for why the version is a parameter rather than a var.
func disclosureStaleAt(ctx context.Context, q Queryer, userID int64, current int) (bool, error) {
	row, err := q.QueryContext(ctx,
		`SELECT metadata_share, disclosure_version FROM consent_preferences WHERE user_id = ?`,
		userID)
	if err != nil {
		return false, fmt.Errorf("querying disclosure version for user %d: %w", userID, err)
	}
	if row == nil {
		return true, nil
	}
	defer func() { _ = row.Close() }()

	if !row.Next() {
		if err := row.Err(); err != nil {
			return false, fmt.Errorf("reading disclosure version for user %d: %w", userID, err)
		}
		// Never asked, so there is no version and the disclosure is certainly
		// not current. The caller distinguishes this from "declined" via the
		// absent-row signal; here it collapses to "stale", which is the
		// answer NeedsReprompt needs.
		return true, nil
	}

	var choice string
	var version int
	if err := row.Scan(&choice, &version); err != nil {
		return false, fmt.Errorf("scanning disclosure version for user %d: %w", userID, err)
	}
	if ShareChoice(choice) == ChoiceOptedOut {
		// Nothing is published, so nothing can be stale. Returning true here
		// would make a declined user look like someone who needs re-asking,
		// which is the mistake this whole file is about.
		return false, nil
	}
	return version < current, nil
}

// PublishedFields is the exact set of field names the exporter may publish,
// with a one-line description of each for the disclosure prompt.
//
// THIS IS THE SINGLE SOURCE OF TRUTH. The disclosure prompt renders from it and
// the exporter's field map is checked against it, so the text a user reads and
// the fields actually sent cannot drift apart. A hand-written prompt is a
// disclosure that becomes a lie the first time a field is added, and the test
// that would catch that
// (TestDisclosureMatchesExporterFields) is what keeps them in step.
//
// Order is the display order in the prompt and is therefore not alphabetical:
// it runs from the most identifying field to the least, because a user reading
// a consent dialog reads the top.
var PublishedFields = []PublishedField{
	{Name: "title", Description: "the title of a scene"},
	{Name: "details", Description: "the description of a scene"},
	{Name: "date", Description: "the date a scene was recorded"},
	{Name: "director", Description: "the director of a scene"},
	{Name: "studio", Description: "the studio that produced a scene"},
	{Name: "tags", Description: "the tags applied to your scenes"},
	{Name: "performer_names", Description: "performer names and aliases"},
	{Name: "galleries", Description: "that a gallery exists and how many images it has"},
	{Name: "custom_fields", Description: "the values of your custom fields"},
	{Name: "fingerprints", Description: "content fingerprints (phash, oshash) used to find duplicates"},
}

// PublishedField is one disclosed field.
type PublishedField struct {
	Name        string
	Description string
}

// PublishedFieldNames returns the disclosed names in display order.
func PublishedFieldNames() []string {
	names := make([]string, 0, len(PublishedFields))
	for _, f := range PublishedFields {
		names = append(names, f.Name)
	}
	return names
}

// NeverPublished lists what is withheld regardless of consent, and is a
// statement about the code rather than about a setting: no field in this list
// has a path from the database to the payload, and the absence of a way to
// publish a value is the only enforcement that holds when a query is rewritten.
//
// TestExport_ExcludesFilenamesAndPaths walks the marshalled JSON against this
// list and against the shape of real filesystem values, because a struct-level
// assertion cannot see a field added to the payload later.
var NeverPublished = []string{
	"file paths",
	"file names",
	"filesystem sizes",
	"last-access timestamps",
	"hostnames",
	"IP addresses",
	"internal URLs",
	"owner account details",
	"rows belonging to a private library",
}

// DisclosureText renders the prompt shown before the first publish.
//
// GENERATED FROM PublishedFields, never hand-written (see that variable). Sorted
// only in the sense that PublishedFields is already in display order; the sort
// here is over the *copy* returned by a caller, and is deliberately absent --
// display order is the order a person reads.
func DisclosureText() string {
	var b strings.Builder
	b.WriteString("If you share, other instances can read the following about your scenes:\n")
	for _, f := range PublishedFields {
		b.WriteString("  - ")
		b.WriteString(f.Name)
		b.WriteString(": ")
		b.WriteString(f.Description)
		b.WriteString("\n")
	}
	b.WriteString("\nNever shared: ")
	b.WriteString(strings.Join(NeverPublished, ", "))
	b.WriteString(".\n")
	return b.String()
}

// SetConsent records the user's decision.
//
// Not a method on a type here: it is a free function over a ConsentStore so
// that the call site reads as an action with a defined meaning rather than as a
// method on a value that is only a view. The write bumps the disclosure version
// to the current one, because a user who has just been shown the current list
// has, by definition, seen it — recording an older version would re-prompt them
// for something they have already answered.
func SetConsent(ctx context.Context, s ConsentStore, userID int64, choice ShareChoice) error {
	if !choice.Valid() {
		return fmt.Errorf("consent choice %q is not one of %q or %q", choice, ChoiceOptedIn, ChoiceOptedOut)
	}
	return s.SetConsent(ctx, userID, choice)
}

// PublishedFieldSet returns the disclosed field names as a set, for comparison
// against an exporter's field map.
func PublishedFieldSet() map[string]bool {
	out := make(map[string]bool, len(PublishedFields))
	for _, f := range PublishedFields {
		out[f.Name] = true
	}
	return out
}

// SortedPublishedFieldNames returns the disclosed names sorted, for error
// messages that must not depend on display order.
func SortedPublishedFieldNames() []string {
	names := PublishedFieldNames()
	sort.Strings(names)
	return names
}
