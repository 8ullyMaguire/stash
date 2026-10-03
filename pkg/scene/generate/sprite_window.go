package generate

// stash#3530 — the sprite grid and its VTT cues must describe the SCENE's window, not the file's.
//
// GenerateSpriteTask used to probe the FILE and never look at the scene, so every derived quantity
// -- chunkCount, the step between tiles, the VTT cue spacing, SlowSeek's duration test -- came from
// VideoStreamDuration. A scene at 300..600s of a 7200s file therefore got a sprite tiled across the
// whole two hours, with ~30x too many tiles, and a VTT whose cues point into footage that is not the
// scene. The scrub bar then lands on the wrong second of the wrong file, and no cache key can fix
// that afterwards: the cues are a CONTRACT with the player, not a lookup.
//
// # Why a plan value rather than arithmetic in the task
//
// Every quantity below is a decision, and the interesting bugs are the ones where a decision is
// half-taken: tiles correctly SPACED but starting at frame 0, or a step computed from the window
// while the cue spacing is still the file's. Two sweeps in this repo have already found that shape
// (the preview's tilePlan survivors: helpers were tested while nothing proved the loop called them).
// So the whole decision is a value with named accessors, constructed in one place, and the
// generator's two loops read positions from it rather than computing any.
//
// # Why the unranged branches keep the OLD expressions verbatim
//
// Every scene in every existing installation is unranged, so the unranged branch must reduce to
// the previous arithmetic EXACTLY, not merely to something equivalent -- (FrameCount-1) is not
// duration*frameRate, and a "cleaner" unified formula would re-generate every sprite in every
// library for no visible gain. The two branches differ on purpose and the difference is load-bearing.
//
// # Why a window's VTT cues stay window-RELATIVE
//
// ui/v2.5/src/components/ScenePlayer/vtt-thumbnails.ts computes, in updateThumbnailStyle:
//
//	const duration = this.player.duration();
//	const time = percent * duration;
//
// so a cue is matched against a fraction of whatever the PLAYER is playing. For a windowed scene
// the player must be playing a windowed stream -- StreamDirect already refuses a ranged scene
// outright (409 with the window in the body) and redirects to the transcoded path otherwise -- so
// relative cues are forced by the player, not chosen here. It also makes the grid and the cues
// agree by construction: tile i is at start + i*step and its cue covers [i*step, (i+1)*step) of the
// windowed stream.

import "math"

// SpritePlanOptions is the measured input to a SpritePlan.
//
// Everything here is either a property of the FILE (the probe's figures, which no window can
// change) or a property of the SCENE (the window, and the chunk count derived from it). Keeping
// them in one struct is what lets the constructor decide slow-seek from the right pair of them.
type SpritePlanOptions struct {
	// Window is the scene's window. The zero value means "the whole file", and every
	// accessor below then reduces to the pre-#3530 expression.
	Window SceneWindow

	// FileDuration is the video STREAM duration, which is what the sprite is tiled across.
	FileDuration float64

	// FrameRate is the file's frame rate; a window is converted to frames through it.
	FrameRate float64

	// FileFrames is the file's frame count (VideoFile.FrameCount). It is the clamp for a
	// windowed seek and, in the unranged branch, the step itself.
	FileFrames int64

	// NthFrame is generatorInfo.NthFrame -- NumberOfFrames/ChunkCount -- used by the
	// unranged fast path's cue spacing. It is passed in rather than recomputed because the
	// unranged cue spacing must stay byte-identical, and NumberOfFrames is the manager's
	// figure (it falls back to a real frame count the probe could not give).
	NthFrame int

	// ChunkCount is the number of tiles, already snapped to a perfect square by the caller.
	ChunkCount int
}

// SpritePlan is the whole grid decision for one scene's sprite: which times are sampled, which
// frames they correspond to when seeking by frame, and how the VTT cues are spaced.
//
// A value rather than a set of functions taking the file, because the two loops and the VTT writer
// must agree, and agreement between three call sites is not something arithmetic can guarantee.
type SpritePlan struct {
	window       SceneWindow
	fileDuration float64
	frameRate    float64
	fileFrames   int64
	nthFrame     int
	chunkCount   int

	// duration is the LENGTH being tiled: the window's, or the file's when there is no window.
	duration float64

	slowSeek bool
}

// NewSpritePlan derives the grid decision.
//
// chunkCount is clamped to 1 because a zero would divide by zero in every accessor below, and a
// sprite of one tile is a degraded but valid result -- the same reason generatorInfo.configure()
// clamps it.
func NewSpritePlan(o SpritePlanOptions) SpritePlan {
	p := SpritePlan{
		window:       o.Window,
		fileDuration: o.FileDuration,
		frameRate:    o.FrameRate,
		fileFrames:   o.FileFrames,
		nthFrame:     o.NthFrame,
		chunkCount:   o.ChunkCount,
	}
	if p.chunkCount < 1 {
		p.chunkCount = 1
	}
	p.duration = o.Window.Length(o.FileDuration)
	p.slowSeek = SpriteNeedsFrameSeek(o)
	return p
}

// SpriteNeedsFrameSeek is the slow-seek decision, as a free function.
//
// # WHY IT IS NOT ONLY A PLAN METHOD
//
// The caller has to make this decision BEFORE it can build the final plan, because choosing frame
// seeking triggers a frame RECOUNT (ffprobe reads the file), and the plan is built from the
// recounted count. So there are two moments: decide, then plan. When I first wrote this as a method
// the caller built a throwaway plan to ask, and that plan was built from `videoFile.FrameRate` --
// the probe's figure, which is 0 whenever ffprobe could not determine it -- while the real plan was
// built from `generator.FrameRate`, which calculateFrameRate has resolved by then. Two rates for one
// decision, and the windowed arm reads the frame rate directly, so a windowed short file could be
// judged with a rate of 0 and wrongly told not to seek by frame.
//
// One function, called once by the plan and once by the caller, with the SAME FrameRate both times.
func SpriteNeedsFrameSeek(o SpritePlanOptions) bool {
	chunks := o.ChunkCount
	if chunks < 1 {
		chunks = 1
	}

	if !o.Window.Set {
		// Upstream's own condition, verbatim.
		return o.FileDuration < 5 || (0 < o.FileFrames && o.FileFrames <= int64(chunks))
	}

	// A window's rule: short enough to need frame seeking, AND with a frame per tile so the grid
	// cannot round past the window's frame count and repeat itself.
	return o.Window.Length(o.FileDuration) < 5 && o.Window.Length(o.FileDuration)*o.FrameRate >= float64(chunks)
}

// Duration is the length the grid spans: the window's length, else the file's.
func (p SpritePlan) Duration() float64 { return p.duration }

// ChunkCount is the number of tiles.
func (p SpritePlan) ChunkCount() int { return p.chunkCount }

// SlowSeek reports whether tiles are sought by FRAME rather than by time.
//
// The unranged condition is upstream's own, unchanged: a short video, or a file with fewer frames
// than tiles, seeks by frame because seeking by time is imprecise there.
//
// A window gets a DIFFERENT rule, and the difference is not tidiness -- it is the trap this file
// exists for. Today's condition is file-level, so for a window it both over- and under-fires: a
// 2-hour file passes it even when the window is 3 seconds long, and a 90-frame file passes it even
// when the window holds 40 frames and the grid wants 81. The second produces duplicate frames
// (GetSpriteGridSize rounds the chunk count up past the window's frame count), which is why the
// windowed branch additionally requires the WINDOW to have a frame per tile. A window short enough
// to need frame seeking but too small to give one frame per tile is served by time seeks instead --
// degraded, but not a grid of repeated frames.
func (p SpritePlan) SlowSeek() bool { return p.slowSeek }

// StepSize is the spacing between tiles, in seconds.
func (p SpritePlan) StepSize() float64 { return p.duration / float64(p.chunkCount) }

// Time is the absolute file offset of tile i.
//
// For an unranged scene this is i*duration/chunkCount, which is what the generator computed inline
// before #3530.
func (p SpritePlan) Time(i int) float64 {
	return p.window.startOf() + float64(i)*p.StepSize()
}

// windowFrames is how many frames the window spans.
//
// A zero frame rate yields zero, which makes every frame below 0 and therefore clamped to the
// window's first frame: a file whose rate could not be determined gets one tile per position rather
// than a panic. calculateFrameRate already logs that case.
func (p SpritePlan) windowFrames() float64 { return p.duration * p.frameRate }

// Frame is the absolute frame index of tile i, for the frame-seeking path.
//
// The unranged branch is the previous expression VERBATIM, including (FileFrames-1): it is not
// duration*frameRate, and every existing scene's sprite depends on the difference.
//
// The windowed branch converts through the frame rate, because a frame index is absolute and there
// is no "frame 300 of the window" without doing so:
//
//	firstFrame = round(window.start * frameRate)
//	stepFrame  = windowLength * frameRate / chunkCount
//	frame      = firstFrame + round(i * stepFrame)
//
// Computing only stepFrame is the half-fix this comment is here about: the grid comes out correctly
// SPACED and starting at frame 0, i.e. still the wrong footage, and every spacing assertion passes.
func (p SpritePlan) Frame(i int) int64 {
	if !p.window.Set {
		stepFrame := float64(p.fileFrames-1) / float64(p.chunkCount)
		return int64(math.Round(float64(i) * stepFrame))
	}

	firstFrame := int64(math.Round(p.window.startOf() * p.frameRate))
	stepFrame := p.windowFrames() / float64(p.chunkCount)
	frame := firstFrame + int64(math.Round(float64(i)*stepFrame))

	// Clamped to the window's last frame, and to the file's: a window whose end is the file's end
	// plus a rounding error must not ask ffmpeg for a frame that does not exist.
	last := p.lastWindowFrame()
	if frame > last {
		frame = last
	}
	if frame < 0 {
		frame = 0
	}
	return frame
}

// lastWindowFrame is the highest frame index inside the window.
func (p SpritePlan) lastWindowFrame() int64 {
	end := p.window.endOf(p.fileDuration)

	last := int64(math.Round(end*p.frameRate)) - 1
	if p.fileFrames > 0 && last > p.fileFrames-1 {
		last = p.fileFrames - 1
	}
	if last < 0 {
		last = 0
	}
	return last
}

// VttStep is the cue spacing written into the thumbnail VTT, in seconds.
//
// The unranged values are upstream's own, one per branch, kept verbatim:
//
//	fast  : NthFrame / FrameRate
//	slow  : (FrameCount-1) / ChunkCount / FrameRate
//
// The second exists because a file with fewer frames than tiles has NthFrame == 0, which would
// write every cue at 0.000 -- a VTT in which every tile claims the same instant.
//
// A window's spacing is the WINDOW's step, not the file's. The cues are matched against the
// media element's own timeline (see the file comment), so they must be spaced as the windowed
// stream is, and must agree with Time(): tile i is at start+i*step and its cue covers
// [i*step, (i+1)*step) of that stream.
func (p SpritePlan) VttStep() float64 {
	if !p.window.Set {
		if p.slowSeek {
			return (float64(p.fileFrames-1) / float64(p.chunkCount)) / p.frameRate
		}
		return float64(p.nthFrame) / p.frameRate
	}
	return p.StepSize()
}
