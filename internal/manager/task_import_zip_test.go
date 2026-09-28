package manager

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// stash#7240: "The `unzipFile` method in `internal/manager/task_import.go:156`
// joins zip entry names directly into the extraction path without validating
// that the result stays within BaseDir: `fn := filepath.Join(t.BaseDir,
// f.Name)`. A zip entry named `../../../../arbitrary.txt` resolves to a path
// outside [the destination]."
//
// This exercises unzipFile itself rather than only the helper it calls, because
// a unit test on fsutil.SafeJoin proves the helper is correct and proves
// nothing about whether the vulnerable call site still calls it. A guard that
// is bypassed by a later refactor is the failure this test is here to prevent.
func writeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating zip: %v", err)
	}
	defer f.Close()

	w := zip.NewWriter(f)
	for name, content := range entries {
		// Names containing a backslash are written verbatim: Go's zip writer
		// does not normalise them, and some archivers emit them.
		e, err := w.Create(name)
		if err != nil {
			t.Fatalf("creating zip entry %q: %v", name, err)
		}
		if _, err := e.Write([]byte(content)); err != nil {
			t.Fatalf("writing zip entry %q: %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing zip: %v", err)
	}
}

// A zip whose entries climb out of the destination must be refused, and the
// escaped file must not exist anywhere outside the destination afterwards.
func TestUnzipFileRefusesPathTraversal(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "dest")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	// The payload's target, outside base but inside the temp root so the test
	// cleans up after itself.
	canary := filepath.Join(root, "arbitrary.txt")

	zipPath := filepath.Join(root, "malicious.zip")
	writeZip(t, zipPath, map[string]string{
		"../../arbitrary.txt": "pwned",
	})

	task := &ImportTask{BaseDir: base, TmpZip: zipPath}
	err := task.unzipFile()
	if err == nil {
		t.Fatal("unzipFile accepted a zip with a traversing entry name; " +
			"the containment guard is not being applied at the call site")
	}

	if _, statErr := os.Stat(canary); statErr == nil {
		t.Fatalf("%s was created outside the destination directory; "+
			"arbitrary file write is still possible", canary)
	}
}

// The guard must not merely reject the literal "..". These are the shapes a
// naive string check misses, listed here so a future simplification of the
// guard has to confront each one.
func TestUnzipFileRefusesEveryTraversalShape(t *testing.T) {
	cases := []struct {
		name  string
		entry string
	}{
		{"literal dots", "../../escaped.txt"},
		{"buried in a relative path", "sub/dir/../../../../escaped.txt"},
		{"climbing to a sibling sharing a prefix", "../dest-evil/escaped.txt"},
		{"absolute", "/tmp/escaped.txt"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			base := filepath.Join(root, "dest")
			if err := os.MkdirAll(base, 0o755); err != nil {
				t.Fatal(err)
			}
			zipPath := filepath.Join(root, "attack.zip")
			writeZip(t, zipPath, map[string]string{tc.entry: "pwned"})

			task := &ImportTask{BaseDir: base, TmpZip: zipPath}
			if err := task.unzipFile(); err == nil {
				t.Fatalf("unzipFile accepted entry %q", tc.entry)
			}
		})
	}
}

// The guard must not break the feature. An ordinary archive with nested
// directories has to extract correctly, at the expected paths.
func TestUnzipFileStillExtractsOrdinaryArchives(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "dest")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(root, "normal.zip")
	writeZip(t, zipPath, map[string]string{
		"scene.mp4":             "video",
		"extras/behind.mp4":     "bonus",
		"extras/nested/deep.txt": "deep",
		".hidden":               "dotfile",
	})

	task := &ImportTask{BaseDir: base, TmpZip: zipPath}
	if err := task.unzipFile(); err != nil {
		t.Fatalf("unzipFile rejected a legitimate archive: %v", err)
	}

	for rel, want := range map[string]string{
		"scene.mp4":              "video",
		"extras/behind.mp4":      "bonus",
		"extras/nested/deep.txt": "deep",
		".hidden":                "dotfile",
	} {
		got, err := os.ReadFile(filepath.Join(base, rel))
		if err != nil {
			t.Errorf("expected %s to be extracted: %v", rel, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
}
