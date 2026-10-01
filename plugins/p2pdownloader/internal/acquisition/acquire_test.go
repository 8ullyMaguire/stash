package acquisition

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE THREE STATES, and the middle one is the reason the type exists. This is
// §6b.3's table, asserted state by state, because a boolean would pass a test
// written against two of these rows.
func TestTheThreeStatesDifferExactlyAsTheTableSays(t *testing.T) {
	// off: fetches no, seeds no, uploads no
	offF, err := AcquireOff.MayFetch()
	require.Error(t, err, "off must refuse fetching")
	assert.False(t, offF)
	assert.ErrorIs(t, err, ErrNotPermitted)
	offS, err := AcquireOff.MaySeed()
	assert.Error(t, err)
	assert.False(t, offS)
	assert.False(t, AcquireOff.UploadsData())

	// fetch_only: fetches YES, seeds no, uploads no
	//
	// This is the row a boolean cannot express, and the assertion is on the PAIR
	// rather than on either value alone: a switch that answered `true` for both
	// would be `full`, and one that answered `false` for both would be `off`.
	ffF, err := AcquireFetchOnly.MayFetch()
	require.NoError(t, err, "fetch_only fetches")
	assert.True(t, ffF)
	ffS, err := AcquireFetchOnly.MaySeed()
	require.Error(t, err, "fetch_only must refuse seeding -- this is the whole point of the middle state")
	assert.False(t, ffS)
	assert.False(t, AcquireFetchOnly.UploadsData(),
		"§6b.3: fetch_only has NO publish path, which is why non-negotiable #7 "+
			"cannot be violated through it and why it is the default")

	// full: fetches yes, seeds yes, uploads per the existing control
	fuF, err := AcquireFull.MayFetch()
	require.NoError(t, err)
	assert.True(t, fuF)
	fuS, err := AcquireFull.MaySeed()
	require.NoError(t, err)
	assert.True(t, fuS)
	assert.True(t, AcquireFull.UploadsData(),
		"full has a publish path, so the per-torrent upload control is consulted. "+
			"NOTE this is not 'uploads freely' -- internal/policy still decides per torrent.")
}

// THE ZERO VALUE IS off, and it is the safety property rather than a default.
//
// This is the assertion that matters most in the file. An unset column, an unset
// struct field, and a missing JSON key all read as the zero value, so if the zero
// value were permissive the failure would be silent and would begin acquiring
// before any operator decided anything.
func TestTheZeroValueIsTheSafeState(t *testing.T) {
	var unset AcquireMode // exactly what a new column or an unset field holds

	// MEASURED, and it is NOT the string "off": the zero value is "". My first
	// version of this test asserted `unset == AcquireOff`, which failed, and a
	// probe showed why -- a string type's zero value is the empty string, and
	// `off` is a named constant that merely HAPPENS to be one of the valid strings.
	//
	// That is the better outcome, not a compromise. "off" would need a special case
	// in every reader, whereas "" is already refused by exactly the fail-closed
	// path an unrecognised value takes. An unset switch and a switch set to a value
	// this build does not understand are the same problem -- a row nobody decided
	// -- so they are refused the same way.
	assert.Equal(t, "", string(unset), "the zero value is the empty string")
	assert.False(t, Known(unset), "and it is therefore NOT a known mode")

	// THE PROPERTY THAT MATTERS is the behaviour, not the string.
	may, err := unset.MayFetch()
	assert.Error(t, err, "an unset switch refuses fetching")
	assert.False(t, may)
	assert.False(t, unset.UploadsData(),
		"an unset switch must have no publish path. If this fails, an instance that "+
			"never chose a mode would start seeding on upgrade.")
	assert.False(t, Decide(unset, false).Fetch,
		"and the queue agrees, so the opt-out is enforced at every entry point")

	// Which refusal? The UNRECOGNISED one -- and that is the informative
	// difference, because it points at a row nobody wrote rather than at a setting
	// somebody turned off.
	assert.ErrorIs(t, err, ErrUnknownMode,
		"an unset switch reports 'unrecognised'. ErrNotPermitted instead would send "+
			"the operator to a settings screen that already reads off.")

	// The same for a struct field that was never assigned: both are the zero
	// value, so both are covered by the assertions above.
	type config struct {
		Mode AcquireMode
	}
	assert.Equal(t, "", string(config{}.Mode),
		"a config struct that forgot to set the field reads as unset, not as full")
}

func TestAnUnrecognisedModeDoesNotAcquire(t *testing.T) {
	for _, m := range []AcquireMode{"", "true", "1", "ON", "Off", "FULL", "seed", "fetch-only"} {
		assert.False(t, Known(m), "mode %q is not one this build knows", m)

		fetch, err := m.MayFetch()
		assert.Error(t, err, "mode %q must refuse fetching", m)
		assert.False(t, fetch)

		seed, err := m.MaySeed()
		assert.Error(t, err, "mode %q must refuse seeding", m)
		assert.False(t, seed)

		assert.False(t, m.UploadsData(),
			"mode %q has no publish path. An unrecognised value defaulting to the "+
				"most permissive behaviour is how a stranger's material gets "+
				"redistributed without anyone deciding it.", m)
	}
}

// Parse refuses rather than defaulting, and says which value it refused — so a
// migration writing a value this build does not know is loud.
func TestParseRefusesAnUnknownValueAndNamesIt(t *testing.T) {
	for _, s := range []string{"off", "fetch_only", "full"} {
		m, err := Parse(s)
		require.NoError(t, err, "the three real values must parse")
		assert.Equal(t, AcquireMode(s), m)
	}

	for _, s := range []string{"", "yes", "true", "fetchonly", "FULL", "partial"} {
		m, err := Parse(s)
		require.Error(t, err, "%q must not parse", s)
		assert.ErrorIs(t, err, ErrUnknownMode)
		assert.Contains(t, err.Error(), s,
			"the error must NAME the refused value. A parse error that says only "+
				"'unknown mode' sends an operator looking at the code instead of the row.")
		assert.Equal(t, AcquireOff, m,
			"and the returned mode on error is the safe one")
	}
}

// The queue is "what the mesh holds, MINUS what this instance already has", and the
// refusal must name the SWITCH. An operator who cannot tell a switched-off queue
// from a broken one will work around the switch instead of reading it.
func TestDecideConsultsTheSwitchBeforeAnythingElse(t *testing.T) {
	// Off, for content we do NOT have: the switch is the reason.
	d := Decide(AcquireOff /*alreadyHave*/, false)
	assert.False(t, d.Fetch)
	assert.Contains(t, d.Reason, "R083",
		"the refusal names the requirement, so it is findable")

	// Off, for content we DO have. The switch still wins, because an instance that
	// has switched acquisition off must not be told its queue is merely full --
	// that hides the switch behind an unrelated fact.
	d2 := Decide(AcquireOff, true)
	assert.False(t, d2.Fetch)
	assert.NotContains(t, d2.Reason, "already holds",
		"§6b.3's opt-out is a hard stop. If `already have` can be the reason an "+
			"opted-out instance sees an empty queue, the operator cannot tell the "+
			"switch is what is empty.")

	// fetch_only, not held: acquired.
	assert.True(t, Decide(AcquireFetchOnly, false).Fetch,
		"fetch_only fetches, which is why it is the default")

	// fetch_only, held: not acquired, and for the RIGHT reason.
	d3 := Decide(AcquireFetchOnly, true)
	assert.False(t, d3.Fetch)
	assert.Contains(t, d3.Reason, "already holds",
		"here `already holds` IS the reason, and it is not a permission failure")

	// full, not held: acquired.
	assert.True(t, Decide(AcquireFull, false).Fetch)

	// An unrecognised mode, either way: refused, and the reason is the mode.
	for _, have := range []bool{true, false} {
		du := Decide(AcquireMode("mystery"), have)
		assert.False(t, du.Fetch)
		assert.Contains(t, du.Reason, "does not recognise",
			"with alreadyHave=%v the refusal must still be about the mode", have)
		assert.NotContains(t, du.Reason, "already holds",
			"and must NOT be reported as a full queue: that hides the mode behind an "+
				"unrelated fact")
	}
}

// The enforcement point is the PERMISSION, so there is deliberately no filtered
// query here. Asserted structurally, because the absence of a method cannot be
// tested by calling it.
func TestThereIsNoFilteredQueryToBypass(t *testing.T) {
	// A caller holding a list cannot tell a filtered list from a complete one, so
	// §6b.3 requires enforcement at the permission read instead. The exported API
	// must therefore contain no `Query...` that returns candidates.
	for _, name := range exportedNames() {
		assert.NotContains(t, name, "Query",
			"the package exports %s. A filtered-query shape is the one a refactor "+
				"can bypass by forgetting the WHERE clause; §6b.3 requires the "+
				"permission to be read instead.", name)
		assert.NotContains(t, name, "List",
			"the package exports %s, which has the same shape", name)
		assert.NotContains(t, name, "Candidates",
			"the package exports %s, which has the same shape", name)
	}
}

// A mode must never be able to OVERRIDE a per-torrent refusal. AcquireFull widens
// what may be FETCHED; it does not license redistribution the consent tier forbids.
func TestFullDoesNotMeanUploadFreely(t *testing.T) {
	// The distinction the doc comment claims: mode answers "may we fetch", the
	// tier policy answers "may these bytes leave".
	assert.True(t, AcquireFull.UploadsData(),
		"full has a publish path, so the control is consulted")

	// The way that is enforced is by NOT being able to express 'full' as a reason
	// to upload. There is no method here that takes a tier and returns an upload
	// decision -- that decision belongs to internal/policy, on the core side of
	// the seam, precisely because the plugin cannot be trusted to make it.
	for _, name := range exportedNames() {
		assert.NotContains(t, name, "Tier",
			"the package exports %s, which would mean the plugin is deciding "+
				"redistribution. internal/policy decides that from the consent tier, "+
				"on the core side of the seam, because asking the thing that wants "+
				"to upload would be asking the wrong party.", name)
	}
}

// exportedNames lists this package's exported functions and methods, by parsing
// its own source.
//
// The two structural tests above assert an ABSENCE -- that there is no filtered
// query and no tier-aware method -- and an absence cannot be tested by calling
// something. Reflection cannot see a function nobody calls, so the source is
// parsed instead.
//
// _test.go files are EXCLUDED, which took one attempt to learn: the first version
// scanned everything and matched `TestThereIsNoFilteredQueryToBypass` and
// `TestFullDoesNotMeanUploadFreely`, because both contain the words being
// forbidden. A guard that fails on its own name is scanning the wrong file.
func exportedNames() []string {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		return nil
	}

	var out []string
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, d := range f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok || !fn.Name.IsExported() {
					continue
				}
				out = append(out, fn.Name.Name)
			}
		}
	}
	sort.Strings(out)
	return out
}
