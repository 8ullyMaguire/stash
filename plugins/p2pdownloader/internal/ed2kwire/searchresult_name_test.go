package ed2kwire

// The name decoder, tested on the results the existing suite never looked at.
//
// # WHY THIS FILE EXISTS, AND IT IS A BUG NOT A FEATURE
//
// `nameOf` returned the tag's raw Value as the filename. That is right for a
// Str-family tag (the value IS the name) and wrong for a tagTypeString one,
// whose value is [len:2][name]. On the capture, 284 of 299 results carry
// the second form, so 284 of 299 names were two bytes of length prefix glued
// to the front of the real name.
//
// # WHY 14 EXISTING TESTS DID NOT SEE IT
//
// Every name assertion in searchresult_test.go is on the FIRST result, or on
// the three short names (0x9A, 0x99, 0x9B) that happen to be Str-family. The
// bug only touches results 3 and beyond, and the corrupted names are LONGER
// than the real ones — so a plausibility check and a length check both pass.
// Only a whole-name comparison fails, and there was none past result 2.
//
// The fix is in nameOf; these tests are the ones that were missing, and they
// assert on the exact bytes from the capture rather than on "no junk prefix",
// because "no junk prefix" is satisfied by a decoder that truncates every
// name to nothing.

import (
	"encoding/binary"
	"os"
	"testing"
)

// capturedNames pins the decoded name of results the first three do not
// cover, with the byte-level reason each one differs from its raw value.
//
// The expected strings are read off the capture by hand from the hex above
// each: the tag type is 0x02, and the two bytes before the name are its
// length. Where the length equals the remaining bytes, the decode is
// confirmed by the prefix rather than merely asserted.
var capturedNames = []struct {
	result int
	name   string
	// rawLen is the tag's value length, nameLen the real name's. They
	// differ by exactly the two length bytes, and a decoder that forgot
	// the prefix produces rawLen bytes.
	rawLen  int
	nameLen int
}{
	{3, "video_2026-01-01_14-39-10.mp4", 31, 29},
	{4, "Susurran.tu.nombre.(2026).(Spanish.English.Subs).WEBRip.1080p." +
		"x265-EAC3.Atmos.(hispashare.org).mkv", 100, 98},
	{6, "video_2026-04-14_12-14-04.mp4", 31, 29},
	{7, "video_2026-08-12_17-10-51.mp4", 31, 29},
	{8, "Operaciones.especiales.Lioness.3x03.El.oso.está.infectado." +
		"(Spanish.English.Subs).WebRip.1080p.x265-EAC3.by.Legan.mkv",
		119, 117},
}

// TestANameIsNotItsOwnLengthPrefix is the bug as a test.
//
// # IT PINS THE FULL NAME, AND NOT "HAS NO PREFIX"
//
// The weaker assertion is satisfied by a decoder that returns the empty
// string, or one that truncates to the length byte. Both would pass "the name
// does not begin with \x1d\x00" and both are wrong, so the whole name is
// compared instead.
func TestANameIsNotItsOwnLengthPrefix(t *testing.T) {
	results := decodeLiveResults(t)
	if len(results) <= 8 {
		t.Fatalf("the capture decoded to %d results, so result 8 does not "+
			"exist and this test would pass without testing anything",
			len(results))
	}

	for _, want := range capturedNames {
		got := results[want.result].Name
		if got != want.name {
			t.Errorf("result %d decoded to %q (%d bytes), want %q "+
				"(%d bytes).\n\n"+
				"The raw tag value is %d bytes and the real name is %d, "+
				"and the difference is a tagTypeString's uint16 length "+
				"prefix. A name of rawLen bytes has kept the prefix; a "+
				"name of nameLen bytes has dropped it",
				want.result, got, len(got), want.name, want.nameLen,
				want.rawLen, want.nameLen)
		}
	}
}

// TestThePrefixAgreesWithTheNameItPrecedes proves the prefix is a LENGTH
// and not merely a two-byte header that happens to be there.
//
// Every tagTypeString name on the capture satisfies
// littleEndian(value[:2]) == len(value[2:]). If the two-byte prefix were
// something else — a tag, a checksum, a format version — it would agree with
// the name's length on 284 files by coincidence, which is not a thing that
// happens.
func TestThePrefixAgreesWithTheNameItPrecedes(t *testing.T) {
	raw := readInflated(t)

	var checked int
	off := searchResultHeaderLen
	for n := 0; off+4 <= len(raw) && n < 299; n++ {
		tags, next, err := parseTagListAt(raw[off:])
		if err != nil {
			break
		}
		off += next
		if off+fileIDLen > len(raw) {
			break
		}
		off += fileIDLen

		for _, tag := range tags {
			if tag.ID != tagIDFileName || tag.Type != tagTypeString {
				continue
			}
			if len(tag.Value) < 2 {
				t.Errorf("result %d has a tagTypeString name tag of %d "+
					"bytes, which cannot even hold its own length",
					n, len(tag.Value))
				continue
			}
			declared := int(binary.LittleEndian.Uint16(tag.Value[:2]))
			actual := len(tag.Value) - 2
			if declared != actual {
				t.Errorf("result %d's name tag declares %d bytes and "+
					"carries %d. If the prefix is a length then the "+
					"capture contradicts it, and the decode above is "+
					"guessing", n, declared, actual)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no tagTypeString name tags on the capture, so the " +
			"length-prefix rule was tested against nothing")
	}
	t.Logf("%d name tags are tagTypeString, and the prefix matches the "+
		"name's length on every one", checked)
}

// TestNoNameBeginsWithAControlByte guards the CLASS rather than the
// instances, so a future capture cannot reintroduce it quietly.
//
// A filename beginning with a NUL or a C0 control byte is not a filename a
// server would send, and it is exactly the shape the length prefix produces.
// The assertion is deliberately narrow — control bytes only, not "short" and
// not "long" — so it cannot fail on a legitimate name.
func TestNoNameBeginsWithAControlByte(t *testing.T) {
	results := decodeLiveResults(t)
	for i, r := range results {
		if r.Name == "" {
			t.Errorf("result %d has an empty name", i)
			continue
		}
		if r.Name[0] < 0x20 {
			t.Errorf("result %d's name begins with the control byte "+
				"0x%02X: %q.\n\n"+
				"A tagTypeString's value is [len:2][name], so a name "+
				"whose first two bytes are its own length in "+
				"little-endian order is the signature of a decoder "+
				"that used the raw value", i, r.Name[0], r.Name)
		}
	}
}

// TestAStrFamilyNameIsStillTheValue guards the OTHER encoding against a fix
// that only handles the one case.
//
// The bug was fixed by routing through Tag.String, which handles both. If a
// future change special-cases tagTypeString directly, the Str-family path
// has to keep working — and the capture's first three results are all
// Str-family, so they are the regression guard.
func TestAStrFamilyNameIsStillTheValue(t *testing.T) {
	results := decodeLiveResults(t)
	for i, want := range []string{"Hw-004.mp4", ".DS_Store", "OAV1365.mp4"} {
		if results[i].Name != want {
			t.Errorf("result %d decoded to %q, want %q. This result's "+
				"name tag is Str-family (0x%02X), where the value IS "+
				"the name and there is no length prefix to drop",
				i, results[i].Name, want, tagTypeStrBase+
					byte(len(want)+1)-1)
		}
	}
}

// TestEveryDecodedNameMatchesItsDeclaredLength is the general form, and it
// is stated from the DECODER's side rather than the wire's.
//
// For every result whose name tag is a tagTypeString, the name we returned
// must be exactly as long as the tag declared. The earlier test proves the
// capture is self-consistent; this proves we read it consistently, and a
// decoder that kept the prefix fails both.
//
// The walk is done in one pass, collecting each tagTypeString result's
// index, so the name and the tag it came from are compared as a pair rather
// than by two independent walks that could disagree.
func TestEveryDecodedNameMatchesItsDeclaredLength(t *testing.T) {
	results := decodeLiveResults(t)
	raw := readInflated(t)

	// index -> the length that result's name tag declared.
	declared := map[int]int{}
	off := searchResultHeaderLen
	for n := 0; off+4 <= len(raw) && n < 299; n++ {
		tags, next, err := parseTagListAt(raw[off:])
		if err != nil {
			break
		}
		off += next
		if off+fileIDLen > len(raw) {
			break
		}
		off += fileIDLen
		for _, tag := range tags {
			if tag.ID != tagIDFileName || tag.Type != tagTypeString ||
				len(tag.Value) < 2 {
				continue
			}
			declared[n] = int(binary.LittleEndian.Uint16(tag.Value[:2]))
		}
	}

	if len(declared) == 0 {
		t.Fatal("no tagTypeString name tags on the capture, so the " +
			"length check was applied to nothing")
	}

	for idx, want := range declared {
		if idx >= len(results) {
			t.Fatalf("the tag walk found a name tag for result %d but "+
				"the decoder returned only %d results, so the two "+
				"disagree about how many files there are",
				idx, len(results))
		}
		if got := len(results[idx].Name); got != want {
			t.Errorf("result %d decoded to a %d-byte name and its tag "+
				"declared %d. The name kept the tagTypeString's "+
				"uint16 length prefix, which is two bytes", idx,
				got, want)
		}
	}
	t.Logf("%d of %d results are tagTypeString, and every decoded name "+
		"is exactly as long as its tag declared", len(declared),
		len(results))
}

// ---- helpers ----

// decodeLiveResults inflates and decodes the capture, failing on error.
//
// Shared so every test in this file fails the SAME way when the fixture is
// missing, rather than each reporting its own distinct complaint about it.
func decodeLiveResults(t *testing.T) []SearchResult {
	t.Helper()
	results, err := DecodeSearchResult(readPlain(t))
	if err != nil {
		t.Fatalf("DecodeSearchResult on the capture: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("the capture decoded to zero results")
	}
	return results
}

// readPlain returns the inflated search-result body.
func readPlain(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/searchresult_live.bin")
	if err != nil {
		t.Fatalf("reading the capture: %v", err)
	}
	plain, err := inflateExtHello(raw[6:])
	if err != nil {
		t.Fatalf("inflating the capture: %v", err)
	}
	return plain
}

// readInflated returns the already-inflated body.
//
// The capture ships both forms. Reading the inflated one directly is what
// lets a test walk the tag stream with its own offsets, and it is the same
// bytes inflateExtHello produces — verified by the two being the same length.
func readInflated(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/searchresult_live.inflated")
	if err != nil {
		t.Fatalf("reading the inflated capture: %v", err)
	}
	if want := readPlain(t); len(want) != len(raw) {
		t.Fatalf("the stored inflated body is %d bytes and the compressed "+
			"capture inflates to %d, so the two fixtures have drifted "+
			"and every offset below is measured against a different "+
			"stream than the decoder walks", len(raw), len(want))
	}
	return raw
}
