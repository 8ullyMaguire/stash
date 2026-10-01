package pkg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stash#7240 covers "import and package install". The import half is fixed in
// internal/manager/task_import.go. This is the package-install half, which was
// a separate defect: installPackage passed filepath.Clean(f.Name) to
// store.writeFile, which then did filepath.Join(packageDir, name).
//
// filepath.Clean is not containment. It turns "../../evil" into "../../evil" --
// still a traversing path -- and filepath.Join then resolves it outside the
// package directory. Cleaning and containing are different operations and only
// the second one stops an arbitrary file write.
func TestWriteFileRefusesEscapingNames(t *testing.T) {
	base := t.TempDir()
	store := &Store{BaseDir: base}

	cases := []struct {
		name  string
		entry string
	}{
		{"traversing out of the package dir", "../../escaped.txt"},
		{"single level", "../escaped.txt"},
		{"buried in a longer path", "a/b/../../../../escaped.txt"},
		{"sibling sharing a name prefix", "../other-pkg/escaped.txt"},
		{"absolute", "/tmp/escaped.txt"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := store.writeFile("mypkg", tc.entry, 0o644, strings.NewReader("pwned"))
			if err == nil {
				t.Fatalf("writeFile accepted %q; a package can still write outside its directory", tc.entry)
			}
		})
	}
}

// The refusal must be visible where it happened, not as a generic write error.
func TestWriteFileErrorNamesTheEntry(t *testing.T) {
	store := &Store{BaseDir: t.TempDir()}
	err := store.writeFile("mypkg", "../../evil.txt", 0o644, strings.NewReader("x"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "../../evil.txt") {
		t.Errorf("error %q does not name the offending entry", err)
	}
}

// A legitimate package layout must still install: the guard is on the escape,
// not on nesting.
func TestWriteFileStillWritesNormalPackageFiles(t *testing.T) {
	base := t.TempDir()
	store := &Store{BaseDir: base}

	entries := map[string]string{
		"plugin.js":              "// plugin",
		"lib/helper.js":          "// helper",
		"lib/nested/deep.json":   `{"a":1}`,
		"README.md":              "# readme",
	}
	for name, content := range entries {
		if err := store.writeFile("mypkg", name, 0o644, strings.NewReader(content)); err != nil {
			t.Fatalf("writeFile(%q) rejected a legitimate package file: %v", name, err)
		}
	}

	// Every file must exist INSIDE the package directory, and the package
	// directory is the only thing that should have been created.
	pkgDir := store.packageDir("mypkg")
	for name, content := range entries {
		got, err := os.ReadFile(filepath.Join(pkgDir, name))
		if err != nil {
			t.Errorf("expected %s under the package dir: %v", name, err)
			continue
		}
		if string(got) != content {
			t.Errorf("%s = %q, want %q", name, got, content)
		}
	}
}

// The strongest form of the test: after a refused entry, nothing may exist
// outside the package directory. A guard that returns an error but has already
// created the file would pass the error check alone.
func TestWriteFileEscapeLeavesNothingBehind(t *testing.T) {
	base := t.TempDir()
	store := &Store{BaseDir: base}

	// The target the entry tries to reach, as a sibling of the package dir.
	canary := filepath.Join(base, "escaped.txt")

	if err := store.writeFile("mypkg", "../../escaped.txt", 0o644, strings.NewReader("pwned")); err == nil {
		t.Fatal("writeFile accepted a traversing entry name")
	}
	if _, err := os.Stat(canary); err == nil {
		t.Fatalf("%s exists; the write escaped containment even though an error was returned", canary)
	}
}
