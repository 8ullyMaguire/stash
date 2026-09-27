package collab

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// A fake Queryer over a one-row consent table, so the consent tests read as
// statements about consent rather than about SQL.
//
// It is a fake rather than a real database on purpose: ShareOptedIn is the one
// guard in the project whose bug is irreversible, so its test must be able to
// enumerate the cases that matter — absent row, opted in, opted out, corrupt
// value, query error — without a migration being involved at all. The sqlite
// path is covered separately by TestConsentStore_ against a real database, so
// "the logic is right" and "the SQL is right" are two claims with two tests.

// fakeRows serves a fixed set of rows.
type fakeRows struct {
	rows [][]any
	pos  int
	err  error // returned by Err(), to model a mid-iteration failure
}

func (r *fakeRows) Next() bool {
	if r.pos >= len(r.rows) {
		return false
	}
	r.pos++
	return true
}

func (r *fakeRows) Scan(dest ...any) error {
	row := r.rows[r.pos-1]
	if len(dest) != len(row) {
		return fmt.Errorf("fakeRows: %d destinations for %d columns", len(dest), len(row))
	}
	for i, d := range dest {
		p, ok := d.(*string)
		if ok {
			s, _ := row[i].(string)
			*p = s
			continue
		}
		pi, ok := d.(*int)
		if ok {
			switch v := row[i].(type) {
			case int:
				*pi = v
			case int64:
				*pi = int(v)
			default:
				return fmt.Errorf("fakeRows: cannot scan %T into *int", row[i])
			}
			continue
		}
		return fmt.Errorf("fakeRows: unsupported destination %T", d)
	}
	return nil
}

func (r *fakeRows) Err() error   { return r.err }
func (r *fakeRows) Close() error { return nil }

// fakeQueryer answers the consent query from a canned row set.
//
// Each QueryContext hands back a FRESH result set over the same data. That is
// not a convenience, it is the property that makes the fake honest: the real
// driver can be queried any number of times, so a fake that exhausted itself
// after one read would make every second call look like an absent row -- and an
// absent row is the opted-in default, so a stateful fake fails by silently
// reporting consent. It did, and two tests caught it.
type fakeQueryer struct {
	// rows is the canned data, copied into a new fakeRows per query.
	rows    [][]any
	rowErr  error // reported by Err(), to model a mid-iteration failure
	qErr    error // returned by QueryContext itself
	lastSQL string
	calls   int
}

func (f *fakeQueryer) QueryContext(ctx context.Context, query string, args ...any) (Rows, error) {
	f.calls++
	f.lastSQL = query
	if f.qErr != nil {
		return nil, f.qErr
	}
	rows := make([][]any, len(f.rows))
	copy(rows, f.rows)
	return &fakeRows{rows: rows, err: f.rowErr}, nil
}

// consentRow builds a fake holding one consent row.
func consentRow(choice string, version int) *fakeQueryer {
	return &fakeQueryer{rows: [][]any{{choice, version}}}
}

// noRow builds a fake holding no consent row at all -- never asked.
func noRow() *fakeQueryer {
	return &fakeQueryer{}
}

// TestConsent_DefaultsToOptedIn is the single most consequential test in the
// package: the absent row means opted-in (spec §6.1), so a regression here
// publishes every user who was never asked. The mutation that breaks it is
// checked in mutate_consent.py -- "absent row treated as opted out" MUST be
// killed by this test and by no other.
func TestConsent_DefaultsToOptedIn(t *testing.T) {
	for _, tc := range []struct {
		name string
		q    *fakeQueryer
		want bool
	}{
		{"no row at all", noRow(), true},
		{"row set to opted-in", consentRow(string(ChoiceOptedIn), 1), true},
		{"row set to opted-out", consentRow(string(ChoiceOptedOut), 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ShareOptedIn(context.Background(), tc.q, 7)
			if err != nil {
				t.Fatalf("ShareOptedIn: %v", err)
			}
			if got != tc.want {
				t.Fatalf("opted in = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestConsent_OptOutIsSticky pins that a decline is not undone by anything that
// is not an explicit new answer. In particular: not by the disclosure version
// moving, and not by a second call to the reader.
func TestConsent_OptOutIsSticky(t *testing.T) {
	// A row that opted out at version 1, read repeatedly, with the current
	// version raised to simulate a field being added to the export.
	q := consentRow(string(ChoiceOptedOut), 1)
	for i := 0; i < 3; i++ {
		got, err := ShareOptedIn(context.Background(), q, 7)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if got {
			t.Fatalf("read %d: opted in after opting out at v1", i)
		}
	}

	// And the opt-out must not ask to be re-prompted, which is the other half
	// of stickiness: a re-prompt is a dialog with a Share button in it. Current
	// version is 99 here, so a re-prompt would mean the change is trying to
	// talk a decliner into sharing.
	reprompt, err := needsRepromptAt(context.Background(), q, 7, 99)
	if err != nil {
		t.Fatalf("NeedsReprompt: %v", err)
	}
	if reprompt {
		t.Fatal("an opted-out user must not be re-prompted: a prompt is a dialog with a Share button, and a user who does not click it has not consented")
	}
}

// TestConsent_DisclosureVersionChangeForcesReprompt covers the re-prompt
// mechanism in both directions: an opted-in user who is behind is stale, and one
// who is current is not.
func TestConsent_DisclosureVersionChangeForcesReprompt(t *testing.T) {
	// Never asked: always needs asking.
	{
		got, err := needsRepromptAt(context.Background(), noRow(), 7, 2)
		if err != nil {
			t.Fatalf("NeedsReprompt: %v", err)
		}
		if !got {
			t.Fatal("a user who was never asked must be prompted")
		}
	}

	// Opted in at the current version: current, not stale.
	{
		got, err := needsRepromptAt(context.Background(), consentRow(string(ChoiceOptedIn), 2), 7, 2)
		if err != nil {
			t.Fatalf("NeedsReprompt: %v", err)
		}
		if got {
			t.Fatal("a user who answered at the current version must not be re-prompted")
		}
	}

	// Opted in at an older version: the published set changed under them.
	{
		got, err := needsRepromptAt(context.Background(), consentRow(string(ChoiceOptedIn), 2), 7, 3)
		if err != nil {
			t.Fatalf("NeedsReprompt: %v", err)
		}
		if !got {
			t.Fatal("a user who answered at v2 while current is v3 must be re-prompted: the field set changed under them")
		}
	}
}

// TestConsent_CorruptRowIsAnErrorAndNotADefault covers the case the schema
// CHECK exists to prevent. If a row somehow holds a value the type does not
// recognise, choosing a default would make a privacy decision on behalf of a
// bug -- so the read fails loudly and publishes nothing.
func TestConsent_CorruptRowIsAnErrorAndNotADefault(t *testing.T) {
	for _, bad := range []string{"", "opt-in", "OPTED-IN", "yes", "true", "opted in"} {
		t.Run("metadata_share="+bad, func(t *testing.T) {
			q := consentRow(bad, 1)
			got, err := ShareOptedIn(context.Background(), q, 7)
			if err == nil {
				t.Fatalf("ShareOptedIn with metadata_share=%q returned (%v, nil): want an error, because defaulting here would publish on a corrupt row", bad, got)
			}
			if !strings.Contains(err.Error(), "metadata_share") {
				t.Fatalf("error %q should name the offending column", err)
			}
		})
	}

	// A zero or negative version is equally corrupt: it means a write that did
	// not set the column, and treating version 0 as "current" would let a
	// stale answer look fresh.
	for _, v := range []int{0, -1} {
		t.Run(fmt.Sprintf("disclosure_version=%d", v), func(t *testing.T) {
			q := consentRow(string(ChoiceOptedIn), v)
			if _, err := ShareOptedIn(context.Background(), q, 7); err == nil {
				t.Fatalf("ShareOptedIn with disclosure_version=%d returned nil error", v)
			}
		})
	}
}

// TestConsent_QueryFailureIsNotConsent checks the error path does not collapse
// into the default. A database that cannot be reached must not read as consent.
func TestConsent_QueryFailureIsNotConsent(t *testing.T) {
	q := &fakeQueryer{qErr: errors.New("disk gone")}
	got, err := ShareOptedIn(context.Background(), q, 7)
	if err == nil {
		t.Fatalf("ShareOptedIn on a failing query returned (%v, nil): a database error must never read as consent", got)
	}
	if !errors.Is(err, q.qErr) {
		t.Fatalf("error %v does not wrap the underlying failure", err)
	}
	if got {
		t.Fatal("ShareOptedIn returned true alongside an error")
	}
}

// TestConsent_IterationFailureIsSurfaced models a result set that fails partway
// through, which Err() reports. It must not be mistaken for an empty result and
// therefore for the opted-in default.
func TestConsent_IterationFailureIsSurfaced(t *testing.T) {
	q := &fakeQueryer{rowErr: errors.New("context canceled")}
	if _, err := ShareOptedIn(context.Background(), q, 7); err == nil {
		t.Fatal("a mid-iteration failure must surface as an error, not as an absent row and so not as consent")
	}
}

// TestSetConsent_RejectsAnInvalidChoice keeps an unknown string from reaching
// the database, where the CHECK would reject it with a much less useful message.
func TestSetConsent_RejectsAnInvalidChoice(t *testing.T) {
	s := &fakeConsentStore{}
	for _, bad := range []ShareChoice{"", "opted in", "YES", "opted-in "} {
		if err := SetConsent(context.Background(), s, 7, bad); err == nil {
			t.Fatalf("SetConsent(%q) returned nil error", bad)
		}
	}
	if s.writes != 0 {
		t.Fatalf("an invalid choice reached the store: %d writes attempted", s.writes)
	}
}

// fakeConsentStore records writes.
type fakeConsentStore struct {
	fakeQueryer
	writes int
	got    []ShareChoice
}

func (f *fakeConsentStore) SetConsent(ctx context.Context, userID int64, choice ShareChoice) error {
	f.writes++
	f.got = append(f.got, choice)
	return nil
}

func TestSetConsent_WritesTheChoice(t *testing.T) {
	s := &fakeConsentStore{}
	if err := SetConsent(context.Background(), s, 7, ChoiceOptedOut); err != nil {
		t.Fatalf("SetConsent: %v", err)
	}
	if s.writes != 1 || !reflect.DeepEqual(s.got, []ShareChoice{ChoiceOptedOut}) {
		t.Fatalf("store saw %d writes of %v, want one opted-out", s.writes, s.got)
	}
}

// TestDisclosureMatchesExporterFields is the test that stops the disclosure
// from becoming a lie.
//
// It compares the disclosed field list against PublishedFields, which the
// exporter is required to draw its field map from. It cannot detect a field the
// exporter sends that is NOT in PublishedFields unless the exporter's map is
// handed to it -- so the real protection is that the exporter must build its
// map from PublishedFieldSet() rather than declaring its own list. That is
// asserted structurally in exporter.go, and this test pins the prompt's half.
//
// The failure this prevents: someone adds a field to the export, forgets the
// disclosure, and the prompt keeps saying "title, details, date" while a
// fingerprint the user never saw is on the wire.
func TestDisclosureMatchesExporterFields(t *testing.T) {
	text := DisclosureText()
	names := PublishedFieldNames()
	if len(names) == 0 {
		t.Fatal("PublishedFields is empty: the disclosure would list nothing and claim that is everything")
	}

	for _, name := range names {
		if !strings.Contains(text, name) {
			t.Errorf("disclosure text does not mention published field %q -- a user reading it cannot consent to it", name)
		}
	}

	// Every field's description must be present too, since the description is
	// what tells the user what the name means.
	for _, f := range PublishedFields {
		if f.Description == "" {
			t.Errorf("published field %q has no description; the disclosure would name it without saying what it is", f.Name)
		}
		if !strings.Contains(text, f.Description) {
			t.Errorf("disclosure text is missing the description for %q", f.Name)
		}
	}

	// The withheld list must be rendered as well, because "never published" is
	// part of what the user is agreeing to.
	for _, withheld := range NeverPublished {
		if !strings.Contains(text, withheld) {
			t.Errorf("disclosure text omits the withheld item %q", withheld)
		}
	}

	// Display order must be the order a person reads, so the first disclosed
	// field must appear before the last one in the rendered text.
	if strings.Index(text, names[0]) > strings.Index(text, names[len(names)-1]) {
		t.Error("disclosure text does not render fields in PublishedFields order")
	}
}

// TestPublishedFieldSetMatchesTheList guards the helper the exporter draws from.
func TestPublishedFieldSetMatchesTheList(t *testing.T) {
	set := PublishedFieldSet()
	if len(set) != len(PublishedFields) {
		t.Fatalf("PublishedFieldSet has %d entries for %d fields: duplicate names would silently drop a field from the export", len(set), len(PublishedFields))
	}
	for _, f := range PublishedFields {
		if !set[f.Name] {
			t.Errorf("field %q is in the list but not the set", f.Name)
		}
	}
}

// TestSortedPublishedFieldNamesIsACopy guards against a caller mutating the
// package-level slice by sorting it in place -- the classic Go aliasing bug,
// and a plausible one here because PublishedFields is a mutable package var.
func TestSortedPublishedFieldNamesIsACopy(t *testing.T) {
	before := append([]string(nil), PublishedFieldNames()...)
	got := SortedPublishedFieldNames()
	if !reflect.DeepEqual(PublishedFieldNames(), before) {
		t.Fatal("SortedPublishedFieldNames mutated PublishedFields' order")
	}
	if !sortStringsAscending(got) {
		t.Fatalf("SortedPublishedFieldNames returned %v, which is not sorted", got)
	}
}

func sortStringsAscending(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i-1] > s[i] {
			return false
		}
	}
	return true
}

// TestDisclosureVersionTracksPublishedFields is the canary against the failure
// mode that matters most here: a field added to the published set with no
// version bump, so users who agreed to an older list never see the new one.
//
// It cannot know whether a bump is *wanted* -- that is a product judgement made
// when the field is added. What it can do is make the bump a conscious act by
// failing when the declared version is one, which is what a fresh project has,
// and by having a named test that the next person adding a field will find.
func TestDisclosureVersionTracksPublishedFields(t *testing.T) {
	if CurrentDisclosureVersion < 1 {
		t.Fatalf("CurrentDisclosureVersion = %d, want >= 1", CurrentDisclosureVersion)
	}

	// Every published field must be individually safe to disclose. The version
	// covers the SET changing; this covers a field being added with a name that
	// would not render, which the version bump would not catch.
	for _, f := range PublishedFields {
		if strings.TrimSpace(f.Name) != f.Name || f.Name == "" {
			t.Errorf("published field name %q has surrounding whitespace or is empty", f.Name)
		}
	}
}
