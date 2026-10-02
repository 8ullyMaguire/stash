package sqlite

// Scene time ranges (migration 122, docs/ISSUE-3530-spec.md).
//
// ## WHY ONE SHARED SQL CONSTANT AND NOT A GO HELPER
//
// Five places need "how long is this scene", as SQL rather than Go:
//
//	scene.go:Duration()        the library total
//	scene.go:TotalDuration()   the per-query total behind FindScenes.duration
//	performer.go               the `scenes_duration` sort clause
//	studio.go                  the `scenes_duration` sort clause
//	tag.go                     the `scenes_duration` sort clause
//
// Four of the five are hand-written ORDER BY subqueries built with fmt.Sprintf, so a Go
// helper cannot reach them without first rewriting each site into goqu — a bigger change
// than this one should make. One exported string used by all five means a fix to this
// arithmetic is one edit rather than five, and it makes the five VISIBLY identical instead
// of accidentally identical.
//
// No site aliases `scenes_files` or `video_files` (measured: all five join the bare names),
// so the fragment is unqualified. If a future site adds an alias, it needs a qualified copy
// — an unqualified name in a correlated subquery silently binds to the INNERMOST scope,
// which here would be right by accident and wrong after the next edit.
//
// ## THE EXPRESSION
//
// Written as the same three steps scene.go performs, in the same order, because the order is
// the rule:
//
//	step 1   end   = COALESCE(end_time, fileDuration)        an explicit end, else the file's
//	step 2   end   = MIN(end, fileDuration)                  but never past what the file has
//	step 3   result = MAX(0, end - COALESCE(start_time, 0))
//
// with two cases that must NOT be run through the arithmetic, because they are the whole
// file and the arithmetic would be a different route to the same number:
//
//	start NULL, end NULL   -> fileDuration      (EVERY pre-existing scene, so this branch
//	                                             is what makes 122 a no-op for existing
//	                                             libraries -- bit-for-bit, not approximately)
//	start NULL, end set    -> end               (step 1 takes end_time; steps 2-3 leave it)
//
// ## WHY `999999999` IS IN THE SECOND ARM OF MIN
//
// `video_files.duration` is 0 (or NULL) whenever ffprobe could not determine it, and
// `MIN(end, 0)` would erase every range on files still being written, partial downloads, and
// containers ffprobe does not recognise. There is no `+Inf` literal in SQLite, so the unknown
// length is replaced by a number no real end can exceed -- about 31 years of seconds. That is
// step 2's clamp made inert for an unknown length, matching scene.go's `fileDur > 0` guard.
// Pinned by TestARangeSurvivesAFileWithNoKnownDuration.
//
// It appears ONCE, in step 2's clamp, and not in step 1. Step 1's
// `COALESCE(end_time, fileDuration)` must keep the 0 as a real 0: an open-ended range on a
// file of unknown length has no computable duration, and 0 is the schema's own word for
// "not known" — substituting the sentinel there would report 999999999 seconds instead.
// An earlier draft had the NULLIF in both arms, and a mutation sweep showed the step-1 copy
// was unreachable: `COALESCE(NULLIF(d,0), 0)` and `COALESCE(d, 0)` are the same value for
// every d, so no test could ever distinguish them. It was removed rather than tested, because
// a test that must contrive a case to kill a no-op is a test of the contrive.
//
// **An earlier draft of this expression was wrong and the case table caught it.** It clamped
// the computed DURATION against the sentinel rather than the END against the file's length,
// which meant no clamping happened at all: `start 2600, end 2900` on a 2700s file reported
// 300 (the whole window) instead of 100 (what the file can supply), and an open tail
// `start 2400, end NULL` reported 2700 instead of 300. Four of eleven cases failed. The
// comment above the old expression described an intent the expression did not implement,
// which is the same failure as the SQLite/CLI divergence in §7 of the spec: a claim about a
// constant is not a measurement of it.
const SceneRangeDurationSQL = `CASE
	WHEN scenes_files.start_time IS NULL AND scenes_files.end_time IS NULL
		THEN COALESCE(video_files.duration, 0)
	ELSE MAX(0, MIN(
		COALESCE(scenes_files.end_time, COALESCE(video_files.duration, 0)),
		COALESCE(NULLIF(video_files.duration, 0), 999999999)
	) - COALESCE(scenes_files.start_time, 0))
END`
