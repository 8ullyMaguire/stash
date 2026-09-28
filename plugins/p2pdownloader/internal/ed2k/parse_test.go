package ed2k

import (
	"errors"
	"testing"
)

// The hashes used below are all well formed: 32 hex characters. They are
// spelled out rather than generated so that a test failure names the field
// that was wrong.
const (
	goodHash = "0123456789abcdef0123456789abcdef"
	// The same bytes as goodHash, uppercased. eMule emits both spellings.
	goodHashUpper = "0123456789ABCDEF0123456789ABCDEF"
	// One character short of 32, and one character long.
	shortHash = "0123456789abcdef0123456789abcde"
	longHash  = "0123456789abcdef0123456789abcdef0"
	// 32 characters, not hex.
	nonHexHash = "0123456789abcdef0123456789abcdeg"
	// A hash of the right shape that names nothing.
	zeroHash = "00000000000000000000000000000000"
)

func link(kind, name, size, hash string) string {
	return "ed2k://|" + kind + "|" + name + "|" + size + "|" + hash + "|"
}

// TestTheOrdinaryLinkParses is the control case every other row in this file
// is measured against: if a refusal below ever starts passing for the wrong
// reason, this is the row that says the link itself is fine.
//
// A test table whose rows are all refusals proves the parser can say no. It
// does not prove it can say yes, and a parser that refuses everything is green
// against a table of refusals.
func TestTheOrdinaryLinkParses(t *testing.T) {
	loc, err := Parse(link("file", "video.mkv", "1024", goodHash))
	if err != nil {
		t.Fatalf("an ordinary file link was refused: %v", err)
	}
	if loc.Kind != KindFile {
		t.Errorf("Kind = %v, want %v", loc.Kind, KindFile)
	}
	if loc.Name != "video.mkv" {
		t.Errorf("Name = %q, want %q", loc.Name, "video.mkv")
	}
	if loc.Size != 1024 {
		t.Errorf("Size = %d, want 1024", loc.Size)
	}
	if loc.Hash.String() != goodHash {
		t.Errorf("Hash = %s, want %s", loc.Hash, goodHash)
	}
}

// TestAFolderLinkIsNotAFileLink: a `|folder|` link's hash covers a FILE LIST, so
// the two must not be interchangeable. This asserts the Kind is carried
// through rather than defaulted, because a caller that reads a folder link's
// hash as a file hash looks for a file that does not exist.
func TestAFolderLinkIsNotAFileLink(t *testing.T) {
	loc, err := Parse(link("folder", "album", "2048", goodHash))
	if err != nil {
		t.Fatalf("a folder link was refused: %v", err)
	}
	if loc.Kind != KindFolder {
		t.Errorf("Kind = %v, want %v — a folder link's hash covers a file "+
			"list, so a caller reading it as a file hash looks for a file "+
			"that does not exist", loc.Kind, KindFolder)
	}
	if loc.Kind.String() != "folder" {
		t.Errorf("Kind.String() = %q, want %q", loc.Kind.String(), "folder")
	}

	// The two forms are distinguishable in the OUTPUT, not merely accepted.
	// A parser that ignored the kind field would pass the assertions above if
	// KindFile were the zero value, which is why the file case is asserted
	// separately in TestTheOrdinaryLinkParses.
	file, err := Parse(link("file", "album", "2048", goodHash))
	if err != nil {
		t.Fatalf("the file form of the same fields was refused: %v", err)
	}
	if file.Kind == loc.Kind {
		t.Errorf("a |file| link and a |folder| link with identical name, size "+
			"and hash both parsed as Kind %v. They are not interchangeable",
			file.Kind)
	}
}

// TestAnUnknownKindIsRefused, including the `server` and `serverlist` forms
// aMule also defines. A locator this package cannot act on must not parse as a
// file: `|server|1.2.3.4|4661|` has four fields and would otherwise reach the
// size check with a host in the name slot.
func TestAnUnknownKindIsRefused(t *testing.T) {
	for _, kind := range []string{"list", "server", "serverlist", "collection", ""} {
		t.Run(kind, func(t *testing.T) {
			_, err := Parse(link(kind, "video.mkv", "1024", goodHash))
			if !errors.Is(err, ErrMalformed) {
				t.Errorf("a |%s| link gave err = %v, want ErrMalformed. "+
					"Only file and folder are handled, and anything else "+
					"must not reach a caller as a file", kind, err)
			}
		})
	}
}

// TestTheNameIsNotPercentDecoded: classic eMule links carry RAW names, so
// decoding a name that was never encoded turns a valid file into a 404.
func TestTheNameIsNotPercentDecoded(t *testing.T) {
	const name = "My%20Video%281080p%29.mkv"
	loc, err := Parse(link("file", name, "1024", goodHash))
	if err != nil {
		t.Fatalf("a link whose name carries percent escapes was refused: %v", err)
	}
	if loc.Name != name {
		t.Errorf("Name = %q, want %q — the name is not percent-encoded in "+
			"the link and decoding it would name a different file",
			loc.Name, name)
	}
}

// TestUppercaseHexIsAcceptedAndPrintedLowercase.
//
// Both halves, because they are different claims. Accepting uppercase is about
// the input; printing lowercase is about the output. A peer comparing hash
// STRINGS treats AABB and aabb as different files, so a printer that emitted
// uppercase would produce links that do not resolve.
func TestUppercaseHexIsAcceptedAndPrintedLowercase(t *testing.T) {
	loc, err := Parse(link("file", "video.mkv", "1024", goodHashUpper))
	if err != nil {
		t.Fatalf("an uppercase hash was refused: %v", err)
	}
	if loc.Hash.String() != goodHash {
		t.Errorf("Hash.String() = %q, want %q. The input was uppercase and "+
			"the protocol's own form is lowercase, so a peer comparing hash "+
			"strings must see the lowercase one", loc.Hash.String(), goodHash)
	}
	// Round trip: the printed form must parse back to the same hash.
	again, err := Parse(link("file", "video.mkv", "1024", loc.Hash.String()))
	if err != nil {
		t.Fatalf("a link built from the printed hash was refused: %v", err)
	}
	if again.Hash != loc.Hash {
		t.Errorf("the hash did not survive a print-and-reparse round trip: "+
			"%s then %s", loc.Hash, again.Hash)
	}
}

// TestTheHashLengthIsCheckedBeforeDecoding.
//
// The three rows are 31 characters, 33 characters and 32 non-hex characters.
// The first two are the ones `hex.DecodeString` handles differently: it accepts
// ANY even length, so a 30-character field would decode to 15 bytes and a
// 34-character field to 17. A parser that decoded first and checked the length
// after would have to truncate silently, and a silently truncated hash
// identifies a DIFFERENT FILE — the one outcome worse than a refusal.
func TestTheHashLengthIsCheckedBeforeDecoding(t *testing.T) {
	for _, c := range []struct{ name, hash string }{
		{"31 characters", shortHash},
		{"33 characters", longHash},
		{"32 characters that are not hex", nonHexHash},
		{"an empty hash field", ""},
		{"a hash with surrounding whitespace", " " + goodHash + " "},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse(link("file", "video.mkv", "1024", c.hash))
			if err == nil {
				t.Fatalf("a hash field of %q was accepted", c.hash)
			}
			if !errors.Is(err, ErrMalformed) {
				t.Errorf("err = %v, want ErrMalformed", err)
			}
		})
	}

	// The truncation is the thing being prevented, so assert on the boundary
	// directly rather than only through Parse. hex.DecodeString accepts ANY
	// even length, so 34 characters decodes to 17 bytes and fits past a length
	// check that ran after the decode.
	if _, err := ParseHash(goodHash + "00"); err == nil {
		t.Errorf("ParseHash accepted 34 characters (%q), which decodes to 17 "+
			"bytes and cannot fit a Hash", goodHash+"00")
	}
	// 32 characters is the right LENGTH and is accepted even though these are
	// not the bytes of goodHash — the length check is not a content check, and
	// a test that conflated the two would assert something the parser does not
	// promise.
	if _, err := ParseHash("0123456789abcdef0123456789abcde0"); err != nil {
		t.Errorf("ParseHash refused 32 valid hex characters: %v", err)
	}
}

// TestTheSizeMustBePositive. A link identifies a file by (hash, length), both
// halves, so a length of zero or less is not a file this protocol can fetch —
// and a zero length with a valid hash is the shape a truncated link takes.
func TestTheSizeMustBePositive(t *testing.T) {
	for _, size := range []string{"0", "-1", "-5", "abc", "", "1.5", "1024 ", " 1024", "0x400"} {
		t.Run(size, func(t *testing.T) {
			_, err := Parse(link("file", "video.mkv", size, goodHash))
			if !errors.Is(err, ErrMalformed) {
				t.Errorf("a size of %q gave err = %v, want ErrMalformed",
					size, err)
			}
		})
	}

	// The control: a size of 1 is the smallest legal one, so the `> 0` guard
	// is not `>= 2` or a minimum block size by accident.
	loc, err := Parse(link("file", "video.mkv", "1", goodHash))
	if err != nil {
		t.Fatalf("a size of 1 was refused: %v", err)
	}
	if loc.Size != 1 {
		t.Errorf("Size = %d, want 1", loc.Size)
	}
}

// TestAnEscapingNameIsRefused. Each row is a name chosen by whoever made the
// link, and each is a way of writing outside the directory the operator
// configured.
func TestAnEscapingNameIsRefused(t *testing.T) {
	for _, c := range []struct{ name, why string }{
		{"..", "the traversal itself"},
		{"..\\..\\windows", "the same attack, other separator"},
		{"../escape", "a parent walk with the POSIX separator"},
		{"sub/../../escape", "a traversal that does not start at the name"},
		{"/etc/passwd", "an absolute POSIX path"},
		{`C:\x`, "a Windows drive letter, which filepath.VolumeName reports " +
			"as \"\" for on Linux — so a Linux build would otherwise pass a " +
			"name that is absolute on the machine that opens the link"},
		{`c:\x`, "the same drive letter in lower case"},
		{`\\server\share`, "a UNC path"},
		{`..\..\WINDOWS`, "a traversal in upper case, which a Windows " +
			"client would accept"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse(link("file", c.name, "1024", goodHash))
			if !errors.Is(err, ErrEscapingName) {
				t.Errorf("the name %q is %s, so it must be refused with "+
					"ErrEscapingName; got err = %v", c.name, c.why, err)
			}
		})
	}
}

// TestTheTraversalCheckIsASubstringTestNotAComponentTest, and the
// over-refusal is DELIBERATE.
//
// checkName tests for the two-character string ".." anywhere in the name
// rather than for a path COMPONENT equal to "..". So every name with two
// consecutive dots is refused, including "..leading.dots", "foo..bar" and
// "my..file.mkv" — none of which escapes anything.
//
// That is a false positive, and it is the right trade for a name arriving from
// a stranger's database: the cost is an obscure filename, the benefit is that
// the check cannot be defeated by a separator the parser did not anticipate.
// It is asserted here so the behaviour is a documented contract rather than an
// accident, and so a future change to a component-wise check is a DELIBERATE
// change someone has to look at.
//
// The same trap exists in `filepath.Rel`, which this project hit in
// `internal/paths`: a component merely BEGINNING with dots is not a
// traversal, so a `HasPrefix(rel, "..")` wrongly rejects "..leading.dots".
func TestTheTraversalCheckIsASubstringTestNotAComponentTest(t *testing.T) {
	// Refused, though none of these escapes anything.
	for _, name := range []string{
		"..leading.dots", "foo..bar", "...", "a..", "my..file.mkv",
		"..", "..\\..\\windows", "../escape", "sub/../escape",
	} {
		if err := checkName(name); err == nil {
			t.Errorf("checkName(%q) accepted it, so the \"..\" check is no "+
				"longer a substring test", name)
		}
	}

	// The control. These must be accepted, or the row above proves nothing by
	// refusing everything — a name with a single dot, a space, an extension and
	// a unicode character is the overwhelming majority of real links.
	for _, name := range []string{
		"video.mkv",
		"a.b.c.mp4",
		"2024.09.28 release.mkv",
		"my file (1080p).mkv",
		"Amélie - 2001 [1080p].mkv",
		"sub.dir/file.mkv",
		"file",
		"_leading-underscore.srt",
	} {
		if err := checkName(name); err != nil {
			t.Errorf("checkName(%q) refused a legitimate name: %v", name, err)
		}
	}
}

// TestALinkWithFieldsBeyondTheHashIsRefused, and the fixture is the one that
// only THIS guard can catch.
//
// The first version of this test used a name with a pipe in it
// (`|file|a|b|1024|hash|`), and it passed with the field-count guard deleted.
// Not because the guard is redundant but because the SIZE check refuses the
// same link: after the split, fields[2] is "b", which is not a number. Two
// layers refusing one input means the test is asserting the outcome and not the
// guard — and the mutation that removes the guard survives, which is exactly
// the signal that the row is not testing what its name says.
//
// So the fixture here is a link whose first four fields are all VALID and whose
// extra fields come AFTER the hash. Nothing else refuses it: the kind is
// known, the name is a real name, the size is a number, the hash is 32 hex
// characters. With the guard removed it parses to nil error — a link this
// plugin does not fully understand is accepted and the trailing fields are
// silently discarded. Measured both ways.
func TestALinkWithFieldsBeyondTheHashIsRefused(t *testing.T) {
	for _, c := range []struct{ raw, why string }{
		{"ed2k://|file|video.mkv|1024|" + goodHash + "|junk|",
			"one field past the terminator"},
		{"ed2k://|file|video.mkv|1024|" + goodHash + "|extra|junk|",
			"two fields past the terminator"},
		{"ed2k://|folder|album|2048|" + goodHash + "|1|a.mkv|1024|" + goodHash + "|",
			"an embedded file list, which this package does not parse"},
	} {
		t.Run(c.why, func(t *testing.T) {
			_, err := Parse(c.raw)
			if !errors.Is(err, ErrMalformed) {
				t.Errorf("a link with %s gave err = %v, want ErrMalformed. "+
					"Every field this parser needs is valid, so nothing "+
					"else refuses it — and accepting it means silently "+
					"discarding fields we did not understand", c.why, err)
			}
		})
	}

	// The control, in the same file, because the row above refuses links and
	// a table of refusals does not prove the parser can accept one.
	loc, err := Parse(link("file", "video.mkv", "1024", goodHash))
	if err != nil {
		t.Fatalf("the four-field control link was refused: %v", err)
	}
	if loc.Name != "video.mkv" || loc.Size != 1024 {
		t.Errorf("the control parsed as %q/%d, want video.mkv/1024",
			loc.Name, loc.Size)
	}
}

// TestANameWithAPipeIsRefusedRatherThanGuessedAt.
//
// `ed2k://|file|a|b|1024|<hash>|` has six fields where the protocol defines
// five (four plus the trailing terminator). It is genuinely ambiguous: it
// could be a file called "a|b" of 1024 bytes, or a file called "a" whose size
// field is "b". The parser refuses rather than picking one.
//
// This row exists because an earlier version of the file's doc comment
// described a parser that would recover the name, and the code never did it.
// The assertion is that the link is REFUSED, which is the only honest answer
// the protocol allows.
//
// It is not the row that pins the field-count guard — the size check refuses
// this link too, and a test that cannot tell which guard fired is measuring
// the outcome rather than the mechanism. TestALinkWithFieldsBeyondTheHashIsRefused
// is the row that pins the guard.
func TestANameWithAPipeIsRefusedRatherThanGuessedAt(t *testing.T) {
	for _, c := range []string{
		"ed2k://|file|a|b|1024|" + goodHash + "|",
		"ed2k://|file|a|b|c|d|" + goodHash + "|",
	} {
		_, err := Parse(c)
		if !errors.Is(err, ErrMalformed) {
			t.Errorf("the ambiguous link %q gave err = %v, want "+
				"ErrMalformed. The link is ambiguous and must be refused "+
				"rather than resolved by guessing which field is the name",
				c, err)
		}
	}
}

// TestTheLinkIsNotAURL. `ed2k://` is not a URL: url.Parse accepts it and hands
// back a URL whose Host is the entire link, so a caller that trusted
// url.Parse's decomposition would be rebuilding a locator from a field that
// was never one. The scheme is matched by PREFIX, and this row is what says so.
func TestTheLinkIsNotAURL(t *testing.T) {
	for _, raw := range []string{
		"magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
		"http://example.com/file.torrent",
		"file:///etc/passwd",
		"ed2k:/|file|video.mkv|1024|" + goodHash + "|",
		"ed2k|file|video.mkv|1024|" + goodHash + "|",
		"not a link at all",
		"",
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := Parse(raw)
			if !errors.Is(err, ErrNotALocator) {
				t.Errorf("err = %v, want ErrNotALocator for a string that is "+
					"not an ed2k link", err)
			}
		})
	}
}

// TestTheSchemeIsMatchedCaseInsensitivelyAndSurroundedByWhitespaceIsFine: a
// link pasted with a trailing newline is the common case and not a malformed
// one, and aMule's own examples vary in case.
func TestTheSchemeIsMatchedCaseInsensitivelyAndSurroundedByWhitespaceIsFine(t *testing.T) {
	want := "video.mkv"
	for _, raw := range []string{
		link("file", want, "1024", goodHash),
		"\n  " + link("file", want, "1024", goodHash) + "  \t\n",
		"ED2K://|FILE|" + want + "|1024|" + goodHash + "|",
		"Ed2K://|File|" + want + "|1024|" + goodHashUpper + "|",
	} {
		t.Run(raw, func(t *testing.T) {
			loc, err := Parse(raw)
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if loc.Name != want {
				t.Errorf("Name = %q, want %q", loc.Name, want)
			}
			if loc.Size != 1024 {
				t.Errorf("Size = %d, want 1024", loc.Size)
			}
			if loc.Hash.String() != goodHash {
				t.Errorf("Hash = %s, want %s", loc.Hash, goodHash)
			}
		})
	}
}

// TestTheSchemeIsPresentButTheShapeIsNot. `ed2k://file|...` carries the scheme
// and is still not a link, so a scheme check that does not also check the
// shape would let it through.
func TestTheSchemeIsPresentButTheShapeIsNot(t *testing.T) {
	for _, raw := range []string{
		"ed2k://file|video.mkv|1024|" + goodHash + "|",
		"ed2k://|file|onlyname|",
		"ed2k://|file|name|size|",
		"ed2k://|",
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := Parse(raw)
			if !errors.Is(err, ErrMalformed) {
				t.Errorf("err = %v, want ErrMalformed", err)
			}
		})
	}
}

// TestTheEmptyNameIsRefused: a link with no name identifies nothing writable,
// and accepting it would produce a Locator whose Name is the empty string for
// a caller to put on disk.
func TestTheEmptyNameIsRefused(t *testing.T) {
	_, err := Parse(link("file", "", "1024", goodHash))
	if !errors.Is(err, ErrMalformed) {
		t.Errorf("a link with an empty name gave err = %v, want ErrMalformed",
			err)
	}
}

// TestTheThreeErrorsAreDistinct: a malformed hash is somebody's broken
// database, a traversal in the name is an ATTACK, and an unrecognised scheme
// is a caller passing the wrong string. An operator reading a task list needs
// those to read differently, which is why they are three sentinels rather than
// one "bad locator" error.
//
// A caller that checked only `err != nil` would pass this file's other tests;
// the point is that errors.Is separates them.
func TestTheThreeErrorsAreDistinct(t *testing.T) {
	rows := []struct {
		name string
		raw  string
		want error
	}{
		{"wrong scheme", "http://example.com/x", ErrNotALocator},
		{"bad hash", link("file", "video.mkv", "1024", shortHash), ErrMalformed},
		{"zero size", link("file", "video.mkv", "0", goodHash), ErrMalformed},
		{"traversal", link("file", "../escape", "1024", goodHash), ErrEscapingName},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			_, err := Parse(r.raw)
			if !errors.Is(err, r.want) {
				t.Errorf("err = %v, want it to wrap %v", err, r.want)
			}
			// And the other two must NOT match, or the sentinels are not
			// doing the job they exist for.
			for _, other := range []error{ErrNotALocator, ErrMalformed, ErrEscapingName} {
				if other == r.want {
					continue
				}
				if errors.Is(err, other) {
					t.Errorf("err = %v also wraps %v, so the two are not "+
						"distinguishable to a caller", err, other)
				}
			}
		})
	}
}

// TestTheHashIsAnArrayNotAString: the type is what makes a wrong length
// unrepresentable, so this asserts the constant the parser and the printer both
// depend on, and the round trip.
func TestTheHashIsAnArrayNotAString(t *testing.T) {
	if HashLength != 16 {
		t.Fatalf("HashLength = %d, want 16 — the parser's length check and "+
			"the printer's width both read it", HashLength)
	}
	h, err := ParseHash(goodHash)
	if err != nil {
		t.Fatalf("ParseHash refused a well formed hash: %v", err)
	}
	if len(h) != HashLength {
		t.Errorf("len(Hash) = %d, want %d", len(h), HashLength)
	}
	if got := h.String(); got != goodHash {
		t.Errorf("String() = %q, want %q", got, goodHash)
	}
	// String() is a value receiver on a fixed-size array, so it works on a
	// literal and on the zero value.
	if zero := (Hash{}); zero.String() != zeroHash {
		t.Errorf("the zero Hash prints as %q, want %q", zero.String(), zeroHash)
	}
	if h.IsZero() {
		t.Error("a decoded non-zero hash reports IsZero")
	}
	if !(Hash{}).IsZero() {
		t.Errorf("the zero hash does not report IsZero")
	}
}

// TestTheZeroHashIsRecognisable: this is how a caller tells "the link carried a
// hash" from "the link's hash field decoded to nothing". The magnet path
// refuses a zero infohash for the same reason, because a hash that names
// nothing cannot be matched to its bytes and cannot be dropped later.
func TestTheZeroHashIsRecognisable(t *testing.T) {
	loc, err := Parse(link("file", "video.mkv", "1024", zeroHash))
	if err != nil {
		t.Fatalf("a link whose hash is all zeroes was refused: %v", err)
	}
	if !loc.Hash.IsZero() {
		t.Errorf("Hash = %s, which is not all zeroes", loc.Hash)
	}
	// A normal hash must NOT report IsZero, or the check is useless.
	normal, err := ParseHash(goodHash)
	if err != nil {
		t.Fatal(err)
	}
	if normal.IsZero() {
		t.Error("a well formed hash reports IsZero, so the check cannot " +
			"distinguish the two cases")
	}
}

// TestAZeroByteInTheHashIsNotZero: IsZero is all sixteen bytes, not a leading
// zero. A hash beginning 0000 but with content is a real hash.
func TestAZeroByteInTheHashIsNotZero(t *testing.T) {
	h, err := ParseHash("00000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if h.IsZero() {
		t.Error("a hash with a single non-zero byte reports IsZero")
	}
}

// TestKindStringHasAnUninformativeValue: a bool named isFolder has no
// uninformative value, and neither should the closed set's String. An
// out-of-range Kind must not render as "file".
func TestKindStringHasAnUninformativeValue(t *testing.T) {
	if got := Kind(99).String(); got != "unknown" {
		t.Errorf("Kind(99).String() = %q, want %q — a value outside the set "+
			"must not render as a kind that is in it", got, "unknown")
	}
	if got := KindFile.String(); got != "file" {
		t.Errorf("KindFile.String() = %q, want %q", got, "file")
	}
}
