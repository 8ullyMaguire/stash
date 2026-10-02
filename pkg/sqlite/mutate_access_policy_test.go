//go:build integration
// +build integration

package sqlite_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE MUTATION GATE MUST NAME EVERY TEST IT RUNS.
//
// This exists because of a measured failure, and the failure is the whole point.
//
// `mutate_access_policy.py` wrote its `-run` pattern out in two places. A new test --
// TestTheColumnDefaultsApplyToARowThatDoesNotNameThem -- was added to one copy and
// not the other, so the gate reported TWO MUTANTS SURVIVED that the suite was in fact
// able to kill. Verified by hand: with `DEFAULT 0` changed to `DEFAULT 5`, that test
// fails with "expected: 0, actual: 5". The gate said SURVIVED.
//
// A mutation gate's entire output is the word SURVIVED, so a survivor that was never
// actually run against the test that catches it is the one failure this tool cannot
// distinguish from a real finding. It reads as evidence about the code and is
// evidence about the gate -- and it argues for a weaker gate at exactly the moment
// the weaker gate is least wanted.
//
// So the gate's pattern is now a single SUITE_PATTERN constant, and this test asserts
// that every Test function in the suite's file appears in it. Adding a test without
// naming it here fails HERE, loudly, rather than silently producing a gate that
// reports survivors it cannot explain.
func TestTheMutationGateNamesEveryTest(t *testing.T) {
	gatePath := filepath.Join("mutate_access_policy.py")
	raw, err := os.ReadFile(gatePath)
	require.NoError(t, err, "reading the gate script; it is a committed artefact "+
		"precisely so this test can check it")
	gate := string(raw)

	// The single source of truth. Asserted as a literal rather than imported,
	// because a Python module is not importable from Go and the alternative --
	// duplicating it here -- is the drift this test exists to prevent.
	// SUITE_PATTERN IS READ OUT OF THE SCRIPT, NOT COPIED.
	//
	// The first two versions of this test both hardcoded the pattern in Go, and
	// that is the drift being guarded against wearing a different hat: a literal in
	// two files is two things to edit for one test, and the version that is missed
	// fails SILENTLY in the gate while the test that was supposed to notice reports
	// on its own stale copy. Extracting it from the script means there is one place
	// it can be wrong, and this test fails if the script and the suite disagree --
	// which is the actual question.
	//
	// The extraction is a regexp over the script rather than an import, because a
	// Python module cannot be imported from Go and executing it would run the whole
	// gate. Reading the constant is enough: the question is which names the gate
	// will run.
	patternRe := regexp.MustCompile(`(?s)SUITE_PATTERN = \(\s*(.*?)\s*\)`)
	m := patternRe.FindStringSubmatch(gate)
	require.NotNil(t, m, "SUITE_PATTERN is not defined in the gate script; if it was "+
		"renamed or inlined, this test must be updated to follow it -- otherwise the "+
		"gate and this guard have drifted apart silently")

	// Reassemble the Python implicit string join: "a" "b" "c" -> abc.
	fragments := regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(m[1], -1)
	require.NotEmpty(t, fragments, "SUITE_PATTERN's body holds no quoted fragments, "+
		"so the extraction is wrong rather than the script being odd")
	var sb strings.Builder
	for _, f := range fragments {
		sb.WriteString(f[1])
	}
	gatePattern := sb.String()
	require.NotEmpty(t, gatePattern)

	// AND IT IS DEFINED ONCE, so the two copies cannot drift apart again.
	assert.Equal(t, 1, strings.Count(gate, "SUITE_PATTERN = ("),
		"SUITE_PATTERN must be defined exactly once")
	// Every use must be the constant, never an inline literal.
	uses := strings.Count(gate, "run_tests(SUITE_PATTERN)")
	assert.Equal(t, 2, uses, "both the baseline run and the mutant run must use the "+
		"constant. An inline pattern in either place is how the two drifted")

	// NOW THE REAL CHECK: every Test in this suite's file is COVERED by the gate's
	// pattern.
	//
	// A PREFIX match, not an equality check. `go test -run` takes a regular
	// expression matched against the test name with an implicit ^...$, so
	// "TestAccessPolicy" covers TestAccessPolicyCeilingDefaultsToPublic. The first
	// version of this check demanded the full name appear in the pattern, which
	// fails on every test whose name has a suffix -- and the fix for that (listing
	// all fifteen full names) would put the pattern and the test list in a file that
	// has to be edited twice for one new test, which is the drift being guarded
	// against.
	//
	// So the check is: does some alternative in the pattern match this name? That is
	// exactly the question "would the gate run this test", asked the way the tool
	// asks it.
	suiteRaw, err := os.ReadFile("stashforge_access_policy_test.go")
	require.NoError(t, err)
	testNames := regexp.MustCompile(`(?m)^func (Test\w+)\(`).FindAllStringSubmatch(string(suiteRaw), -1)
	require.NotEmpty(t, testNames, "the regex found no tests, which means it is wrong "+
		"rather than that the suite is empty")

	pattern := gatePattern
	// UNANCHORED, because that is how `go test -run` matches: the pattern is a
	// regex searched within each test name, so the alternative "TestAccessPolicy"
	// covers TestAccessPolicyCeilingDefaultsToPublic.
	//
	// The first version ANCHORED this with ^(?:...)$, which was wrong in the
	// strict direction -- it demanded the pattern name equal the test name, so
	// every suffixed test read as uncovered and the guard failed on a green gate.
	// A guard that fires on a correct configuration trains its reader to ignore it,
	// which is the same lesson the relay probe's first run taught: a check that
	// cries wolf on a correct run is worse than no check.
	re, err := regexp.Compile(pattern)
	require.NoError(t, err, "the gate's pattern must be a valid regexp, or `go test "+
		"-run` rejects it and the gate silently runs nothing")

	var uncovered []string
	for _, m := range testNames {
		if !re.MatchString(m[1]) {
			uncovered = append(uncovered, m[1])
		}
	}
	assert.Empty(t, uncovered,
		"these tests exist in the suite but no alternative in the gate's -run pattern "+
			"matches them, so the gate would silently fail to run them: %v", uncovered)
}
