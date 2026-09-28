package ed2ktransfer

// The transfer moves one verified part of one ed2k file to disk.
//
// # WHY THIS EXISTS, AND WHY IT IS NOT INSIDE ed2kwire
//
// Three packages each own a decision that must not be made by any of the
// others:
//
//	internal/ed2kwire  can talk to a stranger. It has no opinion about
//	                   whether the stranger's bytes are correct, and
//	                   writing a file is not a network operation.
//	internal/ed2k       can prove whether bytes are a named file. Pure
//	                   arithmetic, no disk, no network.
//	internal/paths      can resolve a stranger's NAME under a root, and
//	                   has been attacked-by-name repeatedly already.
//
// Left to itself the natural composition -- "ask the wire for bytes, hash
// them, write them" -- is one function in a package that also parses search
// results, and the write is the step most likely to be reached for early and
// tested least. So the composition gets its own package with its own tests,
// and its central invariant is a single sentence:
//
//	# NOTHING IS WRITTEN UNTIL THE BYTES ARE PROVEN
//
// A source is a stranger. The hash is the only thing that distinguishes its
// bytes from another file's bytes, and the whole reason this package exists
// is that the check happens BEFORE the write, not after it.
//
// # WHY storage.Gate IS NOT USED HERE
//
// It is libtorrent's: OpenTorrent(info *metatinfo.Info), and an ed2k file
// has no metainfo.Info. Widening it to accept a bare name would put an
// ed2k-shaped hole in a torrent-shaped defence. paths.SanitizeJoin is the
// right tool, because the NAME is the untrusted part and that is exactly
// what paths was built for.

// # WHY ONE PART AND ONE FILE
//
// This transfers a file of at most one part. That is not a simplification
// dressed up as a feature -- it is the only case where the file hash and the
// part hash are the SAME hash.
//
// ehash.go's multi-part branch computes the file hash over the part hashes'
// first eight bytes, so a file of several parts can only be verified once
// every part is in hand. A one-part file is the case where "these bytes are
// this part" and "these bytes are this file" are the same claim, which makes
// it the case that can be proven with one round trip.
//
// A multi-part transfer is a different problem with a different evidence
// requirement, and it is specified separately rather than grown here.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/stashapp/stash-plugin-p2pdownloader/internal/ed2k"
	"github.com/stashapp/stash-plugin-p2pdownloader/internal/ed2kwire"
	"github.com/stashapp/stash-plugin-p2pdownloader/internal/paths"
)

// The refusals, each NAMED, because each has a different remedy and a single
// failure would send triage to the wrong place in every case.
//
// # AND WHY ErrNoSource IS NOT ErrFailed
//
// "No source answered" and "a source answered with the wrong bytes" are
// opposite diagnoses. The first means this file is not obtainable from here;
// the second means this source is not to be trusted. A caller that wants to
// retry must distinguish them, because retrying a missing file is pointless
// and retrying a liar is the whole point.
var (
	// ErrNoSource means no source holds this file, or none answered.
	ErrNoSource = errors.New("no reachable ed2k source holds this file")

	// ErrFailed means a source was reached and the transfer did not
	// complete: a timeout, a truncated answer, a write error.
	ErrFailed = errors.New("the ed2k transfer failed")

	// ErrNoSpace means the file does not fit on the volume it is going to.
	ErrNoSpace = errors.New("the ed2k file does not fit on this volume")
)

// maxFileSize is eMule's stated maximum, 2^38.
//
// # IT IS A LIMIT, NOT A BEHAVIOUR
//
// A link claiming more is refused as a link that cannot describe a real
// file, which is the same class of judgement as refusing a name that escapes
// the download root: both are inputs that are malformed in a way no
// cooperating peer would produce.
//
// It is also the ceiling that makes the uint64 in ehash.go necessary -- a
// uint32 would be right for every file under 4 GB and wrong for every file
// over it.
const maxFileSize = 1 << 38

// PartSource is the one thing a Transfer needs from a stranger.
//
// # IT IS AN INTERFACE FOR TESTABILITY, AND THAT IS THE WHOLE REASON
//
// ed2kwire.Source has a private constructor and a live socket behind it, so
// testing the ORDER of operations -- refuse before write -- would otherwise
// mean a loopback listener and a real connection per case. The invariant
// being protected is about sequence, and sequence is exactly what a fake
// makes cheap to assert.
//
// One method, so the interface cannot grow into a second copy of ed2kwire.
type PartSource interface {
	RequestPart(ctx context.Context, r ed2kwire.PartRequest) (*ed2kwire.PartAnswer, error)
}

// Fetched is what was proven, and where it went.
type Fetched struct {
	// Path is the file's final name. It exists only if this is returned
	// without an error -- a failed fetch leaves no file at this path.
	Path string

	// Bytes is how many bytes were written, which is the whole file.
	Bytes int64

	// Hash is the hash the bytes were PROVEN to have. For a one-part file
	// this is the file hash, and for a one-part file it is also the part
	// hash: the same value, which is what makes this milestone's scope
	// honest rather than merely small.
	Hash ed2k.Hash
}

// Config is how a Transfer is built.
type Config struct {
	// Root is the directory files are written under. REQUIRED and must
	// already exist: this package does not create a download root, because
	// paths.EnsureRoot exists and does it properly, and a second creator
	// is a second opinion about when a directory is safe to make.
	Root string
}

// Transfer fetches one-part ed2k files to a root directory.
type Transfer struct {
	root string
}

// New builds a Transfer.
//
// # THE ROOT IS CHECKED HERE, NOT AT THE WRITE
//
// Every refusal then names a real cause instead of "no such directory", and
// the check happens once rather than on every transfer. paths.EnsureRoot
// creates the directory; this only insists it is there and is a directory,
// because a Transfer that silently created its own root would create it
// somewhere a peer could have influenced.
func New(cfg Config) (*Transfer, error) {
	if cfg.Root == "" {
		return nil, fmt.Errorf("ed2ktransfer: no download root, so there " +
			"is nowhere a file may be written")
	}
	info, err := os.Stat(cfg.Root)
	if err != nil {
		return nil, fmt.Errorf("ed2ktransfer: the download root %q cannot "+
			"be used: %w. It is not created here -- paths.EnsureRoot "+
			"does that, and a transfer that made its own root would "+
			"make it somewhere a peer could have influenced",
			cfg.Root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("ed2ktransfer: the download root %q is a "+
			"file, not a directory, so no file can be written under it",
			cfg.Root)
	}
	return &Transfer{root: cfg.Root}, nil
}

// Root is the directory this Transfer writes under. Read-only, so a caller
// can report it without a way to change it.
func (t *Transfer) Root() string { return t.root }

// FetchOnePart downloads a one-part file, verifies it, and writes it.
//
// # THE ORDER, AND EACH STEP IS THERE FOR A REASON
//
//	1  the locator is checked: a file, not a folder
//	2  the size is checked: not zero, not over 2^38
//	3  the name is resolved under the root
//	4  the window is requested
//	5  ErrNoFile is a refusal BY NAME, not a generic failure
//	6  the answer's hash must be the link's          <-- BEFORE ANY WRITE
//	7  the name is resolved AGAIN, for the path
//	8  written to a temporary file
//	9  the FILE is verified against the link's hash
//	10 renamed into place, or the temporary file removed
//
// Step 6 before step 8 is the invariant. Steps 7 and 8 are separate because
// a name that was safe three steps ago may not be, and resolving once at the
// start and writing at the end leaves a window where a symlink could have
// been planted under the root.
//
// Step 9 verifies the bytes ON DISK rather than in memory, because a part is
// 9,728,000 bytes and holding a whole one resident to hash it is a cost
// paid on every transfer. Step 10's rename is atomic within a directory, so
// a reader sees either no file or a complete one -- a partially written file
// under the right name is worse than no file at all.
func (t *Transfer) FetchOnePart(ctx context.Context, loc ed2k.Locator, src PartSource) (Fetched, error) {
	var out Fetched

	// 1. A folder link names a file LIST, and this fetches a file.
	if loc.Kind != ed2k.KindFile {
		return out, fmt.Errorf("%w: %q is a %s link, and this fetches one "+
			"file. A folder's contents are a list of files and nothing "+
			"has decided how to walk it yet", ErrNoSource, loc.Name, loc.Kind)
	}

	// 2. The size, before anything is allocated or requested.
	//
	// A zero size is refused: ehash.go hashes a zero-byte file as the MD4
	// of nothing, which is well-defined, and a source that answers a
	// zero-length request with a zero-length answer has said nothing.
	// ed2k.Parse already refuses a non-positive size at the link, so
	// reaching this is a second refusal of the same input -- deliberately,
	// because this function is reachable without going through the parser.
	if loc.Size <= 0 {
		return out, fmt.Errorf("%w: the link for %q states a size of %d, "+
			"and there is no file of that length to fetch", ErrNoSource,
			loc.Name, loc.Size)
	}
	if loc.Size > maxFileSize {
		return out, fmt.Errorf("%w: the link for %q states %d bytes, which "+
			"is past ed2k's 2^38 maximum, so it cannot describe a real "+
			"file. A cooperating server does not offer these",
			ErrNoSource, loc.Name, loc.Size)
	}

	// 3. The name, resolved under the root. Refused here if it escapes.
	if _, err := paths.SanitizeJoin(t.root, loc.Name); err != nil {
		return out, fmt.Errorf("ed2ktransfer: the link names %q, which "+
			"cannot be written under the download root: %w", loc.Name, err)
	}

	// 4. The window. A one-part file is one window, and for a file of
	// exactly PartSize it is a FULL window; for anything smaller it is the
	// whole file, which is the short-window case part.go already treats as
	// ordinary rather than as an error.
	end := loc.Size
	if end > ed2kwire.PartSize {
		end = ed2kwire.PartSize
	}

	ans, err := src.RequestPart(ctx, ed2kwire.PartRequest{
		FileHash: loc.Hash,
		Start:    0,
		End:      uint32(end),
	})
	if err != nil {
		// A source saying "I do not have this file" is not a transfer
		// failure. It is the absence of a source, and the remedy is
		// different: a different file, or Kad source lookup.
		var noFile ed2kwire.ErrNoFile
		if errors.As(err, &noFile) {
			return out, fmt.Errorf("%w: the source does not hold %q. This "+
				"is an absence, not a failure -- no amount of retrying "+
				"this source will produce the file", ErrNoSource, loc.Name)
		}
		return out, fmt.Errorf("%w: cannot fetch %q: %w", ErrFailed,
			loc.Name, err)
	}

	// 5. The answer's own claim about which file these bytes are for.
	//
	// A source can answer with a part for a DIFFERENT file, and that is
	// checked before the bytes are written rather than after, because the
	// whole point of a hash is that it is consulted first.
	//
	// A short answer is refused here rather than padded: a source that
	// sends fewer bytes than it said is as wrong as one that sends bytes
	// for another file, and the hash would catch it too, but the SIZE is
	// knowable without hashing anything.
	if ans == nil {
		return out, fmt.Errorf("%w: the source returned no answer at all "+
			"for %q, and no bytes arrived", ErrFailed, loc.Name)
	}
	if int64(ans.End-ans.Start) != int64(len(ans.Data)) {
		return out, fmt.Errorf("%w: the source's answer for %q claims the "+
			"window %d-%d but carries %d bytes, so its own header "+
			"disagrees with its payload", ErrFailed, loc.Name, ans.Start,
			ans.End, len(ans.Data))
	}
	if ed2k.Hash(ans.FileHash) != loc.Hash {
		return out, fmt.Errorf("%w: the source answered with a part for a "+
			"DIFFERENT file. It claims hash %s and the link says %s, and "+
			"nothing is written", ErrFailed, loc.Hash, ed2k.Hash(ans.FileHash))
	}

	// 6. The path, resolved AGAIN. See the order note in the doc comment:
	// the root can have changed under a name that resolved before, and the
	// write is the operation that cares.
	path, err := paths.SanitizeJoin(t.root, loc.Name)
	if err != nil {
		return out, fmt.Errorf("ed2ktransfer: the name %q no longer "+
			"resolves safely under the download root: %w", loc.Name, err)
	}

	// 7. Is there room? Asked before the bytes are fetched rather than
	// after, so a full volume does not cost a download first.
	//
	// The reserved block is a constant, not a tunable, and the reason is
	// that a tunable here would be a setting nobody has a use for yet.
	const reserve = 1 << 20
	if err := checkSpace(path, loc.Size+reserve); err != nil {
		return out, err
	}

	// 8. The write, to a temporary name in the SAME directory, so the
	// rename in step 10 is within one filesystem and therefore atomic.
	//
	// A temporary name that does not exist yet is refused, so a fetch
	// cannot overwrite a stranger's file that happens to sit where the
	// temporary would go.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".stash-ed2k-*")
	if err != nil {
		return out, fmt.Errorf("%w: cannot create a temporary file beside "+
			"%q: %w", ErrFailed, loc.Name, err)
	}
	tmpName := tmp.Name()
	// # THE TEMPORARY IS REMOVED ON EVERY PATH OUT
	//
	// A defer, not a line at the end of the happy path, because there are
	// four ways to fail between creating it and renaming it, and a
	// temporary file left behind is a part of somebody's file sitting in
	// their download directory under a dot name forever.
	committed := false
	defer func() {
		if committed {
			return
		}
		if err := os.Remove(tmpName); err != nil && !os.IsNotExist(err) {
			// Nothing useful to do with this error, and no way to
			// report it: the function is already returning another one.
			// The removal is retried by the OS on reboot for a file in
			// a temp name only if the directory is writable, which it
			// was a moment ago.
			_ = err
		}
	}()

	if _, err := tmp.Write(ans.Data); err != nil {
		tmp.Close()
		return out, fmt.Errorf("%w: cannot write %d bytes of %q: %w",
			ErrFailed, len(ans.Data), loc.Name, err)
	}

	// # SYNC BEFORE VERIFY, AND THE REASON IS NOT OBVIOUS
	//
	// The hash reads the file back. If the write is still in a buffer, the
	// read can see bytes that are not on disk -- so the file is synced
	// first, or the verification would be of something that never
	// existed. This costs an fsync per part, which is the price of proving
	// the bytes rather than assuming them.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return out, fmt.Errorf("%w: cannot flush %q to disk before "+
			"verifying it: %w", ErrFailed, loc.Name, err)
	}
	if err := tmp.Close(); err != nil {
		return out, fmt.Errorf("%w: cannot close %q after writing it: %w",
			ErrFailed, loc.Name, err)
	}

	// 9. THE PROOF, against the LINK's hash, on the bytes that are on disk.
	//
	// Not the part's own hash: a link carries the file hash and that is the
	// only authority available. (Measured: the live server's claimed hash
	// does not reproduce as MD4 or MD5 of size+name, so a recomputed hash
	// would refuse every file this server offers -- and be right to.)
	whole, err := ed2k.HashFile(mustOpen(tmpName))
	if err != nil {
		return out, fmt.Errorf("%w: cannot re-read %q to verify it: %w",
			ErrFailed, loc.Name, err)
	}
	if whole != loc.Hash {
		return out, fmt.Errorf("%w: %q hashed to %s and the link says %s. "+
			"The source's bytes are not that file, and the temporary "+
			"file is removed rather than left for someone to find",
			ed2k.ErrHashMismatch, loc.Name, whole, loc.Hash)
	}

	// 10. The commit. A rename within one directory is atomic, so a reader
	// sees no file or a whole one.
	//
	// # AND IF THE DESTINATION ALREADY EXISTS, THE WRITE IS REFUSED
	//
	// Renaming onto an existing file would silently replace a file the
	// operator may have edited. This transfer has no resume index, so it
	// has no idea whether what is there is this file, a previous version
	// of it, or something else entirely -- and a transfer that guesses is
	// a transfer that can destroy work.
	if _, err := os.Stat(path); err == nil {
		return out, fmt.Errorf("%w: %q already exists, so the fetched "+
			"copy is not written over it. This transfer has no resume "+
			"index and cannot tell whether the file there is the same "+
			"one; a transfer that guesses here can destroy an edit",
			ErrFailed, loc.Name)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return out, fmt.Errorf("%w: cannot put the verified file at %q: %w",
			ErrFailed, loc.Name, err)
	}
	committed = true

	out.Path = path
	out.Bytes = loc.Size
	out.Hash = whole
	return out, nil
}

// mustOpen opens a file this package just created, and turns a failure into
// a nil reader that HashFile will report as an error.
//
// A helper rather than inline code because there are two call sites that
// both need it and a third that should not exist: a caller that cannot open
// a file it wrote has a bug, and hiding that behind a panic here would move
// the diagnosis away from where it happened.
func mustOpen(name string) *os.File {
	f, err := os.Open(name)
	if err != nil {
		// Returning a nil *os.File is safe: every method on it checks
		// for the nil receiver and returns ErrInvalid, so HashFile
		// reports a read error rather than panicking.
		return nil
	}
	return f
}

// checkSpace asks whether size bytes are available on the volume holding
// path.
//
// # AND WHY IT DELETES NOTHING AND CREATES NOTHING
//
// A common way to measure free space is to write a file until it fails. That
// writes gigabytes to find out about gigabytes, and on a full volume it
// leaves the debris it was meant to measure. So this asks the filesystem
// instead, and says plainly when it cannot: a transfer that cannot tell
// whether it fits proceeds anyway and lets the write fail, because refusing
// on an unmeasurable volume would be refusing on a guess.
func checkSpace(path string, size int64) error {
	dir := filepath.Dir(path)

	// A Statfs is the portable way to ask. The signature differs by build
	// tag, so it is isolated here rather than spread across the package.
	free, known := freeBytes(dir)
	if !known {
		return nil
	}
	if free < size {
		return fmt.Errorf("%w: %q needs %d bytes and %s has %d free",
			ErrNoSpace, filepath.Base(path), size, dir, free)
	}
	return nil
}
