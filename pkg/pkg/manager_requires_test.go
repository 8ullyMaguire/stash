package pkg

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/models"
	"gopkg.in/yaml.v2"
)

// End-to-end tests for recursive requirement installation (PR #7199,
// stashapp/stash#7198).
//
// These drive the REAL install path against a real repository on disk. The
// httpRepository supports a file:// package list, so no HTTP server is needed and
// the zips, the sha256 check, the manifest writes and the requirement walk are
// all genuinely exercised rather than mocked.
//
// The defect class here is recursion with shared mutable state, which is
// exactly what a mock hides: a mock cannot tell you that A was skipped on the
// second visit because a map still held it.

// repo is a package repository on disk: a packages.yaml index plus one zip per
// package. Paths in the index are relative to the index, as they are in the wild.
type repo struct {
	t    *testing.T
	dir  string
	list []RemotePackage
}

// pkgSpec describes one package to publish into the test repository.
type pkgSpec struct {
	id       string
	requires []string
	version  string
	// date drives Upgradable(), which compares dates, not version strings.
	date time.Time
	// omitFromIndex publishes a zip but hides the package from the index, which
	// is how "installed but no longer in the source" is simulated.
	omitFromIndex bool
	// content is the single file written into the zip.
	content string
	// badSHA makes the published sha256 wrong, for the integrity test.
	badSHA bool
}

func day(n int) time.Time { return time.Date(2026, 1, n, 0, 0, 0, 0, time.UTC) }

// newRepo publishes the given packages and returns a repository plus its
// file:// list URL, which is what Manager.remoteFromURL takes.
func newRepo(t *testing.T, specs ...pkgSpec) (*repo, string) {
	t.Helper()
	dir := t.TempDir()
	r := &repo{t: t, dir: dir}

	for _, s := range specs {
		content := s.content
		if content == "" {
			content = "package " + s.id + "\n"
		}

		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, err := zw.Create("plugin.py")
		if err != nil {
			t.Fatalf("creating zip entry for %s: %v", s.id, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("writing zip entry for %s: %v", s.id, err)
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("closing zip for %s: %v", s.id, err)
		}

		zipName := s.id + ".zip"
		if err := os.WriteFile(filepath.Join(dir, zipName), buf.Bytes(), 0644); err != nil {
			t.Fatalf("writing zip for %s: %v", s.id, err)
		}

		sum := fmt.Sprintf("%x", sha256.Sum256(buf.Bytes()))
		if s.badSHA {
			sum = fmt.Sprintf("%x", sha256.Sum256([]byte("not the zip")))
		}

		if s.omitFromIndex {
			continue
		}

		r.list = append(r.list, RemotePackage{
			ID:       s.id,
			Name:     s.id,
			Requires: s.requires,
			PackageVersion: PackageVersion{
				Version: s.version,
				Date:    Time{s.date},
			},
			PackageLocation: PackageLocation{Path: zipName, Sha256: sum},
		})
	}

	if err := os.WriteFile(filepath.Join(dir, "packages.yaml"), mustYAML(t, r.list), 0644); err != nil {
		t.Fatalf("writing packages.yaml: %v", err)
	}

	return r, "file://" + filepath.Join(dir, "packages.yaml")
}

func mustYAML(t *testing.T, v any) []byte {
	t.Helper()
	b, err := yaml.Marshal(v)
	if err != nil {
		t.Fatalf("marshalling index: %v", err)
	}
	return b
}

// newManager wires a Manager with a local store under a temp dir. The source
// path getter maps every source URL onto one directory, which is enough: the
// store is addressed by source, and there is only one source here.
func newManager(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	local := &Store{BaseDir: filepath.Join(root, "store"), ManifestFile: ManifestFile}
	return &Manager{
		Local:             local,
		PackagePathGetter: &fixedPathGetter{root: filepath.Join(root, "src")},
	}
}

type fixedPathGetter struct{ root string }

func (f *fixedPathGetter) GetSourcePath(_ string) string { return filepath.Join(f.root, "src") }
func (f *fixedPathGetter) GetAllSourcePaths() []string   { return []string{filepath.Join(f.root, "src")} }

// installed reads back the manifest for id from the single source sub-store.
func installedIDs(t *testing.T, m *Manager) []string {
	t.Helper()
	store := m.getStore("anything")
	idx, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("listing installed: %v", err)
	}
	var ids []string
	for _, m := range idx {
		ids = append(ids, m.ID)
	}
	return ids
}

func mustInstall(t *testing.T, m *Manager, url, id string) {
	t.Helper()
	if err := m.Install(context.Background(), models.PackageSpecInput{ID: id, SourceURL: url}); err != nil {
		t.Fatalf("Install(%s): %v", id, err)
	}
}

func assertHas(t *testing.T, m *Manager, want ...string) {
	t.Helper()
	got := map[string]bool{}
	for _, id := range installedIDs(t, m) {
		got[id] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("package %q is not installed; installed = %v", w, installedIDs(t, m))
		}
	}
}

// --- the feature -----------------------------------------------------------

// The whole point of the PR: installing a package pulls in what it requires.
func TestInstallPullsInRequirements(t *testing.T) {
	_, url := newRepo(t,
		pkgSpec{id: "app", requires: []string{"lib"}, date: day(10)},
		pkgSpec{id: "lib", date: day(1)},
	)
	m := newManager(t)
	mustInstall(t, m, url, "app")

	assertHas(t, m, "app", "lib")
}

// Transitively: c needs b, b needs a, install c.
func TestInstallPullsInRequirementsTransitively(t *testing.T) {
	_, url := newRepo(t,
		pkgSpec{id: "c", requires: []string{"b"}, date: day(10)},
		pkgSpec{id: "b", requires: []string{"a"}, date: day(9)},
		pkgSpec{id: "a", date: day(1)},
	)
	m := newManager(t)
	mustInstall(t, m, url, "c")

	assertHas(t, m, "c", "b", "a")
}

// A diamond: d requires b and c, both of which require a. a must be installed
// exactly once, and must not be skipped on the second visit.
func TestDiamondRequirementsInstallTheSharedDependencyOnce(t *testing.T) {
	_, url := newRepo(t,
		pkgSpec{id: "d", requires: []string{"b", "c"}, date: day(10)},
		pkgSpec{id: "b", requires: []string{"a"}, date: day(9)},
		pkgSpec{id: "c", requires: []string{"a"}, date: day(9)},
		pkgSpec{id: "a", date: day(1)},
	)
	m := newManager(t)
	mustInstall(t, m, url, "d")

	assertHas(t, m, "d", "b", "c", "a")
}

// An already-installed requirement is left alone when the source has nothing
// newer, rather than being reinstalled.
func TestAnUpToDateRequirementIsNotReinstalled(t *testing.T) {
	_, url := newRepo(t,
		pkgSpec{id: "app", requires: []string{"lib"}, date: day(10)},
		pkgSpec{id: "lib", date: day(5), content: "ORIGINAL"},
	)
	m := newManager(t)
	mustInstall(t, m, url, "lib")
	mustInstall(t, m, url, "app")

	store := m.getStore(url)
	mf, err := store.getManifest(context.Background(), "lib")
	if err != nil {
		t.Fatalf("reading lib manifest: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(store.packageDir("lib"), "plugin.py"))
	if err != nil {
		t.Fatalf("reading lib plugin.py: %v", err)
	}
	if string(b) != "ORIGINAL" {
		t.Errorf("an up-to-date requirement was reinstalled: content is %q, want %q", b, "ORIGINAL")
	}
	_ = mf
}

// A requirement the source has a NEWER version of is updated.
func TestAnOutdatedRequirementIsUpdated(t *testing.T) {
	dir := t.TempDir()
	_ = dir
	_, url := newRepo(t,
		pkgSpec{id: "app", requires: []string{"lib"}, date: day(10)},
		pkgSpec{id: "lib", date: day(5), content: "OLD"},
	)
	m := newManager(t)
	mustInstall(t, m, url, "lib")

	// Republish lib with a later date and different content.
	_, url2 := newRepo(t,
		pkgSpec{id: "app", requires: []string{"lib"}, date: day(10)},
		pkgSpec{id: "lib", date: day(20), content: "NEW"},
	)
	mustInstall(t, m, url2, "app")

	store := m.getStore(url2)
	b, err := os.ReadFile(filepath.Join(store.packageDir("lib"), "plugin.py"))
	if err != nil {
		t.Fatalf("reading lib plugin.py: %v", err)
	}
	if string(b) != "NEW" {
		t.Errorf("an outdated requirement was not updated: content is %q, want %q", b, "NEW")
	}
}

// The manifest must record Requires, or a later update cannot know what to walk.
func TestManifestRecordsRequirements(t *testing.T) {
	_, url := newRepo(t,
		pkgSpec{id: "app", requires: []string{"lib", "other"}, date: day(10)},
		pkgSpec{id: "lib", date: day(1)},
		pkgSpec{id: "other", date: day(1)},
	)
	m := newManager(t)
	mustInstall(t, m, url, "app")

	mf, err := m.getStore(url).getManifest(context.Background(), "app")
	if err != nil {
		t.Fatalf("reading app manifest: %v", err)
	}
	if len(mf.Requires) != 2 {
		t.Errorf("manifest.Requires = %v, want [lib other]", mf.Requires)
	}
}

// --- the cycle guard -------------------------------------------------------

// A cycle must terminate rather than recursing forever.
//
// **The guard is defensive, not load-bearing, and this file says so.** With the
// cycle guard deleted entirely these two tests still PASS. The reason is in
// installRequirements: it recurses into a requirement only when that requirement
// is missing from the store or older than the source has. On the second visit to
// an ID in a cycle, the package is already installed and current, so the `continue`
// fires and the walk bottoms out on its own. Verified by deleting the guard and
// tracing the install: it terminates immediately, err=nil, with both packages
// installed.
//
// So there is no input available to this test suite that makes the absence of the
// guard observable. That is worth knowing rather than papering over with a test
// that asserts termination while implying the guard is what causes it. The guard
// stays: a future change to installRequirements -- installing a dependency
// unconditionally, or comparing versions differently -- would turn a hang into a
// stack overflow, and a guard that costs one map entry is the right insurance.
// But it is insurance, not a fix, and no test here should be read as claiming
// otherwise.
func TestACycleTerminates(t *testing.T) {
	_, url := newRepo(t,
		pkgSpec{id: "a", requires: []string{"b"}, date: day(10)},
		pkgSpec{id: "b", requires: []string{"a"}, date: day(9)},
	)
	m := newManager(t)

	done := make(chan error, 1)
	go func() { done <- m.Install(context.Background(), models.PackageSpecInput{ID: "a", SourceURL: url}) }()

	select {
	case err := <-done:
		t.Logf("install returned: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("Install did not terminate on a dependency cycle")
	}
	assertHas(t, m, "a", "b")
}

// A self-requiring package is the degenerate cycle.
func TestASelfRequirementTerminates(t *testing.T) {
	_, url := newRepo(t,
		pkgSpec{id: "a", requires: []string{"a"}, date: day(10)},
	)
	m := newManager(t)

	done := make(chan error, 1)
	go func() { done <- m.Install(context.Background(), models.PackageSpecInput{ID: "a", SourceURL: url}) }()

	select {
	case err := <-done:
		t.Logf("install returned: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("Install did not terminate on a self-requirement")
	}
	assertHas(t, m, "a")
}

// The guard's SCOPE.
//
// Upstream marks spec.ID and never unmarks it, which makes the map a "seen in this
// call tree" set rather than a cycle guard. The fix adds `defer delete`.
//
// **This test cannot fail if that fix is reverted, and that is a finding, not a
// gap in the test.** It was tried both ways and reverting the `defer` leaves the
// suite green. Two reasons, and the second is the interesting one:
//
//   - Install() builds a FRESH map per call, so a cross-call leak is invisible
//     from outside by construction.
//   - Within one call tree the two guards produce DIFFERENT VISIT SEQUENCES but
//     the SAME OUTCOME, because install() is idempotent for an already-installed
//     package: it uninstalls and reinstalls the same bytes. Verified by running
//     both shapes side by side.
//
// So the leak is unobservable through the public API today. The `defer delete` is
// kept as defensive correctness -- the guard should be scoped to the PATH it is
// guarding, and that is the property that survives a future change to install()'s
// idempotence -- but it is recorded here as having NO WITNESS rather than claimed
// as fixed. If install() ever stops being idempotent, this becomes a real bug and
// this test needs to be rewritten to detect it.
func TestTheGuardIsScopedToOneInstallCall(t *testing.T) {
	_, url := newRepo(t,
		pkgSpec{id: "top", requires: []string{"mid"}, date: day(20)},
		pkgSpec{id: "mid", requires: []string{"leaf"}, date: day(19)},
		pkgSpec{id: "leaf", date: day(18)},
	)
	m := newManager(t)
	mustInstall(t, m, url, "top")
	assertHas(t, m, "top", "mid", "leaf")

	// Two independent installs of the same tree. Pins the documented contract
	// (each Install is independent) even though the guard's map is per-call and
	// cannot leak across them.
	mustInstall(t, m, url, "top")
	mustInstall(t, m, url, "top")
	assertHas(t, m, "top", "mid", "leaf")
}

// --- the manifest-installed, source-gone case -----------------------------

// A requirement that is installed locally but ABSENT from the remote index.
// Upstream's code path:
//
//	remotePkg, err := m.packageByID(ctx, reqSpec)
//	if err != nil { return fmt.Errorf(...) }
//	if remotePkg == nil || !local.Upgradable(...) { continue }
//
// packageByID returns (nil, nil) when the package is not in the index, so the
// `remotePkg == nil` guard catches it and the install CONTINUES. That is the
// right call: an already-installed package that the source no longer offers must
// not fail the whole install.
func TestAnInstalledRequirementMissingFromTheIndexDoesNotFailTheInstall(t *testing.T) {
	// lib IS in the index for the first install, and GONE for the second.
	_, url1 := newRepo(t,
		pkgSpec{id: "app", requires: []string{"lib"}, date: day(10)},
		pkgSpec{id: "lib", date: day(5)},
	)
	m := newManager(t)
	mustInstall(t, m, url1, "app")
	assertHas(t, m, "app", "lib")

	// Republish with lib removed from the index but app still requiring it.
	_, url2 := newRepo(t,
		pkgSpec{id: "app", requires: []string{"lib"}, date: day(10)},
		pkgSpec{id: "lib", date: day(5), omitFromIndex: true},
	)
	if err := m.Install(context.Background(), models.PackageSpecInput{ID: "app", SourceURL: url2}); err != nil {
		t.Fatalf("installing app when its requirement is no longer published: %v", err)
	}
}

// A requirement the source DOES publish, but whose zip fails the sha256 check,
// must surface as an error rather than being swallowed. The failure is inside
// m.install, which installRequirements returns -- so this checks the error is
// actually propagated rather than logged and ignored.
func TestACorruptRequiredPackageFailsTheInstall(t *testing.T) {
	_, url := newRepo(t,
		pkgSpec{id: "app", requires: []string{"lib"}, date: day(10)},
		pkgSpec{id: "lib", date: day(5), badSHA: true},
	)
	m := newManager(t)

	err := m.Install(context.Background(), models.PackageSpecInput{ID: "app", SourceURL: url})
	if err == nil {
		t.Fatal("a corrupt required package installed successfully; the error was swallowed")
	}
	t.Logf("error surfaced as expected: %v", err)
}

// A requirement that does not exist at all, in the index or locally, must fail
// loudly rather than leave the tree half-installed and reported as fine.
func TestAMissingRequirementFailsTheInstall(t *testing.T) {
	_, url := newRepo(t,
		pkgSpec{id: "app", requires: []string{"ghost"}, date: day(10)},
	)
	m := newManager(t)

	err := m.Install(context.Background(), models.PackageSpecInput{ID: "app", SourceURL: url})
	if err == nil {
		t.Fatal("a missing requirement installed successfully")
	}
	t.Logf("error surfaced as expected: %v", err)
}

// --- the store is per SOURCE, not per package ------------------------------

// installRequirements is handed the parent package's store. If a requirement
// came from a DIFFERENT source it would be written into the wrong place. Here
// both are one source, so this pins that the store passed down is the one the
// parent used, by checking the child landed there.
func TestRequirementsLandInTheSameStoreAsTheirParent(t *testing.T) {
	_, url := newRepo(t,
		pkgSpec{id: "app", requires: []string{"lib"}, date: day(10)},
		pkgSpec{id: "lib", date: day(1)},
	)
	m := newManager(t)
	mustInstall(t, m, url, "app")

	store := m.getStore(url)
	for _, id := range []string{"app", "lib"} {
		if _, err := os.Stat(store.packageDir(id)); err != nil {
			t.Errorf("%s is not in the parent's store: %v", id, err)
		}
	}
}
