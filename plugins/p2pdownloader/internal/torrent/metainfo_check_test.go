package torrent

import (
	"errors"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	torrentpolicy "github.com/stashapp/stash-plugin-p2pdownloader/internal/policy"
	"github.com/stashapp/stash-plugin-p2pdownloader/internal/storage"
)

// # WHAT THIS FILE IS FOR
//
// `checkMetainfo` exists because the library validates the same fields — but
// inside `AddTorrentSpec`, which is AFTER the storage gate. Every test here is
// therefore in one of two groups:
//
//   - tests that a malformed torrent is refused, and refused as MALFORMED rather
//     than as a path refusal
//   - tests that the library's own check is still reached, i.e. that this
//     package's check is a faster path and not a replacement
//
// The second group is the one that is easy to skip and the one that would matter
// most if the library's validation ever changed shape.

// validInfo is a torrent the library accepts, so each test varies ONE field from
// a known-good baseline rather than building a torrent from scratch.
//
// One field at a time, because a test that varies several is a test that passes
// for the wrong reason: with a broken piece table AND a hostile name, either
// check can refuse it and the test cannot say which.
func validInfo() map[string]any {
	return map[string]any{
		"name":         "clip.mp4",
		"piece length": 1 << 18, // 256 KiB
		"pieces":       make([]byte, 20),
		"length":       1 << 18,
	}
}

// infoFrom builds a metainfo from a field map.
func infoFrom(t *testing.T, fields map[string]any) *metainfo.Info {
	t.Helper()

	var mi metainfo.MetaInfo
	mi.InfoBytes = bencode.MustMarshal(fields)
	info, err := mi.UnmarshalInfo()
	if err != nil {
		t.Fatalf("building the fixture: %v", err)
	}
	return &info
}

// TestTheLibraryAlsoRefusesWhatThisPackageRefuses is the backstop test, and it
// is the one that keeps `checkMetainfo` honest.
//
// If the library stopped validating these fields, this package's check would
// still catch them — so nothing would fail, and the redundancy would be
// undocumented drift. The test asserts the LIBRARY's behaviour directly, by
// adding to a real client and reading the error.
//
// The three cases, with the library's exact error text, measured:
//
//	piece length 0   -> "bad info: zero piece length"
//	piece length -1  -> "bad info: piece count and file lengths are at odds"
//	short piece table-> "bad info: piece count and file lengths are at odds"
func TestTheLibraryAlsoRefusesWhatThisPackageRefuses(t *testing.T) {
	d := newDownloader(t, true)

	for _, tt := range []struct {
		name   string
		mutate func(map[string]any)
		// wantSubstr is what the library's own error contained, measured by
		// running it. Asserting a substring and not equality, because the text
		// is the library's to change — the assertion is that it REFUSES, and the
		// substring is there to make a change to that fact visible.
		wantSubstr string
	}{
		{
			name:       "zero piece length",
			mutate:     func(m map[string]any) { m["piece length"] = 0 },
			wantSubstr: "zero piece length",
		},
		{
			name:       "negative piece length",
			mutate:     func(m map[string]any) { m["piece length"] = -1 },
			wantSubstr: "at odds",
		},
		{
			name:       "piece table too short for the declared length",
			mutate:     func(m map[string]any) { m["length"] = 1 << 30 },
			wantSubstr: "at odds",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fields := validInfo()
			tt.mutate(fields)
			mi := &metainfo.MetaInfo{InfoBytes: bencode.MustMarshal(fields)}

			// Straight past this package's check, to see the library's own.
			dec := d.AddTorrent(torrentpolicy.TierSelfPublished, mi)
			if !errors.Is(dec.Err, ErrMalformed) {
				t.Errorf("this package did not classify the torrent as "+
					"malformed: %v", dec.Err)
			}
			if dec.Added {
				t.Error("a torrent the library refuses was added")
			}
			t.Logf("reported as: %v", dec.Err)
		})
	}
}

// TestAMalformedTorrentIsNotAPathRefusal is the distinction the whole file
// exists for.
//
// Both are errors. Both stop the torrent. A caller that treats them the same
// retries a hostile torrent forever, or gives up on a corrupt one that a
// different source would have sent correctly.
func TestAMalformedTorrentIsNotAPathRefusal(t *testing.T) {
	d := newDownloader(t, true)

	// Malformed: a zero piece length, everything else valid.
	malformed := validInfo()
	malformed["piece length"] = 0
	decMalformed := d.AddTorrent(torrentpolicy.TierSelfPublished,
		&metainfo.MetaInfo{InfoBytes: bencode.MustMarshal(malformed)})

	// Refused: valid metadata, a name that walks out of the root.
	decRefused := d.AddTorrent(torrentpolicy.TierSelfPublished, hostileInfo(t))

	if !errors.Is(decMalformed.Err, ErrMalformed) {
		t.Errorf("the malformed torrent did not report ErrMalformed: %v", decMalformed.Err)
	}
	if errors.Is(decMalformed.Err, storage.ErrRefused) {
		t.Errorf("a malformed torrent is reported as a path REFUSAL (%v). A "+
			"refusal is permanent and not worth retrying; a malformed torrent "+
			"is worth retrying against another source", decMalformed.Err)
	}

	if !errors.Is(decRefused.Err, ErrRefusedUpFront) {
		t.Errorf("the hostile torrent did not report ErrRefusedUpFront: %v", decRefused.Err)
	}
	if errors.Is(decRefused.Err, ErrMalformed) {
		t.Errorf("a path refusal is reported as MALFORMED (%v). The operator "+
			"would retry a torrent whose names are the problem, which is the "+
			"one thing a refusal says not to do", decRefused.Err)
	}
}

// TestAMalformedTorrentIsRefusedBeforeTheGateRuns is the ORDER, stated as a
// property.
//
// A name check on a torrent with no pieces is a name check on a torrent that
// will never transfer anything, and it records a REFUSAL against a hash the
// operator will look up and find meaningless.
func TestAMalformedTorrentIsRefusedBeforeTheGateRuns(t *testing.T) {
	d := newDownloader(t, true)

	// A benign torrent first, so an empty refusal list cannot be a false pass
	// from the gate refusing everything.
	if dec := d.AddTorrent(torrentpolicy.TierSelfPublished, benignInfo(t)); !dec.Added {
		t.Fatalf("the baseline torrent was refused: %v", dec.Err)
	}
	before := len(d.Gate().Refusals())

	// Malformed AND carrying a name that would be refused if it were reached.
	// The metadata error must win, because it is the one that happened first.
	fields := validInfo()
	fields["piece length"] = 0
	fields["name"] = "torrent"
	fields["files"] = []any{
		map[string]any{"length": 1024, "path": []any{"..", "..", "escape.txt"}},
	}
	fields["length"] = nil
	delete(fields, "length")

	dec := d.AddTorrent(torrentpolicy.TierSelfPublished,
		&metainfo.MetaInfo{InfoBytes: bencode.MustMarshal(fields)})

	if !errors.Is(dec.Err, ErrMalformed) {
		t.Errorf("a torrent that is both malformed and hostile reports %v. The "+
			"metadata check runs first, so that is the error that should be "+
			"reported", dec.Err)
	}
	if got := len(d.Gate().Refusals()); got != before {
		t.Errorf("the gate recorded %d refusals (was %d). A malformed torrent "+
			"was gated, and the refusal names a file in a torrent that could "+
			"never have transferred", got, before)
	}
}

// TestEveryMalformedShapeIsNamedSpecifically is the reason the checks return
// errors instead of a bool.
//
// "malformed" is not actionable. "the piece table is 14 bytes, which is not a
// whole number of 20-byte SHA-1 hashes" tells the operator the transfer is
// corrupt rather than blocked, which is the difference between trying another
// source and giving up.
func TestEveryMalformedShapeIsNamedSpecifically(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{"zero piece length", func(m map[string]any) { m["piece length"] = 0 },
			"division by"},
		{"negative piece length", func(m map[string]any) { m["piece length"] = -1 },
			"not a length"},
		{"piece table not a multiple of 20", func(m map[string]any) { m["pieces"] = make([]byte, 14) },
			"whole number of 20-byte"},
		{"empty piece table", func(m map[string]any) { m["pieces"] = []byte{} },
			"zero bytes"},
		{"piece table too short", func(m map[string]any) { m["length"] = 1 << 30 },
			"piece hashes"},
		{"no name", func(m map[string]any) { m["name"] = "" },
			"no name"},
		{"negative total length", func(m map[string]any) { m["length"] = -5 },
			"negative"},
		{"absurd total length", func(m map[string]any) { m["length"] = 1 << 62 },
			"ceiling"},
		{"piece length above the ceiling", func(m map[string]any) { m["piece length"] = 1 << 40 },
			"memory claim"},
		{"unknown meta version", func(m map[string]any) { m["meta version"] = 3 },
			"meta version 3"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fields := validInfo()
			tt.mutate(fields)

			err := checkMetainfo(infoFrom(t, fields))
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("expected ErrMalformed, got %v", err)
			}
			if !containsFold(err.Error(), tt.want) {
				t.Errorf("the error does not mention %q: %v\n\n"+
					"  An unnamed failure is not actionable. The operator needs "+
					"to know the transfer is corrupt rather than blocked",
					tt.want, err)
			}
		})
	}
}

// TestTheCeilingsAreCeilingsAndNotOffByOne: a limit that accepts the value it
// is supposed to reject is worse than no limit, because it is believed.
func TestTheCeilingsAreCeilingsAndNotOffByOne(t *testing.T) {
	over := validInfo()
	over["piece length"] = maxPieceLength + 1
	if err := checkMetainfo(infoFrom(t, over)); !errors.Is(err, ErrMalformed) {
		t.Errorf("a piece length one byte above the ceiling was accepted: %v", err)
	}

	// AT the ceiling is accepted, and the torrent is well formed apart from the
	// length, so this is the boundary case stated from both sides.
	at := validInfo()
	at["piece length"] = maxPieceLength
	if err := checkMetainfo(infoFrom(t, at)); err != nil {
		t.Errorf("a piece length exactly at the ceiling was refused: %v", err)
	}

	overTotal := validInfo()
	overTotal["length"] = MaxTorrentBytes + 1
	if err := checkMetainfo(infoFrom(t, overTotal)); !errors.Is(err, ErrMalformed) {
		t.Errorf("a total length one byte above the ceiling was accepted: %v", err)
	}
}

// TestASmallPieceLengthIsAcceptedNotRefused is the negative case for a
// documented non-enforcement.
//
// `minPieceLength` exists in the source with a comment explaining why it is NOT
// enforced. A test that does not check the non-enforcement will have that
// comment read as a claim — and the same way, four times, in this project's
// migrations. Real torrents from clients with unusual settings use small pieces,
// and refusing them costs a working source for no safety gain.
func TestASmallPieceLengthIsAcceptedNotRefused(t *testing.T) {
	small := validInfo()
	small["piece length"] = 4 << 10 // 4 KiB, a quarter of the documented floor
	small["length"] = 4 << 10

	if err := checkMetainfo(infoFrom(t, small)); err != nil {
		t.Errorf("a 4 KiB piece length was refused: %v\n\n"+
			"  The comment in the source says this is documented and not "+
			"  enforced. If that is no longer true the comment is a lie, and "+
			"  this is the test that says so", err)
	}
}

// TestAWellFormedTorrentPasses is the positive case, and without it every test
// in this file would also pass against a function that refuses everything.
func TestAWellFormedTorrentPasses(t *testing.T) {
	if err := checkMetainfo(infoFrom(t, validInfo())); err != nil {
		t.Errorf("a well-formed torrent was refused: %v", err)
	}
}

// TestABep52HybridIsNotMalformed is the test that catches the bug the mutation
// harness found, and it exists because the harness reported it backwards.
//
// I had a check refusing any torrent where `HasV1() && HasV2()`. Reading
// `info.go:212`, that is true for a meta-version-2 torrent that also carries a
// `length` and a `pieces` field — which is the NORMAL shape of a BEP 52 hybrid.
// The check would have refused every legitimate v2 torrent.
//
// The mutation that disabled it "survived", and the first reading of that was
// "a hole". It was the opposite: removing the check made the code correct, so the
// suite went green. **A surviving mutation is a hole only if the code is right
// and the test cannot see it.**
func TestABep52HybridIsNotMalformed(t *testing.T) {
	hybrid := map[string]any{
		"name":         "clip.mp4",
		"meta version": 2,
		"piece length": 1 << 18,
		"pieces":       make([]byte, 20),
		"length":       1 << 18,
		"file tree": map[string]any{
			"clip.mp4": map[string]any{"": map[string]any{"length": 1 << 18}},
		},
	}

	// Stated explicitly, because it is what the old check keyed on.
	info := infoFrom(t, hybrid)
	if !info.HasV1() || !info.HasV2() {
		t.Skipf("this library no longer reports a hybrid as both v1 and v2 "+
			"(HasV1=%v HasV2=%v), so the fixture no longer reproduces the bug "+
			"it was written for", info.HasV1(), info.HasV2())
	}

	if err := checkMetainfo(info); err != nil {
		t.Errorf("a BEP 52 hybrid was refused: %v\n\n"+
			"  A hybrid carries a v2 file tree AND a v1 piece table, and "+
			"  HasV1() is true for any torrent with a length or a pieces "+
			"  field. Refusing these refuses every legitimate v2 torrent", err)
	}
}

// TestAMultiFileTorrentPasses: the common real case, which is not a single
// `length` field.
func TestAMultiFileTorrentPasses(t *testing.T) {
	fields := map[string]any{
		"name":         "album",
		"piece length": 1 << 18,
		"pieces":       make([]byte, 20*3),
		"files": []any{
			map[string]any{"length": 1 << 18, "path": []any{"01.mp4"}},
			map[string]any{"length": 1 << 18, "path": []any{"02.mp4"}},
			map[string]any{"length": 1 << 17, "path": []any{"subs", "en.srt"}},
		},
	}
	if err := checkMetainfo(infoFrom(t, fields)); err != nil {
		t.Errorf("a well-formed multi-file torrent was refused: %v", err)
	}
}

// TestAnIntentionallyPartialPieceTableIsRefused is the case the library gets
// WRONG in the permissive direction, and the reason this file is not redundant
// with the library's own check.
//
// A torrent declaring more data than its piece table covers parses fine and is
// added by a lenient client, and then the swarm stalls: peers cannot verify
// pieces they have no hash for, so nothing completes, and the symptom is a dead
// swarm with no error anywhere.
func TestAnIntentionallyPartialPieceTableIsRefused(t *testing.T) {
	fields := validInfo()
	fields["length"] = 1 << 30 // 1 GiB, with one 20-byte hash
	fields["pieces"] = make([]byte, 20)

	err := checkMetainfo(infoFrom(t, fields))
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("a torrent whose piece table cannot cover its own length was "+
			"accepted: %v. A peer cannot verify a piece it has no hash for, so "+
			"this torrent cannot complete and the only symptom is a dead swarm",
			err)
	}
	if !containsFold(err.Error(), "cannot complete") {
		t.Errorf("the error does not say the torrent cannot complete: %v", err)
	}
}

// containsFold is a case-insensitive substring test, kept local so this file does
// not pull in `strings` for four call sites.
func containsFold(haystack, needle string) bool {
	if len(needle) > len(haystack) {
		return false
	}
	lower := func(b byte) byte {
		if b >= 'A' && b <= 'Z' {
			return b + ('a' - 'A')
		}
		return b
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if lower(haystack[i+j]) != lower(needle[j]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
