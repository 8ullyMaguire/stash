package relaycrypto

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE GATE NAMES THIS TEST, AND THIS TEST CHECKS THE GATE NAMED IT.
//
// A mutation gate that silently stops running some tests reports success while
// measuring less than it claims to. Nothing forces the gate's -run pattern and this
// package's test list to agree, so they drift: a test is renamed, the pattern keeps
// the old name, and the mutant that the test was written to kill survives with a green
// gate and a confident summary line.
//
// So the agreement is asserted. The pattern is extracted from the gate source rather
// than duplicated here, which means a test dropped from the gate fails THIS test
// instead of quietly reducing coverage -- the duplication that made drift invisible is
// what is being checked.
func TestTheGateNamesEveryRelayCryptoTest(t *testing.T) {
	gate, err := os.ReadFile(filepath.Join("mutate_relaycrypto.py"))
	require.NoError(t, err, "the gate must live beside the code it mutates")

	src := string(gate)

	// The pattern is the single SUITE_PATTERN assignment, which is built by string
	// concatenation across several lines -- so collect every quoted fragment between
	// the assignment and its closing paren.
	start := strings.Index(src, "SUITE_PATTERN = (")
	require.GreaterOrEqual(t, start, 0, "the gate must define SUITE_PATTERN")

	// Read FORWARD LINE BY LINE until the closing paren at the start of a line.
	//
	// The first version searched for the literal ")\n" from the assignment onwards,
	// which truncates at the FIRST fragment -- because the fragments end with `|` not
	// `)`, but the first one to contain `)\n`... does not exist here, and the search
	// instead ran into the pattern's own closing paren several lines later than
	// intended, capturing the fragments AND reporting only the tail. The symptom was
	// an 11-element "missing" list naming every real test, which reads as total gate
	// coverage failure and was actually one bad search term.
	//
	// Scanning for a paren that is the FIRST CHARACTER of a line, after the opening
	// one, cannot match a fragment's trailing `|`, so the extent is unambiguous.
	expr := ""
	for _, line := range strings.Split(src[start:], "\n")[1:] {
		if strings.HasPrefix(strings.TrimSpace(line), ")") {
			break
		}
		expr += line + "\n"
	}
	require.NotEmpty(t, expr, "SUITE_PATTERN must be a parenthesised concatenation")

	// CAPTURE THE WHOLE LITERAL BETWEEN THE QUOTES, THEN TRIM THE PIPE.
	//
	// The trailing `|` is INSIDE the quotes -- each fragment is the literal
	// "Name|", because that is what Python's `|` string concatenation needs -- so a
	// character class that ends at the closing quote matches nothing but the LAST
	// fragment. Three attempts failed here in a row for that reason alone, each
	// reporting a total-coverage failure that was really one character of the regex.
	//
	// So the class accepts anything that is not a quote, and the pipe is trimmed
	// afterwards. That mirrors how the gate itself assembles the pattern: literal,
	// then separator, repeated.
	names := regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(expr, -1)
	require.NotEmpty(t, names, "SUITE_PATTERN must be built from quoted test names")

	require.NotEmpty(t, names, "SUITE_PATTERN must be built from quoted test names")

	// Every real test in this package must appear in the gate's pattern. The
	// directory is read rather than the file list hardcoded, so a NEW test is caught
	// by not being named in the gate -- which is the failure this exists to prevent.
	entries, err := filepath.Glob("*_test.go")
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	inGate := make(map[string]bool, len(names))
	for _, m := range names {
		inGate[strings.TrimRight(m[1], "|")] = true
	}

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
		"these tests are not named in the gate's SUITE_PATTERN, so the gate would not "+
			"run them and a mutant they are meant to kill would survive a green report")
	assert.Contains(t, inGate, "TestTheGateNamesEveryRelayCryptoTest",
		"the gate must run this test too, or the agreement is only checked when "+
			"something else happens to run it")
}

// AND THE GATE'S OWN CLAIM IS CHECKABLE: every catcher string must name a test.
//
// A mutation gate's most damaging output is a line saying SURVIVED with a catcher
// nobody can run. The mutant list is parsed and each catcher's test name is required to
// be one this package defines, so a typo in the catchers is a test failure rather than
// a note in a log file.
func TestEveryMutantCatcherNamesATestThatExists(t *testing.T) {
	gate, err := os.ReadFile("mutate_relaycrypto.py")
	require.NoError(t, err)
	src := string(gate)

	// Catchers are the LAST quoted string of each MUTANTS tuple. Rather than parse the
	// literal, require that every "TestXxx -- " prefix appearing in the gate is a real
	// test name.
	catchers := regexp.MustCompile(`"(Test[A-Za-z0-9_]+)[^"]*"`).FindAllStringSubmatch(src, -1)
	require.NotEmpty(t, catchers, "the gate must record why each mutant is caught")

	defined := map[string]bool{}
	entries, _ := filepath.Glob("*_test.go")
	for _, entry := range entries {
		content, err := os.ReadFile(entry)
		require.NoError(t, err)
		for _, m := range regexp.MustCompile(`func (Test[A-Za-z0-9_]*)\(`).FindAllStringSubmatch(string(content), -1) {
			defined[m[1]] = true
		}
	}

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
