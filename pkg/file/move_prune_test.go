package file

import (
	"os"
	"path/filepath"
	"testing"
)

// stash#5631 -- moveFiles should optionally clean up empty directories.
//
// This is destructive and irreversible code, so the tests are weighted towards what it must NOT do.
// The containment check (canPrune) is the load-bearing part: a prefix bug there deletes user
// directories, and no happy-path test would notice.

// tempLibrary builds a library root with the given relative directory structure, and returns the
// absolute root plus a cleanup.
func tempLibrary(t *testing.T, dirs ...string) string {
	t.Helper()

	root := t.TempDir()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	return root
}

// moverAt builds a Mover rooted at the given library paths, with no file or folder store. The pruning
// code touches neither -- an emptied directory has no folder row left to update.
func moverAt(roots ...string) *Mover {
	return &Mover{
		rootPaths:      roots,
		pruneEmptyDirs: true,
	}
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

func TestPruneRemovesADirectoryLeftEmpty(t *testing.T) {
	root := tempLibrary(t, "empty-dir")
	if err := os.MkdirAll(filepath.Join(root, "keeps"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "keeps", "a.mp4"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := moverAt(root)
	m.noteEmptiedDir(filepath.Join(root, "empty-dir", "gone.mp4"))
	m.pruneEmptiedDirs()

	if exists(t, filepath.Join(root, "empty-dir")) {
		t.Error("an empty directory left by a move should have been pruned")
	}
	if !exists(t, filepath.Join(root, "keeps")) {
		t.Error("a directory that still holds a file must not be touched")
	}
}

func TestPruneKeepsADirectoryThatStillHasFiles(t *testing.T) {
	root := tempLibrary(t, "src")
	if err := os.WriteFile(filepath.Join(root, "src", "still-here.mp4"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := moverAt(root)
	// Recorded because ONE file left this directory -- but another remains, so it is not empty.
	m.noteEmptiedDir(filepath.Join(root, "src", "moved-away.mp4"))
	m.pruneEmptiedDirs()

	if !exists(t, filepath.Join(root, "src")) {
		t.Error("a directory with a remaining file must not be pruned")
	}
}

func TestPruneWalksUpThroughNestedEmpties(t *testing.T) {
	// The case that needs the walk-up: the last file in a/b/c moves out, so c is empty AND then b is.
	// A single-level check would remove c and leave b stranded.
	root := tempLibrary(t, "a/b/c")

	m := moverAt(root)
	m.noteEmptiedDir(filepath.Join(root, "a", "b", "c", "gone.mp4"))
	m.pruneEmptiedDirs()

	for _, d := range []string{"a/b/c", "a/b", "a"} {
		if exists(t, filepath.Join(root, d)) {
			t.Errorf("%s should have been pruned once the whole chain was empty", d)
		}
	}
}

func TestPruneStopsAtTheFirstNonEmptyAncestor(t *testing.T) {
	// a/b is emptied, but a still holds a file. a must survive, and so must the library root.
	root := tempLibrary(t, "a/b")
	if err := os.WriteFile(filepath.Join(root, "a", "keep.mp4"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := moverAt(root)
	m.noteEmptiedDir(filepath.Join(root, "a", "b", "gone.mp4"))
	m.pruneEmptiedDirs()

	if exists(t, filepath.Join(root, "a", "b")) {
		t.Error("the emptied leaf should have been pruned")
	}
	if !exists(t, filepath.Join(root, "a")) {
		t.Error("an ancestor that still holds a file must stop the walk")
	}
	if !exists(t, root) {
		t.Fatal("the library root must never be removed")
	}
}

func TestPruneNeverRemovesALibraryRoot(t *testing.T) {
	// The regression this guards: pruning the last empty subdirectory walks straight into the root,
	// and removing the library directory itself would be catastrophic.
	root := tempLibrary(t, "only")

	m := moverAt(root)
	m.noteEmptiedDir(filepath.Join(root, "only", "gone.mp4"))
	m.pruneEmptiedDirs()

	if !exists(t, root) {
		t.Fatal("the library root must never be pruned, even when everything under it is empty")
	}
	if exists(t, filepath.Join(root, "only")) {
		t.Error("the empty subdirectory should still have been pruned")
	}
}

func TestPruneRefusesPathsOutsideTheLibrary(t *testing.T) {
	// The containment check. A sibling directory whose name shares a PREFIX with the library must be
	// refused: strings.HasPrefix("/lib2", "/lib") is true, and that is the bug this test exists for.
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	other := filepath.Join(base, "lib2")
	for _, d := range []string{lib, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	m := moverAt(lib)

	// The sibling shares the prefix but is not under the root.
	if m.canPrune(other) {
		t.Error("a directory sharing only a PREFIX with the library root must not be prunable")
	}

	// Somewhere else entirely.
	if m.canPrune(filepath.Join(base, "unrelated")) {
		t.Error("a directory outside every library root must not be prunable")
	}

	// The root itself.
	if m.canPrune(lib) {
		t.Error("the library root must not be prunable")
	}

	// A genuine descendant is fine, or the feature does nothing.
	if !m.canPrune(filepath.Join(lib, "a", "b")) {
		t.Error("a real descendant of the library must be prunable")
	}
}

func TestPruneRefusesTraversalOutOfTheLibrary(t *testing.T) {
	// filepath.Clean is applied before the comparison, so a path containing ".." cannot smuggle a
	// prefix past the containment check and escape upward.
	root := tempLibrary(t, "a")

	m := moverAt(root)

	if m.canPrune(filepath.Join(root, "..", "elsewhere")) {
		t.Error("a path traversing above the library root must not be prunable")
	}
	if m.canPrune(filepath.Clean(root + "/a/../../..")) {
		t.Error("a cleaned path that escapes the root must not be prunable")
	}
}

func TestPruneRefusesTheFilesystemRoot(t *testing.T) {
	// filepath.Dir walks up to "/" eventually; removing that is not a recoverable outcome.
	m := moverAt("/")

	if m.canPrune("/") {
		t.Error("the filesystem root must never be prunable")
	}
	if m.canPrune(".") {
		t.Error("the current directory must never be prunable")
	}
}

func TestPruneIsOffByDefault(t *testing.T) {
	// The default is the safety property. A Mover constructed the ordinary way -- as NewMover does,
	// with nobody calling SetPruneEmptyDirs -- must not prune.
	root := tempLibrary(t, "src")

	m := &Mover{rootPaths: []string{root}}
	m.noteEmptiedDir(filepath.Join(root, "src", "gone.mp4"))
	m.pruneEmptiedDirs()

	if !exists(t, filepath.Join(root, "src")) {
		t.Fatal("pruning must be off unless it was explicitly enabled")
	}
}

func TestNoteEmptiedDirIgnoresTopLevelPaths(t *testing.T) {
	// A file directly in the library root has no prunable parent; recording "/" or "." would hand the
	// walk-up something dangerous to start from.
	m := moverAt("/library")

	m.noteEmptiedDir("/library")
	m.noteEmptiedDir("file.mp4")
	m.noteEmptiedDir("/")

	if len(m.emptiedDirs) != 0 {
		t.Errorf("nothing should have been recorded for top-level paths, got %v", m.emptiedDirs)
	}
}

func TestSortByDepthDescOrdersDeepestFirst(t *testing.T) {
	// Ordering by STRING LENGTH rather than depth is a plausible-looking bug: /a/bb is one segment
	// deeper than /a/bbbb despite being shorter, and length order would strand /a/bbbb.
	paths := []string{"/a", "/a/bbbb", "/a/bb", "/a/b/c"}
	sortByDepthDesc(paths)

	want := []string{"/a/b/c", "/a/bb", "/a/bbbb", "/a"}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("order = %v, want %v", paths, want)
		}
	}
}

func TestPruneHandlesTwoFilesLeavingTheSameDirectory(t *testing.T) {
	// The realistic case: a move of several files out of one directory records it once, and it is
	// pruned only if ALL of them left.
	root := tempLibrary(t, "src")

	m := moverAt(root)
	m.noteEmptiedDir(filepath.Join(root, "src", "a.mp4"))
	m.noteEmptiedDir(filepath.Join(root, "src", "b.mp4"))
	m.pruneEmptiedDirs()

	if exists(t, filepath.Join(root, "src")) {
		t.Error("the directory should be pruned once every file has left it")
	}
}
