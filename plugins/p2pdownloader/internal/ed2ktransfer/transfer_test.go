package ed2ktransfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stashapp/stash-plugin-p2pdownloader/internal/ed2k"
	"github.com/stashapp/stash-plugin-p2pdownloader/internal/ed2kwire"
	"github.com/stashapp/stash-plugin-p2pdownloader/internal/paths"
)

// # WHAT THIS FILE IS ACTUALLY TESTING
//
// The invariant is about SEQUENCE, not about any single value: nothing is
// written until the bytes are proven, and a refusal happens before the write
// rather than after it. So the fakes here record the order things happened in
// and the tests assert on that, because a test that only checks "the file
// has the right bytes" passes just as happily if the file were written
// before the hash was computed.

// fakeSource is a stranger that answers with whatever it was given.
type fakeSource struct {
	// data is what the answer carries.
	data []byte

	// fileHash is what the answer CLAIMS the bytes are for. Distinct from
	// the data's real hash on purpose: a source can lie about either.
	fileHash [16]byte

	// err, if set, is returned instead of an answer.
	err error

	// asked records whether RequestPart was called at all, which is how a
	// test proves a refusal happened BEFORE the network was touched.
	asked bool

	// req is the request that was sent, for asserting the window.
	req ed2kwire.PartRequest

	// end, if non-zero, OVERRIDES the End the answer claims. It exists so
	// a test can make a source's header agree with a payload of any length
	// -- the liar case is about the HASH, and a fake whose End is
	// derived from the request cannot produce a header that matches
	// deliberately-wrong bytes.
	end uint32
}

func (f *fakeSource) RequestPart(ctx context.Context, r ed2kwire.PartRequest) (*ed2kwire.PartAnswer, error) {
	f.asked = true
	f.req = r
	if f.err != nil {
		return nil, f.err
	}
	end := f.end
	if end == 0 {
		end = r.End
	}
	return &ed2kwire.PartAnswer{
		FileHash: f.fileHash,
		Start:    r.Start,
		End:      end,
		Data:     f.data,
	}, nil
}

// root returns a fresh download root, and refuses to run if the root is not
// a directory -- a test that silently ran against the wrong root would make
// every assertion in it meaningless.
func root(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("t.TempDir() did not produce a directory: %v", err)
	}
	return dir
}

// newTransfer builds a Transfer over a fresh root, failing the test rather
// than returning an error -- a transfer that could not be built would make
// every later assertion vacuous.
func newTransfer(t *testing.T) (*Transfer, string) {
	t.Helper()
	dir := root(t)
	tr, err := New(Config{Root: dir})
	if err != nil {
		t.Fatalf("New(%q): %v", dir, err)
	}
	return tr, dir
}

// goodLocator is a well-formed one-part file link whose hash is the hash of
// its content, so the happy path is genuinely provable rather than
// accidentally satisfiable.
func goodLocator(t *testing.T, name string, data []byte) ed2k.Locator {
	t.Helper()
	return ed2k.Locator{
		Kind: ed2k.KindFile,
		Name: name,
		Size: int64(len(data)),
		Hash: ed2k.HashBytes(data),
	}
}

// listing is everything in dir, so a test can assert NOTHING was written
// rather than only asserting what was.
func listing(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %q: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// # THE HAPPY PATH, AND IT IS THE ONLY TEST THAT SHOULD WRITE ANYTHING

func TestAProvenFileIsWrittenAndNothingElseIs(t *testing.T) {
	tr, dir := newTransfer(t)
	data := []byte("the bytes of a file that will be proven")
	loc := goodLocator(t, "proven.bin", data)
	src := &fakeSource{data: data, fileHash: loc.Hash}

	got, err := tr.FetchOnePart(context.Background(), loc, src)
	if err != nil {
		t.Fatalf("FetchOnePart: %v", err)
	}

	if got.Path != filepath.Join(dir, "proven.bin") {
		t.Errorf("Path = %q, want %q", got.Path, filepath.Join(dir, "proven.bin"))
	}
	if got.Bytes != int64(len(data)) {
		t.Errorf("Bytes = %d, want %d", got.Bytes, len(data))
	}
	if got.Hash != loc.Hash {
		t.Errorf("Hash = %s, want %s", got.Hash, loc.Hash)
	}

	onDisk, err := os.ReadFile(got.Path)
	if err != nil {
		t.Fatalf("reading the written file: %v", err)
	}
	if string(onDisk) != string(data) {
		t.Errorf("the file on disk is %q, want %q", onDisk, data)
	}

	// # AND THE DIRECTORY HOLDS EXACTLY ONE FILE
	//
	// A temporary file left behind would be a part of somebody's file
	// sitting in their download directory under a dot name forever, and a
	// test that only checks the destination would not see it.
	if names := listing(t, dir); len(names) != 1 || names[0] != "proven.bin" {
		t.Errorf("the directory holds %v, want exactly [proven.bin] -- a "+
			"temporary file left behind is a real defect, not tidiness",
			names)
	}
}

// # THE CENTRAL INVARIANT: A MISMATCH IS REFUSED AND LEAVES NO FILE

func TestAHashMismatchLeavesNoFileAtAll(t *testing.T) {
	tr, dir := newTransfer(t)
	// The link says one hash; the source sends DIFFERENT bytes.
	loc := goodLocator(t, "liar.bin", []byte("what the link promised"))
	liar := []byte("what the source sent instead")
	src := &fakeSource{
		data:     liar,
		end:      uint32(len(liar)),
		fileHash: loc.Hash, // it even CLAIMS the right hash
	}

	_, err := tr.FetchOnePart(context.Background(), loc, src)
	if err == nil {
		t.Fatal("bytes for a different file were accepted and written")
	}
	if !errors.Is(err, ed2k.ErrHashMismatch) {
		t.Errorf("the error is %v, which is not ErrHashMismatch. A caller "+
			"must be able to tell bad bytes from a bad LINK", err)
	}

	// Nothing at all, not even a temporary.
	if names := listing(t, dir); len(names) != 0 {
		t.Errorf("the directory holds %v after a hash mismatch, want "+
			"nothing. A failed verification that leaves bytes on disk "+
			"is the defect this package exists to prevent", names)
	}
}

// # AND THE WRONG-FILE ANSWER IS CAUGHT BEFORE THE HASH IS EVEN COMPUTED

func TestAnAnswerForAnotherFileIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	tr, dir := newTransfer(t)
	data := []byte("bytes that belong to a different file entirely")
	loc := goodLocator(t, "wrong.bin", data)
	// The source answers with a part for a DIFFERENT file -- a real
	// protocol error a source can make.
	other := goodLocator(t, "other.bin", data)
	_ = other
	otherHash := [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	src := &fakeSource{data: data, fileHash: otherHash}

	_, err := tr.FetchOnePart(context.Background(), loc, src)
	if err == nil {
		t.Fatal("a part for a different file was accepted")
	}
	if !strings.Contains(err.Error(), "DIFFERENT file") {
		t.Errorf("the error does not say the source answered for the "+
			"wrong file: %v", err)
	}
	if names := listing(t, dir); len(names) != 0 {
		t.Errorf("the directory holds %v, want nothing -- the hash is "+
			"checked BEFORE the write, and this proves it", names)
	}
}

// # A HOSTILE NAME IS REFUSED BEFORE THE NETWORK IS TOUCHED AT ALL
//
// This is the strongest form of the ordering claim: not "no file was
// written" but "the stranger was never asked". A name that escapes the root
// is an attack, and an attack should not cost a network round trip.

func TestAHostileNameIsRefusedWithoutAskingTheSource(t *testing.T) {
	tr, dir := newTransfer(t)
	src := &fakeSource{data: []byte("anything")}

	loc := ed2k.Locator{
		Kind: ed2k.KindFile,
		Name: "../../etc/passwd",
		Size: 5,
		Hash: ed2k.HashBytes([]byte("anything")),
	}

	_, err := tr.FetchOnePart(context.Background(), loc, src)
	if err == nil {
		t.Fatal("a name escaping the root was accepted")
	}
	if !errors.Is(err, paths.ErrEscapes) {
		t.Errorf("the error is %v, which is not paths.ErrEscapes", err)
	}
	if src.asked {
		t.Error("the source was asked for bytes for an escaping name. The " +
			"name is the untrusted input and it is refused before the " +
			"network is touched")
	}
	if names := listing(t, dir); len(names) != 0 {
		t.Errorf("the directory holds %v, want nothing", names)
	}
}

// # ErrNoFile IS AN ABSENCE, NAMED AS ONE

func TestASourceThatDoesNotHaveTheFileSaysSoByName(t *testing.T) {
	tr, _ := newTransfer(t)
	loc := goodLocator(t, "absent.bin", []byte("bytes"))
	src := &fakeSource{err: ed2kwire.ErrNoFile{}}

	_, err := tr.FetchOnePart(context.Background(), loc, src)
	if err == nil {
		t.Fatal("a source saying it has no file was treated as success")
	}
	if !errors.Is(err, ErrNoSource) {
		t.Errorf("the error is %v, which is not ErrNoSource.\n\n"+
			"'no source has this file' and 'the transfer failed' are "+
			"opposite diagnoses. Retrying a missing file is pointless; "+
			"retrying a liar is the point", err)
	}
	if errors.Is(err, ErrFailed) {
		t.Error("an absence is also reported as a transfer failure, so the " +
			"two are not distinguishable by errors.Is")
	}
}

// # A TIMEOUT IS A FAILURE, NOT AN ABSENCE

func TestATransportErrorIsAFailureAndNotAnAbsence(t *testing.T) {
	tr, _ := newTransfer(t)
	loc := goodLocator(t, "timeout.bin", []byte("bytes"))
	src := &fakeSource{err: errors.New("i/o timeout")}

	_, err := tr.FetchOnePart(context.Background(), loc, src)
	if err == nil {
		t.Fatal("a transport error was treated as success")
	}
	if !errors.Is(err, ErrFailed) {
		t.Errorf("the error is %v, which is not ErrFailed", err)
	}
	if errors.Is(err, ErrNoSource) {
		t.Error("a transport failure is reported as an absence, which " +
			"would send a caller looking for the file rather than the " +
			"connection")
	}
}

// # A FOLDER LINK IS REFUSED, BY NAME

func TestAFolderLinkIsRefusedBecauseItNamesAFileList(t *testing.T) {
	tr, _ := newTransfer(t)
	src := &fakeSource{data: []byte("bytes")}
	loc := ed2k.Locator{
		Kind: ed2k.KindFolder,
		Name: "a folder",
		Size: 10,
		Hash: ed2k.HashBytes([]byte("bytes")),
	}

	_, err := tr.FetchOnePart(context.Background(), loc, src)
	if err == nil {
		t.Fatal("a folder link was accepted by a one-file transfer")
	}
	if !errors.Is(err, ErrNoSource) {
		t.Errorf("the error is %v, which is not ErrNoSource", err)
	}
	if src.asked {
		t.Error("the source was asked for a folder, which names a file " +
			"list and not a window of bytes")
	}
}

// # A SIZE OF ZERO, OR PAST 2^38, IS A LINK THAT CANNOT BE REAL

func TestAnImpossibleSizeIsRefusedBeforeAnythingIsAsked(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int64
	}{
		{"zero", 0},
		{"negative", -1},
		{"past eMule's maximum", maxFileSize + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, _ := newTransfer(t)
			src := &fakeSource{data: []byte("bytes")}
			loc := ed2k.Locator{
				Kind: ed2k.KindFile,
				Name: "impossible.bin",
				Size: tc.size,
				Hash: ed2k.HashBytes([]byte("bytes")),
			}

			_, err := tr.FetchOnePart(context.Background(), loc, src)
			if err == nil {
				t.Fatalf("a size of %d was accepted", tc.size)
			}
			if !errors.Is(err, ErrNoSource) {
				t.Errorf("the error is %v, which is not ErrNoSource", err)
			}
			if src.asked {
				t.Error("the source was asked for a file whose size is " +
					"impossible. The link is checked first because a " +
					"cooperating source cannot satisfy it")
			}
		})
	}
}

// # THE ANSWER'S OWN HEADER MUST AGREE WITH ITS PAYLOAD

func TestAnAnswerThatDisagreesWithItselfIsRefused(t *testing.T) {
	tr, dir := newTransfer(t)
	loc := goodLocator(t, "liar-header.bin", []byte("12345"))
	src := &fakeSource{data: []byte("12345"), fileHash: loc.Hash}

	// A source that says the window is 0-5 but carries 3 bytes. The
	// transfer's own check catches it, before the hash is computed.
	ans, err := src.RequestPart(context.Background(), ed2kwire.PartRequest{
		Start: 0, End: 5,
	})
	if err != nil {
		t.Fatalf("fake: %v", err)
	}
	ans.End = 8 // now it claims 8 bytes and carries 5

	_, err = tr.FetchOnePart(context.Background(), loc, disagreeingSource{ans})
	if err == nil {
		t.Fatal("an answer whose header disagrees with its payload was " +
			"accepted")
	}
	if !errors.Is(err, ErrFailed) {
		t.Errorf("the error is %v, which is not ErrFailed", err)
	}
	if !strings.Contains(err.Error(), "disagrees with its payload") {
		t.Errorf("the error does not name the fault: %v", err)
	}
	if names := listing(t, dir); len(names) != 0 {
		t.Errorf("the directory holds %v, want nothing", names)
	}
}

// disagreeingSource returns a pre-baked answer, so a test can make a source
// say something RequestPart would not.
type disagreeingSource struct{ ans *ed2kwire.PartAnswer }

func (d disagreeingSource) RequestPart(ctx context.Context, r ed2kwire.PartRequest) (*ed2kwire.PartAnswer, error) {
	return d.ans, nil
}

// # A SOURCE THAT RETURNS NOTHING IS NOT A SUCCESS

func TestASourceReturningNoAnswerAtAllIsRefused(t *testing.T) {
	tr, dir := newTransfer(t)
	loc := goodLocator(t, "silent.bin", []byte("bytes"))

	_, err := tr.FetchOnePart(context.Background(), loc, nilSource{})
	if err == nil {
		t.Fatal("a source that returned nothing was treated as success")
	}
	if !errors.Is(err, ErrFailed) {
		t.Errorf("the error is %v, which is not ErrFailed", err)
	}
	if names := listing(t, dir); len(names) != 0 {
		t.Errorf("the directory holds %v, want nothing", names)
	}
}

// nilSource returns (nil, nil), which a careless implementation would read as
// a successful empty transfer.
type nilSource struct{}

func (nilSource) RequestPart(ctx context.Context, r ed2kwire.PartRequest) (*ed2kwire.PartAnswer, error) {
	return nil, nil
}

// # AN EXISTING FILE IS NOT OVERWRITTEN
//
// This transfer has no resume index, so it cannot tell whether a file at the
// destination is the same one, a previous version, or something else. A
// transfer that guesses can destroy an edit.

func TestAnExistingFileIsNotReplaced(t *testing.T) {
	tr, dir := newTransfer(t)
	const existing = "what the operator put here"
	path := filepath.Join(dir, "existing.bin")
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	data := []byte("the newly fetched bytes")
	loc := goodLocator(t, "existing.bin", data)
	src := &fakeSource{data: data, fileHash: loc.Hash}

	_, err := tr.FetchOnePart(context.Background(), loc, src)
	if err == nil {
		t.Fatal("an existing file was replaced by a fetched one")
	}
	if !errors.Is(err, ErrFailed) {
		t.Errorf("the error is %v, which is not ErrFailed", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the existing file: %v", err)
	}
	if string(after) != existing {
		t.Errorf("the existing file is now %q, want %q -- a transfer with "+
			"no resume index cannot know this is the same file, and "+
			"guessing here destroys work", after, existing)
	}
	// And no temporary left behind either.
	if names := listing(t, dir); len(names) != 1 || names[0] != "existing.bin" {
		t.Errorf("the directory holds %v, want exactly [existing.bin]", names)
	}
}

// # THE WINDOW REQUESTED IS THE WHOLE ONE-PART FILE

func TestTheWindowIsTheWholeFileAndNotAPartSize(t *testing.T) {
	tr, _ := newTransfer(t)
	// A file SMALLER than a part: the window must be the file's own
	// length, not PartSize. Asking for 9,728,000 bytes of a 5-byte file
	// gets silence from any cooperating source.
	data := []byte("12345")
	loc := goodLocator(t, "small.bin", data)
	src := &fakeSource{data: data, fileHash: loc.Hash}

	if _, err := tr.FetchOnePart(context.Background(), loc, src); err != nil {
		t.Fatalf("FetchOnePart: %v", err)
	}
	if src.req.Start != 0 {
		t.Errorf("the window starts at %d, want 0", src.req.Start)
	}
	if int64(src.req.End) != int64(len(data)) {
		t.Errorf("the window ends at %d, want %d (the file's own length)",
			src.req.End, len(data))
	}
	if src.req.End > ed2kwire.PartSize {
		t.Errorf("the window ends at %d, which is past PartSize %d",
			src.req.End, ed2kwire.PartSize)
	}
}

// # THE HASH SENT IS THE LINK'S

func TestTheHashRequestedIsTheLinksOwn(t *testing.T) {
	tr, _ := newTransfer(t)
	data := []byte("bytes")
	loc := goodLocator(t, "hashed.bin", data)
	src := &fakeSource{data: data, fileHash: loc.Hash}

	if _, err := tr.FetchOnePart(context.Background(), loc, src); err != nil {
		t.Fatalf("FetchOnePart: %v", err)
	}
	if src.req.FileHash != loc.Hash {
		t.Errorf("the request carries hash %x, want the link's %x",
			src.req.FileHash, loc.Hash)
	}
}

// # New REFUSES A ROOT THAT CANNOT HOLD FILES

func TestNewRefusesARootThatCannotBeUsed(t *testing.T) {
	t.Run("no root", func(t *testing.T) {
		if _, err := New(Config{}); err == nil {
			t.Error("a Transfer was built with no root, so there is " +
				"nowhere a file may be written")
		}
	})

	t.Run("a missing root", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "not-created")
		_, err := New(Config{Root: missing})
		if err == nil {
			t.Fatal("a Transfer was built over a root that does not exist")
		}
		// It must not have created it: a transfer that made its own root
		// would make it somewhere a peer could have influenced.
		if _, statErr := os.Stat(missing); statErr == nil {
			t.Error("New created the root. paths.EnsureRoot does that, and " +
				"doing it here invents a directory nobody asked for")
		}
	})

	t.Run("a file where a root should be", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "a-file")
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("seeding: %v", err)
		}
		_, err := New(Config{Root: path})
		if err == nil {
			t.Fatal("a Transfer was built over a FILE")
		}
		if !strings.Contains(err.Error(), "not a directory") {
			t.Errorf("the error does not say the root is a file: %v", err)
		}
	})
}

// # A SUBSEQUENT FETCH OF THE SAME FILE IS A SEPARATE CONVERSATION
//
// Not a test of a feature -- a test that the first file stays intact.

func TestAFetchDoesNotDisturbAnEarlierFile(t *testing.T) {
	tr, dir := newTransfer(t)

	first := []byte("the first file's bytes")
	loc1 := goodLocator(t, "first.bin", first)
	if _, err := tr.FetchOnePart(context.Background(), loc1,
		&fakeSource{data: first, fileHash: loc1.Hash}); err != nil {
		t.Fatalf("first fetch: %v", err)
	}

	second := []byte("the second file's bytes, different")
	loc2 := goodLocator(t, "second.bin", second)
	if _, err := tr.FetchOnePart(context.Background(), loc2,
		&fakeSource{data: second, fileHash: loc2.Hash}); err != nil {
		t.Fatalf("second fetch: %v", err)
	}

	if names := listing(t, dir); len(names) != 2 {
		t.Errorf("the directory holds %v, want two files", names)
	}
	firstBack, err := os.ReadFile(filepath.Join(dir, "first.bin"))
	if err != nil {
		t.Fatalf("reading the first file: %v", err)
	}
	if string(firstBack) != string(first) {
		t.Errorf("the first file is now %q, want %q", firstBack, first)
	}
}
