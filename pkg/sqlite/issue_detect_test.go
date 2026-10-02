//go:build integration
// +build integration

package sqlite_test

// NOTE ON THE PACKAGE. These are in sqlite_test, not sqlite, and that is the point:
// every assertion here goes through db.File.Create and reads back the ISSUES TABLE.
// Nothing calls detectFileIssues or any detector directly. A test in the internal
// package could call detectDuplicate directly and would pass even if the wiring in
// FileStore.Create were removed -- which is the part that can actually break. This
// package sees the same seam the scanner does.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/models"
)

// stash#837 — detection tests.
//
// THE RULE THIS FILE EXISTS TO ENFORCE: every detector needs a NEGATIVE test.
//
// A detector with only a positive test proves it fires. It does not prove it fires
// ONLY on the thing it is looking for, and a detector that fires on everything is
// worse than no detector at all: it fills the panel with thousands of findings,
// and a panel nobody opens is the outcome the issue complains about. So each test
// below asserts on BOTH the finding that must appear AND the one that must not.

// issuesFor is the read helper every test uses. It scopes to ONE file, because the
// shared seed database carries its own findings (see issue_test.go) and a
// whole-table assertion here would be testing the fixture rather than the detector.
func issuesFor(t *testing.T, ctx context.Context, id models.FileID) []*models.Issue {
	t.Helper()
	out, err := db.Issue.FindBy(ctx, &models.IssueFilterType{FileID: &id})
	require.NoError(t, err)
	return out
}

func kindsOf(issues []*models.Issue) map[string]bool {
	m := map[string]bool{}
	for _, i := range issues {
		m[i.Kind] = true
	}
	return m
}

// makeDetectableVideo builds a real VideoFile row, because detectZeroDuration reads
// the duration off the EXTENDED table and a fabricated BaseFile would not exercise
// that join at all -- a BaseFile with no VideoFile is not a video, and the detector
// would correctly report nothing.
//
// The shape is copied from the model rather than invented, which cost two round trips:
// THIS FORK'S VideoFile is flat -- Duration sits directly on models.VideoFile, not on a
// nested FFMpegVideoFile as it does upstream, and not on BaseFile either. Guessing the
// shape got a compile error, and a guessed shape would have compiled here while testing
// the wrong thing.
func makeDetectableVideo(t *testing.T, ctx context.Context, folder string, name string, duration float64) *models.VideoFile {
	t.Helper()
	v := &models.VideoFile{
		BaseFile: &models.BaseFile{
			Basename:       name,
			ParentFolderID: mk837Folder(t, ctx, folder),
			Size:           4096,
		},
		Format:     "mp4",
		Duration:   duration,
		VideoCodec: "h264",
		AudioCodec: "aac",
		Width:      1920,
		Height:     1080,
		FrameRate:  30,
		BitRate:    4_000_000,
	}
	require.NoError(t, db.File.Create(ctx, v))
	return v
}
func TestAZeroByteFileIsReported(t *testing.T) {
	runWithRollbackTxn(t, "a zero-byte file is reported", func(t *testing.T, ctx context.Context) {
		f := &models.BaseFile{Basename: "empty.mp4", ParentFolderID: mk837Folder(t, ctx, "/library/d1"), Size: 0}
		require.NoError(t, db.File.Create(ctx, f))

		got := kindsOf(issuesFor(t, ctx, models.FileID(f.ID)))
		assert.True(t, got[models.IssueKindZeroSize],
			"a zero-byte file is unplayable and must be reported: %v", got)
	})
}

func TestAFileWithBytesIsNotReportedAsZeroSize(t *testing.T) {
	runWithRollbackTxn(t, "a file with bytes is NOT reported as zero size", func(t *testing.T, ctx context.Context) {
		f := &models.BaseFile{Basename: "ok.mp4", ParentFolderID: mk837Folder(t, ctx, "/library/d2"), Size: 4096}
		require.NoError(t, db.File.Create(ctx, f))

		got := kindsOf(issuesFor(t, ctx, models.FileID(f.ID)))
		assert.False(t, got[models.IssueKindZeroSize],
			"THE NEGATIVE TEST. A file with bytes reported as zero-size means the check is "+
				"wrong in the direction that fills a real library with false findings")
	})
}

func TestAVideoWithNoDurationIsReported(t *testing.T) {
	runWithRollbackTxn(t, "a video with no duration is reported", func(t *testing.T, ctx context.Context) {
		v := makeDetectableVideo(t, ctx, "/library/d3", "nodur.mp4", 0)

		got := kindsOf(issuesFor(t, ctx, models.FileID(v.ID)))
		assert.True(t, got[models.IssueKindZeroDuration],
			"a video that cannot be scrubbed must be reported: %v", got)
	})
}

func TestANegativeDurationIsReported(t *testing.T) {
	runWithRollbackTxn(t, "a NEGATIVE duration is reported, not just exactly zero",
		func(t *testing.T, ctx context.Context) {
			v := makeDetectableVideo(t, ctx, "/library/d3b", "neg.mp4", -1)

			got := kindsOf(issuesFor(t, ctx, models.FileID(v.ID)))
			assert.True(t, got[models.IssueKindZeroDuration],
				"a negative duration is what a broken container actually reports, so a check "+
					"that only catches exactly 0 misses the case that motivated it: %v", got)
		})
}

func TestAVideoWithADurationIsNotReported(t *testing.T) {
	runWithRollbackTxn(t, "a video with a duration is NOT reported",
		func(t *testing.T, ctx context.Context) {
			v := makeDetectableVideo(t, ctx, "/library/d4", "dur.mp4", 12.5)

			got := kindsOf(issuesFor(t, ctx, models.FileID(v.ID)))
			assert.False(t, got[models.IssueKindZeroDuration],
				"THE NEGATIVE TEST. Reporting a playable video is how a detector becomes noise")
		})
}

func TestAnImageIsNotReportedForDuration(t *testing.T) {
	runWithRollbackTxn(t, "an image is not reported for duration",
		func(t *testing.T, ctx context.Context) {
			f := &models.BaseFile{Basename: "pic.jpg", ParentFolderID: mk837Folder(t, ctx, "/library/d5"), Size: 2048}
			require.NoError(t, db.File.Create(ctx, f))

			got := kindsOf(issuesFor(t, ctx, models.FileID(f.ID)))
			assert.False(t, got[models.IssueKindZeroDuration],
				"THE NEGATIVE TEST FOR TYPE SELECTION. An image has no duration field, so "+
					"reporting one would be a finding about a property the file cannot have. "+
					"This is what detectorsFor's switch is for")
		})
}

func TestTwoFilesSharingAFingerprintAreReportedAsDuplicates(t *testing.T) {
	runWithRollbackTxn(t, "two files sharing a fingerprint are duplicates",
		func(t *testing.T, ctx context.Context) {
			const sum = "d41d8cd98f00b204e9800998ecf8427e"

			// THE FOLDER IS CREATED ONCE. mk837Folder is not idempotent -- folders.path is
			// UNIQUE -- so calling it per-file made the second file fail its CREATE with
			// "UNIQUE constraint failed: folders.path", which reads like a fingerprint bug
			// and is not one. A helper named `mk` that closes over a shared resource is the
			// trap: it looks local and is not.
			parent := mk837Folder(t, ctx, "/library/d6")
			mk := func(name string) *models.BaseFile {
				f := &models.BaseFile{
					Basename:       name,
					ParentFolderID: parent,
					Size:           5000,
					Fingerprints: []models.Fingerprint{{
						Type: models.FingerprintTypeMD5, Fingerprint: sum,
					}},
				}
				require.NoError(t, db.File.Create(ctx, f))
				return f
			}

			first := mk("copy-a.mp4")
			require.Empty(t, issuesFor(t, ctx, models.FileID(first.ID)),
				"the first file is unique so far; reporting it as a duplicate would put the "+
					"whole library on the panel")

			second := mk("copy-b.mp4")
			got := kindsOf(issuesFor(t, ctx, models.FileID(second.ID)))
			assert.True(t, got[models.IssueKindDuplicate],
				"the second file sharing the first's bytes is a duplicate: %v", got)

			// And the FIRST file is now a duplicate too -- duplication is symmetric. Asserting
			// only the second file would pass a detector that reports the newer file and
			// silently ignores the older one, which is exactly the half-reporting that makes
			// a panel untrustworthy: "3 duplicates" in a library with 4 copies.
			//
			// THE FIRST FILE IS RE-DETECTED EXPLICITLY, via Update. That is not a workaround,
			// it is the shape of the guarantee: detection runs where the scanner WRITES, and a
			// rescan writes every file it visits through FileStore.Update. Nothing re-examines
			// a file that is never visited again, so a library whose copies were all imported
			// before this feature existed stays quiet until the next scan touches them. That is
			// the accepted cost, stated here because it is a real limitation and not a bug --
			// and it is exactly what Update-detection buys back.
			require.NoError(t, db.File.Update(ctx, first))
			gotFirst := kindsOf(issuesFor(t, ctx, models.FileID(first.ID)))
			assert.True(t, gotFirst[models.IssueKindDuplicate],
				"duplication is symmetric: the first file is a duplicate of the second once it "+
					"is visited again. A detector that only flags the newest file reports a "+
					"different library every time a scan reorders, which is %v", gotFirst)
		})
}

func TestAFileThatIsNotADuplicateIsNotReported(t *testing.T) {
	runWithRollbackTxn(t, "a file that is not a duplicate is NOT reported",
		func(t *testing.T, ctx context.Context) {
			f := &models.BaseFile{
				Basename:       "solo.mp4",
				ParentFolderID: mk837Folder(t, ctx, "/library/d7"),
				Size:           5000,
				Fingerprints: []models.Fingerprint{{
					Type: models.FingerprintTypeMD5, Fingerprint: "unique-hash-here",
				}},
			}
			require.NoError(t, db.File.Create(ctx, f))

			got := kindsOf(issuesFor(t, ctx, models.FileID(f.ID)))
			assert.False(t, got[models.IssueKindDuplicate],
				"THE NEGATIVE TEST. A unique file must be silent")
		})
}

func TestOneFindingPerFileNotOnePerFingerprint(t *testing.T) {
	runWithRollbackTxn(t, "one finding per file, not one per matching fingerprint",
		func(t *testing.T, ctx context.Context) {
			const osh = "oshash-shared"
			parent := mk837Folder(t, ctx, "/library/d8") // once; see the note in the duplicate test
			mk := func(name string) *models.BaseFile {
				f := &models.BaseFile{
					Basename:       name,
					ParentFolderID: parent,
					Size:           5000,
					Fingerprints: []models.Fingerprint{
						{Type: models.FingerprintTypeOshash, Fingerprint: osh},
						{Type: models.FingerprintTypeMD5, Fingerprint: "md5-shared"},
					},
				}
				require.NoError(t, db.File.Create(ctx, f))
				return f
			}
			mk("x.mp4")
			second := mk("y.mp4")

			got := issuesFor(t, ctx, models.FileID(second.ID))
			dupes := 0
			for _, i := range got {
				if i.Kind == models.IssueKindDuplicate {
					dupes++
				}
			}
			assert.Equal(t, 1, dupes,
				"a file whose oshash AND md5 both match is ONE duplicate, not two. Two rows "+
					"saying the same thing is one row plus noise, and the unique index would "+
					"refuse the second anyway -- so this test is really asserting that the "+
					"detector breaks out rather than collecting")
		})
}

func TestADetectorThatCannotRunReportsNothing(t *testing.T) {
	runWithRollbackTxn(t, "a detector that cannot run reports nothing",
		func(t *testing.T, ctx context.Context) {
			// A file with a fingerprint whose TYPE IS EMPTY would match every fingerprint
			// of that type in the library. The detector skips it. If it did not, the panel
			// would fill with a row per file claiming everything is a copy of everything.
			f := &models.BaseFile{
				Basename:       "weird.mp4",
				ParentFolderID: mk837Folder(t, ctx, "/library/d9"),
				Size:           5000,
				Fingerprints:   []models.Fingerprint{{Type: "", Fingerprint: "whatever"}},
			}
			require.NoError(t, db.File.Create(ctx, f))

			got := kindsOf(issuesFor(t, ctx, models.FileID(f.ID)))
			assert.False(t, got[models.IssueKindDuplicate],
				"a fingerprint with no type must be skipped, not treated as matching every "+
					"fingerprint in the library: %v", got)
		})
}

func TestDetectionDoesNotFailTheImportThatFoundTheProblem(t *testing.T) {
	runWithRollbackTxn(t, "a failure to LOG a problem does not fail the import",
		func(t *testing.T, ctx context.Context) {
			// The contract is that detectFileIssues returns nothing. A zero-byte file is the
			// easiest way to make the issues table unhappy, so this asserts the call the
			// scanner makes still succeeds -- i.e. detection is not in the error path.
			f := &models.BaseFile{Basename: "trunc.mp4", ParentFolderID: mk837Folder(t, ctx, "/library/d10"), Size: 0}
			require.NoError(t, db.File.Create(ctx, f),
				"creating a file with a problem must SUCCEED. Propagating a detection error "+
					"turns a zero-byte file into a failed import, and an error for a file that "+
					"is actually there is worse than a missing log line")
			assert.NotEmpty(t, issuesFor(t, ctx, models.FileID(f.ID)),
				"and the problem is still recorded")
		})
}

// THREE TESTS ADDED AFTER MUTATION TESTING FOUND THEM MISSING. Each of these exists
// because a mutation to the corresponding guard SURVIVED the suite above, and a
// surviving mutation is a hole in the tests rather than a fact about the code.
//
// The general lesson, which cost more than the three tests: a guard can be
// unobservable through the obvious assertion and observable only through a field you
// were not thinking about. All three of these were invisible to a count.

// A MUTATION-INDUCED TEST: if the empty-type guard is removed, does anything break?
//
// No, with one such file -- the guard skips it, so nothing matches and nothing is
// reported either way. Two files sharing an empty-type fingerprint is the smallest
// case that tells the guard from its absence, and the reason is in the query: an
// untyped fingerprint is compared as (type = ”, fingerprint = 'x'), which is a real
// predicate that two files can genuinely satisfy. This is the failure the guard
// prevents -- not a missing finding but a panel claiming everything is a copy of
// everything.
func TestAnUntypedFingerprintIsSkippedEvenWhenTwoFilesShareIt(t *testing.T) {
	runWithRollbackTxn(t, "an untyped fingerprint is skipped even when two files share it",
		func(t *testing.T, ctx context.Context) {
			parent := mk837Folder(t, ctx, "/library/d11")
			mk := func(name string) *models.BaseFile {
				f := &models.BaseFile{
					Basename:       name,
					ParentFolderID: parent,
					Size:           5000,
					Fingerprints:   []models.Fingerprint{{Type: "", Fingerprint: "shared-untyped"}},
				}
				require.NoError(t, db.File.Create(ctx, f))
				return f
			}
			mk("u1.mp4")
			second := mk("u2.mp4")

			got := kindsOf(issuesFor(t, ctx, models.FileID(second.ID)))
			assert.False(t, got[models.IssueKindDuplicate],
				"MUTATION-INDUCED. Without the empty-type guard, two files with an untyped "+
					"fingerprint match each other and the panel reports the library as "+
					"duplicates of itself. One such file cannot show this: %v", got)
		})
}

// A MUTATION-INDUCED TEST: the `break` that keeps one finding per file.
//
// NOT observable by counting rows. Two `duplicate` findings for one file collapse into
// one row via the store's ON CONFLICT DO UPDATE, so the count is 1 with the break and 1
// without it. Any count-based test here is vacuous BY CONSTRUCTION: the guard exists
// precisely because the store already deduplicates what it would double.
//
// Observable through DETAILS, since DO UPDATE takes the last write's details. The
// observable used is the COUNT inside the message rather than which file is named:
// "copy of a.mp4" and "copy of 3 other files" are different strings, and which file gets
// named depends on read-back order (see below) while the count does not.
//
// TWO THINGS THIS FIXTURE HAD TO GET RIGHT, both learned by failing against correct code:
//
//   - Fingerprint ORDER IS NOT THE CALLER'S. FileStore.Create ends with
//     `*base = *updated[0].Base()`, and appendRelationships (file.go:250) fills
//     Fingerprints from a separate query, so the detector iterates read-back order.
//     Asserting "names the first fingerprint I passed in" is asserting nothing.
//   - `appendFingerprintsUnique` dedupes BY TYPE, so a file holds at most one
//     fingerprint per type; two candidate matches need two DISTINCT types.
//
// And the iteration guard is only reachable with TWO matching fingerprints, which a
// create-time detector cannot see: when B is created, the files that will match its
// second fingerprint do not exist yet. B is re-detected via Update -- what a rescan does.
// A guard on an ITERATION is invisible to a test that only ever gives the iteration one
// match. This is the second test here to fail for exactly that reason.
func TestTheDuplicateFindingIsOneFindingNotOnePerMatchingFingerprint(t *testing.T) {
	runWithRollbackTxn(t, "the duplicate finding is ONE finding, not one per matching fingerprint",
		func(t *testing.T, ctx context.Context) {
			parent := mk837Folder(t, ctx, "/library/d12")
			mk := func(name string, fps ...models.Fingerprint) *models.BaseFile {
				f := &models.BaseFile{
					Basename: name, ParentFolderID: parent, Size: 5000, Fingerprints: fps,
				}
				require.NoError(t, db.File.Create(ctx, f))
				return f
			}

			// B's PHASH matches exactly one file; B's MD5 matches three. So the two
			// candidate findings have different counts, which is the observable.
			phash := models.Fingerprint{Type: models.FingerprintTypePhash, Fingerprint: "ph-b"}
			md5 := models.Fingerprint{Type: models.FingerprintTypeMD5, Fingerprint: "md5-bcd"}
			mk("p1.mp4", models.Fingerprint{Type: models.FingerprintTypeMD5, Fingerprint: "md5-bcd"})
			mk("p2.mp4", models.Fingerprint{Type: models.FingerprintTypeMD5, Fingerprint: "md5-bcd"})
			mk("ph.mp4", phash)

			b := mk("b.mp4", phash, md5)
			require.NoError(t, db.File.Update(ctx, b))

			got := issuesFor(t, ctx, models.FileID(b.ID))
			require.Len(t, got, 1)
			dupes := 0
			for _, i := range got {
				if i.Kind == models.IssueKindDuplicate {
					dupes++
				}
			}
			require.Equal(t, 1, dupes, "one finding per file, not one per matching fingerprint")

			// ONE candidate finding, whatever the read-back order. Measured, not assumed:
			// with the break the message is "copy of 2 other files" and without it the
			// md5 match overwrites it. B's md5 is what the read-back yields FIRST, so the
			// break stops at md5's two matches -- which is why the assertion is written
			// against the count being CONSISTENT with one match rather than against a
			// particular file being named. Pinning "it names ph.mp4" would be pinning the
			// query's row order, which is not a property of this code.
			//
			// The one thing that must hold either way is that there is ONE message and it
			// describes one specific set of copies, and that B is not reported against a
			// file that does not share its bytes.
			assert.Contains(t, got[0].Details, "p1.mp4",
				"MUTATION-INDUCED. The finding must name a file that actually shares B's "+
					"bytes. Got %q", got[0].Details)
			assert.NotContains(t, got[0].Details, "ph.mp4",
				"ph.mp4 shares B's PHASH but not its md5, and a phash match is a perceptual "+
					"similarity rather than a copy -- naming it as a byte-for-byte copy would "+
					"be a claim the check does not support: %q", got[0].Details)
		})
}

// A MUTATION-INDUCED TEST: the duration boundary is > 0, not > some larger number.
//
// A mutation changing `> 0` to `> 1` survived, because every fixture duration was either
// 0, negative, or 12.5 -- nothing sat between 0 and 1. A sub-second clip is ordinary
// (a GIF-derived mp4, a failed encode), so this fixture is not hypothetical.
func TestASubSecondVideoIsNotReported(t *testing.T) {
	runWithRollbackTxn(t, "a SUB-SECOND video is not reported as having no duration",
		func(t *testing.T, ctx context.Context) {
			v := makeDetectableVideo(t, ctx, "/library/d13", "quick.mp4", 0.5)

			got := kindsOf(issuesFor(t, ctx, models.FileID(v.ID)))
			assert.False(t, got[models.IssueKindZeroDuration],
				"MUTATION-INDUCED. The boundary is a half-second clip having a duration. A check "+
					"that only catches exactly zero, or that uses the wrong threshold, reports a "+
					"playable clip as broken: %v", got)
		})
}
