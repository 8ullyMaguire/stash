package models

// stash#3530 — the cache key for a scene's generated preview must distinguish windows.
//
// GetVideoPreviewPath(checksum) is shardedJoin(Screenshots, checksum, checksum+".mp4"), and
// `checksum` is scene.GetHash(), which returns s.Checksum or s.OSHash — properties of the FILE. So
// two scenes backed by one file resolve to the SAME preview path, and after 8f84c565f (preview
// content comes from inside the window) they need different previews. Whichever generated first
// wins, silently: both filenames and both URLs look correct.
//
// This is the same defect fixed for HLS segments in stash-3530-hls, where the key was
// hash_streamType_size. Here the hash alone is the key.
//
// The fix is a distinct CHECKSUM rather than a filename change, because shardedJoin derives the
// shard directory from the checksum — appending to the filename would leave both scenes sharing one
// shard directory.

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ranged builds a scene backed by ONE file carrying the given window.
//
// The file is attached with NewRelatedVideoFiles, because GeneratedWindow reads the window from
// scene.Files.Primary() -- and my first version built a bare Scene with the window nowhere, so every
// assertion saw "no window" and every key came out as the plain hash. Six failures, one cause, and
// the failure mode (a correct-looking key that is simply the old one) is exactly what a
// never-widening change would also produce.
func ranged(checksum string, start, end *float64) Scene {
	s := Scene{
		Checksum: checksum,
		OSHash:   checksum,
		ID:       1,
		Files: NewRelatedVideoFiles([]*VideoFile{{
			BaseFile:  &BaseFile{Path: "/media/x.mp4"},
			StartTime: start,
			EndTime:   end,
		}}),
	}
	return s
}

func fp(v float64) *float64 { return &v }

// An unranged scene must keep its EXACT checksum.
//
// This is the guarantee that makes the change safe: every scene in every existing installation is
// unranged, so if this moves, every generated preview in every library 404s and is regenerated —
// tens of thousands of files, over SMB, for no reason.
func TestAnUnrangedSceneKeepsItsExactChecksum(t *testing.T) {
	s := Scene{Checksum: "d3adb33f", OSHash: "d3adb33f"}

	for _, algo := range AllHashAlgorithm {
		assert.Equal(t, s.GetHash(algo), GeneratedChecksum(s, algo),
			"an unranged scene's generated key must be byte-identical to its hash under %s, or "+
				"every already-generated preview stops resolving", algo)
	}
}

// Two scenes of ONE file must get different keys.
//
// This is the bug. With one key for the pair, scene B is served scene A's preview and nothing
// reports it: the URL resolves, the file exists, and the content is of the wrong window.
func TestTwoScenesOfOneFileGetDifferentChecksums(t *testing.T) {
	const checksum = "d3adb33f"
	start, end := fp(60), fp(300)

	a := GeneratedChecksum(Scene{Checksum: checksum, OSHash: checksum}, HashAlgorithmMd5)
	b := GeneratedChecksum(ranged(checksum, start, end), HashAlgorithmMd5)

	assert.NotEqual(t, a, b,
		"two scenes of one file must not share a preview cache key, or the second is silently "+
			"served the first one's preview")

	// Both must remain recognisable as the same file, or nothing can relate them.
	assert.Contains(t, b, checksum, "the window key must still contain the file's checksum")
}

// Two DIFFERENT windows of the same file must also differ from each other, not just from the
// unranged case. A key that only distinguished "windowed vs not" would be no better than one key.
func TestDifferentWindowsOfOneFileGetDifferentChecksums(t *testing.T) {
	const checksum = "d3adb33f"

	first := GeneratedChecksum(ranged(checksum, fp(0), fp(240)), HashAlgorithmMd5)
	second := GeneratedChecksum(ranged(checksum, fp(240), fp(480)), HashAlgorithmMd5)
	open := GeneratedChecksum(ranged(checksum, fp(240), nil), HashAlgorithmMd5)

	assert.NotEqual(t, first, second, "adjacent windows must not collide")
	assert.NotEqual(t, second, open,
		"a bounded window ending at 480 and an open-ended one starting at 240 cover different "+
			"lengths and must not share a key")

	// And identical windows MUST produce identical keys, or a preview is regenerated on every
	// run and the cache does nothing.
	again := GeneratedChecksum(ranged(checksum, fp(240), fp(480)), HashAlgorithmMd5)
	assert.Equal(t, second, again, "the same window must always produce the same key")
}

// A window starting at 0 is a WINDOW, and its key must differ from the unranged one.
//
// The zero value is not "no window" here for exactly the reason it was not in the HLS cache key: a
// scene covering 0..240s of a 2-hour file is nothing like the whole file, and giving it the
// unranged key means it keeps showing the whole file's preview.
func TestAWindowStartingAtZeroIsStillAWindow(t *testing.T) {
	const checksum = "d3adb33f"

	unranged := GeneratedChecksum(Scene{Checksum: checksum, OSHash: checksum}, HashAlgorithmMd5)
	fromZero := GeneratedChecksum(ranged(checksum, fp(0), fp(240)), HashAlgorithmMd5)

	assert.NotEqual(t, unranged, fromZero,
		"a window of 0-240s is not the whole file, even though it starts at 0")
}

// The format, pinned.
//
// It is asserted rather than described because the format is what makes a directory listing
// diagnosable: someone looking at generated/screens needs to tell which window a file belongs to.
// Both ends are present, three decimals, seconds.
func TestTheWindowSuffixNamesBothEnds(t *testing.T) {
	got := GeneratedChecksum(ranged("d3adb33f", fp(60), fp(300)), HashAlgorithmMd5)

	assert.Equal(t, "d3adb33f_w60.000-300.000", got,
		"the suffix must carry BOTH ends: a key naming only the start would collide for every "+
			"window that begins at the same offset, which is the common case for split files")
}

// An open-ended window must still name its start, and must be distinguishable from the window that
// happens to end at 0 (which is impossible, and must not be produced).
func TestAnOpenEndedWindowNamesItsStart(t *testing.T) {
	open := GeneratedChecksum(ranged("d3adb33f", fp(240), nil), HashAlgorithmMd5)

	assert.Contains(t, open, "240", "the start must be in the key")
	assert.NotContains(t, open, "-0.000",
		"an open-ended window must not render its missing end as 0, or it would collide with a "+
			"degenerate zero-length window")
}

// Both hash algorithms must be handled, since GetHash returns different values for each and a
// scene's generated files are keyed by whichever is configured.
func TestTheWindowAppliesToBothHashAlgorithms(t *testing.T) {
	const checksum = "d3adb33f"
	const oshash = "0123456789abcdef"

	md5Scene := Scene{Checksum: checksum, OSHash: oshash}
	windowed := ranged(checksum, fp(0), fp(60))
	windowed.OSHash = oshash
	_ = md5Scene

	for _, algo := range AllHashAlgorithm {
		base := md5Scene.GetHash(algo)
		key := GeneratedChecksum(windowed, algo)

		assert.Contains(t, key, base,
			"under %s the key must still contain the hash it is derived from", algo)
		assert.NotEqual(t, base, key,
			"under %s a windowed scene must not reuse the plain hash as its key", algo)
	}
}

// A scene whose file is missing, or which has no checksum at all, must not panic and must not
// produce a key that looks like a real one. The generate task probes the file separately and the
// scene may have lost it.
func TestASceneWithNoChecksumIsHandled(t *testing.T) {
	assert.NotPanics(t, func() {
		empty := Scene{}
		for _, algo := range AllHashAlgorithm {
			assert.Equal(t, "", GeneratedChecksum(empty, algo),
				"a scene with no checksum must produce an empty key, not a suffix on nothing -- "+
					"which would be a bare `_w0.000-0.000` filename")
		}
	})
}

// A zero-length window is unreachable, so the nil-check is the only guard and it is enough.
//
// Found by mutation: relaxing `pf == nil || (StartTime == nil && EndTime == nil)` to just
// `pf == nil` changed NO result. The obvious explanation would be a missing test, but the real one
// is that the case cannot occur: migration 122's `scenes_files_end_after_start` CHECK is
//
//	CHECK (end_time IS NULL OR start_time IS NULL OR end_time > start_time)
//
// so start_time = 0 with end_time = 0 is REJECTED by the database, through every store and every
// API. A window therefore always has either a non-zero start or a NULL end.
//
// The nil-check is still the right code -- it is what distinguishes "no window" from "a window",
// and it is what a future caller reading this needs to see -- but it guards against a state the
// schema forbids, not one it permits. Recording that here so a future sweep does not report this as
// a missing test and "fix" it by deleting the guard, which would then be load-bearing for a reason
// nobody could reconstruct.
func TestAZeroLengthWindowCannotBeStoredSoItCannotBeDistinguished(t *testing.T) {
	// The shape a relaxed guard would have to handle: both ends present and equal.
	//
	// I first asserted this must produce an EMPTY key, on the reasoning that a degenerate window
	// is indistinguishable from no window. The code produces `d3adb33f_w0.000` instead -- and that
	// is the BETTER answer: a distinct key cannot collide with the unranged scene's, whereas an
	// empty key would be served the whole file's preview. A window that somehow existed is more
	// safely given its own slot than silently merged with the file.
	assert.NotEqual(t, "", GeneratedChecksum(ranged("d3adb33f", fp(0), fp(0)), HashAlgorithmMd5),
		"if a zero-length window ever existed it must still get its OWN key, not the unranged one: "+
			"collapsing it onto the plain hash would serve it the whole file's preview")
	assert.NotEqual(t,
		GeneratedChecksum(ranged("d3adb33f", fp(0), fp(0)), HashAlgorithmMd5),
		GeneratedChecksum(Scene{Checksum: "d3adb33f", OSHash: "d3adb33f"}, HashAlgorithmMd5),
		"and above all it must not collide with the unranged scene")

	// The reachable shapes, for contrast: a NULL end with a zero start IS storable and IS a window.
	fromZero := GeneratedChecksum(ranged("d3adb33f", fp(0), nil), HashAlgorithmMd5)
	assert.NotEqual(t, "", fromZero,
		"start 0 with a NULL end is legal and is a window: the scene runs from the head of the "+
			"file to its end, which is NOT the same as the whole file unless the file has no length")
}

// A scene with a WINDOW but no checksum must produce an empty key, not a bare suffix.
//
// This is the only assertion that can see the `base == ""` guard, and it exists because the earlier
// version of the empty-checksum test used a scene with NO window -- which returns "" either way, so
// the mutant deleting the guard survived it. A test whose subject is a guard has to set up the ONE
// state where the guard changes the answer.
//
// The reason it matters: without the guard the key is "_w60.000-300.000", and every windowed scene
// with a missing checksum would resolve to the SAME key. Worse, `shardedJoin` would derive a shard
// directory from it, so they would also share one directory.
func TestASceneWithAWindowButNoChecksumGetsNoKey(t *testing.T) {
	// A window, and no checksum: the state the guard exists for.
	orphan := ranged("", fp(60), fp(300))
	orphan.Checksum = ""
	orphan.OSHash = ""

	assert.Equal(t, "", GeneratedChecksum(orphan, HashAlgorithmMd5),
		"a scene with no checksum must get NO key, not a bare window suffix: '_w60.000-300.000' "+
			"would be identical for every such scene, so they would share a cache entry and a shard "+
			"directory. An empty key makes the caller regenerate, which is correct.")

	// And it must not produce anything that looks like a usable filename.
	got := GeneratedChecksum(orphan, HashAlgorithmMd5)
	assert.NotContains(t, got, "_w",
		"nothing that looks like a window suffix may come from a scene with no checksum")
}
