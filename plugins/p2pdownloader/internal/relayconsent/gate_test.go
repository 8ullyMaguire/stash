package relayconsent

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE GATE NAMES THESE TESTS, AND THESE TESTS CHECK THAT IT DOES.
//
// A mutation gate that silently stops running some tests still reports success, and
// the cost of that is paid later and by nobody in particular: a test is renamed, the
// gate's -run pattern keeps the old name, and the mutant that test was written to kill
// survives behind a green gate and a confident summary line.
//
// Nothing forces the two lists to agree. So the agreement is asserted, from the gate's
// own source rather than from a copy here -- because a copy is exactly the thing that
// drifts.
//
// THE PARSING IS DELIBERATELY CRUDE, and the reason is worth stating. Three attempts
// at parsing that expression elegantly all failed, and the failures were:
//
//   - scanning for `)\n` truncated at the wrong place
//   - splitting on `|` leaves each closing quote welded to the next opening quote, so
//     the quotes sit in the MIDDLE of every split piece and trimming the ends cannot
//     work
//   - a regex character class of `[A-Za-z0-9_]*` between quotes matches EMPTY, because
//     the trailing `|` is INSIDE each quoted literal
//
// So this matches whole `"..."` pairs and trims the pipe afterwards. That mirrors how
// Python actually assembles the pattern: a literal, then a separator, repeated. A
// hand-rolled parser here would be a second thing to keep in step with the gate, which
// is the failure this file exists to prevent.
func TestTheGateNamesEveryRelayConsentTest(t *testing.T) {
	gate, err := os.ReadFile("mutate_relayconsent.py")
	require.NoError(t, err, "the gate must live beside the code it mutates")

	src := string(gate)

	start := strings.Index(src, "SUITE_PATTERN = (")
	require.GreaterOrEqual(t, start, 0, "the gate must define SUITE_PATTERN")

	expr := ""
	for _, line := range strings.Split(src[start:], "\n")[1:] {
		if strings.HasPrefix(strings.TrimSpace(line), ")") {
			break
		}
		expr += line + "\n"
	}
	require.NotEmpty(t, expr, "SUITE_PATTERN must be a parenthesised concatenation")

	inGate := map[string]bool{}
	for _, m := range regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(expr, -1) {
		inGate[strings.TrimRight(m[1], "|")] = true
	}
	require.NotEmpty(t, inGate, "SUITE_PATTERN must be built from quoted test names")

	// EVERY TEST IN THIS PACKAGE MUST BE NAMED IN THE GATE. The directory is read
	// rather than the file list hardcoded, so a NEW test is caught by not being named
	// -- which is the whole failure mode.
	entries, err := filepath.Glob("*_test.go")
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	var missing []string
	found := 0
	for _, entry := range entries {
		content, err := os.ReadFile(entry)
		require.NoError(t, err)
		for _, m := range regexp.MustCompile(`func (Test[A-Za-z0-9_]*)\(`).FindAllStringSubmatch(string(content), -1) {
			found++
			if !inGate[m[1]] {
				missing = append(missing, m[1])
			}
		}
	}

	require.NotZero(t, found, "no tests found to check; the regex is wrong, not the gate")
	assert.Empty(t, missing,
		"these tests are not in the gate's SUITE_PATTERN, so the gate would not run "+
			"them and a mutant they are meant to kill would survive a green report")
	assert.True(t, inGate["TestTheGateNamesEveryRelayConsentTest"],
		"the gate must run this test too, or the agreement is only checked when "+
			"something else happens to run it")
}

// EVERY MUTANT'S CATCHER MUST NAME A TEST THAT EXISTS.
//
// The most damaging output a gate can produce is a SURVIVED line naming a catcher
// nobody can run. So the catcher strings are checked against the tests this package
// defines, and a typo becomes a test failure rather than a note in a log file.
func TestEveryRelayConsentCatcherNamesATestThatExists(t *testing.T) {
	gate, err := os.ReadFile("mutate_relayconsent.py")
	require.NoError(t, err)
	src := string(gate)

	defined := definedTestNames(t)
	catchers := regexp.MustCompile(`"(Test[A-Za-z0-9_]+)[^"]*"`).FindAllStringSubmatch(src, -1)
	require.NotEmpty(t, catchers, "the gate must record why each mutant is caught")

	var unknown []string
	for _, m := range catchers {
		if !defined[m[1]] {
			unknown = append(unknown, m[1])
		}
	}
	assert.Empty(t, unknown,
		"these mutant catchers name tests that do not exist, so a SURVIVED line would "+
			"point a reader at a test they cannot run")
}

// definedTestNames is the one place the test list is read.
func definedTestNames(t *testing.T) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	entries, err := filepath.Glob("*_test.go")
	require.NoError(t, err)
	for _, entry := range entries {
		content, err := os.ReadFile(entry)
		require.NoError(t, err)
		for _, m := range regexp.MustCompile(`func (Test[A-Za-z0-9_]*)\(`).FindAllStringSubmatch(string(content), -1) {
			names[m[1]] = true
		}
	}
	return names
}
