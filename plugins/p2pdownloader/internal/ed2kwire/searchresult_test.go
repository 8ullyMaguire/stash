package ed2kwire

import (
	"encoding/binary"
	"encoding/hex"
	"os"
	"testing"
)

// The search RESULT side: real server bytes, decoded.
//
// # WHY THE FIXTURE IS A CAPTURE AND NOT A FIXTURE
//
// Every other test in this package round-trips through our own encoder, which
// proves our two halves agree and nothing more. This one is different and it
// is the most valuable test in the file: testdata/searchresult_live.bin is an
// OP_SEARCHRESULT frame captured from 85.17.116.222:6082 on 2026-09-28 while
// searching for "ubuntu". It is a real server's bytes, so a decoder that
// agrees with it agrees with the network.
//
// # WHAT THE CAPTURE IS
//
//	protocol 0xD4  PACKEDPROT, which means the payload is zlib-compressed
//	opcode   0x33  OP_SEARCHRESULT
//	size     27951 counting the opcode, so 27,950 payload bytes
//	inflated 40,828 bytes, holding 299 results
//
// It ends ONE BYTE after the last complete result. That is not a defect in
// the capture; a decoder that insists on consuming every byte would reject
// every answer this server sends.

// theLiveFrame reads the captured frame, failing the test if it is absent.
//
// A missing fixture is a FAILURE and not a skip. A test that skips while the
// suite reports green is the failure mode this package has been bitten by
// twice, and "someone deleted testdata" is a real break rather than a reason
// to report nothing.
func theLiveFrame(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/searchresult_live.bin")
	if err != nil {
		t.Fatalf("reading the captured search result: %v\n\n"+
			"This fixture is a real OP_SEARCHRESULT frame. It is not "+
			"optional and not generated: without it this test is "+
			"checking our decoder against our own encoder, which "+
			"proves the halves agree and nothing about the wire", err)
	}
	return raw
}

// mustInflateLive inflates the captured frame, failing the test on error.
func mustInflateLive(t *testing.T) []byte {
	t.Helper()
	frame := theLiveFrame(t)
	plain, err := inflateExtHello(frame[6:])
	if err != nil {
		t.Fatalf("inflating the captured result: %v", err)
	}
	return plain
}

// TestTheCapturedSearchResultDecodes: 299 files, names and all.
func TestTheCapturedSearchResultDecodes(t *testing.T) {
	frame := theLiveFrame(t)

	if frame[0] != 0xD4 {
		t.Errorf("the frame's protocol byte is 0x%02X, want 0xD4 "+
			"(PACKEDPROT). A search RESULT is compressed and a plain "+
			"0xE3 frame is a different packet", frame[0])
	}
	if frame[5] != 0x33 {
		t.Errorf("the frame's opcode is 0x%02X, want 0x33 "+
			"(OP_SEARCHRESULT)", frame[5])
	}

	plain, err := inflateExtHello(frame[6:])
	if err != nil {
		t.Fatalf("the captured result does not inflate: %v", err)
	}
	if len(plain) < searchResultHeaderLen {
		t.Fatalf("it inflated to %d bytes, shorter than its %d-byte header",
			len(plain), searchResultHeaderLen)
	}

	results, err := DecodeSearchResult(plain)
	if err != nil {
		t.Fatalf("DecodeSearchResult on REAL server bytes: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("the capture decoded to zero results.\n\n" +
			"An empty list here is indistinguishable from a server that " +
			"found nothing, which is the specific failure this decoder " +
			"must not have")
	}
	t.Logf("decoded %d results from %d inflated bytes", len(results), len(plain))

	// # THE NAMES ARE THE PROOF
	//
	// Counts and sizes are numbers, and a decoder that mis-walks the stream
	// can still produce plausible ones. Filenames are ASCII chosen by
	// strangers, so "Hw-004.mp4" appearing intact means the walk is aligned
	// to the byte.
	first := results[0]
	if first.Name != "Hw-004.mp4" {
		t.Errorf("the first result is named %q, want %q. A wrong name "+
			"means the tag walk is misaligned, and every field after "+
			"it is then read from the wrong offset", first.Name, "Hw-004.mp4")
	}
	if first.Size != 505365630 {
		t.Errorf("the first result's size is %d, want 505365630", first.Size)
	}
	if first.Type != 2 {
		t.Errorf("the first result's type is %d, want 2 (video)", first.Type)
	}
	if first.Sources != 25 || first.SourcesComplete != 25 {
		t.Errorf("the first result reports %d sources and %d complete, "+
			"want 25 and 25", first.Sources, first.SourcesComplete)
	}
	// The port of 4662 is eMule's standard Kad port, and it is what
	// confirmed the 22-byte file-ID layout rather than a guess.
	if first.Port != 4662 {
		t.Errorf("the first result's port is %d, want 4662 -- eMule's "+
			"standard Kad port, and the value that established the "+
			"22-byte file ID", first.Port)
	}
}

// TestTheCapturedHashIsSixteenBytesAndDiffersPerFile.
//
// The hash is the file's identity, and two results claiming the same one
// would mean the 22-byte walk is off and every hash is the previous file's.
func TestTheCapturedHashIsSixteenBytesAndDiffersPerFile(t *testing.T) {
	plain := mustInflateLive(t)
	results, err := DecodeSearchResult(plain)
	if err != nil {
		t.Fatalf("DecodeSearchResult: %v", err)
	}

	seen := make(map[[16]byte]bool, len(results))
	for i, r := range results {
		if r.Hash == ([16]byte{}) {
			t.Fatalf("result %d (%q) has an all-zero hash. A zero hash is "+
				"what a misaligned walk produces, and a request "+
				"quoting it would ask for nothing", i, r.Name)
		}
		seen[r.Hash] = true
	}
	if len(seen) != len(results) {
		t.Errorf("%d results share %d distinct hashes, so the file IDs "+
			"are not being read per result", len(results), len(seen))
	}
	t.Logf("%d results, %d distinct file hashes", len(results), len(seen))
}

// TestTheLastResultIsCompleteEvenThoughTheFrameIsNot.
//
// # A TRAILING BYTE IS NOT A TRUNCATED RESULT
//
// The capture's 40,828-byte body ends one byte after the final file ID. A
// decoder that required the walk to consume every byte would reject every
// answer this server sends -- and the rejection would look like "this server
// found nothing", because an error and an empty list mean different things
// to a caller but the same thing to a log line.
func TestTheLastResultIsCompleteEvenThoughTheFrameIsNot(t *testing.T) {
	plain := mustInflateLive(t)

	results, err := DecodeSearchResult(plain)
	if err != nil {
		t.Fatalf("DecodeSearchResult: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no results, so there is no last one to be complete")
	}

	last := results[len(results)-1]
	if last.Name == "" {
		t.Error("the last result has no name, so the final file ID was " +
			"read from the wrong offset and the last result is " +
			"probably truncated")
	}
	if last.Port == 0 {
		t.Error("the last result has port 0, which is not a value a " +
			"server reports for a source it is offering")
	}
}

// TestAStrFamilyTypeByteCarriesTheLength: the detail that took three tries.
//
// # 0x9A IS A TEN-BYTE STRING, AND THE SUBTRACTION IS FROM THE MASKED TYPE
//
// On the wire a length-carrying string is 0x80 | (0x10 + n), so 0x9A is ten
// bytes, 0x99 is nine, 0x9B is eleven. Three tags in the capture agree:
// 0x9A then "Hw-004.mp4", 0x99 then ".DS_Store", 0x9B then "OAV1365.mp4".
//
// # AND parseTag MASKS THE HIGH BIT FIRST, WHICH IS WHY IT WORKS
//
// tag.go stores Type as payload[0] & 0x7F, so a wire 0x9A becomes 0x1A and
// 0x1A - 0x10 is 10. The mask is what makes the masked convention and the
// wire convention agree, and it is worth stating because a reader who
// subtracts 0x10 from the RAW wire byte gets 138 and swallows the rest of the
// packet. That is not hypothetical: a first attempt at this decoder did
// exactly that and reported a count of 2.4 billion.
func TestAStrFamilyTypeByteCarriesTheLength(t *testing.T) {
	plain := mustInflateLive(t)
	results, err := DecodeSearchResult(plain)
	if err != nil {
		t.Fatalf("DecodeSearchResult: %v", err)
	}

	// If the Str length were misread, the names would be absurdly long --
	// each one swallowing the following tags. A name longer than, say, 512
	// bytes is a misaligned walk, not a filename.
	for i, r := range results {
		if len(r.Name) > 512 {
			t.Fatalf("result %d has a %d-byte name (%q...). A Str "+
				"family type byte carries its own length; reading "+
				"it from the raw wire byte without masking gives "+
				"0x9A-0x10=138 and swallows every following tag",
				i, len(r.Name), r.Name[:40])
		}
		if len(r.Name) == 0 {
			t.Fatalf("result %d has an empty name", i)
		}
	}

	// And the three short names in the capture, which pin the arithmetic
	// exactly rather than approximately.
	byName := make(map[string]SearchResult, len(results))
	for _, r := range results {
		byName[r.Name] = r
	}
	for _, want := range []string{"Hw-004.mp4", ".DS_Store", "OAV1365.mp4"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("no result named %q. The capture contains it, and "+
				"its three different lengths (10, 9, 11) are what "+
				"settle the Str-family base", want)
		}
	}
}

// TestEachResultIsFollowedByTwentyTwoBytes: the boundary that is not 16.
//
// # 16 LOOKS LIKE AN OBVIOUS BOUNDARY AND IS NOT ONE
//
// After a result's tag list come a 16-byte hash, a 4-byte user ID and a
// 2-byte port. Reading only the hash puts the next count two bytes early, and
// the parse then reports a count of 988,510,410 -- a number that is not an
// error but is impossible, which is the worst kind.
//
// The port is what confirms it: 4662 on the first result is eMule's standard
// Kad port, a value nobody would guess.
func TestEachResultIsFollowedByTwentyTwoBytes(t *testing.T) {
	plain := mustInflateLive(t)

	// Walk by hand to the first result's end, then read the 22 bytes.
	_, n, err := parseTagListAt(plain[searchResultHeaderLen:])
	if err != nil {
		t.Fatalf("parsing the first result's tags: %v", err)
	}
	off := searchResultHeaderLen + n

	if off+fileIDLen > len(plain) {
		t.Fatalf("the first result ends at %d and the body is %d bytes, so "+
			"there is no room for a %d-byte file ID", off, len(plain),
			fileIDLen)
	}

	port := binary.LittleEndian.Uint16(plain[off+20 : off+22])
	if port != 4662 {
		t.Errorf("the port at the end of the first result's %d bytes is "+
			"%d, want 4662. A 16-byte read would land here two bytes "+
			"early and produce a count of 988,510,410 instead of an "+
			"error", fileIDLen, port)
	}
	user := binary.LittleEndian.Uint32(plain[off+16 : off+20])
	if user == 0 {
		t.Error("the user ID is 0, which is not what a server offering a " +
			"source reports")
	}
}

// TestAShortBodyIsAnErrorNotAnEmptyList: the rule the whole file follows.
func TestAShortBodyIsAnErrorNotAnEmptyList(t *testing.T) {
	for _, n := range []int{0, 1, 8, searchResultHeaderLen - 1} {
		results, err := DecodeSearchResult(make([]byte, n))
		if err == nil {
			t.Errorf("a %d-byte body decoded without error into %d "+
				"results. A body shorter than the header cannot "+
				"describe results, and an empty list here reads as "+
				"'the server found nothing'", n, len(results))
		}
	}
}

// TestATruncatedResultIsRefusedButATrailingByteIsNot.
//
// # THE TWO CASES LOOK IDENTICAL AND ARE NOT
//
// A result cut short mid-file-ID, and a body that ends with a stray byte after
// its last complete result, are both "there are not enough bytes left". They
// are handled differently on purpose, and the reason is the capture: its
// 300th entry has five complete tags and one byte of a 22-byte file ID.
//
// Refusing that would make a real server's real answer undecodable, over one
// trailing byte, and would cost the caller 299 good results. Inventing a
// result from the missing bytes is the other failure. So a result with no
// room for its identity ENDS the list.
//
// What must still be an error is a result whose TAGS are truncated: there the
// packet genuinely claims a file and cannot supply it, and silently dropping
// it would report a partial answer as a whole one.
func TestATruncatedResultIsRefusedButATrailingByteIsNot(t *testing.T) {
	plain := mustInflateLive(t)

	// The capture's own tail: 299 results and a stray byte, and the decoder
	// must return all 299 without complaint.
	full, err := DecodeSearchResult(plain)
	if err != nil {
		t.Fatalf("the capture itself does not decode: %v", err)
	}
	if len(full) != 299 {
		t.Errorf("the capture decoded to %d results, want 299", len(full))
	}

	// Now cut mid-file-ID. The tags of the first result are complete and its
	// 22-byte identity is not there, so the list ends with zero results --
	// NOT an error, and that is the behaviour being pinned.
	_, n, err := parseTagListAt(plain[searchResultHeaderLen:])
	if err != nil {
		t.Fatalf("parsing the first result: %v", err)
	}
	end := searchResultHeaderLen + n
	cut := plain[:end+fileIDLen-3]

	results, err := DecodeSearchResult(cut)
	if err != nil {
		t.Errorf("a result cut mid-file-ID returned %v, want no error and an "+
			"empty list. This is the same shape as the capture's "+
			"trailing byte, and erroring here makes every real "+
			"answer undecodable", err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results from a body with no complete result in "+
			"it, want 0. Inventing one would mean reading a hash out "+
			"of memory", len(results))
	}

	// And a result whose TAGS are cut short IS an error, because there the
	// packet claims a file it cannot describe.
	_, n2, err := parseTagListAt(plain[searchResultHeaderLen:])
	if err != nil {
		t.Fatalf("parsing the first result again: %v", err)
	}
	tagsCut := plain[:searchResultHeaderLen+n2-4]
	if _, err := DecodeSearchResult(tagsCut); err == nil {
		t.Error("a result whose tag list is cut short decoded without error. " +
			"The packet claims a file and cannot supply it, and " +
			"dropping it silently would report a partial answer as a " +
			"whole one")
	}
}

// TestTheInflatedCaptureIsZlibNotRawDeflate: the frame's protocol byte says so.
//
// # 0xD4 IS PACKEDPROT, AND THAT IS THE WHOLE POINT
//
// A result arrives zlib-compressed while a REQUEST does not. Reading one as
// the other is the asymmetry search.go's header warns about, and the
// compressed body looks like noise either way -- so the only thing that
// catches it is trying to inflate it.
func TestTheInflatedCaptureIsZlibNotRawDeflate(t *testing.T) {
	frame := theLiveFrame(t)
	body := frame[6:]

	if body[0]&0x0F != 0x08 {
		t.Errorf("the payload starts 0x%02X, whose low nibble is not 8, so "+
			"it is not a zlib stream. The capture is a PACKEDPROT "+
			"frame and its payload IS compressed", body[0])
	}
	if (uint16(body[0])*256+uint16(body[1]))%31 != 0 {
		t.Errorf("the payload's first two bytes 0x%02X 0x%02X are not a "+
			"multiple of 31, so they are not a zlib header",
			body[0], body[1])
	}

	// And it genuinely inflates, to far more than it occupied.
	plain, err := inflateExtHello(body)
	if err != nil {
		t.Fatalf("inflating the capture: %v", err)
	}
	if len(plain) <= len(body) {
		t.Errorf("it inflated to %d bytes from %d compressed, which is not "+
			"compression", len(plain), len(body))
	}
	t.Logf("%d compressed -> %d inflated", len(body), len(plain))
}

// TestAGoldenFirstResult: the first result as bytes, so the decode is pinned.
//
// # GOLDEN, NOT RE-DERIVED
//
// The expected values below are read off the capture by hand. What this adds
// beyond the count-and-name test is the HASH, as hex: a 32-character string
// that cannot be produced by a wrong walk, because a wrong walk reads a
// different file's 16 bytes.
//
// # WHY NOT A ROUND TRIP
//
// There is no encoder for a search result -- a stranger writes these bytes. A
// round trip is not available, which is the whole reason this file exists and
// the reason the fixture is a capture.
func TestAGoldenFirstResult(t *testing.T) {
	plain := mustInflateLive(t)
	results, err := DecodeSearchResult(plain)
	if err != nil {
		t.Fatalf("DecodeSearchResult: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no results decoded")
	}

	first := results[0]
	const wantHash = "40d349929c69b3735a1d5247b6fedde6"
	if got := hex.EncodeToString(first.Hash[:]); got != wantHash {
		t.Errorf("the first result's hash is %s, want %s.\n\n"+
			"These 32 characters are read off the capture at the "+
			"offset the 22-byte file ID implies. A different value "+
			"means the walk is aligned to the wrong byte, which no "+
			"count or name assertion would catch", got, wantHash)
	}
	if first.UserID != 988510410 {
		t.Errorf("the first result's user ID is %d, want 988510410",
			first.UserID)
	}
}
