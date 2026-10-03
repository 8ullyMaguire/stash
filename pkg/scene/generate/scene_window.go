package generate

import "github.com/stashapp/stash/pkg/models"

// stash#3530 — SceneWindow is "the part of a file this scene plays".
//
// It exists because a PROPORTION OF A LENGTH IS NOT A POSITION, and the generator does that
// conversion in several places. Before #3530 a scene WAS a file, so the two coincided and the
// arithmetic was invisible. Now that one file can back several scenes with different windows, each
// of those places is a place to take a frame from the wrong part of the file:
//
//	at = videoFile.Duration * 0.2   ->   ScreenshotTime(input, at, ...) -> args.Seek(at)
//
// Duration is the WINDOW's length, and at is an ABSOLUTE file offset. For a scene at 300..600 of a
// 7200s file the old expression yields 60 -- 240 seconds before the scene begins.
//
// ## Set, not zero values
//
// `Set` is what distinguishes "no window" from "a window at 0". A scene covering 0..60 of a file is
// a WINDOW and must have its cover taken from inside it, while an unranged scene has no window at
// all. Collapsing the two onto the zero value would give a 0-start window the whole-file behaviour,
// which is exactly the mistake the HLS cache key already had to guard against.
type SceneWindow struct {
	Start float64
	End   float64 // 0 means "to the end of the file"
	Set   bool
}

// WindowOf builds a SceneWindow from a file's stored window.
//
// A file with no window at all yields the zero SceneWindow, and At then reduces to the original
// expression -- so every pre-existing scene's cover and preview is byte-identical. That is the same
// guarantee migration 122 gave for durations, and it is why the unranged branch is explicit.
func WindowOf(vf *models.VideoFile) SceneWindow {
	if vf == nil || (vf.StartTime == nil && vf.EndTime == nil) {
		return SceneWindow{}
	}

	w := SceneWindow{Set: true}
	if vf.StartTime != nil {
		w.Start = *vf.StartTime
	}
	if vf.EndTime != nil {
		w.End = *vf.EndTime
	}
	return w
}

// At returns the absolute file offset for a point INSIDE the window, given as a proportion of
// `length`.
//
// `length` is how long the scene runs, which is NOT always the window's length: for an open-ended
// window it is the remainder of the file, which the caller knows and this type cannot infer. The
// contract is "length is the scene's duration", whatever produced it.
//
// An unranged scene reduces to fraction*length, which is what the code did before #3530 for every
// scene in every installation.
func (w SceneWindow) At(fraction, length float64) float64 {
	if !w.Set {
		return fraction * length
	}
	return w.Start + fraction*length
}

// resolveScreenshotAt picks the timestamp for a cover.
//
// An EXPLICIT `at` wins and is used verbatim, because it is an instruction in file seconds: a user
// asking for the frame at 400s means 400s, and rebasing it into the window would return 520 with
// no indication that anything had moved. Only the DEFAULT is window-relative.
//
// This is the seam the mutation sweep targets, so it is a named function rather than an inline
// expression -- an inline `if` in the task body would make the two rules indistinguishable to a
// reader and impossible to test without running ffmpeg.
func resolveScreenshotAt(at *float64, window SceneWindow, length float64) float64 {
	if at != nil {
		return *at
	}
	return window.At(screenshotDurationProportion, length)
}

// startOf is the window's start, or 0 when there is no window.
func (w SceneWindow) startOf() float64 {
	if !w.Set {
		return 0
	}
	return w.Start
}

// endOf is the window's end, or the file's duration when there is no window or no end.
//
// An open-ended window runs to the end of the file, which is knowledge the caller has and this type
// cannot infer -- the same contract as At's `length`.
func (w SceneWindow) endOf(fileDuration float64) float64 {
	if !w.Set || w.End <= 0 {
		return fileDuration
	}
	return w.End
}

// Length is how long the window runs, given the file's own duration.
//
// The `length` argument is the file's duration because an open-ended window has no end of its own.
// An unranged window's length IS the file's duration, which is why the two cases need one
// expression: there is no window to lengthen.
func (w SceneWindow) Length(fileDuration float64) float64 {
	return w.endOf(fileDuration) - w.startOf()
}

// rebaseExclude shifts a RELATIVE offset (a proportion of the window's length) onto the window.
//
// getStepSizeAndOffset works in proportions of videoDuration, which #3530 made the window's
// length. Its result is correct as a proportion and wrong as a position, exactly like `At`.
//
// No `length` argument, deliberately: the offset is already a proportion, so converting it needs
// only the window's start. An earlier draft took `length` and ignored it, which is a signature that
// lies about what the function needs -- and a mutation sweep would never have caught it, because
// dropping an unused parameter changes no result.
func (w SceneWindow) rebaseExclude(relative float64) float64 {
	if !w.Set {
		return relative
	}
	return w.Start + relative
}
