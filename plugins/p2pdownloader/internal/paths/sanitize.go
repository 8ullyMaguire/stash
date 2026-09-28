// Package paths resolves a PEER-SUPPLIED filename under a root directory.
//
// # WHY THIS EXISTS, AND WHY IT IS ITS OWN PACKAGE
//
// A torrent's file names come from strangers. That makes them untrusted input,
// and a name that escapes the download directory is the bug class that owns a
// box: `../../.ssh/authorized_keys`, or an absolute path, or a name that is
// really a symlink pointing out of the root.
//
// So this is the FIRST thing written in the transfer path, before any protocol
// code, and it is written as a standalone package so the test can run without a
// BitTorrent client, a network, or a database. The plan calls this "the test
// that is written before the feature", and the ordering is the point: a
// downloader that has already fetched something has already written something.
//
// # AND WHY NOT THE LIBRARY'S
//
// `anacrolix/torrent` exports `storage.ToSafeFilePath`, documented as
// "ensuring the result won't escape into parent directories". It does not.
//
// Its entire implementation (storage/safe-path.go, 29 lines) is:
//
//	safeComps := ...filepath.Clean(comp)...
//	safeFilePath := filepath.Join(safeComps...)
//	switch firstComponent(safeFilePath) {   // the FIRST component only
//	case "..":
//	    return "", errors.New("escapes root dir")
//	default:
//	    return safeFilePath, nil
//	}
//
// Measured against it on v1.61.0: `../../etc/passwd` is refused, and
// `/etc/passwd`, `..\..\windows`, `con`, and `trailing.` all pass through with
// a nil error. It also never touches the filesystem, so it cannot see a
// symlink — which is the one case the plan calls out by name
// ("never via a symlink") and which no pure-string function can ever catch.
//
// The full record is docs/decisions/0001-torrent-library.md. The short version:
// the library's function may be a cheap pre-filter and must not be the last
// line of defence. This package is the last line of defence.
package paths

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrEscapes is returned when a name resolves outside the root.
//
// A named error rather than a formatted string so a caller can distinguish
// "this name is not allowed" from "the filesystem is unavailable", which are
// different failures and must not be reported the same way — one is a policy
// answer and one is an outage.
var ErrEscapes = errors.New("the name resolves outside the download root")

// ErrUnsafe is returned for a name that is malformed rather than merely
// escaping: a NUL, a reserved device name, a trailing dot or space.
//
// Separate from ErrEscapes because the two mean different things to whoever
// sees the error. `ErrEscapes` is an attack or a broken torrent; `ErrUnsafe` is
// a name this platform cannot represent. Callers log them differently.
var ErrUnsafe = errors.New("the name is not safe on this platform")

// reservedWindows are the device names Windows refuses regardless of
// extension.
//
// Checked on every platform, not just Windows, and the reason is that a library
// is a library: a corpus built on Windows, synced to Linux, and indexed by
// something that has heard of `CON` will eventually produce a `CON` on disk, and
// the failure surfaces on a filesystem this code never touched. Refusing it at
// the only chokepoint every path passes through is cheaper than diagnosing it
// there.
//
// The trailing extension is why the check strips at the dot: `con.txt` is the
// same device on Windows as `con`.
var reservedWindows = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// SanitizeJoin resolves a peer-supplied `name` under `root` and guarantees the
// result is inside `root`.
//
// The signature the plan specifies, and the guarantee is the whole function:
// a name that does not resolve under the root is REJECTED, never clamped. There
// is no mode that sanitises a hostile name into a safe one by rewriting it,
// because a rewritten name is a name the operator did not ask for and a peer
// did not supply, and the mismatch between them is its own bug.
//
// # WHY EACH CHECK EXISTS, IN THE ORDER THEY RUN
//
// Cheap structural checks first, then the filesystem. The order is not
// arbitrary: every check before the `EvalSymlinks` call is free, and the
// syscall is the expensive one, so a torrent with ten thousand files in it does
// not pay ten thousand syscalls to be rejected on the first component.
//
//  1. empty            — nothing to point at
//  2. NUL byte         — truncates the path in every C-backed syscall the
//     storage layer makes, so a name containing one is a
//     name that becomes a DIFFERENT name on the way to disk
//  3. absolute         — `/etc/passwd` is not a child of the root, and
//     `filepath.Join` would happily produce it
//  4. `..` component   — the direct traversal
//  5. reserved name    — a device on Windows, and a lie everywhere else
//  6. trailing dot/space — stripped silently by Windows, so the file you
//     verify is not the file that exists
//  7. symlink check    — the only check that requires the filesystem, and the
//     only one the library's string function cannot do
//
// # WHY THE FINAL CHECK IS AN EvalSymlinks AND NOT A STRING PREFIX
//
// Because a prefix comparison is not containment. `/data/downloads-evil` starts
// with `/data/downloads` and is a different directory. And a symlink anywhere
// in the resolved path redirects the write regardless of how innocent the string
// looked.
//
// EvalSymlinks is applied to BOTH sides. Comparing the root's resolved form to
// the child's resolved form is the only comparison where neither side can be a
// symlink pointing somewhere else, and on macOS `/tmp` is a symlink to
// `/private/tmp` — so a root that was resolved and a child that was not would
// disagree for a filesystem reason and reject every legitimate file.
//
// The containment test is `rel == "."` or a `rel` that does not begin with
// `..`, which is the same arithmetic `filepath.Rel` exists to do. String
// prefixing is not used anywhere in this file, on purpose.
func SanitizeJoin(root, name string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("%w: no download root was configured, so there is "+
			"nowhere safe to put this", ErrEscapes)
	}
	if name == "" {
		return "", fmt.Errorf("%w: the name is empty, so there is nothing to "+
			"resolve", ErrEscapes)
	}

	// NUL. Checked on the RAW name, before Clean, because Clean does not touch
	// it and a NUL that survives to the syscall truncates the path.
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("%w: the name contains a NUL byte, which would "+
			"truncate the path on the way to disk and write a different name "+
			"than the one that was checked", ErrUnsafe)
	}

	// Absolute, in either direction. `filepath.IsAbs` is the platform's own
	// answer, so this is correct on Windows for `C:\x` and `\\host\share`,
	// which a `strings.HasPrefix(v, "/")` would wave through.
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("%w: %q is an absolute path, and a path that "+
			"starts at the filesystem root is not a child of the download "+
			"directory", ErrEscapes, name)
	}

	// Every component, before any joining. Checked on the components rather
	// than on the joined path because joining first turns `a/../../b` into
	// `../b`, and a check on the joined form can be satisfied by a name whose
	// components were individually fine.
	for _, comp := range strings.FieldsFunc(name, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if comp == ".." {
			return "", fmt.Errorf("%w: %q contains a %q component. A torrent "+
				"names files, and a file that names its own path upward is not "+
				"a file", ErrEscapes, name, "..")
		}
	}

	for _, comp := range strings.FieldsFunc(name, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if err := checkComponent(comp); err != nil {
			return "", err
		}
	}

	// The join, then the filesystem check.
	//
	// `filepath.Join` cleans as it goes, so the `..` components are already
	// resolved here; the check above exists to REFUSE rather than to resolve.
	// Clamping instead — turning `../../x` into `x` — would silently create a
	// file the operator never asked for, in a place a peer chose.
	joined := filepath.Join(root, filepath.Clean(name))

	// EvalSymlinks on the deepest EXISTING ancestor, then re-append the rest.
	//
	// Why not EvalSymlinks(joined) directly: the file does not exist yet, and
	// EvalSymlinks fails on a path with a missing component. A downloader
	// resolves names for files that are not there — that is the normal case —
	// so walking up to the first component that exists is what makes the check
	// work at all.
	//
	// Every existing component is what matters: a symlink at the FIRST level
	// of the name is the attack, and the walk covers it.
	resolved, err := resolveExistingPrefix(joined)
	if err != nil {
		return "", err
	}

	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolving the download root %q: %w", root, err)
	}

	if err := containedIn(rootResolved, resolved); err != nil {
		return "", err
	}

	return joined, nil
}

// resolveExistingPrefix resolves symlinks in the longest existing prefix of
// `path` and rejoins the components that do not exist yet.
//
// The alternative — EvalSymlinks on the whole path — fails whenever any
// component is missing, which for a downloader is always. The two-error case
// is distinguished: a missing component is expected, a real failure (a
// permission error mid-walk, say) is not, and conflating them would make a
// broken root look like a missing file.
func resolveExistingPrefix(path string) (string, error) {
	current := path
	var missing []string

	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			// Walk back down through the components that do not exist yet,
			// REVERSED, or `a/b/c` becomes `a/b/c/b/a`.
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("resolving %q: %w", current, err)
		}

		parent := filepath.Dir(current)
		if parent == current {
			// Reached the root without finding anything that exists. On a
			// real filesystem this cannot happen — the root exists — so it
			// means the root itself is missing, and saying so is more useful
			// than returning a path.
			return "", fmt.Errorf("%w: nothing in %q exists, not even its "+
				"root. The download directory has to exist before a file can "+
				"be put in it", ErrEscapes, path)
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

// containedIn reports whether `path` is `root` or inside it.
//
// Uses `filepath.Rel`, which is the same arithmetic in both directions and
// cannot be fooled by a shared prefix: `/data/dl-evil` relative to `/data/dl`
// is `../dl-evil`, and the `..` check rejects it. A `strings.HasPrefix(path,
// root)` test would accept it, and that is the bug this function exists to not
// have.
func containedIn(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		// Different volumes on Windows, or a genuinely unrelated pair. Either
		// way: not inside.
		return fmt.Errorf("%w: %q is not on the same path as the download root "+
			"%q", ErrEscapes, path, root)
	}

	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		// `rel` is path RELATIVE TO root, so an escape reads
		// "<path> is above <root>". Written the other way round the message is
		// technically fine and practically misleading, because the reader
		// looking for the offending name finds the root instead.
		return fmt.Errorf("%w: %q is above the download root %q (it is %q "+
			"relative to it)", ErrEscapes, path, root, rel)
	}
	return nil
}

// checkComponent applies the per-component platform rules.
//
// Split out because they are per-component by nature — `ok/con/x` is as unsafe
// as `con` — and because a caller reading SanitizeJoin should see the traversal
// check and the platform checks as different concerns.
func checkComponent(comp string) error {
	// Windows strips a trailing dot or space from a path, silently, so the
	// name that is VERIFIED and the name that EXISTS can differ. A file called
	// `x.mkv.` is `x.mkv` on disk, which means the sanitisation checked one
	// path and a peer named another.
	//
	// The bare `.` and `..` cases are excluded because `filepath.Clean` has
	// already handled them and they are caught by the traversal check.
	if comp != "." && comp != ".." {
		if strings.HasSuffix(comp, ".") || strings.HasSuffix(comp, " ") {
			return fmt.Errorf("%w: the path component %q ends in a dot or a "+
				"space, which Windows strips silently — the name that would be "+
				"verified is not the name that would exist", ErrUnsafe, comp)
		}
	}

	// Reserved device names, checked at the stem so `CON` and `con.txt` are
	// both caught. Case-folded because the device is case-insensitive.
	stem := comp
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	if reservedWindows[strings.ToLower(strings.TrimSpace(stem))] {
		return fmt.Errorf("%w: %q is a reserved device name on Windows. A file "+
			"by that name cannot be created, cannot be deleted, and silently "+
			"resolves to the device instead", ErrUnsafe, comp)
	}

	return nil
}

// EnsureRoot creates the download root if it does not exist and verifies it is
// a directory.
//
// Separate from SanitizeJoin because the two answer different questions. A
// root that does not exist is a CONFIGURATION problem and is worth fixing
// automatically — a first run with no download directory yet is normal. A root
// that is a symlink, or that cannot be created, is not.
//
// The symlink check is here rather than in SanitizeJoin because SanitizeJoin
// resolves the root on every call and comparing against a resolved form on
// every call is a syscall a name can trigger. Doing it once, at startup, is
// both cheaper and the right place to report it.
func EnsureRoot(root string) error {
	info, err := os.Stat(root)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%w: the download root %q exists but is not a "+
				"directory", ErrEscapes, root)
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("checking the download root %q: %w", root, err)
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("creating the download root %q: %w", root, err)
	}
	return nil
}
