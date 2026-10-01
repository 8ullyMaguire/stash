package library

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// # WHY THIS FILE EXISTS
//
// The plan's exit criterion for M5 is a plugin that can get a file "scanned and
// linked" — and the whole architecture of step 5.0 is that the plugin reaches
// the host only through its documented API. This is the test that makes that
// architectural claim into a fact, and it is the reason the phrase "through the
// plugin API only" is in the plan at all.
//
// It is a boundary test, not a behaviour test: the behaviour is in the other
// files, and this one asks a single question — can this package reach the core
// by any path at all?
//
// # WHY A SOURCE SCAN AND NOT ONLY AN IMPORT GRAPH
//
// `go list -deps` is the right first question but not the only one, and the gap
// is worth naming:
//
//   - It reports the BUILD graph. A file under a build tag this platform does
//     not select is not in the graph, and a plugin that imports the core under
//     `//go:build windows` is in the source and out of the answer.
//   - It reports the graph for THIS checkout on THIS platform. A CI run on a
//     different OS, a `//go:build` file, or a generated file excluded by
//     constraints is a different answer from the one CI computed.
//   - It says nothing about what the code is DOING. A package that shells out to
//     a `stash` binary, reads `stash.db` with a SQLite driver, or writes a scan
//     row over HTTP to an undocumented endpoint has no core import and is
//     violating the milestone anyway.
//
// So this test does all three, and the third is the one that catches the
// failure the import graph cannot.

// pluginModuleRoot is the plugin's own go.mod, found by walking up from this
// file.
//
// Found by WALKING rather than hard-coded, because a hard-coded relative path
// breaks the moment a test runs from a different working directory, and a
// boundary test that silently skips itself when the path does not resolve is
// worse than no test: it reports a boundary that is not there.
func pluginModuleRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test's own source file")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s, so this is not inside the plugin module", dir)
		}
		dir = parent
	}
}

// TestP2PDownloaderLibraryIntegrationUsesNoCoreImports is the test the plan
// names, and the one that decides whether step 5.5 was done correctly.
//
// A core import in this package is not a style problem. The plugin is a separate
// MODULE, so a `github.com/stashapp/stash/...` import here either fails the
// build outright or — worse — succeeds via a `replace` directive, and either way
// the downloader is no longer removable by deleting a directory, which is the
// entire premise of M5.
func TestP2PDownloaderLibraryIntegrationUsesNoCoreImports(t *testing.T) {
	root := pluginModuleRoot(t)

	// 1. The dependency graph, on this platform.
	r := exec.Command("go", "list", "-deps", "./...")
	r.Dir = root
	r.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	out, err := r.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps failed: %v\n%s", err, out)
	}
	var core []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "github.com/stashapp/stash/") &&
			!strings.HasPrefix(line, "github.com/stashapp/stash-plugin-p2pdownloader/") {
			core = append(core, line)
		}
	}
	if len(core) > 0 {
		t.Errorf("the plugin module depends on the core:\n  %s\n"+
			"Step 5.0's whole claim is that the downloader is removable by "+
			"deleting a directory, and a core dependency is the end of that "+
			"claim", strings.Join(core, "\n  "))
	}

	// 2. The SOURCE, which is a wider question than the build graph.
	//
	// Every .go file under the module, build tags included, and the test files
	// too: a test that imports the core is a test that cannot run in CI where
	// the core is not on the module path, and one that only compiles in the
	// author's checkout is a boundary that exists for the author.
	var sources []string
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if name := info.Name(); name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			sources = append(sources, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module: %v", err)
	}
	if len(sources) == 0 {
		t.Fatal("found no Go sources, so this test is not looking at anything")
	}

	for _, path := range sources {
		rel, _ := filepath.Rel(root, path)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("reading %s: %v", rel, err)
			continue
		}
		// Only import lines, and only the real module path. A mention of the
		// core in a COMMENT is documentation — this file names the core
		// repeatedly — and grepping the whole file would make the test fail on
		// its own explanation.
		for _, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "import") &&
				!strings.HasPrefix(trimmed, `"github.com/stashapp/stash/`) {
				continue
			}
			if strings.Contains(trimmed, "stash-plugin-p2pdownloader") {
				continue
			}
			if strings.Contains(trimmed, "github.com/stashapp/stash/") {
				t.Errorf("%s imports the core:\n\t%s\n"+
					"The plugin is a separate module. An import here ends M5's "+
					"central claim, whether it fails the build or succeeds "+
					"through a replace directive", rel, trimmed)
			}
		}
	}
}

// TestTheLibraryIntegrationReachesTheHostOnlyOverHTTP is the third question,
// and the one the import graph cannot ask.
//
// A plugin with no core imports can still be a plugin that pokes the host's
// database directly — open `stash.db`, INSERT a row into `scenes`, and the
// milestone's "let the normal scanner find it" is satisfied by nothing at all
// while every import test passes. That is the failure the plan's words "do not
// hand-insert scan rows, do not call the scanner's Go API, do not write to
// `files`" are aimed at, and it deserves its own assertion rather than a
// comment.
//
// So: no source file in this module may name a core database, and no source
// file may shell out to a host binary.
func TestTheLibraryIntegrationReachesTheHostOnlyOverHTTP(t *testing.T) {
	root := pluginModuleRoot(t)

	// Things that would let a plugin reach into the host without importing it.
	// Each is a specific, checkable string rather than a category, because a
	// category is a judgement and a string is a fact.
	forbidden := []struct{ needle, why string }{
		{"stash.db", "the host's SQLite database. Writing a scan row into it " +
			"by hand is the thing step 5.5 exists NOT to do, and it satisfies " +
			"\"scanned and linked\" while nothing scanned anything"},
		{"sqlite3", "a SQLite driver. Same reason: the plugin has no business " +
			"opening the host's storage"},
		{"pkg/sqlite", "the core's own database package, by path rather than by " +
			"import, so this catches a reference in a string as well as in code"},
		{"internal/manager", "the core's task manager. `ScanJob` is a type the " +
			"plugin would be constructing by hand, which is calling the " +
			"scanner's Go API"},
		{"exec.Command(\"stash", "shelling out to a host binary. The plugin " +
			"talks to the host over its API; a subprocess is not that"},
		{"exec.Command(\"stash-box", "same, for the other host"},
	}

	var sources []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if err == nil && info != nil && info.IsDir() &&
				(info.Name() == ".git" || info.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			sources = append(sources, path)
		}
		return nil
	})

	// THIS FILE IS EXCLUDED, and that is not a convenience.
	//
	// The list above has to exist somewhere, and a guard that scans the file
	// holding its own list fails on that list. It is the second time in this
	// plugin: the magnet-path grep failed for exactly the same reason, on
	// `.AddMagnet(`, which matched this package's own safe `AddMagnet`. The
	// general rule, learned twice — **a check that fails the moment you write
	// the thing it asks for is a check that gets deleted.**
	//
	// Excluding the whole file rather than only its list is the safer half of
	// the two options, and it is the one that cannot rot: a new forbidden
	// string added here is automatically exempt, where exempting individual
	// lines would need updating in the same edit.
	self := "internal/library/boundary_test.go"

	for _, path := range sources {
		rel, _ := filepath.Rel(root, path)
		if rel == self {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue // documentation naming them is the point
			}
			for _, f := range forbidden {
				if strings.Contains(line, f.needle) {
					t.Errorf("%s references %q: %s", rel, f.needle, f.why)
				}
			}
			_ = trimmed
		}
	}
}

// TestTheHandOffUsesTheHostsOwnScanAndNotAFingerprintOfItsOwn is the design
// claim, asserted.
//
// The plan allows either: link by phasher/osher through the host's query
// surface, or let the scanner's own fingerprint match do it. This package chose
// the second, which means the plugin must NOT compute a fingerprint and must not
// try to attach a scene to a file itself.
//
// The test is narrow on purpose — it checks this package, because a hash
// implementation elsewhere in the plugin would be a different package's problem
// and a test that scanned the whole module would report it here.
func TestTheHandOffUsesTheHostsOwnScanAndNotAFingerprintOfItsOwn(t *testing.T) {
	root := pluginModuleRoot(t)
	dir := filepath.Join(root, "internal", "library")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading the library package: %v", err)
	}
	// Go sources only, and THIS FILE excluded for the reason given above: the
	// list of forbidden words is in it. The first version of this test swept
	// every file in the directory and failed on the mutation harness, which
	// names `phash` in a row label describing what the harness checks.
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") ||
			strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Errorf("reading %s: %v", e.Name(), err)
			continue
		}
		for _, forbidden := range []string{"crypto/sha1", "crypto/md5", "crypto/sha256",
			"perceptual", "phash", "OsHash", "osHash", "fingerprint"} {
			for _, line := range strings.Split(string(raw), "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "//") {
					continue
				}
				if strings.Contains(line, forbidden) {
					t.Errorf("%s references %q. The plugin asks the HOST to "+
						"fingerprint and link; computing a fingerprint here would "+
						"mean a second, divergent implementation of matching, and "+
						"the whole point is that the host owns that decision",
						e.Name(), forbidden)
				}
			}
		}
	}
}
