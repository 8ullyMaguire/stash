package manager

// stash#3530 — the sprite plan, derived from the scene's window rather than the file.
//
// This is the arithmetic that NewSpriteGenerator used to do inline, interleaved with fsutil calls
// and ffmpeg probing. Extracting it is what makes the windowing testable at all: the constructor
// needs a real file on disk, and an untestable constructor is how the original defect survived.
//
// Every quantity here reduces, for an unranged scene, to exactly what the code computed before.
// That is the guarantee that keeps every existing sprite and VTT byte-identical.

import (
	"math"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/scene/generate"
)

// spritePlan is the whole window-sensitive decision for one sprite, as a value.
//
// Every method is total: given any input it returns a finite number, because a NaN step silently
// produces a sprite full of black cells rather than an error.
type spritePlan struct {
	// length is how long the scene runs: the window's length, or the file's when unranged.
	length float64

	// start is where the scene begins in the file.
	start float64

	// SpriteInterval is seconds between tiles, derived from `length` not the file's duration.
	SpriteInterval float64

	// ChunkCount is the (perfect-square) number of tiles.
	ChunkCount int

	// SlowSeek is the frame-seek path, chosen from the WINDOW's length and frame count.
	SlowSeek bool

	// frameRate and totalFrames back the frame conversion for SlowSeek.
	frameRate   float64
	totalFrames int64
	windowStart float64
	windowEnd   float64
}

// planSprite derives the sprite plan for a file and a window.
//
// Returns nil only where the pre-existing code would have failed: a non-positive duration. That is
// the same refusal as before, kept so no scene that failed before starts succeeding for an
// unexplained reason.
func planSprite(videoFile ffmpeg.VideoFile, w generate.SceneWindow, config SpriteGeneratorConfig) *spritePlan {
	length, start := windowBounds(videoFile, w)
	if length <= 0 {
		return nil
	}

	// calculateSpriteInterval takes the file, so it must see a file whose duration IS the scene's
	// length. Rather than mutate the caller's probe result, this works on a copy -- the probe result
	// is shared with the ffmpeg calls that follow and must keep describing the real file.
	sceneFile := videoFile
	sceneFile.VideoStreamDuration = length

	interval := calculateSpriteInterval(sceneFile, config)
	if interval <= 0 {
		interval = length
	}
	chunkCount := int(math.Ceil(length / interval))

	// Snap to a perfect square so the grid has no empty cells.
	//
	// The SNAP itself is unchanged -- it is the INPUT that #3530 moves. It used to be computed from
	// the file's duration, which is the defect: a 300s window in a 7200s file got a grid sized for
	// two hours, mostly empty cells, and every cell that existed was the wrong footage.
	//
	// A window shorter than the snapped square deserves a note: a 2s window still wants 81 tiles,
	// because MinimumSprites says so and the config is the user's. The grid cannot represent 81
	// distinct instants in 2s, so tiles repeat -- which is exactly what the SlowSeek guard below
	// refuses rather than producing a sprite full of duplicates.
	gridSize := generate.GetSpriteGridSize(chunkCount)
	chunkCount = gridSize * gridSize

	frameRate := videoFile.FrameRate
	if frameRate <= 0 {
		// ffprobe reports 0 for some files. Multiplying out a window conversion by 0 yields NaN, and
		// a NaN frame index becomes a black cell rather than an error, so fall back to the seconds
		// path's assumption and let SlowSeek be decided without a frame count.
		frameRate = 0
	}
	windowFrames := length * frameRate

	plan := &spritePlan{
		length:         length,
		start:          start,
		SpriteInterval: interval,
		ChunkCount:     chunkCount,
		frameRate:      frameRate,
		totalFrames:    videoFile.FrameCount,
		windowStart:    start,
		windowEnd:      start + length,
	}

	// The slow path is chosen from the WINDOW, not the file. Both of today's conditions are
	// file-level and are right only for an unranged scene.
	//
	// The second condition gains a guard that matters for windows: a window with fewer frames than
	// the grid needs cannot be filled by frame seeking, because GetSpriteGridSize rounds the count
	// UP to a square and the generator would then ask for frames the window does not contain,
	// repeating itself.
	byDuration := length < 5

	// The file-level frame-count test, exactly as before, so an unranged scene is unchanged.
	byFrameCount := 0 < videoFile.FrameCount && videoFile.FrameCount <= int64(chunkCount)
	plan.SlowSeek = byDuration || byFrameCount

	// The window's own guard, which is new and is the reason this function exists.
	//
	// A window can hold FEWER FRAMES than the grid needs tiles, because GetSpriteGridSize rounds
	// chunkCount UP to a perfect square. Frame seeking then asks for frames the window does not
	// contain and repeats them -- a sprite of duplicated cells, silently. Today's condition is
	// file-level, so it is right only when the scene is the whole file.
	//
	// Only applied when the frame rate is known: with rate 0 the window's frame count is unknown,
	// and refusing the slow path on an unknown would disable it for every such file.
	if plan.SlowSeek && frameRate > 0 {
		if windowFrames > 0 && windowFrames < float64(chunkCount) {
			plan.SlowSeek = false
		}
	}

	return plan
}

// windowBounds returns how long the scene runs and where it starts.
func windowBounds(videoFile ffmpeg.VideoFile, w generate.SceneWindow) (length, start float64) {
	if !w.Set {
		return videoFile.VideoStreamDuration, 0
	}
	start = w.Start
	// End 0 means "to the end of the file", which SceneWindow cannot know by itself -- the file's
	// duration lives here. Passing 0 through would make length zero and the step divide by zero.
	end := w.End
	if end <= 0 {
		end = videoFile.VideoStreamDuration
	}
	length = end - start
	if length <= 0 {
		return 0, start
	}
	return length, start
}

// stepSize is seconds between tiles, from the SCENE's length.
func (p *spritePlan) stepSize() float64 {
	if p == nil || p.ChunkCount <= 0 {
		return 0
	}
	return p.length / float64(p.ChunkCount)
}

// time is the absolute file offset of tile `i`.
func (p *spritePlan) time(i int) float64 {
	return p.windowStart + float64(i)*p.stepSize()
}

// firstFrameFloat is the window's first frame, as a float so a zero frame rate is visible rather
// than silently rounded.
func (p *spritePlan) firstFrameFloat() float64 {
	if p == nil || p.frameRate <= 0 {
		return 0
	}
	return p.windowStart * p.frameRate
}

// firstFrame is the window's FIRST frame, in the file's frame numbering.
//
// This is the half-fix the spec warns about: a plan that computed only the step would produce a
// correctly spaced grid still starting at frame 0 -- the wrong footage at the right intervals, which
// passes every duration-only assertion.
func (p *spritePlan) firstFrame() int {
	return int(math.Round(p.firstFrameFloat()))
}

// frame is the absolute frame for tile `i`, clamped to the window.
//
// Clamping is what keeps the last tile inside the scene: the final interval of a window need not
// divide evenly, so the computed frame can overshoot by a fraction and round past the end.
func (p *spritePlan) frame(i int) int {
	if p == nil {
		return 0
	}
	if p.frameRate <= 0 || p.ChunkCount <= 0 {
		return p.firstFrame()
	}
	stepFrames := (p.length * p.frameRate) / float64(p.ChunkCount)
	f := p.firstFrame() + int(math.Round(float64(i)*stepFrames))

	// The FILE's frame count is a real ceiling, and it is applied FIRST and unconditionally.
	//
	// A window can overrun the file it claims to live in -- a stale range, or a re-encoded shorter
	// file -- and frame seeking past the last frame yields nothing, so the tile would come back blank.
	//
	// It goes first deliberately. My first version clamped the WINDOW floor afterwards, on the
	// reasoning that the window "should win". That is wrong: the two bounds can only conflict when the
	// window lies outside the file, and in that case there IS no valid frame -- the floor would
	// hand back a frame past the end of the media, which ffmpeg turns into a blank tile. Collapsing
	// every tile onto the file's last real frame is visibly wrong (a sprite of one repeated frame)
	// rather than silently blank, which is the better failure and the only one available.
	if p.totalFrames > 0 && int64(f) > p.totalFrames-1 {
		f = int(p.totalFrames - 1)
	}

	// Never before the window's start.
	//
	// There is deliberately NO clamp to the window's END, because it is unreachable -- and the reason
	// is the SLOW SEEK GUARD above, not the arithmetic alone.
	//
	// The last tile lands at `(chunk-1) * windowFrames/chunk`. That is below `windowFrames` whenever
	// `windowFrames >= chunkCount`, because `(chunk-1)/chunk = 1 - 1/chunk`. And the guard already
	// guarantees `windowFrames >= chunkCount` for every plan the generator can build: an exhaustive
	// sweep of frame rates, window lengths and MinimumSprites values found zero plans where the slow
	// path was chosen with fewer window frames than tiles.
	//
	// So the clamp would be dead code. Dead code that reads as a safety guarantee is worse than no
	// code, because the next reader trusts it -- and dropping the guard above DOES make the clamp
	// reachable, which is the only reason to keep it in mind.
	if f < p.firstFrame() {
		f = p.firstFrame()
	}
	return f
}

// vttStepSize is the cue spacing for the VTT.
//
// The cues are relative to the media element's own timeline -- `vtt-thumbnails.ts` computes
// `time = percent * player.duration()` -- so for a windowed scene, which is streamed windowed, this
// is the window's step and not a file position.
func (p *spritePlan) vttStepSize() float64 {
	return p.stepSize()
}
