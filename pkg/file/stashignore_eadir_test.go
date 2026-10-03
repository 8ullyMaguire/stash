package file

// stash#3171: Synology creates an `@eaDir` directory beside every video, containing one subfolder
// per video, and the scanner walked all of it into the folders table.
//
// The reporter asked for "no way for the user to turn this off". The answer turns out to be that
// there IS one, and this test exists to establish that on the reporter's exact filename rather
// than on `excluded_dir` -- because "@eaDir" is not an ordinary name. It begins with `@`, which is
// legal in a gitignore pattern, but a plausible implementation could have anchored it, globbed it,
// or treated a leading non-dot character specially. So the mechanism is verified on the real name.
//
// If this test ever fails, the honest response is NOT to hard-code an `@eaDir` special case into the
// scanner. That would be the wrong fix: it would special-case one vendor's NAS metadata in the scan
// path, for every user, forever. `.stashignore` is the user-facing mechanism and it already does
// this job.

import (
	"path/filepath"
	"testing"
)

func TestStashIgnoreExcludesASynologyEaDirTree(t *testing.T) {
	tmpDir := t.TempDir()

	// The shape Synology produces: a real video, an @eaDir beside it, and inside @eaDir one
	// subdirectory per video -- which is where the 100-fold row explosion comes from.
	createTestFile(t, tmpDir, "video1.mp4")
	createTestDir(t, tmpDir, "@eaDir")
	createTestFile(t, tmpDir, "@eaDir/video1.mp4")
	createTestDir(t, tmpDir, "@eaDir/video1")
	createTestFile(t, tmpDir, "@eaDir/video1/video1.mp4")
	createTestDir(t, tmpDir, "sub")
	createTestFile(t, tmpDir, "sub/video2.mp4")
	createTestDir(t, tmpDir, "sub/@eaDir")
	createTestFile(t, tmpDir, "sub/@eaDir/video2.mp4")

	createTestFileWithContent(t, tmpDir, ".stashignore", "@eaDir/\n")

	filter := NewStashIgnoreFilter()
	accepted := walkAndFilter(t, tmpDir, filter)

	for _, p := range accepted {
		if filepath.Base(p) == "@eaDir" || containsEaDir(p) {
			t.Errorf("stash#3171: %q survived an @eaDir/ pattern", p)
		}
	}

	// The real videos must still be there. An exclusion this broad would be worse than the bug:
	// it would silently stop scanning the user's actual library.
	for _, want := range []string{"video1.mp4", "sub/video2.mp4"} {
		if !containsPath(accepted, want) {
			t.Errorf("stash#3171: %q must still be scanned alongside the @eaDir exclusion, got %v",
				want, accepted)
		}
	}
}

// TestStashIgnoreExcludesEaDirWithoutATrailingSlash is the second form a user would write.
//
// The reporter would not know that gitignore distinguishes `@eaDir` from `@eaDir/`, and both are
// natural things to type. If only one works, the answer to the issue is half an answer.
func TestStashIgnoreExcludesEaDirWithoutATrailingSlash(t *testing.T) {
	for _, pattern := range []string{"@eaDir/\n", "@eaDir\n"} {
		tmpDir := t.TempDir()
		createTestFile(t, tmpDir, "video1.mp4")
		createTestDir(t, tmpDir, "@eaDir")
		createTestFile(t, tmpDir, "@eaDir/video1.mp4")
		createTestFileWithContent(t, tmpDir, ".stashignore", pattern)

		accepted := walkAndFilter(t, tmpDir, NewStashIgnoreFilter())
		for _, p := range accepted {
			if containsEaDir(p) {
				t.Errorf("stash#3171: pattern %q left %q scanned", pattern, p)
			}
		}
		if !containsPath(accepted, "video1.mp4") {
			t.Errorf("stash#3171: pattern %q also excluded the real video, got %v", pattern, accepted)
		}
	}
}

// containsEaDir reports whether any path SEGMENT is @eaDir. Comparing the whole path with a
// substring would also match a folder legitimately named "my @eaDir backup", which is a different
// thing from the Synology metadata directory.
func containsEaDir(p string) bool {
	for _, seg := range filepath.SplitList(p) {
		if seg == "@eaDir" {
			return true
		}
	}
	for _, seg := range splitPath(p) {
		if seg == "@eaDir" {
			return true
		}
	}
	return false
}

func splitPath(p string) []string {
	var out []string
	for {
		dir, file := filepath.Split(p)
		if file != "" {
			out = append([]string{file}, out...)
		}
		if dir == "" || dir == "/" {
			return out
		}
		p = filepath.Clean(dir)
		if p == "." {
			return out
		}
	}
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}
