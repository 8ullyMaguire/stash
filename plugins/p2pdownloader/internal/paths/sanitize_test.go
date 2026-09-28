package paths

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// # THE CASES, AND WHY EACH ONE IS HERE
//
// The plan names seven: `../../etc/passwd`, `/etc/passwd`, `a/../../b`, a name
// with a NUL, a symlink pointing outside the root, a name with a trailing dot,
// and a legitimate nested path that must succeed.
//
// The last one is the one that makes the other six mean anything. A function
// that rejects everything passes every hostile case; only the legitimate case
// distinguishes a gate from a wall.
//
// The rest are additions, and each came from something concrete:
//
//   - the Windows reserved names and the trailing SPACE, because
//     `storage.ToSafeFilePath` was measured passing `con` and `trailing.`
//     straight through, and a name that library accepts is a name this has to
//     catch;
//   - `..\..\windows`, which is the same attack with a different separator, and
//     which the library also passes through;
//   - a symlink in a SUBDIRECTORY, not just as the first component — the first
//     component is the obvious one, and a check written for it looks complete;
//   - a sibling directory sharing a string prefix, because a
//     `strings.HasPrefix` containment test accepts it and that mistake is
//     invisible in a test that only tries to escape upwards.

// tmpRoot returns a real, existing directory for a test, and skips on a
// platform where the fixture cannot be built.
//
// A real directory rather than a synthetic path string, because half the
// function is `EvalSymlinks` and half of the OTHER half is "does this component
// exist" — testing either against a path that does not exist tests a different
// code path than the one that runs in production.
func tmpRoot(t *testing.T) string {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("these fixtures use POSIX symlinks and modes; on Windows the " +
			"reserved-name cases still apply but the traversal fixtures do not " +
			"behave the same way")
	}

	root := t.TempDir()
	// t.TempDir is itself under a symlink on macOS (/var -> /private/var), so
	// resolve it once here. Every expectation below compares against the
	// resolved form, and a test that resolves one side and not the other fails
	// for a filesystem reason and reads as a bug in SanitizeJoin.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolving the temp root %q: %v", root, err)
	}
	return resolved
}

// TestHostileNamesAreRefused is the plan's list, and the assertions are on the
// ERROR, not on the absence of a path — a function that returns `("", nil)` for
// a hostile name has produced a path, and a caller that checks only `err` would
// proceed with an empty one.
func TestHostileNamesAreRefused(t *testing.T) {
	root := tmpRoot(t)

	cases := []struct {
		name    string
		why     string
		wantErr error
	}{
		{"../../etc/passwd", "the plan's case: a direct traversal", ErrEscapes},
		{"/etc/passwd", "absolute, and filepath.Join would produce it happily", ErrEscapes},
		{"a/../../b", "traversal hidden behind a legitimate-looking prefix", ErrEscapes},
		{"..", "the component on its own", ErrEscapes},
		{"a/b/../../../c", "traversal after two innocent components", ErrEscapes},
		{"ok/../../..", "traversal back past the first component", ErrEscapes},
		{"..\\..\\windows", "the same attack with a backslash, which the " +
			"library's ToSafeFilePath also passes through", ErrEscapes},
		{"a\\..\\..\\b", "backslash traversal behind a prefix", ErrEscapes},
		{"with\x00nul", "a NUL truncates the path in the C-backed syscalls " +
			"the storage layer makes, so the file written is not the file checked", ErrUnsafe},
		{"trailing.", "Windows strips it, so the name verified is not the " +
			"name that exists", ErrUnsafe},
		{"trailing ", "and so does a trailing space", ErrUnsafe},
		{"dir/trailing.", "in a subdirectory, not just at the top", ErrUnsafe},
		{"con", "a Windows device, which silently resolves to the device", ErrUnsafe},
		{"CON", "case does not help, the device is case-insensitive", ErrUnsafe},
		{"con.txt", "the extension does not help either", ErrUnsafe},
		{"dir/nul.mkv", "in a subdirectory", ErrUnsafe},
		{"lpt1", "another reserved name", ErrUnsafe},
		{"", "nothing to point at", ErrEscapes},
	}

	for _, c := range cases {
		got, err := SanitizeJoin(root, c.name)
		if err == nil {
			t.Errorf("SanitizeJoin(root, %q) = %q, no error. %s",
				c.name, got, c.why)
			continue
		}
		if !errors.Is(err, c.wantErr) {
			t.Errorf("SanitizeJoin(root, %q) failed with %v, which is not %v. "+
				"A refusal for the wrong reason is still a refusal today, and "+
				"the reason is what a caller branches on to tell an attack from "+
				"a bad name", c.name, err, c.wantErr)
		}
		if got != "" {
			t.Errorf("SanitizeJoin(root, %q) returned %q ALONGSIDE the error. A "+
				"caller that checks only err proceeds with the path, and an "+
				"empty one is worse than none", c.name, got)
		}
		// And the thing that actually matters: whatever came back, if anything,
		// must not be outside the root.
		if got != "" {
			if !underRoot(t, root, got) {
				t.Errorf("SanitizeJoin(root, %q) = %q, which is OUTSIDE the root",
					c.name, got)
			}
		}
	}
}

// TestLegitimateNamesAreAccepted is what makes the refusals above mean
// something, and it is a wall of its own: a downloader that refuses everything
// is safe and useless.
func TestLegitimateNamesAreAccepted(t *testing.T) {
	root := tmpRoot(t)

	cases := []string{
		"file.mkv",
		"nested/file.mkv",
		"a/b/c/d/deep.mkv",
		"file with spaces.mkv",
		"file.with.dots.mkv",
		"Ünïcödé ñame.mkv",       // a non-ASCII name, which is most of them
		"日本語.mkv",                // CJK
		"dots...in...the...name", // not a TRAILING dot
		"a b/c d/e f.mkv",        // spaces in a component, not trailing
		"..leading.dots",         // leading dots are fine; only `..` is not
		"file.mkv.part",          // an incomplete download's name
		"!@#$%^&()[]{}'.mkv",     // punctuation
		"no-extension",
		strings.Repeat("deep/", 20) + "file.mkv",
	}

	for _, name := range cases {
		got, err := SanitizeJoin(root, name)
		if err != nil {
			t.Errorf("SanitizeJoin(root, %q) refused a legitimate name: %v", name, err)
			continue
		}
		if !underRoot(t, root, got) {
			t.Errorf("SanitizeJoin(root, %q) = %q, which is not under the root",
				name, got)
			continue
		}
		if want := filepath.Join(root, filepath.Clean(name)); got != want {
			t.Errorf("SanitizeJoin(root, %q) = %q, expected %q. A gate that "+
				"rewrites a legitimate name into a different one puts a file "+
				"somewhere the operator did not ask for", name, got, want)
		}
	}
}

// underRoot is the containment assertion, and it is `filepath.Rel` for the same
// reason the production code is: a string prefix is not containment.
//
// The one subtlety: a component that merely STARTS with dots is not a
// traversal. `rel` for `/a/b/..leading.dots` is the string `..leading.dots`,
// and a naive `strings.HasPrefix(rel, "..")` rejects it — which is wrong, and
// wrong in a way that looks like a bug in SanitizeJoin rather than in this
// helper. The test asserting `..leading.dots` is a legitimate name is what
// found it, which is the argument for having that case at all: a name that
// begins with dots is common enough in real corpora that a gate refusing them
// would be refused in turn.
//
// So the check is on PATH COMPONENTS, which is what `filepath.Rel` documents
// it for: a relative path is inside iff no component is `..`.
func underRoot(t *testing.T, root, path string) bool {
	t.Helper()

	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	for _, comp := range strings.Split(rel, string(filepath.Separator)) {
		if comp == ".." {
			return false
		}
	}
	return true
}

// TestEnsureRootNeverDestroysWhatIsAlreadyThere is a property EnsureRoot does
// not obviously have, and the mutation harness found that it did not.
//
// A plausible "fix" for "EnsureRoot accepted a regular file" is
// `os.RemoveAll(root)` followed by `os.MkdirAll(root)`. That is the shape a
// re-entrant setup routine takes, it compiles, it returns nil for every input,
// and it DELETES A USER'S FILE. The existing test missed it because
// `os.MkdirAll` on a path occupied by a file returns an error anyway — so the
// no-op version of the clobber passed, and the destructive one would too.
//
// The assertion is therefore about the FILE still being there, not about the
// function returning an error. A gate that reports the right error after
// destroying the thing it was refusing to destroy has still destroyed it.
func TestEnsureRootNeverDestroysWhatIsAlreadyThere(t *testing.T) {
	base := t.TempDir()

	// A regular file with contents that must survive.
	file := filepath.Join(base, "precious")
	const contents = "two years of downloads"
	if err := os.WriteFile(file, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing the decoy: %v", err)
	}

	_ = EnsureRoot(file)

	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("EnsureRoot DESTROYED %q: %v. A setup routine that removes what "+
			"it finds is a setup routine that deletes user data", file, err)
	}
	if string(got) != contents {
		t.Errorf("EnsureRoot altered %q: it now contains %q, was %q",
			file, got, contents)
	}

	// And the same for a non-empty DIRECTORY, which is the more likely accident:
	// a download root that already holds a season of files.
	dir := filepath.Join(base, "existing-downloads")
	if err := os.MkdirAll(filepath.Join(dir, "show", "s01e01"), 0o755); err != nil {
		t.Fatalf("creating the tree: %v", err)
	}
	keep := filepath.Join(dir, "show", "s01e01", "episode.mkv")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatalf("writing the episode: %v", err)
	}

	if err := EnsureRoot(dir); err != nil {
		t.Errorf("EnsureRoot on an existing non-empty directory: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("EnsureRoot destroyed the contents of an existing download "+
			"root: %v. That directory is the user's, and EnsureRoot's job is to "+
			"use it", err)
	}
}

// TestASymlinkInTheNameIsRefused is the case the library's function cannot
// possibly catch, and the one the plan names with "never via a symlink".
//
// A string function sees `link/file.mkv` and is satisfied: no `..`, no leading
// slash, looks fine. The filesystem sees `link` pointing at `/etc`, and the
// write lands there.
func TestASymlinkInTheNameIsRefused(t *testing.T) {
	root := tmpRoot(t)
	outside := t.TempDir()

	// Resolve the outside directory too, for the same reason the root is
	// resolved: comparing a resolved path against an unresolved one rejects
	// every legitimate file on a filesystem where /tmp is a symlink.
	outsideResolved, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatalf("resolving %q: %v", outside, err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600); err != nil {
		t.Fatalf("writing the decoy: %v", err)
	}

	// The symlink is the FIRST component of the name, which is the obvious
	// placement.
	if err := os.Symlink(outsideResolved, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}

	for _, name := range []string{
		"link/secret",
		"./link/secret",
		"link",
		"sub/../link/secret", // resolves to link/secret and must still be caught
	} {
		got, err := SanitizeJoin(root, name)
		if err == nil {
			t.Errorf("SanitizeJoin(root, %q) = %q with no error. The name "+
				"reaches %q through a symlink inside the root, and a string "+
				"check cannot see that — which is why the filesystem check is "+
				"the last one and not the only one", name, got, outsideResolved)
		}
	}
}

// TestASymlinkInASubdirectoryIsRefused is the placement nobody writes the test
// for.
//
// The first-component case is obvious enough that a check written for it looks
// complete. A symlink three levels down, reached through three innocent
// directories, is the same attack with more steps — and `resolveExistingPrefix`
// exists precisely because the whole path has to be walked, not just the head.
func TestASymlinkInASubdirectoryIsRefused(t *testing.T) {
	root := tmpRoot(t)
	outside := t.TempDir()

	outsideResolved, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatalf("resolving %q: %v", outside, err)
	}

	// Three innocent directories, then the symlink.
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("creating the deep directory: %v", err)
	}
	if err := os.Symlink(outsideResolved, filepath.Join(deep, "escape")); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}

	for _, name := range []string{
		"a/b/c/escape",
		"a/b/c/escape/../escape",
		"a/b/c",
	} {
		got, err := SanitizeJoin(root, name)
		if err != nil {
			// The first two are the interesting ones; the third is a legitimate
			// directory and must succeed, which the other test covers.
			continue
		}
		if strings.Contains(got, "escape") {
			t.Errorf("SanitizeJoin(root, %q) = %q with no error. The escape is "+
				"three levels down, past three legitimate directories", name, got)
		}
	}

	// And the one that must be refused, asserted directly rather than through
	// the loop above.
	if got, err := SanitizeJoin(root, "a/b/c/escape"); err == nil {
		t.Errorf("SanitizeJoin(root, %q) = %q with no error", "a/b/c/escape", got)
	}
}

// TestASiblingDirectoryWithASharedPrefixIsNotInside is the `strings.HasPrefix`
// mistake, named.
//
// `/tmp/root` and `/tmp/root-evil` share a string prefix and share nothing
// else. A containment test written as `strings.HasPrefix(path, root)` accepts
// every file in the sibling, and it is invisible in a test suite that only ever
// tries to escape UPWARD — which is what most of them do.
func TestASiblingDirectoryWithASharedPrefixIsNotInside(t *testing.T) {
	base := t.TempDir()
	baseResolved, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatalf("resolving %q: %v", base, err)
	}

	root := filepath.Join(baseResolved, "root")
	evil := filepath.Join(baseResolved, "root-evil")

	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("creating the root: %v", err)
	}
	if err := os.MkdirAll(evil, 0o755); err != nil {
		t.Fatalf("creating the sibling: %v", err)
	}

	if !strings.HasPrefix(evil, root) {
		t.Fatalf("this fixture no longer demonstrates anything: %q does not "+
			"start with %q", evil, root)
	}

	// SanitizeJoin is never asked about the sibling by name — a name is
	// relative — so the check is on containedIn, which is where a prefix test
	// would live.
	if err := containedIn(root, evil); err == nil {
		t.Errorf("containedIn(%q, %q) returned no error. The two share a "+
			"string prefix and nothing else, which is exactly the case a "+
			"HasPrefix containment test gets wrong", root, evil)
	}

	if err := containedIn(root, root); err != nil {
		t.Errorf("containedIn(root, root) = %v. A path IS inside itself", err)
	}
	if err := containedIn(root, filepath.Join(root, "a", "b")); err != nil {
		t.Errorf("containedIn(root, root/a/b) = %v", err)
	}
}

// TestANameBeginningWithDotsIsInsideTheRoot pins the distinction from both
// sides, because the naive form of the check gets it backwards and the mistake
// is invisible in a suite that only tests escapes.
//
// `rel` for a file called `..leading.dots` inside the root is the string
// `..leading.dots`. A `strings.HasPrefix(rel, "..")` test rejects it, so a gate
// written that way refuses legitimate files — and the refusal names an escape
// that never happened, which sends whoever debugs it looking for a traversal
// bug that is not there.
//
// The correct test is on path COMPONENTS, and the difference shows up in exactly
// two names: `..leading.dots` (inside) and `..` (outside). Both are asserted
// here, because asserting only the inside case would pass with a check that
// rejects everything, and asserting only the outside case would pass with the
// buggy one.
func TestANameBeginningWithDotsIsInsideTheRoot(t *testing.T) {
	root := tmpRoot(t)

	// Inside: a component that merely starts with dots.
	for _, name := range []string{
		"..leading.dots",
		"..x",
		"..dots.then.extension.mkv", // leading dots are fine; a TRAILING one is not
		"dir/..hidden/file.mkv",
		"..hidden",
	} {
		got, err := SanitizeJoin(root, name)
		if err != nil {
			t.Errorf("SanitizeJoin(root, %q) refused a name that is inside the "+
				"root: %v. A component beginning with dots is not a traversal, "+
				"and refusing it makes the gate fail on real corpora", name, err)
			continue
		}
		if !underRoot(t, root, got) {
			t.Errorf("SanitizeJoin(root, %q) = %q, which is not under the root",
				name, got)
		}
	}

	// And `...` is NOT in that list, which is the case that makes the trailing-
	// dot rule worth having: it begins with dots and is a traversal-shaped name
	// by appearance, and it is refused for a completely different reason --
	// because it ENDS in a dot, which Windows strips. A gate that refused it as
	// a traversal would be right by accident and wrong in the message.
	if got, err := SanitizeJoin(root, "..."); err == nil {
		t.Errorf("SanitizeJoin(root, %q) = %q with no error. It ends in a dot", "...", got)
	} else if !errors.Is(err, ErrUnsafe) {
		t.Errorf("SanitizeJoin(root, %q) failed with %v, which is not ErrUnsafe. "+
			"Its problem is the trailing dot, not traversal, and the error is "+
			"what a caller branches on", "...", err)
	}

	// Outside: the component IS `..`. Same function, opposite answer, so the
	// check distinguishes them.
	for _, name := range []string{
		"..",
		"dir/..",
		"..leading.dots/../..", // contains a real `..` too
	} {
		if got, err := SanitizeJoin(root, name); err == nil {
			t.Errorf("SanitizeJoin(root, %q) = %q with no error. The component "+
				"IS %q, which is a traversal", name, got, "..")
		}
	}

	// And containedIn agrees, called directly.
	if err := containedIn(root, filepath.Join(root, "..hidden")); err != nil {
		t.Errorf("containedIn rejected a `..hidden` child: %v", err)
	}
	if err := containedIn(root, filepath.Dir(root)); err == nil {
		t.Error("containedIn accepted the root's own parent")
	}
}

// TestAMissingRootIsReportedRatherThanSilentlyCreated: SanitizeJoin does not
// create anything. A caller that forgot to call EnsureRoot gets an error naming
// the missing directory, not a path into a directory that is not there.
func TestAMissingRootIsReportedRatherThanSilentlyCreated(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "not-created-yet")

	got, err := SanitizeJoin(missing, "file.mkv")
	if err == nil {
		t.Errorf("SanitizeJoin on a missing root returned %q with no error", got)
	}
	if _, statErr := os.Stat(missing); statErr == nil {
		t.Error("SanitizeJoin created the root. Creating it is EnsureRoot's job; " +
			"a gate that also creates directories can create them somewhere " +
			"unexpected")
	}
}

// TestEnsureRootCreatesItOnceAndRefusesAFile is the counterpart.
func TestEnsureRootCreatesItOnceAndRefusesAFile(t *testing.T) {
	base := t.TempDir()

	// Created.
	created := filepath.Join(base, "downloads", "nested")
	if err := EnsureRoot(created); err != nil {
		t.Fatalf("EnsureRoot on a missing directory: %v", err)
	}
	if info, err := os.Stat(created); err != nil || !info.IsDir() {
		t.Errorf("EnsureRoot did not create a directory at %q (err=%v)", created, err)
	}

	// Idempotent — a second call on an existing directory is not an error, or
	// every restart would fail.
	if err := EnsureRoot(created); err != nil {
		t.Errorf("EnsureRoot on an existing directory: %v", err)
	}

	// A FILE is refused, and refused LOUDLY rather than clobbered.
	file := filepath.Join(base, "a-file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("writing the decoy file: %v", err)
	}
	if err := EnsureRoot(file); err == nil {
		t.Error("EnsureRoot accepted a regular file as the download root")
	}
	if _, err := os.Stat(file); err != nil {
		t.Errorf("EnsureRoot disturbed the file: %v", err)
	}
}

// TestTheRootItselfBeingASymlinkIsAccepted, which is the flip side of the
// symlink tests and just as easy to get wrong.
//
// macOS puts /tmp behind a symlink, and a user may well symlink their
// download directory to a fast disk. Refusing that would break a legitimate
// setup, and the test that refuses it would be the one telling you to fix your
// resolver instead of fixing the code.
func TestTheRootItselfBeingASymlinkIsAccepted(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real-downloads")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("creating the real directory: %v", err)
	}

	link := filepath.Join(base, "linked-downloads")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}

	got, err := SanitizeJoin(link, "file.mkv")
	if err != nil {
		t.Fatalf("a symlinked download root was refused: %v\n\n"+
			"  This is a legitimate setup — it is what /tmp is on macOS — and a "+
			"gate that refuses it is a gate users work around", err)
	}
	if !underRoot(t, link, got) {
		t.Errorf("SanitizeJoin(%q, \"file.mkv\") = %q, which is not under the root",
			link, got)
	}
}

// TestAResultIsActuallyWritable is the property the function exists for, and it
// is the one that makes the symlink refusals mean something: the returned path
// has to be a place a file can be created, and a parent that does not exist is
// a path the storage layer will fail on later, far from here.
func TestAResultIsActuallyWritable(t *testing.T) {
	root := tmpRoot(t)

	got, err := SanitizeJoin(root, "a/b/c/file.mkv")
	if err != nil {
		t.Fatalf("SanitizeJoin: %v", err)
	}

	// The parent does not exist yet, and that is correct: a downloader resolves
	// names for files that have not been created. What must be true is that the
	// parent CAN be created, and that creating it lands inside the root.
	parent := filepath.Dir(got)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("the returned path's parent is not creatable: %v", err)
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		t.Fatalf("resolving the created parent: %v", err)
	}
	if !underRoot(t, root, resolvedParent) {
		t.Errorf("creating the parent of %q produced %q, which is outside the "+
			"root. The path looked fine and the filesystem disagreed, which is "+
			"the whole reason the last check is a filesystem check", got, resolvedParent)
	}

	// And the file itself lands where it was promised.
	if err := os.WriteFile(got, []byte("x"), 0o600); err != nil {
		t.Fatalf("writing to the sanitised path: %v", err)
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("the file is not where SanitizeJoin said it would be: %v", err)
	}
}
