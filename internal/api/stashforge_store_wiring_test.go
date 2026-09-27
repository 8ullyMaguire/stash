package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// # WHY THIS FILE EXISTS
//
// StashForge has now hit the same defect FOUR times, and each time the whole
// suite was green:
//
//   1. `collab.ProposalStore` had no implementation anywhere in the tree for a
//      full milestone. M2 was green throughout.
//   2. `collab.ReputationStore` — the same, one package over (M2c).
//   3. The media access gate (M4 step 4.3) was implemented, tested, and
//      unreachable: `library_id` was on no table and no request carried a user
//      id, so nothing could call it. "Referenced != used."
//   4. `sqlite.ConsentStore` was fully implemented and fully tested, and
//      `NewConsentStore` appeared in exactly one place in the tree: its own
//      constructor. Nothing built it, so nothing could reach it, so every test
//      of it was a test of a function nothing calls.
//
// In every case the code referenced the thing perfectly, the tests of the thing
// passed, and the product did not do it. A test suite proves that a function
// works; it says nothing about whether any caller exists.
//
// # THE CHECK
//
// For every constructor in the StashForge stores: it must be CALLED from a
// non-test file. A constructor called only from its own definition is a component
// with no wiring, and it is the single cheapest indicator of the whole family —
// it needs no call-graph analysis, only a grep, and it is wrong in a way no
// amount of passing tests will ever report.

// storeConstructor finds `func New<Type>Store(` in the StashForge store files.
//
// Scoped to the store files rather than the whole tree because the rule is about
// the StashForge collaboration stores specifically: upstream's own constructors
// are wired by upstream, and a general version of this test would flag code
// nobody is proposing to change.
var storeConstructor = regexp.MustCompile(`(?m)^func (New[A-Za-z]*Store)\(`)

// wiredCall matches a call to a constructor: `NewFooStore(`.
var wiredCall = regexp.MustCompile(`\bNew[A-Za-z]*Store\(`)

func TestStashForgeStoreConstructorsAreActuallyWired(t *testing.T) {
	const storeDir = "../../pkg/sqlite"

	entries, err := os.ReadDir(storeDir)
	if err != nil {
		t.Fatalf("reading %s: %v", storeDir, err)
	}

	// Every non-test .go file in the REPO, so a constructor wired from
	// pkg/sqlite/database.go or internal/manager/init.go both count. Read once:
	// the file list is needed for every constructor and re-walking the tree per
	// constructor is the slow version of a check that should be instant.
	//
	// The root is the repository, not `..`. The first version walked `..`, which
	// from internal/api is internal/ -- so it never saw pkg/sqlite at all, where
	// every upstream store is actually constructed. It reported all 30
	// constructors as unwired, which is the shape of a check that has found
	// nothing and is shouting about it. A check that fails on everything on its
	// first run is a check whose SCOPE is wrong, not one that has found 30 bugs.
	sources := readNonTestSources(t, "../..")

	var checked int
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") {
			continue
		}

		path := filepath.Join(storeDir, name)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}

		for _, m := range storeConstructor.FindAllStringSubmatch(string(body), -1) {
			ctor := m[1]
			checked++

			if !isWired(sources, ctor) {
				t.Errorf("%s defines %s, and NO non-test file anywhere in the tree "+
					"calls it.\n\n"+
					"  This is not a store that is switched off -- it is a store "+
					"nothing can reach, so every test of it is testing a function "+
					"no product code calls. The tests pass, the schema exists, and "+
					"the feature does nothing.\n\n"+
					"  This has happened four times in this project "+
					"(ProposalStore, ReputationStore, the media gate, ConsentStore). "+
					"  Fix: build it in internal/manager/init.go beside the other "+
					"StashForge stores, after Database.Open, and add the field to "+
					"the Manager struct.", name, ctor)
			}
		}
	}

	if checked == 0 {
		t.Fatal("no store constructors matched. The regex is wrong, so this test " +
			"is passing vacuously — a store that is unwired would not be reported")
	}
	t.Logf("checked %d store constructors", checked)
}

// isWired reports whether any non-test source CALLS the constructor.
//
// The subtlety, and the reason this is not a one-line `bytes.Contains`: a
// constructor's own DEFINITION line is `func NewFooStore(`, which matches a
// search for `NewFooStore(`. A naive check therefore finds the definition and
// concludes every store is wired — which is a test that passes on precisely the
// bug it was written for.
func isWired(sources map[string]string, ctor string) bool {
	// Word-bounded, so NewMediaScopeStore does not appear to wire
	// NewMediaScope. A prefix match here would make an unwired store look
	// wired whenever a longer sibling name is called — which is exactly the
	// failure this test exists to prevent, reproduced inside the test.
	call := regexp.MustCompile(`\b` + regexp.QuoteMeta(ctor) + `\(`)

	for _, body := range sources {
		if countCalls(call, body) > 0 {
			return true
		}
	}
	return false
}

// countCalls counts real CALLS of the pattern, ignoring definition lines.
//
// A line beginning with `func` is the definition; anything else matching
// `NewFooStore(` is somebody using it. That is a heuristic, and it is the right
// one for this codebase: a call cannot start a line with `func`, and a
// definition always does.
//
// It also has to count the STRUCT LITERAL form, which is how upstream's stores
// are wired:
//
//	SceneMarker: NewSceneMarkerStore(),
//
// The first version of this skipped any line whose trimmed text started with an
// identifier followed by a colon, reasoning that it was a struct field and not a
// call. That flagged NewSceneMarkerStore, NewStudioStore, NewTagStore and two
// others as unwired — all of which are wired, in database.go. A check that
// reports four false positives on the first run gets ignored, and then it is not
// a check.
func countCalls(re *regexp.Regexp, body string) int {
	n := 0
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "func ") {
			continue
		}
		if re.MatchString(line) {
			n++
		}
	}
	return n
}

// readNonTestSources reads every non-test .go file under root, keyed by path.
//
// Test files are excluded because the whole point is to find what PRODUCT code
// calls; a constructor called from a test is exactly the case where a feature has
// a thorough test suite and no users.
func readNonTestSources(t *testing.T, root string) map[string]string {
	t.Helper()

	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // an unreadable directory is not this test's business
		}
		if info.IsDir() {
			// node_modules and .git are large and contain no Go.
			base := filepath.Base(path)
			if base == "node_modules" || base == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		out[path] = string(body)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if len(out) == 0 {
		t.Fatal("no non-test Go sources found. The walk found nothing, so this " +
			"test would report every constructor as unwired — or, if it somehow " +
			"does not, it is not looking at the tree at all")
	}
	return out
}
