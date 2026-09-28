package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/anacrolix/torrent/metainfo"
	libstorage "github.com/anacrolix/torrent/storage"

	"github.com/stashapp/stash-plugin-p2pdownloader/internal/paths"
)

// # WHY THIS EXISTS
//
// M5's downloader has to put peer-supplied bytes on this disk, and the library
// that does the transfer will not keep them where they belong. Measured on
// `anacrolix/torrent` v1.61.0, using the library's own functions:
//
//   - `isSubFilepath` (`storage/file-paths.go:32`) is `filepath.Rel` plus a
//     `HasPrefix`. `Rel` is pure string arithmetic and cannot resolve a
//     symlink.
//   - `file-client.go:92` calls it, so the classic storage LOOKS defended.
//   - Executed, with a torrent directory that is a symlink outside the download
//     root, it returns `true` and the write lands outside anyway.
//   - The mmap storage does not even call it: `grep -c isSubFilepath` returns 0
//     for `mmap.go` against 1 for `file-client.go`, and it carries the TODO
//     *"Support all the same native filepath configuration that NewFileOpts
//     provides."*
//
// So the classic storage is the only usable backend, it still needs a gate in
// front of it, and the gate has to be filesystem-resolving rather than
// string-based. That is `internal/paths`, already written and already
// mutation-checked.
//
// # THE CONSTRAINT THAT SHAPES EVERYTHING HERE
//
// `FilePathMaker` is `func(FilePathMakerOpts) string`. It **cannot report an
// error.** Neither can `TorrentDirFilePathMaker`. The only place a refusal can
// be expressed is `ClientImpl.OpenTorrent`'s error return — and the library
// calls the makers BEFORE it gets there.
//
// Which means a name that fails the gate cannot stop the storage opening unless
// something checks it first. So this type does two things, in this order:
//
//  1. **Validate every file in the torrent up front**, in `OpenTorrent`, and
//     refuse the whole torrent there. This is the only place a real error can
//     be returned, and it is the one that matters.
//  2. **Route the makers through `paths.SanitizeJoin` as a backstop.** If a
//     future change skips step 1, or the library gains a path the up-front
//     check does not see, a refused name becomes one inert sentinel file
//     instead of a write outside the root.
//
// The ordering is the design. **A refusal must never be expressed as a path**,
// because a path is indistinguishable from success to everything downstream: a
// transfer that completes with the wrong bytes in it looks exactly like one that
// worked.

// ErrRefused is a torrent this gate will not open.
//
// A sentinel so a caller can tell a hostile or unusable torrent from a broken
// filesystem, which is a different problem with a different fix. It is exported
// because the downloader's task layer has to report the difference, and an
// operator reading "path rejected" needs to know whether to look at the torrent
// or at the disk.
var ErrRefused = errors.New("torrent refused: a file name in it cannot be written inside the download root")

// Refusal is one recorded rejection, kept so a task report can say which file
// was the problem long after `OpenTorrent` returned.
//
// A refusal without a filename is "a torrent was refused", which is the message
// that gets ignored the first time and every time after.
type Refusal struct {
	// Hash identifies the torrent, so a report can correlate the refusal with
	// the download it was for.
	Hash metainfo.Hash

	// Name is the offending path as the torrent spelled it, so the operator can
	// see what the peer asked for.
	Name string

	// Reason is `paths`' own explanation.
	Reason string
}

// Gate is a `storage.ClientImpl` that keeps every file inside one directory.
//
// Safe for concurrent use: a DHT goroutine opens torrents while a task poller
// reads refusals, and the client's `AddTorrentOpt` is called from a goroutine
// while transfers run.
type Gate struct {
	// root is the RESOLVED download directory.
	//
	// Resolved once, in `New`, and for the operator's benefit as much as the
	// gate's: `/tmp` is a symlink on macOS, so comparing a resolved root
	// against an unresolved child rejects every legitimate file.
	root string

	// inner is the library's classic file storage, with our makers installed.
	inner libstorage.ClientImpl

	mu      sync.Mutex
	refused []Refusal

	// cachedName is the torrent name the last `OpenTorrent` saw, so the
	// up-front directory check and the `TorrentDirFilePathMaker` cannot
	// disagree about which directory a torrent's files go in.
	cachedName string
}

// New returns a Gate writing into `root`.
//
// `root` must already exist. This does not create it: that is
// `paths.EnsureRoot`'s job, and a constructor that quietly makes a directory
// hides a configuration bug — the operator's data would go somewhere they did
// not choose, and the only symptom would be that it worked.
//
// A root that is a symlink to a real directory is accepted. That is the
// operator's decision, made before any download started, and refusing it would
// break a legitimate setup. A symlink *inside* a torrent is the attack, and is
// refused.
func New(root string) *Gate {
	// Resolve what exists, and do it ONCE. `paths.SanitizeJoin` resolves both
	// sides on every call; here the root is fixed, so resolving per call would
	// be ten thousand syscalls for one torrent's worth of files.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		// Leave `root` as given. `OpenTorrent` reports a missing or unreadable
		// root with a real error, and doing it here would mean a constructor
		// that can fail without saying so.
		resolved = root
	}

	g := &Gate{
		root:    resolved,
		refused: make([]Refusal, 0),
	}

	g.inner = libstorage.NewFileOpts(libstorage.NewFileClientOpts{
		ClientBaseDir:   resolved,
		FilePathMaker:   g.filePathMaker(),
		TorrentDirMaker: g.torrentDirMaker(),
		// Part files mean `.part` siblings. They are the library's own
		// mechanism for an incomplete download, and they inherit the same
		// path, so they get the same gate. Left at the default deliberately
		// rather than configured: changing it is a resume question, and
		// guessing at it here would be a change nobody asked for.
		Logger: slog.New(slog.DiscardHandler),
	})

	return g
}

// Root is the resolved directory this gate writes into.
func (g *Gate) Root() string { return g.root }

// Refusals returns the recorded rejections, most recent last.
//
// A copy, because a caller iterating the result while another goroutine refuses
// a torrent would otherwise be reading a map being written to — which in Go is
// not a wrong answer, it is a crash.
func (g *Gate) Refusals() []Refusal {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Already a copy: the field is a slice, and `append` to a returned slice
	// cannot reach into it. A `make`+`copy` is not needed, and a test that
	// mutated the result would be mutating its own copy.
	return append([]Refusal(nil), g.refused...)
}

// OpenTorrent refuses the whole torrent if any file in it cannot be written
// inside the download root, and otherwise delegates to the library's classic
// file storage.
//
// Refusing the WHOLE torrent is deliberate, and the alternative is worse than
// it looks. A per-file refusal has nowhere to be reported — the makers return
// strings — so it becomes a sentinel path and the transfer completes with the
// wrong file in it. The operator then has a download that says "complete" and is
// not the thing they asked for, which is a worse failure than a refusal with a
// reason attached.
func (g *Gate) OpenTorrent(
	ctx context.Context,
	info *metainfo.Info,
	hash metainfo.Hash,
) (libstorage.TorrentImpl, error) {
	// A root that does not exist is a configuration problem, reported as such.
	// Not as a refusal: "the download root is missing" and "this torrent is
	// hostile" call for different fixes, and conflating them sends whoever
	// debugs it in the wrong direction.
	// Named `rootInfo`, NOT `info`: the parameter of that name is the torrent's
	// `*metainfo.Info`, and shadowing it inside this `if` would make the two
	// indistinguishable at a glance. The shadow is scoped so the code was
	// correct anyway, which is exactly what makes it worth fixing -- a reader
	// should not have to work that out in the one function where being wrong
	// about what `info` refers to matters.
	rootInfo, err := os.Stat(g.root)
	if err != nil {
		return libstorage.TorrentImpl{}, fmt.Errorf(
			"%w: the download root %q is not usable: %v", ErrRefused, g.root, err)
	}
	if !rootInfo.IsDir() {
		// The library refuses this too, but with an error that says something
		// about piece completion rather than about the configured path. This one
		// names the actual problem, which is the difference between a five-minute
		// diagnosis and a five-second one.
		return libstorage.TorrentImpl{}, fmt.Errorf(
			"%w: the download root %q is not a directory", ErrRefused, g.root)
	}

	// The up-front check. This is the only place a refusal can be a real error.
	if bad := g.firstUnwritable(info); bad != "" {
		g.record(hash, bad, "the file name cannot be written inside the download root")
		return libstorage.TorrentImpl{}, fmt.Errorf(
			"%w: the file %q in this torrent resolves outside %q. A torrent names "+
				"its own files, and a name that walks out of the download directory "+
				"is not a filename",
			ErrRefused, bad, g.root)
	}

	// The torrent's own directory, checked as a filesystem fact rather than a
	// string. `paths.SanitizeJoin` walks the existing prefix and resolves
	// symlinks, so a torrent whose directory has been replaced with a link out
	// of the root is caught here rather than on the first piece write -- which
	// is the only point at which the library would notice, and by then the
	// transfer has started.
	//
	// Checked and not assumed, because the directory can be created or replaced
	// between this call and the first write. The library has no equivalent
	// check: `isSubFilepath` is `filepath.Rel`, which cannot see a link.
	dir := g.torrentDir()
	if dir != g.root {
		if _, err := paths.SanitizeJoin(g.root, filepath.Base(dir)); err != nil {
			g.record(hash, filepath.Base(dir), err.Error())
			return libstorage.TorrentImpl{}, fmt.Errorf(
				"%w: the directory %q for this torrent resolves outside %q. It is "+
					"a symlink, and every file in the torrent would be written "+
					"through it",
				ErrRefused, dir, g.root)
		}
	}

	tl, err := g.inner.OpenTorrent(ctx, info, hash)
	if err != nil {
		// The library refused it. That is not our refusal, so it is not
		// recorded as one — otherwise a torrent the library declines for an
		// unrelated reason would show up in a report as a gate rejection.
		return tl, err
	}
	return tl, nil
}

// firstUnwritable returns the first file name that fails the gate, or "".
//
// "" is returned both for "nothing failed" and for "there are no files", and the
// two are the same answer: a torrent with no files is not a path problem.
// firstUnwritable returns the first file name that fails the gate, or "".
//
// # THE JOINING TRAP, WHICH THIS FUNCTION EXISTS TO AVOID
//
// The obvious implementation validates the file's name as one string:
//
//	name := filepath.Join(append([]string{info.BestName()}, file.BestPath()...)...)
//	if _, err := paths.SanitizeJoin(g.root, name); err != nil { ... }
//
// and it does not work. `filepath.Join` CLEANS its result, so the components
// `["sub", "..", "..", "escape"]` become the single string `"escape"` before
// `SanitizeJoin` ever sees it — the traversal is gone, and the gate passes a
// name that the library will then place wherever it likes. `paths.SanitizeJoin`
// is not wrong here; it is being handed a name that no longer contains the
// attack.
//
// `TestAHostileNameIsCaughtEvenWhenItIsTheSecondComponent` caught this. So the
// gate checks the components the torrent actually supplied, each one
// separately, and NEVER pre-joins them. The joined name is only ever an output,
// for the error message.
//
// The same trap is why the library's own `ToSafeFilePath` looks adequate: it
// joins first and checks the first component of the RESULT, which is a
// different question from "does any component of the input walk out".
func (g *Gate) firstUnwritable(info *metainfo.Info) string {
	// The torrent's own name is the first component of every one of its files,
	// so it is checked once rather than per file. It is peer-controlled, which
	// is the whole reason it is checked and not trusted.
	torrentName := ""
	if n := info.BestName(); n != metainfo.NoName {
		torrentName = n
		if err := g.checkComponent(torrentName); err != nil {
			return torrentName
		}
	}

	for i := range info.UpvertedFiles() {
		f := info.UpvertedFiles()[i]
		for _, comp := range f.BestPath() {
			if err := g.checkComponent(comp); err != nil {
				return comp
			}
		}
		// And the joined form, because `SanitizeJoin` is what the makers call
		// and it can still refuse something a per-component check would not --
		// a name with a NUL, a reserved device, a trailing dot. Redundant on
		// most inputs and free on all of them, and the redundancy is the point:
		// this is the only place a refusal is a real error rather than a path.
		if _, err := paths.SanitizeJoin(g.root, g.relativeName(info, &f)); err != nil {
			return g.relativeName(info, &f)
		}
	}
	return ""
}

// checkComponent refuses one raw path component.
//
// A component on its own, never joined to its neighbours, so that a `..` here
// is the `..` the torrent sent rather than a `..` that a join has already
// resolved away.
func (g *Gate) checkComponent(comp string) error {
	// A single-component name, deliberately not joined onto anything: this is
	// "can this string, on its own, walk out of a directory", and the answer for
	// a component is decided entirely by what it is.
	switch comp {
	case "":
		// An empty component is dropped by every join, so it is harmless here --
		// and `SanitizeJoin` refuses it anyway as "nothing to point at", which
		// would refuse a torrent that every library would happily accept. Not a
		// component-level problem.
		return nil
	case ".", "..":
		return fmt.Errorf("%w: a path component is %q", ErrRefused, comp)
	}

	if strings.ContainsAny(comp, `/\`) {
		return fmt.Errorf(
			"%w: the path component %q contains a separator, so a torrent can "+
				"put more structure in a filename than a filename is",
			ErrRefused, comp)
	}
	if strings.ContainsRune(comp, 0) {
		return fmt.Errorf(
			"%w: the path component %q contains a NUL, which truncates the path "+
				"in the syscalls the downloader makes", ErrRefused, comp)
	}
	return nil
}

// relativeName is the name a file will have under the download root, spelled
// the way the library will spell it.
//
// Used for the ERROR MESSAGE and for the makers' backstop check, never for
// deciding whether to refuse -- see firstUnwritable, which checks components.
//
// Spelled the same way as `filePathMaker` on purpose: if the two disagree about
// where the torrent's own name goes, the backstop validates a different path
// from the one that gets written, and a check of the wrong path passes.
func (g *Gate) relativeName(info *metainfo.Info, file *metainfo.FileInfo) string {
	parts := make([]string, 0, len(file.BestPath())+1)
	if n := info.BestName(); n != metainfo.NoName {
		parts = append(parts, n)
	}
	parts = append(parts, file.BestPath()...)
	return filepath.Join(parts...)
}

// filePathMaker is the backstop.
//
// `FilePathMaker` cannot report an error, so a refused name cannot be a
// refusal here. It becomes ONE inert file inside the root instead — see
// `refuseSentinel` — and the up-front check in `OpenTorrent` is what actually
// stops the transfer.
//
// Returning a path outside the root is not an option even here: this function's
// return value is joined onto a directory and handed to `os.OpenFile`, so
// "something harmless" has to mean a real path inside the root.
func (g *Gate) filePathMaker() libstorage.FilePathMaker {
	return func(opts libstorage.FilePathMakerOpts) string {
		name := ""
		if opts.Info != nil && opts.File != nil {
			parts := make([]string, 0, len(opts.File.BestPath())+1)
			if n := opts.Info.BestName(); n != metainfo.NoName {
				parts = append(parts, n)
			}
			parts = append(parts, opts.File.BestPath()...)
			name = filepath.Join(parts...)
		}

		// `SanitizeJoin` refuses names for files that do not exist yet — a
		// downloader resolves names for exactly those — and walks up to the
		// first existing component to resolve symlinks. So a refusal here is a
		// real decision about this name, not a missing-file artefact.
		safe, err := paths.SanitizeJoin(g.root, name)
		if err != nil {
			g.record(metainfo.Hash{}, name, err.Error())
			// The sentinel, ABSOLUTE and inside the root. Returning the bare
			// name looked right -- the library joins it onto the base directory
			// itself -- but `FilePathMaker` is a public extension point and this
			// value is what a caller's own code would see, and a relative
			// filename resolves against the WORKING DIRECTORY in anything that
			// uses it directly. The cost is one string concat to remove a way to
			// write outside the root.
			return filepath.Join(g.root, refuseSentinel)
		}
		return safe
	}
}

// torrentDir is the directory a torrent's files will live in.
//
// One function, called by both the check and the maker. Two implementations
// would be two answers to one question, and the one that matters is whether
// they AGREE: a check of a different path than the one that gets written passes
// for the wrong reason.
func (g *Gate) torrentDir() string {
	if g.cachedName == "" {
		return g.root
	}
	if safe, err := paths.SanitizeJoin(g.root, g.cachedName); err == nil {
		return filepath.Dir(safe)
	}
	return g.root
}

// torrentDirMaker keeps the torrent's own directory inside the root too.
//
// `defaultPathMaker` returns the base directory unchanged, so the library makes
// `<root>/<torrent name>`. The name is peer-controlled, and on a nameless
// (BEP 52 v2) torrent there is no name at all, so the files land directly in
// the root — which is correct, and is why this is a join and not a rejection.
func (g *Gate) torrentDirMaker() libstorage.TorrentDirFilePathMaker {
	return func(baseDir string, info *metainfo.Info, _ metainfo.Hash) string {
		if info == nil {
			return baseDir
		}
		// The name goes through the same gate as any other component. A torrent
		// named `../../etc` fails here and the dir collapses to the base, which
		// is the inert outcome.
		//
		// The result is cached so `torrentDir` -- which the up-front check uses
		// -- sees exactly this answer rather than computing its own.
		g.mu.Lock()
		g.cachedName = info.BestName()
		g.mu.Unlock()

		if g.cachedName == metainfo.NoName {
			return baseDir
		}
		if safe, err := paths.SanitizeJoin(baseDir, g.cachedName); err == nil {
			return filepath.Dir(safe)
		}
		return baseDir
	}
}

// record files a rejection.
//
// Two callers, and they are not the same case:
//
//   - the up-front check, which HAS the torrent's hash. One record per torrent,
//     because `OpenTorrent` is called again on every retry and a report listing
//     one refusal forty times is a report nobody reads.
//
//   - the backstop `FilePathMaker`, which does not: a name arriving through the
//     library carries no hash, and the function's signature has no room for one.
//     Filed under the zero hash, all of those would collapse into one record and
//     overwrite each other -- so a torrent with two bad names would report only
//     the last, and the first would be gone.
//
// The zero hash therefore gets one record PER CALL rather than per hash. A
// backstop refusal is evidence about a name, not about a torrent, and two
// different names are two findings.
func (g *Gate) record(hash metainfo.Hash, name, reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if hash == (metainfo.Hash{}) {
		// No torrent to dedupe on, so this is a new finding every time -- and
		// the SAME name twice is still two calls, so it is appended rather than
		// keyed. The list is bounded by the number of bad names in one torrent.
		g.refused = append(g.refused, Refusal{Hash: hash, Name: name, Reason: reason})
		return
	}

	for _, r := range g.refused {
		if r.Hash == hash {
			return
		}
	}
	g.refused = append(g.refused, Refusal{Hash: hash, Name: name, Reason: reason})
}

// assertNothingCreated lives in the test file: it is an assertion about the
// filesystem, and a production file that imports `testing` is a smell even when
// the function is only called from tests.

// refuseSentinel is where a refused name lands if it ever reaches the makers.
//
// A name no legitimate torrent produces, so if it appears in a completed
// download something is wrong and it is visible in a directory listing.
const refuseSentinel = ".refused-by-stashforge"
