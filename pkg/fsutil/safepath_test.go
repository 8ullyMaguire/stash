package fsutil

import (
	"path/filepath"
	"strings"
	"testing"
)

// stash#7240: zip entry names are joined into the extraction path without
// validating the result stays inside the destination, so an entry named
// "../../../../arbitrary.txt" writes anywhere the process can reach.
//
// These cases are the ones a naive filepath.Clean-based guard misses, which is
// the point: a guard that only rejects the literal string ".." is defeated by
// "a/b/../../../etc", and one that compares joined paths with HasPrefix is
// defeated by a sibling directory sharing a name prefix.
func TestSafeJoinRejectsTraversal(t *testing.T) {
	const base = "/tmp/import"

	cases := []struct {
		name    string
		entry   string
		wantErr bool
	}{
		// The literal attack from the issue body.
		{"issue example", "../../../../arbitrary.txt", true},
		{"single level up", "../evil", true},
		{"hidden in a longer relative path", "a/b/../../../etc/passwd", true},
		{"traversal after a normal-looking prefix", "subdir/../../outside", true},
		{"just ..", "..", true},
		{"dots with no slash after a deep prefix", "a/b/c/../../../../x", true},

		// A sibling directory sharing a name prefix. "/tmp/import-evil" has
		// the string prefix "/tmp/import" but is not inside it. A HasPrefix
		// guard on the joined path passes this through.
		{"sibling directory sharing a prefix", "../import-evil/x", true},

		// Absolute paths. filepath.Join would discard baseDir entirely and
		// return "/etc/passwd".
		{"absolute posix", "/etc/passwd", true},
		{"absolute double slash", "//etc/passwd", true},

		// Legitimate entries must pass, or the guard breaks the feature.
		{"plain file", "scene.mp4", false},
		{"nested file", "extras/behind-the-scenes.mp4", false},
		{"dot-prefixed file", ".hidden", false},
		{"file whose name contains dots", "my..file.mp4", false},
		{"name starting with dots but not traversal", "...leading", false},
		{"current dir reference", "./scene.mp4", false},
		{"inner parent that stays inside", "a/b/../c.mp4", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SafeJoin(base, tc.entry)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("SafeJoin(%q, %q) = %q, want an error: the entry escaped containment",
						base, tc.entry, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("SafeJoin(%q, %q) returned error for a legitimate entry: %v",
					base, tc.entry, err)
			}
			// A permitted entry must actually land inside the base.
			rel, relErr := filepath.Rel(base, got)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				t.Fatalf("SafeJoin(%q, %q) = %q, which is not inside the base", base, tc.entry, got)
			}
		})
	}
}

// The resolved path for a normal entry must be what filepath.Join produces, so
// the guard changes nothing about where a legitimate file lands.
func TestSafeJoinAgreesWithJoinForLegitimateEntries(t *testing.T) {
	const base = "/tmp/import"
	for _, entry := range []string{"scene.mp4", "a/b/c.mp4", "./x.mp4", "a/b/../c.mp4"} {
		got, err := SafeJoin(base, entry)
		if err != nil {
			t.Fatalf("SafeJoin(%q): unexpected error %v", entry, err)
		}
		if want := filepath.Join(base, entry); got != want {
			t.Errorf("SafeJoin(%q) = %q, want %q (same as filepath.Join)", entry, got, want)
		}
	}
}

// A base directory that is itself relative, or ends in a separator, must not
// change the verdict. The guard cleans the base first, and a base that needs
// cleaning is exactly the case where a prefix comparison silently fails.
func TestSafeJoinNormalisesTheBase(t *testing.T) {
	for _, base := range []string{"/tmp/import/", "/tmp/./import", "/tmp/nested/../import"} {
		if _, err := SafeJoin(base, "../evil"); err == nil {
			t.Errorf("base %q: traversal was not caught; the base was not cleaned before comparison", base)
		}
		if _, err := SafeJoin(base, "ok.mp4"); err != nil {
			t.Errorf("base %q: legitimate entry rejected: %v", base, err)
		}
	}
}

// The error must name the offending entry. A guard that reports "invalid path"
// forces a reader to bisect the archive by hand; the whole value of failing
// fast here is knowing which entry was hostile.
func TestSafeJoinErrorNamesTheEntry(t *testing.T) {
	_, err := SafeJoin("/tmp/import", "../../evil.txt")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "../../evil.txt") {
		t.Errorf("error %q does not name the offending entry", err)
	}
}
