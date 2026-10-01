// Package curate computes the two completion inputs most likely to be built as
// counters: snapshot coverage and performer/source links.
//
// M7 step 7.6c (R069, R070), spec §6a.15. Spec sheet §6a.15/step 7.6c.
//
// WHY THIS IS A SEPARATE PACKAGE AND NOT TWO FIELDS ON rank.Counts. The plan's
// warning is specific: "'how many snapshots does this scene have' is a number
// someone will put in a column, and it must be a COUNT in the view." Adding these
// as fields to the completion Counts would be the first step toward exactly that —
// a struct that looks like a row and starts being persisted.
//
// So the rule here is stronger than "don't add a column": THE COUNTS ARE DERIVED
// FROM ROWS HERE, AND NOTHING ELSE ACCEPTS THEM AS INPUT. Every type in this
// package is built by a function that walks a set of records. There is no
// constructor that takes a count, so a caller cannot invent one.
//
// The temptation this exists to refuse, stated plainly. Snapshot coverage is
// almost free to compute as a stored integer: a counter incremented when a
// snapshot row is written, or recomputed when snapshots are purged. Both are
// cheap, and both rot the instant a row is deleted by any route this code does not
// own — an import, a federated sync, a retention sweep. Completion is a progress
// bar that a curator trusts; a stale one is worse than an expensive one.

package curate

import (
	"errors"
	"fmt"
)

// ErrNegativeCount is returned when a derived count would be negative.
//
// Unreachable from a COUNT, which cannot be negative — and that is exactly why it
// is worth having. A count that arrives negative means the rows were walked by
// code that has a bug, and clamping it to zero would render a plausible-looking
// progress bar over a broken query.
var ErrNegativeCount = errors.New("curate: a derived count cannot be negative")

// Snapshot is one generated collage or frame record (R029, step 7.5).
type Snapshot struct {
	SceneID string
	// Frames is how many keyframes the collage covers. §6a.15 wants 12-24 evenly
	// spaced, so a snapshot with 3 frames is a real gap and not a malformed row.
	Frames int
}

// SnapshotCoverage counts snapshots for ONE SCENE by walking them.
//
// THE SCENE ID IS NOT OPTIONAL AND NOT IGNORED. My first version aggregated every
// snapshot it was handed, leaving the caller responsible for filtering -- and a
// mutation that skipped the scene check survived every test, because every test
// passed a single scene's rows. A caller whose query returned snapshots for many
// scenes would get one tally attributed to whichever scene it happened to be asking
// about, and the progress bar would be confidently wrong.
//
// So the scene filter lives HERE, where the data enters, rather than in every
// caller. Passing a mixed list is now safe: rows for other scenes are ignored.
//
// The signature is the other constraint: it takes snapshots, not a number. There is
// no SnapshotCoverageFromInt, and adding one would be the moment this package stops
// meaning anything.
func SnapshotCoverage(sceneID string, snapshots []Snapshot) (count, frames int, err error) {
	for _, s := range snapshots {
		if s.SceneID != sceneID {
			continue
		}
		if s.Frames < 0 {
			return 0, 0, fmt.Errorf("%w: snapshot for scene %q has %d frames",
				ErrNegativeCount, s.SceneID, s.Frames)
		}
		count++
		frames += s.Frames
	}
	return count, frames, nil
}

// SnapshotTarget is what §6a.15 expects: a scene is expected to be covered.
const SnapshotTarget = 1

// SnapshotComplete reports whether a scene's snapshot coverage counts as done.
//
// ONE IS ENOUGH, and this is a judgement worth stating because the alternative
// (require several collages) is defensible in a way that is worth resisting. A
// second collage of the same scene is not more identified — the ident board's
// snapshot collages exist so identification can happen on an instance that may not
// hold the file (§6a.9), and one sufficiently detailed collage serves that. Making
// the target 3 would put a permanent 2/3 on every scene in a curated instance for
// no gain in identification quality.
func SnapshotComplete(count int) bool {
	return count >= SnapshotTarget
}

// Link is one performer or source association on a scene.
type Link struct {
	SceneID     string
	PerformerID int
	// SourceID is the metadata source this association came from (R070: "performer
	// and source links"). Zero means the link was added by a user rather than
	// scraped.
	SourceID int
	// Confirmed means the association has been verified by a user (§6a.15's
	// "present and verified"). An unconfirmed scraped link is present but not
	// finished, which is why it is tracked separately rather than counted the same.
	Confirmed bool
}

// LinkCounts is what a scene's associations come to, DERIVED.
type LinkCounts struct {
	Performers int
	Confirmed  int
	// FromSources is how many arrived from a scraper rather than a user. Useful for
	// display ("12 performers, 9 from sources") and, more importantly, for the
	// quest that targets the unconfirmed ones.
	FromSources int
}

// LinksFor counts ONE SCENE's links by walking them.
//
// It filters by scene for the same reason SnapshotCoverage does: the caller is not
// trusted to have filtered, because a mixed list produces a confidently wrong
// tally rather than an error. Same mutation, same fix, same place.
//
// THE UNLINKED SCENE IS THE CANONICAL GAP (R070's note), and this function is why
// that gap is findable: a scene with no links has zero performers, so the quest
// over this count lists it without anybody having stored "unlinked" anywhere.
func LinksFor(sceneID string, links []Link) (LinkCounts, error) {
	var c LinkCounts
	for _, l := range links {
		if l.SceneID != sceneID {
			continue
		}
		if l.PerformerID < 0 {
			return LinkCounts{}, fmt.Errorf("%w: link for scene %q has performer %d",
				ErrNegativeCount, l.SceneID, l.PerformerID)
		}
		if l.SourceID < 0 {
			return LinkCounts{}, fmt.Errorf("%w: link for scene %q has source %d",
				ErrNegativeCount, l.SceneID, l.SourceID)
		}

		// A link with no performer is not a link. It is almost always a scraped
		// source with no performer resolved, and counting it would report a scene
		// as linked when it has nobody attached — which is precisely the gap a
		// curator is trying to close.
		if l.PerformerID == 0 {
			continue
		}

		c.Performers++
		if l.Confirmed {
			c.Confirmed++
		}
		if l.SourceID != 0 {
			c.FromSources++
		}
	}
	return c, nil
}

// PerformerTarget is the expected-field count §6a.15 uses for a scene's performer
// list.
//
// ONE, not three. The same reasoning as SnapshotTarget: a scene with no performer
// link is entirely unidentified and a scene with one is identified. Requiring three
// would make completion a measure of a scene's popularity rather than of whether
// anybody knows who is in it — and a scene genuinely featuring one performer would
// sit permanently at 33%, which tells a curator nothing actionable.
const PerformerTarget = 1

// LinkComplete reports whether a scene's performer links count as done.
//
// CONFIRMED, NOT PRESENT. A scraped, unconfirmed link is a claim from a source, and
// §6a.15 wants metadata "present and verified". Counting an unconfirmed link as
// complete would let an importer fill every scene's completion bar without a human
// ever looking, which turns the progress bar into a measure of importer coverage.
func LinkComplete(c LinkCounts) bool {
	return c.Confirmed >= PerformerTarget
}

// InputCompleteness is the two inputs reduced to one answer, for a caller that
// renders a combined progress figure.
type InputCompleteness struct {
	Snapshots  int
	LinkCounts LinkCounts
	// Of is how many inputs were required. Always 2 today — both are inputs to the
	// completion view and both are cheap. It is a field rather than a constant so
	// adding a third input does not change every caller's arithmetic.
	Of int
	// Done is how many of them are complete.
	Done int
}

// Fraction is the combined completeness, or 1 when nothing is required.
//
// 1 rather than 0 for the empty case, for the same reason as rank.Completion: an
// entity with no inputs is not 0% curated, and a bar at 0% for a scene nothing is
// expected of looks like a bug to every curator who sees it.
func (i InputCompleteness) Fraction() float64 {
	if i.Of <= 0 {
		return 1
	}
	return float64(i.Done) / float64(i.Of)
}

// Combine evaluates both inputs for one entity.
//
// It takes the RECORDS for both, not two counts, which is what keeps the whole
// package honest: a caller cannot hand this function a number it obtained from
// somewhere else, so "implemented as a column" is not a mistake available here.
func Combine(sceneID string, snapshots []Snapshot, links []Link) (InputCompleteness, error) {
	sc, _, err := SnapshotCoverage(sceneID, snapshots)
	if err != nil {
		return InputCompleteness{}, err
	}
	lc, err := LinksFor(sceneID, links)
	if err != nil {
		return InputCompleteness{}, err
	}

	out := InputCompleteness{
		Snapshots:  sc,
		LinkCounts: lc,
		Of:         2,
	}
	if SnapshotComplete(sc) {
		out.Done++
	}
	if LinkComplete(lc) {
		out.Done++
	}
	return out, nil
}
