package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
	libstorage "github.com/anacrolix/torrent/storage"
)

// # WHAT THIS PACKAGE IS
//
// A `storage.ClientImpl` that hands the library the **classic** os-backed
// storage with a `FilePathMaker` that routes every peer-supplied name through
// `internal/paths.SanitizeJoin` first.
//
// It exists because of a measured fact, not a hypothetical. `anacrolix/torrent`
// v1.61.0's own containment check — `isSubFilepath`, at
// `storage/file-client.go:92` — is `filepath.Rel` plus `HasPrefix`, which is
// pure string arithmetic and cannot resolve a symlink. Executed against the
// library's own functions with a torrent directory that is a symlink pointing
// outside the download root:
//
//	file path     : <root>/downloads/innocent/passwd   (innocent -> outside)
//	isSubFilepath -> true
//	really is     : <root>/outside                    (EvalSymlinks)
//	the decoy OUTSIDE the download root now reads: "PEERS CONTROLLED BYTES"
//
// The mmap storage is worse: `grep -c isSubFilepath` is 0 for `mmap.go`, and it
// carries the TODO *"Support all the same native filepath configuration that
// NewFileOpts provides"*. So the classic storage is the only candidate, and
// even it needs ours in front of it.
//
// # THE AWKWARD PART, WHICH IS WHY THESE TESTS ARE SHAPED THIS WAY
//
// `FilePathMaker` is `func(FilePathMakerOpts) string`. **It cannot report an
// error.** The same is true of `TorrentDirFilePathMaker`. There is exactly one
// place a refusal can be expressed — `ClientImpl.OpenTorrent`'s error return —
// and neither maker is it.
//
// So a name that fails the gate CANNOT stop the storage from opening. The
// options are:
//
//   - return a path that is guaranteed inert (a single fixed filename inside
//     the root, for every refused name), and record the refusal where the
//     downloader can act on it; or
//   - validate every name for a torrent BEFORE handing it to the library, in
//     `OpenTorrent`, and refuse the whole torrent there — which is the only
//     place a real error can be returned.
//
// This package does the second, and the first as a backstop. That ordering is
// the whole design: **a refusal must never be expressed as a path**, because a
// path is indistinguishable from a success by everything downstream.

// TestTheGateRefusesTheWholeTorrentBeforeTheLibrarySeesIt is the primary
// property, and it is why this package exists rather than a `FilePathMaker`.
//
// The scenario: a torrent whose one file is named `../escape`. A backstop-only
// design returns some inert path and lets the transfer start; the operator
// finds out at the end that nothing arrived, or worse, that it arrived and was
// not the file. The whole torrent is refused at `OpenTorrent` instead, with
// the offending name in the error.
func TestTheGateRefusesTheWholeTorrentBeforeTheLibrarySeesIt(t *testing.T) {
	root := tmpRoot(t)
	gate := New(root)

	info := &metainfo.Info{
		Name:        "innocent-torrent-name",
		PieceLength: 1,
		Pieces:      make([]byte, 20),
		Files: []metainfo.FileInfo{
			{Length: 1, Path: []string{"..", "..", "..", "etc", "passwd"}},
		},
	}

	_, err := gate.OpenTorrent(t.Context(), info, testHash())
	if err == nil {
		t.Fatal("OpenTorrent accepted a torrent whose file escapes the download " +
			"root. Nothing was written, but the transfer would have started, and " +
			"the operator would find out at the end that the download is unusable")
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("OpenTorrent failed with %v, which is not ErrRefused. The caller "+
			"branches on this to tell a hostile torrent from a broken filesystem",
			err)
	}
	if !strings.Contains(err.Error(), "..") {
		t.Errorf("the error does not name the offending component: %v. Without it "+
			"the operator cannot tell which of ten thousand filenames was the "+
			"problem", err)
	}

	// And nothing was created anywhere. Not in the root, not above it.
	assertNothingCreated(t, root)
}

// TestAHostileNameIsCaughtEvenWhenItIsTheSecondComponent is the case that a
// first-component check misses, and the same mistake the library makes.
//
// `internal/paths` checks every component, and this test is here because the
// library's own function does not. A torrent with a file at `sub/../../escape`
// has an innocent first component.
func TestAHostileNameIsCaughtEvenWhenItIsTheSecondComponent(t *testing.T) {
	root := tmpRoot(t)
	gate := New(root)

	for _, path := range [][]string{
		{"ok", "..", "..", "..", "escape"},
		{"sub", "..", "..", "escape"},
		{"/etc", "passwd"},
		{"a", "b", "..", "..", "..", "..", "escape"},
	} {
		info := multiFileComps("innocent", path)

		got, err := gate.OpenTorrent(t.Context(), info, testHash())
		if err == nil {
			t.Errorf("OpenTorrent accepted the file path %q", path)
			if got.Close != nil {
				_ = got.Close()
			}
		}
	}
	assertNothingCreated(t, root)
}

// TestAWhollyLegitimateTorrentIsAccepted is what makes the refusals mean
// something, and it is a wall of its own.
//
// A downloader that refuses everything is safe and useless, and the cost of a
// gate this aggressive shows up as "the downloader just says invalid torrent"
// with nothing in the log to explain it.
func TestAWhollyLegitimateTorrentIsAccepted(t *testing.T) {
	root := tmpRoot(t)
	gate := New(root)

	cases := []struct {
		name  string
		info  *metainfo.Info
		dirs  []string
		files []string
	}{
		{
			name: "a single file",
			info: multiFile("torrent-name", "movie.mkv"),
		},
		{
			name: "a nested path",
			info: multiFile("torrent-name", "a/b/c/movie.mkv"),
		},
		{
			name: "a name beginning with dots, which is not a traversal",
			info: multiFile("torrent-name", "..hidden/movie.mkv"),
		},
		{
			name: "a non-ASCII name, which is most of them",
			info: multiFile("Ünïcödé", "日本語/café.mkv"),
		},
		{
			name: "dots inside the name",
			info: multiFile("torrent-name", "dots...in...name.mkv"),
		},
		{
			name: "a nameless torrent (BEP 52 v2), where the file IS the name",
			info: multiFile(metainfo.NoName, "movie.mkv"),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tl, err := gate.OpenTorrent(t.Context(), c.info, testHash())
			if err != nil {
				t.Fatalf("OpenTorrent refused a legitimate torrent: %v", err)
			}
			if tl.Close != nil {
				_ = tl.Close()
			}
		})
	}
}

// TestARealSymlinkedTorrentDirectoryIsRefused is the case the library cannot
// see, and the reason this package is not just "set DataDir".
//
// The torrent directory itself is a symlink pointing outside the download
// root. Nothing about the *string* is wrong: every component is innocuous, and
// the library's own `isSubFilepath` says `true`. Only `EvalSymlinks` sees it.
//
// And the setup is not exotic. A user who has one library directory on a fast
// disk and symlinks the download root is doing something entirely reasonable —
// `TestTheRootItselfBeingASymlinkIsAccepted` in `internal/paths` says so, and
// that is not in conflict with this test. The difference is WHO made the
// symlink and WHEN: the root is the operator's and exists before the download
// starts, whereas this one is created by, or selected by, a torrent, and
// resolving outside is what makes it hostile.
func TestARealSymlinkedTorrentDirectoryIsRefused(t *testing.T) {
	root := tmpRoot(t)
	outside := t.TempDir()
	outsideResolved, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatalf("resolving %q: %v", outside, err)
	}

	// A torrent named "innocent" whose directory is a symlink out of the root.
	// This is the library's own layout: baseDir + BestName() + file path.
	link := filepath.Join(root, "innocent")
	if err := os.Symlink(outsideResolved, link); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}

	gate := New(root)
	info := multiFile("innocent", "passwd")

	tl, err := gate.OpenTorrent(t.Context(), info, testHash())
	if err == nil {
		t.Error("OpenTorrent accepted a torrent whose directory is a symlink " +
			"pointing outside the download root. The library's own isSubFilepath " +
			"says this path is fine, which is exactly why the gate exists")
		if tl.Close != nil {
			_ = tl.Close()
		}
	}

	// Nothing may exist outside the root, whatever the library thought.
	if _, err := os.Lstat(filepath.Join(outsideResolved, "passwd")); err == nil {
		t.Error("a file was created outside the download root")
	}
}

// TestARealSymlinkInsideTheTorrentIsRefused attacks the BACKSTOP specifically,
// and the distinction is not cosmetic.
//
// The first version of this test called `OpenTorrent` with a file reached
// through a symlink -- and passed with the backstop's `SanitizeJoin` REMOVED,
// because the up-front check refused the same name. Two layers, two tests, each
// covering for the other: removing either one still looked covered, which is
// how "the gate is safe" can be true of neither layer alone.
//
// So this test calls the backstop DIRECTLY, which is also what the library does:
// `FilePathMaker` has no way to report a failure, so the backstop is the only
// thing standing between a name and `os.OpenFile` if the up-front check is ever
// bypassed or does not cover a path the library discovers later.
func TestARealSymlinkInsideTheTorrentIsRefused(t *testing.T) {
	root := tmpRoot(t)
	outside := t.TempDir()
	outsideResolved, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatalf("resolving %q: %v", outside, err)
	}
	secret := filepath.Join(outsideResolved, "secret")
	if err := os.WriteFile(secret, []byte("decoy"), 0o600); err != nil {
		t.Fatalf("writing the decoy: %v", err)
	}

	// The torrent directory exists and is legitimate; a link inside it escapes.
	torrentDir := filepath.Join(root, "torrent")
	if err := os.MkdirAll(torrentDir, 0o755); err != nil {
		t.Fatalf("creating the torrent directory: %v", err)
	}
	if err := os.Symlink(outsideResolved, filepath.Join(torrentDir, "link")); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}

	// The backstop, on its own.
	maker := New(root).filePathMaker()
	got := maker(libstorage.FilePathMakerOpts{
		Info: &metainfo.Info{Name: "torrent"},
		File: &metainfo.FileInfo{Length: 1, Path: []string{"link", "secret"}},
	})

	if !strings.HasPrefix(got, root) {
		t.Errorf("the backstop returned %q, which is outside the download root "+
			"%q. The file inside the torrent is a symlink, and the name reaches "+
			"%q through it", got, root, outsideResolved)
	}

	// The refusal IS the sentinel, so that is what is asserted.
	//
	// NOT `!strings.Contains(got, "link")` -- t.TempDir names the directory
	// after the test, so a containment check on any substring of the hostile
	// name matches the fixture's own path and the assertion fails for a
	// filesystem reason that reads as a gate bug.
	want := filepath.Join(root, refuseSentinel)
	if got != want {
		t.Errorf("the backstop returned %q, expected the refusal sentinel %q. A "+
			"path that merely happens to be inside the root is not a refusal: it "+
			"is indistinguishable from a success to the caller", got, want)
	}
}

// TestTheLibraryIsNeverAskedToOpenATorrentWeRefused is the primary property,
// and the first version of it was measuring the wrong thing.
//
// It built a torrent with a file at `../escape` and asserted the download root
// was never created. But the LIBRARY refuses that torrent too -- `isSubFilepath`
// catches `..` walks -- so the assertion held whether or not the gate ran. The
// mutation "remove the up-front check" SURVIVED it, which is how the mistake
// was found.
//
// So the test has to use a name the library ACCEPTS and the gate refuses, and
// the only such name is a symlink: `isSubFilepath` is `filepath.Rel`, and
// `Rel` cannot resolve a link.
//
// The assertion is on the download root's contents rather than on the error,
// because "the library would have caught it too" is exactly what makes this
// test necessary and exactly what it must not rely on.
func TestTheLibraryIsNeverAskedToOpenATorrentWeRefused(t *testing.T) {
	root := filepath.Join(tmpRoot(t), "not-yet-created")
	outside := t.TempDir()
	outsideResolved, err := filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatalf("resolving %q: %v", outside, err)
	}

	// A symlink already pointing out of the root, under the name the torrent
	// will be given. The library would follow it; the gate must not.
	if err := os.Symlink(outsideResolved, filepath.Join(filepath.Dir(root), "innocent")); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}

	gate := New(root)
	_, err = gate.OpenTorrent(t.Context(), multiFile("innocent", "passwd"), testHash())
	if err == nil {
		t.Fatal("OpenTorrent accepted a torrent whose directory is a symlink " +
			"out of the download root, and the library's own isSubFilepath " +
			"accepts it too -- so nothing downstream would have refused this")
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("failed with %v, which is not ErrRefused", err)
	}

	// And nothing was written through the link, which is the part that matters.
	if _, err := os.Lstat(filepath.Join(outsideResolved, "passwd")); err == nil {
		t.Error("a file was created outside the download root")
	}
	// `NewFileOpts` creates its base directory eagerly, so its existence means
	// the library WAS reached.
	if _, err := os.Stat(root); err == nil {
		t.Error("the download root was created while the torrent was refused, " +
			"which means the library was reached -- the failure this package " +
			"exists to prevent")
	}
}

// TestANulInAComponentIsRefused is a check that had no test, which the
// mutation harness found: "a NUL in a component is allowed" SURVIVED.
//
// A NUL is refused because the C-backed syscalls the downloader makes truncate
// at it, so the file written is not the file that was checked -- and the
// truncation happens in the kernel, where nothing can see it.
func TestANulInAComponentIsRefused(t *testing.T) {
	root := tmpRoot(t)
	gate := New(root)

	_, err := gate.OpenTorrent(t.Context(),
		multiFileComps("innocent", []string{"with\x00nul.mkv"}), testHash())
	if err == nil {
		t.Error("OpenTorrent accepted a file name containing a NUL. The write " +
			"would be truncated at the NUL by the syscall, so the file created " +
			"would not be the file that was named")
	}
}

// TestAnEmptyComponentIsTolerated, because the gate's NUL and `..` checks sit
// next to an empty-component case that DELIBERATELY returns nil.
//
// Every `filepath.Join` drops an empty component, so a torrent with one is
// accepted by the library and must be accepted here. A gate that refused it
// would refuse torrents that work, and the failure would be "invalid torrent"
// with nothing in the log to explain it.
//
// The mutation "an empty component stops being tolerated" is in the harness for
// the same reason: this is a decision, and a decision nothing checks is a
// decision nobody made on purpose.
func TestAnEmptyComponentIsTolerated(t *testing.T) {
	root := tmpRoot(t)
	gate := New(root)

	tl, err := gate.OpenTorrent(t.Context(),
		multiFileComps("innocent", []string{"", "file.mkv"}), testHash())
	if err != nil {
		t.Errorf("OpenTorrent refused a torrent with an empty path component: %v. "+
			"Every join drops it, the library handles it, and refusing here "+
			"breaks a torrent that works", err)
		return
	}
	if tl.Close != nil {
		_ = tl.Close()
	}
}

// TestTheRefusalIsRecordedForTheCaller is the other half of "a path cannot
// carry an error".
//
// Once the gate has refused, something has to be able to say WHY in a task
// report, long after `OpenTorrent` returned. So refusals are retained with the
// name that caused them.
func TestTheRefusalIsRecordedForTheCaller(t *testing.T) {
	root := tmpRoot(t)
	gate := New(root)

	info := multiFile("innocent", "../../escape")
	hash := testHash()

	_, err := gate.OpenTorrent(t.Context(), info, hash)
	if err == nil {
		t.Fatal("the hostile torrent was accepted")
	}

	refusals := gate.Refusals()
	if len(refusals) != 1 {
		t.Fatalf("recorded %d refusals, expected 1", len(refusals))
	}
	got := refusals[0]
	if got.Hash != hash {
		t.Errorf("the refusal is filed under hash %x, expected %x", got.Hash, hash)
	}
	if got.Name == "" {
		t.Error("the refusal does not name the file that caused it, so a report " +
			"would have to say 'a torrent was refused' and nothing more")
	}
	if got.Reason == "" {
		t.Error("the refusal has no reason")
	}

	// Asking twice does not duplicate: a caller polling for news should not see
	// the same refusal grow.
	_, _ = gate.OpenTorrent(t.Context(), info, hash)
	if again := gate.Refusals(); len(again) != 1 {
		t.Errorf("the same torrent refused twice produced %d records, expected 1. "+
			"Retries happen, and a report that lists one refusal four times is a "+
			"report nobody reads", len(again))
	}
}

// TestAConcurrentOpenRecordsEveryRefusal is the race, because a downloader
// opens torrents from a DHT goroutine and a task poller reads at the same time.
// `-race` runs it; the assertions are here so a failure is legible.
func TestAConcurrentOpenRecordsEveryRefusal(t *testing.T) {
	root := tmpRoot(t)
	gate := New(root)

	const n = 16
	var wg sync.WaitGroup
	wg.Add(2 * n)

	for i := range n {
		// A refusal.
		go func() {
			defer wg.Done()
			_, _ = gate.OpenTorrent(t.Context(),
				multiFile("t", "../escape"), testHash())
		}()
		// A reader, concurrent with the writers.
		go func() {
			defer wg.Done()
			for range 4 {
				_ = gate.Refusals()
			}
		}()
		_ = i
	}
	wg.Wait()

	if got := len(gate.Refusals()); got != 1 {
		t.Errorf("recorded %d refusals, expected 1. All %d writes are the same "+
			"info hash, so they are one torrent refused repeatedly", got, n)
	}
}

// TestEveryRefusedNameEndsUpInOnePlace is a property of the backstop path: if
// a name reaches the library anyway, every file it would have written must land
// under the sentinel, so at most one file is ever created and it is obviously
// not the download.
func TestEveryRefusedNameEndsUpInOnePlace(t *testing.T) {
	root := tmpRoot(t)
	gate := New(root)

	// Bypass the up-front check deliberately, by asking the path maker the
	// question directly. This is what the library would do with a name the gate
	// refuses.
	maker := gate.filePathMaker()
	for _, hostile := range [][]string{
		{"..", "escape"},
		{"a", "..", "..", "b"},
		{"/etc/passwd"},
		{`..\..\windows`},
	} {
		name := filepath.Join(hostile...)
		got := maker(libstorage.FilePathMakerOpts{
			Info: &metainfo.Info{Name: "innocent"},
			File: &metainfo.FileInfo{Path: hostile, Length: 1},
		})
		if !strings.HasPrefix(got, root) {
			t.Errorf("the path maker returned %q for %q, which is outside the "+
				"download root %q", got, name, root)
		}
	}
}

// TestTheRootItselfBeingASymlinkIsStillAccepted, because the gate must not
// become the thing it is defending against.
//
// The operator symlinking their download root to a fast disk is a reasonable
// setup, and the root is resolved once by `New`. Refusing it would break that
// setup and, worse, would make this package look like it was written by someone
// who had heard of path traversal and nothing else.
func TestTheRootItselfBeingASymlinkIsStillAccepted(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("creating the real directory: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}

	gate := New(link)
	tl, err := gate.OpenTorrent(t.Context(), multiFile("innocent", "a.mkv"), testHash())
	if err != nil {
		t.Fatalf("a symlinked download root was refused: %v\n\n"+
			"  The root is the operator's and is resolved once. A torrent cannot "+
			"  influence it, and refusing it breaks a legitimate setup", err)
	}
	if tl.Close != nil {
		_ = tl.Close()
	}
}

// TestAMissingRootIsReported, not created.
//
// `EnsureRoot` is `internal/paths`' job. This constructor must not silently
// make a directory, because a caller that forgot to call it has a
// configuration bug and a downloader that hides that bug is a downloader whose
// data goes somewhere the operator did not choose.
func TestAMissingRootIsReported(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "never-created")

	gate := New(missing)
	_, err := gate.OpenTorrent(t.Context(), multiFile("t", "a.mkv"), testHash())
	if err == nil {
		t.Error("OpenTorrent accepted a download root that does not exist")
	}
	if _, statErr := os.Stat(missing); statErr == nil {
		t.Error("OpenTorrent created the download root. Creating it is " +
			"paths.EnsureRoot's job, and doing it here hides a configuration bug")
	}

	// A FILE where the download root should be. Same property -- the root has
	// to be a usable directory -- and a different failure: a `DataDir` pointing
	// at a file is a configuration mistake, and reporting it as a hostile
	// torrent sends the operator to look at the wrong thing entirely.
	asFile := filepath.Join(base, "a-file")
	if err := os.WriteFile(asFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("writing the decoy: %v", err)
	}
	if _, err := New(asFile).OpenTorrent(t.Context(),
		multiFile("t", "a.mkv"), testHash()); err == nil {
		t.Error("OpenTorrent accepted a regular file as the download root")
	}
}

// # TEST HELPERS
//
// `multiFile` builds a torrent the library will open. `Pieces` and `PieceLength`
// are filled in well enough for the storage layer to accept it, because the
// question every test here asks is what the storage DOES with the name, and a
// torrent rejected as malformed would answer it by saying nothing.

// multiFile is a one-file torrent whose name is given as a slash-separated path.
func multiFile(torrentName string, path string) *metainfo.Info {
	return &metainfo.Info{
		Name:        torrentName,
		Length:      1,
		PieceLength: 1,
		Pieces:      make([]byte, 20),
		Files:       []metainfo.FileInfo{{Length: 1, Path: strings.Split(path, "/")}},
	}
}

// multiFileComps is multiFile for a name given as components rather than a
// slash-separated string, for the cases where the splitting is the point --
// an absolute name, and one whose first component is a legitimate `..`.
func multiFileComps(torrentName string, path []string) *metainfo.Info {
	return &metainfo.Info{
		Name:        torrentName,
		Length:      1,
		PieceLength: 1,
		Pieces:      make([]byte, 20),
		Files:       []metainfo.FileInfo{{Length: 1, Path: path}},
	}
}

// testHash is a fixed info hash, so a refusal can be correlated with a torrent
// and a retry can be recognised as the same one.
func testHash() metainfo.Hash {
	var h metainfo.Hash
	for i := range h {
		h[i] = byte(i)
	}
	return h
}

// assertNothingCreated is the assertion that a refusal wrote nothing.
//
// Not "no file has the hostile name" — a walk of the root's PARENT, because the
// escape that matters is the one nobody would think to look for: something
// created above the root, or through a link.
func assertNothingCreated(t *testing.T, root string) {
	t.Helper()

	parent := filepath.Dir(root)
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("reading %q: %v", parent, err)
	}
	for _, e := range entries {
		p := filepath.Join(parent, e.Name())
		if p == root {
			continue
		}
		// t.TempDir cleans up its own subtree, so a sibling carrying one of
		// these names can only be an escape.
		if strings.Contains(e.Name(), "escape") || strings.Contains(e.Name(), "passwd") {
			t.Errorf("the gate created %q, outside the download root %q", p, root)
		}
	}
}

// tmpRoot is a real directory for a test, resolved.
//
// Real rather than a synthetic string, because half the gate is `EvalSymlinks`
// and half of the rest is "does this component exist". Testing either against a
// path that does not exist exercises a different code path than the one that
// runs in production.
func tmpRoot(t *testing.T) string {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("these fixtures use POSIX symlinks and modes; on Windows the " +
			"reserved-name cases still apply but the traversal fixtures do not " +
			"behave the same way")
	}

	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolving the temp root %q: %v", root, err)
	}
	return resolved
}

// TestANameOnlySanitizeJoinRefusesIsStillRefused covers the joined-name layer,
// which nothing else isolates.
//
// The per-component check and the joined-name `SanitizeJoin` both refuse `..`
// walks, so every test that used one also passed with the other removed -- two
// layers, one test, and the redundancy was only apparent. The name used here is
// one the per-component check ACCEPTS and `SanitizeJoin` refuses: a trailing dot
// (Windows strips it, so the name written is not the name checked) and a
// reserved device.
//
// The names are the two that came out of measuring the library's own function in
// ADR 0001, which is where they first showed up as a real corpus problem rather
// than a hypothetical.
func TestANameOnlySanitizeJoinRefusesIsStillRefused(t *testing.T) {
	root := tmpRoot(t)
	gate := New(root)

	for _, name := range []string{
		"trailing.",       // Windows strips it
		"trailing ",       // and so does a trailing space
		"dir/trailing.",   // in a subdirectory
		"con",             // a device, which silently resolves to the device
		"CON",             // case does not help
		"con.txt",         // the extension does not help
		"lpt1",            // another reserved name
		"with\x00nul.mkv", // and the NUL, which SanitizeJoin refuses too
	} {
		tl, err := gate.OpenTorrent(t.Context(), multiFile("innocent", name), testHash())
		if err == nil {
			t.Errorf("OpenTorrent accepted the file %q. The per-component check "+
				"passes it -- a name is a name -- and this is the layer that is "+
				"supposed to catch it", name)
			if tl.Close != nil {
				_ = tl.Close()
			}
		}
	}
}

// TestALibraryRefusalIsNotRecordedAsOurs: a torrent the GATE refuses never
// reaches the library, so the test that covers the library's own error path has
// to use a torrent the gate ACCEPTS.
//
// Getting this wrong is quiet and wrong in a specific way: a task report would
// say the gate refused a torrent, sending the operator to look at consent tiers
// and path handling when the actual cause was, say, a piece-completion database
// that would not open.
func TestALibraryRefusalIsNotRecordedAsOurs(t *testing.T) {
	root := tmpRoot(t)
	gate := New(root)

	// A torrent the gate accepts: one innocuous file. Close the gate's inner
	// storage so the library's OpenTorrent fails for a reason that is not ours.
	before := len(gate.Refusals())

	gate.inner = closedStorage{}
	_, err := gate.OpenTorrent(t.Context(), multiFile("innocent", "a.mkv"), testHash())
	if err == nil {
		t.Fatal("the test is not exercising the library's error path: " +
			"OpenTorrent succeeded against a storage that always fails")
	}
	if errors.Is(err, ErrRefused) {
		t.Errorf("the library's failure was reported as ErrRefused (%v). That is "+
			"our error: it means the caller will treat a broken piece database "+
			"as a hostile torrent", err)
	}

	if after := gate.Refusals(); len(after) != before {
		t.Errorf("the library's own refusal was recorded as ours: %d refusals, "+
			"expected %d. A report would then blame the gate for something it "+
			"did not do", len(after), before)
	}
}

// TestEveryBackstopRefusalIsKept is the zero-hash case, and it is a real gap the
// harness found.
//
// `record` is called from two places: the up-front check, which has the torrent's
// hash, and the backstop `FilePathMaker`, which does not -- a name arriving
// through the library carries no hash. So backstop refusals are all filed under
// the zero hash, and each one overwrites the last.
//
// The consequence is small but it is the kind of small thing that is only
// noticed after it has cost someone an afternoon: a torrent with two bad names
// reports the second one only, and the first is gone.
func TestEveryBackstopRefusalIsKept(t *testing.T) {
	root := tmpRoot(t)
	gate := New(root)
	maker := gate.filePathMaker()

	// Two levels of `..`, because the first is spent leaving the torrent's own
	// directory: `torrent/../first` cleans to `first`, which is INSIDE the root
	// and correctly accepted. One level here would test nothing and the test
	// would pass for the wrong reason.
	//
	// The names also come as components, not pre-joined, for the same reason
	// the gate checks components.
	for _, name := range [][]string{
		{"..", "..", "first"},
		{"..", "..", "..", "second"},
		{"..", "..", "..", "..", "third"},
	} {
		maker(libstorage.FilePathMakerOpts{
			Info: &metainfo.Info{Name: "torrent"},
			File: &metainfo.FileInfo{Length: 1, Path: name},
		})
	}

	got := gate.Refusals()
	if len(got) != 3 {
		t.Errorf("recorded %d backstop refusals, expected 3. They are filed "+
			"under no hash, so without care each one overwrites the last and a "+
			"torrent with two bad names reports only the second", len(got))
	}
}

// closedStorage is a `storage.ClientImpl` that always fails, so the library's
// error path can be reached without contriving a broken filesystem.
type closedStorage struct{}

func (closedStorage) OpenTorrent(
	context.Context, *metainfo.Info, metainfo.Hash,
) (libstorage.TorrentImpl, error) {
	return libstorage.TorrentImpl{}, errors.New("storage is closed (test fixture)")
}
