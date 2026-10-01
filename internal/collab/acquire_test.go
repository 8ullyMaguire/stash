package collab

import (
	"errors"
	"testing"
)

// R083, §6b.3's three-state switch. These are TYPE tests and they live beside the
// type rather than in the database suite, because the properties here are about the
// type's shape and every one of them is false of a bool.

// §6b.3's table, transcribed. Written as a literal rather than derived from the
// methods, so changing a method does not silently rewrite the expectation.
func TestAutoAcquireSatisfiesTheSpecTable(t *testing.T) {
	cases := []struct {
		state          AutoAcquire
		fetches, seeds bool
		uploadsData    bool
	}{
		{AcquireOff, false, false, false},
		{AcquireFetchOnly, true, false, false},
		{AcquireFull, true, true, false},
	}

	for _, c := range cases {
		if got := c.state.Fetches(); got != c.fetches {
			t.Errorf("%q.Fetches() = %v, want %v", c.state, got, c.fetches)
		}
		if got := c.state.Seeds(); got != c.seeds {
			t.Errorf("%q.Seeds() = %v, want %v", c.state, got, c.seeds)
		}
		if got := c.state.UploadsData(); got != c.uploadsData {
			t.Errorf("%q.UploadsData() = %v, want %v -- §6b.3's table says 'per the "+
				"existing upload control' for ALL THREE states, so this switch never "+
				"widens data uploads", c.state, got, c.uploadsData)
		}
		// The named operations agree with the capabilities, so a caller reading the
		// acquisition path does not have to know that seeding is a different question.
		if got := c.state.MayAcquire(); got != c.fetches {
			t.Errorf("%q.MayAcquire() = %v, want %v", c.state, got, c.fetches)
		}
		if got := c.state.MaySeed(); got != c.seeds {
			t.Errorf("%q.MaySeed() = %v, want %v", c.state, got, c.seeds)
		}
	}
}

// THE ZERO VALUE IS off, so an unset value is the safe state rather than the harmful
// one. §6b.3 requires this of the switch and it is the property a bool cannot have:
// the zero value of a bool is false, which would be `fetch_only` if false meant
// fetch.
func TestTheZeroValueIsOff(t *testing.T) {
	var unset AutoAcquire
	if !unset.Valid() {
		t.Fatal("the zero value should still be a nameable state, so an unset column " +
			"reports 'off' rather than 'something I do not recognise'")
	}
	if unset.Fetches() || unset.Seeds() {
		t.Error("the zero value must not fetch and must not seed: a struct literal " +
			"that omits the field has acquired nothing")
	}
}

// AN UNRECOGNISED VALUE REFUSES EVERYTHING. This is the fail-closed property, and it
// is false of the zero value being safe: the zero value is safe because it is a known
// state, while an unknown value is safe because it is refused. Both directions are
// needed and they are different mechanisms.
func TestAnUnrecognisedValueRefusesEverything(t *testing.T) {
	// NOT including "": the empty value is the ZERO VALUE and resolves to `off` rather
	// than being refused, which is what makes plan step 8.1's "an unset column is the
	// safe state" true. TestAnEmptyValueIsSafe covers it separately, because it is a
	// decision rather than an accident and deserves its own name.
	for _, a := range []AutoAcquire{
		" ", "OFF", "fetch-only", "fetchonly", "full ", "true", "1", "none", "auto",
	} {
		if a.Valid() {
			t.Errorf("%q must not be valid", string(a))
		}
		if a.Fetches() {
			t.Errorf("%q must not fetch: an unrecognised value is not permission to "+
				"take content in, and this value may have come from a newer core", string(a))
		}
		if a.Seeds() {
			t.Errorf("%q must not seed, and this is the direction that matters most: "+
				"seeding is publishing, and a typo must not become a publish", string(a))
		}
		if a.UploadsData() {
			t.Errorf("%q must not upload data", string(a))
		}
	}
}

// ParseAutoAcquire REFUSES rather than coercing, and says which switch it is about.
// Coercing to the schema default is the tempting repair and it turns a typo into a
// posture nobody chose.
func TestParseAutoAcquireRefusesAndNamesItsOwnSwitch(t *testing.T) {
	for _, s := range []string{"off", "fetch_only", "full"} {
		got, err := ParseAutoAcquire(s)
		if err != nil {
			t.Errorf("%q must parse: %v", s, err)
		}
		if got.String() != s {
			t.Errorf("%q parsed to %q", s, got)
		}
	}

	for _, s := range []string{" ", "OFF", "fetch-only", "partial", "full\t"} {
		got, err := ParseAutoAcquire(s)
		if err == nil {
			t.Errorf("%q must be refused, got %v", s, got)
			continue
		}
		if !errors.Is(err, ErrAutoAcquireInvalid) {
			t.Errorf("%q: got %v, want ErrAutoAcquireInvalid", s, err)
		}
		// NOT the metadata-share sentinel. The two switches have opposite remedies: a
		// share refusal wants the user asked again, an acquire refusal wants the value
		// rejected, so a caller branching on the wrong one retries or degrades against
		// the wrong control.
		if errors.Is(err, ErrShareChoiceInvalid) {
			t.Errorf("%q must not report the metadata-share sentinel: this is an "+
				"instance capability switch, not a user's consent answer", s)
		}
		if got != AcquireOff {
			t.Errorf("%q: the refused value returned is %q, and it must be the refusing "+
				"state so a caller that ignores the error acquires nothing", s, got)
		}
	}
}

// THREE STATES, THREE DISTINCT BEHAVIOURS. If any two behaved identically on every
// capability the type would be three names for two behaviours, and the middle state —
// the one §6b.3 exists to express — would be fiction.
func TestTheThreeStatesAreThreeDistinctBehaviours(t *testing.T) {
	type behaviour struct{ fetches, seeds, uploads bool }
	seen := map[behaviour]AutoAcquire{}

	for _, a := range AcquireStates() {
		b := behaviour{a.Fetches(), a.Seeds(), a.UploadsData()}
		if other, dup := seen[b]; dup {
			t.Errorf("%q and %q have identical behaviour %+v -- the type is three "+
				"names for fewer behaviours", other, a, b)
		}
		seen[b] = a
	}
	if len(seen) != 3 {
		t.Errorf("got %d distinct behaviours across %d states", len(seen), len(AcquireStates()))
	}
}

// THE STATES ARE ORDERED BY PARTICIPATION, which is what makes `fetch_only` readable
// as the middle rather than as an arbitrary third name. Asserted on the list's order
// because a caller sorting by string would otherwise produce a misleading UI order.
func TestTheStatesAreOrderedByParticipation(t *testing.T) {
	got := AcquireStates()
	want := []AutoAcquire{AcquireOff, AcquireFetchOnly, AcquireFull}
	if len(got) != len(want) {
		t.Fatalf("got %d states, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("state %d is %q, want %q -- the order is increasing participation, "+
				"and a UI that sorts by string would otherwise put `full` before "+
				"`fetch_only`", i, got[i], want[i])
		}
	}
}

// AcquireStates RETURNS A COPY. It is a function rather than a package var precisely
// because a mutable list of valid states is a list somebody will append to — and
// appending a fourth state is exactly what §6b.3 requires a migration for.
func TestAcquireStatesCannotBeMutated(t *testing.T) {
	first := AcquireStates()
	first[0] = AcquireFull
	first = append(first, AutoAcquire("smuggled"))

	second := AcquireStates()
	if len(second) != 3 {
		t.Fatalf("the returned slice was mutated: got %d states", len(second))
	}
	for _, s := range second {
		if s == "smuggled" || (s == AcquireFull && second[0] == AcquireFull) {
			t.Fatalf("a smuggled state persisted: %v", second)
		}
	}
	if !AcquireOff.Valid() || AcquireOff.Fetches() {
		t.Error("the real states were disturbed by mutating the returned slice")
	}
}

// THE EMPTY VALUE IS off, NOT A REFUSAL — and this is a decision, not an accident.
//
// Plan step 8.1 requires that "the switch's zero value must be `off`, so an unset
// column is the safe state rather than the harmful one". A string-typed constant
// cannot have `off` as its zero value: "" is not "off". The requirement is about
// SAFETY, and safety lives in the refusals, so "" resolves to off at the boundary
// while `Valid()` stays strict for wire values.
//
// My first test asserted "" was invalid, and it failed against this — correctly. The
// two rules are only apparently contradictory:
//
//   - "" is the zero value of a Go struct or a context entry. Omitting the field means
//     "nobody chose", and nobody-chose must acquire nothing.
//   - "OFF" is a value somebody WROTE. It must be refused, because a peer on a newer
//     core or a hand-edited row must not acquire content.
//
// So "" is safe and "OFF" is refused, and they are distinguishable precisely because
// `off` has a spelling and "" does not.
func TestAnEmptyValueIsSafe(t *testing.T) {
	var unset AutoAcquire

	// Safe on every refusal, which is the whole property.
	if unset.Fetches() {
		t.Error("an unset switch must not acquire: a struct literal that omitted the " +
			"field has acquired nothing")
	}
	if unset.Seeds() {
		t.Error("an unset switch must not seed, and this is the direction that matters " +
			"most: seeding is publishing")
	}
	if unset.UploadsData() {
		t.Error("an unset switch must not upload data")
	}

	// Nameable, so an error message can say what it is rather than printing nothing.
	if !unset.Valid() {
		t.Error("the zero value must still be a nameable state")
	}

	// And it resolves to off at the boundary, which is what makes it the same answer a
	// caller would get for a deliberate choice of off.
	got, err := ParseAutoAcquire("")
	if err != nil {
		t.Errorf("an empty value must resolve rather than error: %v", err)
	}
	if got != AcquireOff {
		t.Errorf("an empty value resolved to %q, want %q -- an unset switch and a "+
			"deliberate `off` must give the same answer, or a caller has to handle "+
			"three cases instead of two", got, AcquireOff)
	}
	if unset.MayAcquire() || unset.MaySeed() {
		t.Error("and the named operations must agree with the capabilities")
	}
}
