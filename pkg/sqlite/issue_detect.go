package sqlite

import (
	"context"
	"fmt"
	"strconv"

	"github.com/stashapp/stash/pkg/logger"

	"github.com/stashapp/stash/pkg/models"
)

// stash#837 — detection. Four kinds, and the shape of the whole file is set by one
// decision recorded in the plan and repeated here because it is the expensive one:
//
//	DETECTION RUNS WHERE THE FILE ROW IS WRITTEN, and is NOT A SEPARATE SWEEP.
//
// A sweep would have to stat every file, which is the expensive thing the scan has
// just avoided. Running here costs one indexed lookup per file that has already
// been read.
//
// WHY detectFileIssues RETURNS NOTHING. A failure to LOG a problem must not fail
// the scan that found the problem. Getting this the other way round — propagating
// the error — turns a zero-byte file into a failed import, which is a much worse
// outcome than a missing log line: the user sees an error for a file that is
// actually there. A caller that needs to know checks the issues table.
//
// So the errors from Record are swallowed deliberately, and the swallowing is
// LOGGED rather than silent — see the comment at the call site.

// detectFileIssues records what is wrong with a file that has just been written.
//
// It never returns an error, and that is the API contract rather than an omission.
// See the comment above.
func detectFileIssues(ctx context.Context, f models.File) {
	for _, d := range detectorsFor(f) {
		// A detector may legitimately decide there is nothing wrong. The ones that
		// fire are handed to Record, which is idempotent for a live finding and
		// refuses to re-raise a dismissed one.
		for _, found := range d(ctx, f) {
			if err := recordIssue(ctx, &found); err != nil {
				// See the contract above: do not fail the scan. Logged so a
				// systematically failing insert is visible rather than invisible.
				logger.Errorf("issue detection: %s/%s for file %d could not be recorded: %v",
					found.Domain, found.Kind, f.Base().ID, err)
			}
		}
	}
}

// recordIssue is the single write path, kept as a function so a test can prove
// detection never writes through anything else.
func recordIssue(ctx context.Context, i *models.Issue) error {
	return NewIssueStore().Record(ctx, i)
}

// duplicateOther is one file that shares this one's bytes. A NAMED struct, because
// dbWrapper maps by db tag and an inline anonymous struct is a second place to keep
// in step with the query above.
type duplicateOther struct {
	FileID   int    `db:"file_id"`
	Basename string `db:"basename"`
}

// fileDetector answers "what is wrong with this file?" and returns zero or more
// findings. Returning a SLICE rather than a single finding is what lets one file
// be a duplicate AND zero-byte without either check being able to suppress the
// other.
type fileDetector func(ctx context.Context, f models.File) []models.Issue

// detectorsFor picks the detectors that apply to this file's TYPE.
//
// A zero-duration check on an image is meaningless — an image has no duration —
// and running it anyway would produce findings about a field that cannot be set,
// which is the "detector that fires on everything" failure in its purest form.
func detectorsFor(f models.File) []fileDetector {
	var out []fileDetector
	// Duplicate applies to EVERY file. It is a property of the bytes.
	out = append(out, detectDuplicate)

	switch f.(type) {
	case *models.VideoFile:
		out = append(out, detectZeroDuration)
	}
	// zero_size applies to every file: a file of zero bytes is broken whatever it
	// is.
	out = append(out, detectZeroSize)
	return out
}

// detectDuplicate — another file shares this one's fingerprint.
//
// WHY A FINGERPRINT AND NOT A CHECKSUM COLUMN. There is no checksum column;
// `files_fingerprints` is the checksum store, keyed (file_id, type, fingerprint)
// with an index on (type, fingerprint). So the duplicate test is the index's own
// lookup and needs no new index to be fast.
//
// WHY EXCLUDE SELF. The file was inserted a moment before this runs, so its own
// fingerprint row already exists. Without the exclusion every file would be a
// duplicate of itself and the panel would list the entire library.
func detectDuplicate(ctx context.Context, f models.File) []models.Issue {
	base := f.Base()
	var found []models.Issue

	for _, fp := range base.Fingerprints {
		// A fingerprint whose type is empty would match every fingerprint of that
		// type in the library. Skip rather than risk reporting the library as
		// duplicates of itself.
		if fp.Type == "" {
			continue
		}

		// Count OTHER files sharing this (type, fingerprint).
		var others []duplicateOther
		//
		// fp.Fingerprint is bound AS-IS rather than converted to []byte, because that is
		// what fingerprint.go's own insert does (`goqu.Vals{fileID, f.Type, f.Fingerprint}`)
		// and a conversion here would make this query disagree with the writer about
		// what the column holds -- a mismatch that compares false rather than erroring.
		err := dbWrapper.Select(ctx, &others, `
			SELECT ff.file_id AS file_id, files.basename AS basename
			FROM files_fingerprints ff
			JOIN files ON files.id = ff.file_id
			WHERE ff.type = ? AND ff.fingerprint = ? AND ff.file_id <> ?
			LIMIT 4`,
			fp.Type, fp.Fingerprint, int(base.ID))
		if err != nil {
			// Not "no duplicates" and not fatal: the check could not be run. Reported
			// as no finding, because inventing a finding from a failed lookup is how
			// a panel fills with nonsense.
			logger.Errorf("duplicate detection for file %d could not run: %v", base.ID, err)
			continue
		}

		if len(others) == 0 {
			continue
		}

		found = append(found, models.Issue{
			FileID:  fileIDPtr(base.ID),
			Domain:  models.IssueDomainFile,
			Kind:    models.IssueKindDuplicate,
			Details: describeDuplicates(others),
		})
		// ONE finding per file, not one per matching fingerprint. A file with an
		// MD5 and an oshash that both match reports the same fact twice, and two
		// rows saying "this is a duplicate" is one row plus noise.
		break
	}
	return found
}

// describeDuplicates renders the human-facing half of a duplicate finding.
//
// The count is included because "this file is a duplicate" is unactionable on its
// own — a user needs to know whether to keep one copy or four.
func describeDuplicates(others []duplicateOther) string {
	var names []string
	for _, o := range others {
		names = append(names, o.Basename)
	}
	if len(others) == 1 {
		return "byte-for-byte copy of " + names[0]
	}
	return fmt.Sprintf("byte-for-byte copy of %d other files, including %s",
		len(others), names[0])
}

// detectZeroDuration — a video whose duration is zero or negative.
//
// A video with no duration cannot be scrubbed, so every downstream duration filter
// and every duration-sorted page is wrong for it. It is a real defect, not a
// cosmetic one.
//
// WHY `<= 0` AND NOT `== 0`. A NEGATIVE duration is what a container with a broken
// header actually reports, and a check that only catches exactly zero misses the
// case that motivated it.
func detectZeroDuration(ctx context.Context, f models.File) []models.Issue {
	vf, ok := f.(*models.VideoFile)
	if !ok {
		return nil
	}
	if vf.Duration > 0 {
		return nil
	}
	return []models.Issue{{
		FileID: fileIDPtr(vf.ID),
		Domain: models.IssueDomainFile,
		Kind:   models.IssueKindZeroDuration,
		Details: "the file's container reports a duration of " +
			formatSeconds(vf.Duration) + ", so it cannot be played or scrubbed",
	}}
}

// detectZeroSize — a file of zero bytes.
//
// Zero bytes with a non-zero duration is the shape of an interrupted download or a
// truncated copy, and it is also the shape a scanner produces for a file it could
// not read. Either way the library is holding a row for something that is not
// playable.
func detectZeroSize(ctx context.Context, f models.File) []models.Issue {
	base := f.Base()
	if base.Size != 0 {
		return nil
	}
	return []models.Issue{{
		FileID:  fileIDPtr(base.ID),
		Domain:  models.IssueDomainFile,
		Kind:    models.IssueKindZeroSize,
		Details: "the file is zero bytes, so there is nothing to play or to read",
	}}
}

// formatSeconds renders a duration for a human, without inventing precision. A
// negative duration is rendered as-is rather than as an absolute value: the sign is
// the information ("the container reported a nonsense number"), and a detector that
// hid it would describe the symptom as an absence.
func formatSeconds(d float64) string {
	return strconv.FormatFloat(d, 'f', 3, 64) + "s"
}

func fileIDPtr(id models.FileID) *models.FileID {
	p := id
	return &p
}
