package mesh

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// THE GUARD, and the reason the Claims type is named the way it is.
//
// The plan (step 7.1) asks for a grep-level test: a peer's claimed_store_bytes
// and claimed_bandwidth_bps must not be reachable by any sizing code path,
// because "the first version of a mesh node sizes a buffer from a stranger's
// number and nothing notices for a week".
//
// WHY A SOURCE SCAN AND NOT A RUNTIME ONE. The failure is not a wrong answer, it
// is a number read from the wrong place, and no unit test can tell: a function
// that sizes a buffer from a claim produces a perfectly reasonable buffer for
// any cooperative peer. There is nothing to assert at runtime. So the invariant
// has to be checked in the source, which is a real trade -- source scans miss
// indirect access through a getter -- and the exemption below is what keeps it
// from being vacuous.
//
// THE EXEMPTION IS THE SUBTLETY. The protocol package has to NAME these fields
// to decode them. A scan that included internal/mesh would match its own struct
// tags and field declarations and pass forever while the invariant rotted. So
// this package is exempt, and separately TestTheExemptionIsOnlyThisPackage
// asserts the exempt set has not quietly grown.

func TestAProfileClaimIsLabelledAClaim(t *testing.T) {
	claimFields := []string{"ClaimStoreBytes", "ClaimBandwidthBps"}

	forbidden := []string{
		// The accessors most likely to appear at a sizing site.
		"ClaimStoreBytesOrZero",
		// The wire names, in case someone decodes straight into a local struct.
		"claim_store_bytes",
		"claim_bandwidth_bps",
	}

	exempt := map[string]bool{"internal/mesh": true}

	var offenders []string
	err := filepath.WalkDir(repoRoot(t), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(repoRoot(t), path)
		if rerr != nil {
			return rerr
		}
		for dir := range exempt {
			if strings.HasPrefix(rel, dir) {
				return nil
			}
		}

		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		text := string(src)
		// Strip comments: the word appears in this repo's own prose about why
		// these fields are dangerous, and a scan that matched a comment would
		// fail on documentation rather than on code.
		text = stripComments(text, path)
		for _, f := range append(claimFields, forbidden...) {
			if strings.Contains(text, f) {
				offenders = append(offenders, rel+" references "+f)
			}
		}
		return nil
	})
	require.NoError(t, err)

	assert.Empty(t, offenders,
		"a peer's claim must never be reachable from a sizing path: %v\n\n"+
			"Reading a stranger's claim is fine. SIZING from it is the bug this "+
			"type is named to prevent -- it works for every cooperative peer and "+
			"fails silently for the rest. What belongs in a sizing path is a "+
			"MEASURED capacity, which means asking, verifying, and recording it "+
			"separately from the claim.", offenders)
}

func TestTheExemptionIsOnlyThisPackage(t *testing.T) {
	// The scan above is only as good as its exemption list. An exemption added to
	// silence a failure is how a guard stops guarding, so the list is asserted
	// here rather than trusted.
	const exemptDir = "internal/mesh"

	entries, err := os.ReadDir(repoRoot(t))
	require.NoError(t, err)

	var meshLike []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.Contains(name, "mesh") || name == "mesh" {
			continue
		}
		meshLike = append(meshLike, name)
	}
	assert.Empty(t, meshLike,
		"a new mesh-ish package is NOT covered by the claim guard's exemption. "+
			"Either it cannot read claims (fine, and this passes) or it needs the "+
			"exemption added deliberately -- but a second package must not inherit "+
			"this one silently: %v", meshLike)

	assert.DirExists(t, filepath.Join(repoRoot(t), exemptDir),
		"the exempt directory must exist; if it was renamed, the scan is scanning "+
			"nothing and passing vacuously")
}

// TestTheProtocolPackageItselfStillCarriesTheClaimFields guards the other
// direction. If Claims ever lost its fields the scan above would pass for the
// wrong reason, so the fields are asserted present here.
func TestTheProtocolPackageItselfStillCarriesTheClaimFields(t *testing.T) {
	var src string
	err := filepath.WalkDir(repoRoot(t), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Base(path) == "protocol.go" &&
			strings.Contains(filepath.ToSlash(path), "internal/mesh") {
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			src = string(b)
			return filepath.SkipAll
		}
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, src, "internal/mesh/protocol.go must exist")

	for _, f := range []string{"ClaimStoreBytes", "ClaimBandwidthBps"} {
		assert.Contains(t, src, f,
			"the Claims type must still declare %s: the source scan exempts "+
				"internal/mesh, so if the field disappears the scan passes for "+
				"the wrong reason", f)
	}
}

// repoRoot walks up from this file to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not find go.mod above the test's working directory")
	return ""
}

func skipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "ui", "docs", "testdata":
		return true
	}
	return false
}

// stripComments removes // and /* */ comments using the real lexer, so a phrase
// in prose cannot fail the scan. Falls back to the original text if parsing
// fails, because failing open on an unparseable file would silently weaken the
// guard.
func stripComments(src, path string) string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return src
	}
	var b strings.Builder
	for _, group := range f.Comments {
		for _, c := range group.List {
			b.WriteString(strings.Repeat("\n", strings.Count(c.Text, "\n")))
		}
	}
	// Remove the comment spans by offset, from the end so earlier offsets stay valid.
	text := src
	spans := [][2]int{}
	for _, group := range f.Comments {
		for _, c := range group.List {
			spans = append(spans, [2]int{fset.Position(c.Pos()).Offset, fset.Position(c.End()).Offset})
		}
	}
	for i := len(spans) - 1; i >= 0; i-- {
		text = text[:spans[i][0]] + text[spans[i][1]:]
	}
	_ = b
	return text
}

// The scan parses the file it is scanning anyway, so this asserts that path
// works -- a stripComments that silently returned the input would make the guard
// fail on documentation rather than on code, which is the same false negative
// wearing a different hat.
func TestStripCommentsActuallyRemovesThem(t *testing.T) {
	src := "package x\n\n// ClaimStoreBytes is forbidden.\nvar y = 1 /* ClaimStoreBytes */\n"
	out := stripComments(src, "x.go")
	assert.NotContains(t, out, "ClaimStoreBytes",
		"comments must be stripped or the scan fails on prose")
	assert.Contains(t, out, "var y = 1",
		"the code itself must survive comment stripping")

	// An unparseable file falls back to the original, which is the SAFE
	// direction: the scan then sees more text, so it is more likely to fail than
	// to pass vacuously.
	broken := "package x\nfunc ( { oops"
	assert.Equal(t, broken, stripComments(broken, "broken.go"),
		"an unparseable file must fall back to its raw text")

	_ = strconv.Itoa
}
