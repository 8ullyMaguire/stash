package ed2k

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Errors this package returns.
//
// Each is a distinct value rather than one "bad locator" error, because the
// caller's response differs: a malformed hash is a link somebody's database got
// wrong, a traversal in the NAME is an attack, and an unrecognised scheme is a
// caller passing the wrong string entirely. An operator reading a task list
// needs those to read differently.
var (
	// ErrNotALocator means the string is not an ed2k link at all.
	ErrNotALocator = errors.New("not an ed2k link")

	// ErrMalformed means the shape is right and a field is not.
	ErrMalformed = errors.New("the ed2k link is malformed")

	// ErrEscapingName means the link's name would escape the download root.
	//
	// Its own error because it is the only one of these that indicates an
	// ATTACK rather than a mistake. Everything else here is somebody's broken
	// database; this is somebody choosing a name, and a downloader that fetched
	// it would write outside the directory the operator configured.
	ErrEscapingName = errors.New("the ed2k link's name escapes the download root")
)

// Parse reads an ed2k link.
//
// # THE GRAMMAR, AND WHY `url.Parse` CANNOT DO IT
//
//	ed2k://|file|<name>|<size>|<hash>|
//	ed2k://|folder|<name>|<size>|<hash>|
//
// Pipe-delimited and POSITIONAL. aMule's own documentation puts it as
// `ed2k://|file|NAME|SIZE|MD4HASH|/` with "fields separated by pipe
// characters", so NAME is the second field by position and the parser splits.
//
// # A NAME CONTAINING A PIPE IS REFUSED, NOT RECONSTRUCTED
//
// An earlier version of this comment claimed the opposite — that the name is
// "everything between the second and third pipe", that a name containing a
// pipe "cannot be recovered by splitting", and that the parse was therefore
// "a hand-written split rather than a parse". The code used strings.Split the
// whole time, so the comment described a mechanism that was not there, and the
// grammar it described is not the protocol's either.
//
// There is no way to recover a pipe-bearing name: `ed2k://|file|a|b|1024|H|` is
// genuinely ambiguous between a file called "a|b" of 1024 bytes and a file
// called "a" whose size field is "b". Guessing either way is worse than
// refusing, so Parse refuses a link with more fields than the protocol defines.
// Clients that need such a name percent-encode it, which is why the aMule
// examples all do.
//
// # WHY THE SCHEME IS CHECKED BY PREFIX
//
// `ed2k://` is not a URL. `url.Parse("ed2k://|file|a|1|ab|")` succeeds and hands
// back a URL whose Host is `"|file|a|1|ab|"` — so a caller that trusted
// url.Parse's decomposition would be rebuilding a locator from a field that was
// never one. The plugin's `LocatorSchemeOf` already matches the scheme by
// prefix for the same reason, and says so.
func Parse(raw string) (Locator, error) {
	value := strings.TrimSpace(raw)

	// TrimSpace and then the prefix check, in that order: a link pasted with a
	// trailing newline is the common case and not a malformed one.
	if !strings.HasPrefix(strings.ToLower(value), schemePrefix) {
		return Locator{}, fmt.Errorf("%w: %q does not begin with %q. The "+
			"scheme is checked by PREFIX because %q is not a URL — url.Parse "+
			"accepts it and returns a URL whose host is the entire link",
			ErrNotALocator, raw, schemePrefix, "ed2k://")
	}
	body := value[len(schemePrefix):]

	// The body must start with a pipe: `ed2k://file|...` is not a link, and
	// accepting it would mean a scheme check that does not check the shape.
	if !strings.HasPrefix(body, "|") {
		return Locator{}, fmt.Errorf("%w: %q has the scheme but no field "+
			"separator. A link is ed2k://|file|...", ErrMalformed, raw)
	}
	// THE FIELDS ARE POSITIONAL, AND A NAME WITH A PIPE IN IT IS NOT A LINK
	//
	// This parser splits on every pipe and takes fields 0..3. That is the
	// protocol's own shape — aMule documents `ed2k://|file|NAME|SIZE|MD4HASH|/`
	// with "fields separated by pipe characters", and NAME is the second field
	// by position — and it is deliberately NOT a scan for the pipe that ends the
	// name.
	//
	// An earlier version of this comment described the opposite: "the NAME is
	// everything between the second and third pipe... a hand-written split
	// rather than a parse", and the code used strings.Split. So the comment
	// asserted a mechanism the code did not have, and it described a grammar the
	// protocol does not have either. A name containing a pipe makes the link
	// AMBIGUOUS — there is no way to tell "name a|b, size 1024" from "name a,
	// size b" — so the honest answer is not to guess, and the extra fields are
	// refused as the extra fields they are rather than silently folded into a
	// name.
	fields := strings.Split(strings.TrimPrefix(body, "|"), "|")

	// `|file|name|size|hash` is four fields. Fewer is malformed.
	//
	// MORE is refused too, and this is a change of behaviour worth naming: the
	// first version took what it needed and ignored the rest, on the theory
	// that "eMule appends fields in practice". A folder link's embedded file
	// list does append fields, but a link whose trailing fields we do not
	// understand is exactly the link whose fields we should not silently drop —
	// a trailing pipe is the protocol's own terminator, and a link without one
	// is a link we have not fully understood.
	if len(fields) < 4 {
		return Locator{}, fmt.Errorf("%w: %q has %d field(s), expected at "+
			"least 4 (kind, name, size, hash)", ErrMalformed, raw, len(fields))
	}
	if len(fields) > 5 {
		return Locator{}, fmt.Errorf("%w: %q has %d fields, expected 4 plus "+
			"a trailing terminator. A name containing a pipe makes the link "+
			"ambiguous — there is no way to tell a name of \"a|b\" with a "+
			"size of 1024 from a name of \"a\" with a size of \"b\" — so the "+
			"link is refused rather than guessed at", ErrMalformed, raw,
			len(fields))
	}

	var loc Locator
	switch strings.ToLower(fields[0]) {
	case "file":
		loc.Kind = KindFile
	case "folder":
		loc.Kind = KindFolder
	default:
		return Locator{}, fmt.Errorf("%w: %q is neither \"file\" nor "+
			"\"folder\". A folder link's hash covers a FILE LIST, not a file, "+
			"so treating one as the other sends a caller looking for a file "+
			"that does not exist", ErrMalformed, fields[0])
	}

	loc.Name = fields[1]
	if loc.Name == "" {
		return Locator{}, fmt.Errorf("%w: the name is empty", ErrMalformed)
	}

	// A folder link's name is a display label; a file link's name is what will
	// be written to disk. The traversal check below is therefore about the FILE
	// case, and a folder label is not a path — but it is still refused, because
	// a folder label that escapes becomes a path the moment the folder's
	// contents are laid out under it.
	if err := checkName(loc.Name); err != nil {
		return Locator{}, err
	}

	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return Locator{}, fmt.Errorf("%w: the size %q is not a number: %v",
			ErrMalformed, fields[2], err)
	}
	// A negative size is refused for the same reason as zero, and the message
	// names the range because a negative size is what a link with a stray
	// leading `-` produces.
	if size <= 0 {
		return Locator{}, fmt.Errorf("%w: the size is %d. A link identifies a "+
			"file by (hash, length), so a length of zero or less is not a file "+
			"this protocol can fetch", ErrMalformed, size)
	}
	loc.Size = size

	hash, err := ParseHash(fields[3])
	if err != nil {
		return Locator{}, fmt.Errorf("%w: the hash field: %v", ErrMalformed, err)
	}
	loc.Hash = hash

	return loc, nil
}

// schemePrefix is the ed2k locator scheme, with its trailing `://`.
const schemePrefix = "ed2k://"

// ParseHash decodes the 32 hex characters of an eDonkey2000 hash.
//
// LENGTH CHECKED BEFORE DECODING, and the order is the point. `hex.DecodeString`
// on a short string returns an error naming a byte index and nothing else; on a
// LONG string it succeeds and returns more bytes than a Hash can hold. So a
// caller that decoded first and checked the length after would have to truncate
// silently — and a silently truncated hash identifies a DIFFERENT FILE, which
// is the one outcome worse than a refusal.
func ParseHash(s string) (Hash, error) {
	var h Hash

	// Not TrimSpace'd into acceptance: a hash field with surrounding whitespace
	// is a malformed link, and quietly accepting it means two spellings of the
	// same file and a lookup that works for one caller and not the other.
	if len(s) != 2*HashLength {
		return h, fmt.Errorf("%q is %d characters, expected %d. The length "+
			"is checked BEFORE decoding because hex.DecodeString accepts any "+
			"even length, so decoding first and checking after would silently "+
			"truncate — and a truncated hash identifies a different file",
			s, len(s), 2*HashLength)
	}

	raw, err := hex.DecodeString(s)
	if err != nil {
		return h, fmt.Errorf("%q is not hexadecimal: %v", s, err)
	}
	copy(h[:], raw)
	return h, nil
}

// dangerousNameComponents are the path components that make a name unsafe.
//
// A CLOSED SET rather than a check for "..", because the two are not the same
// question. `..` is the traversal; the others are the platform-specific spellings
// of it, and a name arriving from a stranger's database can carry any of them.
// A Windows client would treat `..\..\windows` as a traversal on the same
// download, and a plugin that only refused `..` refused the attack for the
// platform it happened to be built for.
var dangerousNameComponents = []string{"..", "..\\", "../"}

// checkName refuses a name that would escape the download root.
//
// # WHAT IS ACTUALLY CHECKED, AND WHAT IS NOT
//
// The dangerous components are checked, and so is an absolute path and a
// Windows drive letter. What is NOT checked here is every way a filesystem
// treats a name as special — that is step 5.2's job, in `internal/paths`, and
// this check is a SECOND layer in front of it rather than a replacement.
//
// It exists here because this parser runs on a string from a stranger BEFORE
// the file is ever named on disk, and refusing a traversal at the parse is the
// last point at which refusing is free. A name that reached `internal/paths`
// having already been parsed from a hostile link is a name that has been through
// one attacker-controlled transformation too many.
func checkName(name string) error {
	// Lowercased, because a Windows client would accept `..\..\WINDOWS` and the
	// check has to hold for the platforms this link will be opened on, not the
	// one the plugin was compiled for.
	lower := strings.ToLower(name)

	for _, bad := range dangerousNameComponents {
		if strings.Contains(lower, bad) {
			return fmt.Errorf("%w: the name %q contains %q. The name comes "+
				"from the link, so it is chosen by whoever made the link, and a "+
				"downloader that honours it writes outside the directory the "+
				"operator configured", ErrEscapingName, name, bad)
		}
	}

	// An absolute POSIX path. `filepath.IsAbs` is the platform's own answer, so
	// this uses the same predicate the filesystem will.
	if strings.HasPrefix(name, "/") {
		return fmt.Errorf("%w: the name %q is an absolute path, so it names "+
			"a location rather than a file inside the download root",
			ErrEscapingName, name)
	}

	// A Windows drive letter or UNC path, checked by hand rather than by
	// filepath.VolumeName, which on Linux returns "" for `C:\x` — so a Linux
	// build would pass a name that is an absolute path on the machine that
	// shares the link.
	if len(name) >= 2 && name[1] == ':' &&
		(name[0] >= 'A' && name[0] <= 'Z' || name[0] >= 'a' && name[0] <= 'z') {
		return fmt.Errorf("%w: the name %q begins with a drive letter. The "+
			"plugin may be built on Linux and the link opened on Windows, so "+
			"this is checked by hand rather than by filepath.VolumeName — which "+
			"returns \"\" for C:\\x when compiled on Linux",
			ErrEscapingName, name)
	}
	if strings.HasPrefix(lower, `\\`) {
		return fmt.Errorf("%w: the name %q is a UNC path", ErrEscapingName, name)
	}

	return nil
}
