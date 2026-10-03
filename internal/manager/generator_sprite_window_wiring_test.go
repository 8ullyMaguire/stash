package manager

// stash#3530 — the sprite generator must READ the plan, for all three of tile time, tile frame and
// cue spacing.
//
// sprite_window_test.go proves the arithmetic. It does not prove the generator uses it, and that
// distinction is this file's entire reason for existing — twice in this repo's history, the same
// gap produced a green suite over unwired code:
//
//   - #3530's preview: tests asserted rebaseExclude()/startOf() directly, so reverting the CALL in
//     previewVideo changed no result. Two survivors, and the fix was extracting tilePlan so the
//     loop reads a value rather than calling helpers.
//   - #3530's aggregates: four of five SceneRangeDurationSQL call sites were never wired while the
//     constant's own mutation sweep read 5/5 killed.
//
// So this asserts on SOURCE. Proving it behaviourally means running ffmpeg 81 times per grid, and
// the source assertion fails loudly with the line number when someone reverts the wiring, which is
// what a wiring guard is for.

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readGeneratorSprite(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("generator_sprite.go")
	require.NoError(t, err, "generator_sprite.go must be readable; this test asserts on its source")
	return string(src)
}

// bodyOf returns the source of the named top-level func in the given source.
//
// The signature is matched loosely on purpose: `func (g *SpriteGenerator) generateSpriteImage(`
// and `func NewSpriteGenerator(` both start with `func ` and end with `(` before the first
// newline-terminated `}`. My first version anchored on the receiver, and then three tests failed
// with "NewSpriteGenerator must still exist" -- the constructor is a plain function and my matcher
// was naming things it had invented rather than things the file contains. A guard that cannot find
// its own subject is a guard that cannot fail.
func bodyOf(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "func "+name+"(")
	if start == -1 {
		start = strings.Index(src, "func (g *SpriteGenerator) "+name+"(")
	}
	require.NotEqual(t, -1, start, "%s must still exist in the source", name)
	rest := src[start:]
	end := strings.Index(rest, "\n}\n")
	require.NotEqual(t, -1, end, "%s must be a complete top-level function", name)
	return rest[:end]
}

// The tile-time loop must read the plan.
//
// The inline expression it replaced was `time := float64(i) * stepSize` with
// `stepSize := VideoStreamDuration / ChunkCount`. Reverting to it produces a grid tiled across the
// WHOLE file for a windowed scene — the defect.
func TestTheTileLoopReadsThePlan(t *testing.T) {
	body := bodyOf(t, readGeneratorSprite(t), "generateSpriteImage")

	assert.Contains(t, body, "g.Plan.Time(i)",
		"the tile loop must take its time from the plan. Reverting to the inline "+
			"`i*VideoStreamDuration/ChunkCount` tiles a windowed scene across the whole file")

	assert.NotContains(t, body, "VideoStreamDuration / float64(g.Info.ChunkCount)",
		"the file's duration must not be the step: it is the window's")
}

// The frame loop must read the plan too, and it is the one a duration-only test cannot catch.
//
// `stepFrame := float64(FrameCount-1)/float64(ChunkCount)` gives a correctly SPACED grid starting at
// frame 0 — still the wrong footage, and every spacing assertion in sprite_window_test.go passes.
func TestTheFrameLoopReadsThePlan(t *testing.T) {
	body := bodyOf(t, readGeneratorSprite(t), "generateSpriteImage")

	assert.Contains(t, body, "g.Plan.Frame(i)",
		"the frame-seeking loop must take its frame from the plan. A frame index is absolute, so "+
			"an inline i*stepFrame starts at frame 0 even when the plan knows the window starts at "+
			"300s -- correctly spaced, wrong footage, and no spacing assertion catches it")

	assert.NotContains(t, body, "float64(g.Info.VideoFile.FrameCount-1) / float64(g.Info.ChunkCount)",
		"the step-frame expression must be gone from the loop; it belongs to the plan's unranged "+
			"branch alone")
}

// The VTT writer must read the plan's cue spacing.
func TestTheVTTWriterReadsThePlansCueSpacing(t *testing.T) {
	body := bodyOf(t, readGeneratorSprite(t), "generateSpriteVTT")

	assert.Contains(t, body, "g.Plan.VttStep()",
		"the VTT writer must take its cue spacing from the plan. The cues are a CONTRACT with the "+
			"player -- vtt-thumbnails.ts matches them against percent*player.duration() -- so a "+
			"windowed scene needs the window's spacing or the scrub bar lands on the wrong second")

	assert.NotContains(t, body, "g.Info.NthFrame",
		"NthFrame is the FILE's frame step; for a windowed scene the step is the window's, and "+
			"taking it from the file produces cues pointing into footage the scene never plays")
}

// The constructor must TILE THE WINDOW and key on it. A generator that computes a correct chunk
// count and then builds its plan from the file's duration passes every arithmetic test above.
func TestTheConstructorTilesTheWindow(t *testing.T) {
	src := readGeneratorSprite(t)
	body := bodyOf(t, src, "NewSpriteGenerator")

	assert.Contains(t, body, "window.Length(videoFile.VideoStreamDuration)",
		"the grid's length must be the WINDOW's. calculateSpriteInterval and the chunk count both "+
			"derive from this, so a file's duration here tiles a windowed scene across the whole file")

	assert.Contains(t, body, "config.SpriteInterval = calculateSpriteInterval(spriteDuration, config)",
		"the interval must come from the window's length")

	assert.Contains(t, body, "chunkCount := int(math.Ceil(spriteDuration / config.SpriteInterval))",
		"the chunk count must come from the window's length -- a 300s window wants 81 tiles 3.7s "+
			"apart, not 81 tiles 89s apart")
}

// The windowed slow-seek branch must be EXCLUSIVE with the file-level one.
//
// If both run, a 2-hour file passes the file-level test however short its window is, and a
// short-windowed scene gets frame seeking it cannot support — which is the duplicate-frame grid.
func TestTheSlowSeekBranchesAreExclusive(t *testing.T) {
	body := bodyOf(t, readGeneratorSprite(t), "NewSpriteGenerator")

	assert.Contains(t, body, "if window.Set {",
		"the windowed slow-seek decision must be guarded on the window")

	assert.Contains(t, body, "} else if videoFile.VideoStreamDuration < 5 ||",
		"the file-level condition must be the ELSE arm. If both branches can fire, a windowed "+
			"scene is judged by the file's duration and can get a grid of duplicate frames")

	// Two separate `if` statements rather than one if/else is the shape that lets both fire.
	ifCount := strings.Count(body, "if videoFile.VideoStreamDuration < 5")
	assert.Equal(t, 1, ifCount,
		"there must be exactly ONE place that makes the file-level slow-seek decision")
}

// The plan must be built AFTER configure() (NthFrame) and AFTER the frame recount, and the
// slow-seek decision must be made with the SAME frame rate the plan carries.
//
// Two orderings are wrong and both fail silently. Building the plan before configure() gives
// NthFrame == 0, so every cue is written at 0.000. Deciding frame seeking with a different rate
// than the plan holds judges a windowed file by a rate of 0 when ffprobe could not read it -- which
// is precisely the case frame seeking exists for.
func TestThePlanIsBuiltAfterTheFrameCountIsConfigured(t *testing.T) {
	body := bodyOf(t, readGeneratorSprite(t), "NewSpriteGenerator")

	configure := strings.Index(body, "generator.configure()")
	require.NotEqual(t, -1, configure, "configure() must still run before the plan is built")

	// The options value must be CONSTRUCTED before configure() -- it carries NthFrame -- so the
	// plan itself (the NewSpritePlan call) is what must come after.
	options := strings.Index(body, "planOptions := generate.SpritePlanOptions{")
	require.NotEqual(t, -1, options, "the plan's options must be built into one named value, so the "+
		"slow-seek decision and the plan cannot be given different inputs")

	plan := strings.Index(body, "generate.NewSpritePlan(")
	require.NotEqual(t, -1, plan, "the plan must still be built in the constructor")

	decision := strings.Index(body, "generate.SpriteNeedsFrameSeek(")
	require.NotEqual(t, -1, decision, "the windowed slow-seek decision must go through "+
		"SpriteNeedsFrameSeek, the same function the plan uses")

	assert.Less(t, configure, plan,
		"the plan reads generator.NthFrame, which configure() computes. Built earlier it reads 0, "+
			"and every VTT cue is written at 0.000 for a file whose frame count could not be read")

	assert.Less(t, options, decision,
		"the decision must read the SAME options value the plan does. Building its own struct is "+
			"how two frame rates end up deciding one thing")

	// One rate, read from generator.FrameRate, and not from the probe's field.
	assert.Equal(t, 1, strings.Count(body, "FrameRate:    generator.FrameRate,"),
		"exactly one place supplies the plan's frame rate")
	assert.NotContains(t, body, "FrameRate:    videoFile.FrameRate,",
		"videoFile.FrameRate is the PROBE's figure and is 0 whenever ffprobe could not determine "+
			"it; generator.FrameRate is the resolved one. Using the probe's makes a windowed short "+
			"file look frameless")
}

// The task must read the window through the loader that HAS one, and must compute its key after.
//
// Both were found by measurement rather than by reading (pkg/sqlite/scene_window_loader_test.go), and
// both fail silently: the first generates a sprite of the whole file with every arithmetic test
// green, the second writes to the unwindowed path while the route looks for the windowed one, so
// two scenes of one file overwrite each other's sprite forever.
func TestTheSpriteTaskLoadsTheWindowedPrimaryFile(t *testing.T) {
	src, err := os.ReadFile("task_generate_sprite.go")
	require.NoError(t, err)
	body := string(src)

	assert.Contains(t, body, "LoadPrimaryFileWithWindow(",
		"the sprite task must load through LoadPrimaryFileWithWindow. LoadPrimaryFile goes through "+
			"FileStore.Find, which does not select scenes_files.start_time/end_time at all -- "+
			"MEASURED in pkg/sqlite/scene_window_loader_test.go -- so it reports no window whatever "+
			"the row says, and the grid comes out tiled across the whole file")

	assert.NotContains(t, body, "t.Scene.LoadPrimaryFile(",
		"LoadPrimaryFile must not be used for the window")

	load := strings.Index(body, "LoadPrimaryFileWithWindow(")
	require.NotEqual(t, -1, load)

	key := strings.Index(body, "models.GeneratedChecksum(t.Scene, t.fileNamingAlgorithm)")
	require.NotEqual(t, -1, key,
		"the sprite key must be GeneratedChecksum, or two scenes of one file share one sprite")

	assert.Less(t, load, key,
		"the key MUST be computed after the window is loaded: before it there is no window to put "+
			"in the key, so the sprite is written to the unwindowed path while the route looks for "+
			"the windowed one -- regenerated on every request, silently, with no error anywhere")
}

// The existence check must take the SAME key as the write, as a parameter.
//
// A second GetHash inside the check would look at the OTHER scene's sprite for a windowed scene,
// find it present, and skip -- so a windowed scene would never get a sprite at all, and the task
// would report success.
func TestTheSpriteExistenceCheckTakesTheKeyAsAParameter(t *testing.T) {
	src, err := os.ReadFile("task_generate_sprite.go")
	require.NoError(t, err)
	body := string(src)

	start := strings.Index(body, "func (t *GenerateSpriteTask) spriteRequired(")
	require.NotEqual(t, -1, start, "spriteRequired must still exist")
	check := body[start:]

	end := strings.Index(check, "\n}\n")
	require.NotEqual(t, -1, end)
	check = check[:end]

	assert.Contains(t, check, "sceneHash string",
		"the check must RECEIVE the key. Recomputing it here would use the plain file hash, which "+
			"for a windowed scene is the other scene's key -- the check would find that sprite "+
			"present and skip, so a windowed scene never gets one")

	assert.NotContains(t, check, "GetHash(",
		"the check must not recompute the hash; it is given the window-aware one")
}

// The route must serve the windowed sprite, or the generator writes files nothing reads.
func TestTheSpriteRoutesUseTheWindowAwareKey(t *testing.T) {
	src, err := os.ReadFile("../api/routes_scene.go")
	require.NoError(t, err)
	body := string(src)

	start := strings.Index(body, "func spriteSceneHash(")
	require.NotEqual(t, -1, start, "spriteSceneHash must still exist")
	end := strings.Index(body[start:], "\n}\n")
	require.NotEqual(t, -1, end)
	helper := body[start : start+end]

	assert.Contains(t, helper, "models.GeneratedChecksum(",
		"the sprite routes must key with GeneratedChecksum: the sprite is generated from inside the "+
			"window, so serving it by the plain hash serves the UNWINDOWED sprite of another scene "+
			"-- a sprite of the wrong footage, at a URL that looks entirely correct")

	// Both handlers must go through the helper, so they cannot drift apart.
	for _, name := range []string{"VttThumbs", "VttSprite"} {
		h := strings.Index(body, "func (rs sceneRoutes) "+name+"(")
		require.NotEqual(t, -1, h, "%s must still exist", name)
		rest := body[h:]
		hb := rest[:strings.Index(rest, "\n}\n")]
		assert.Contains(t, hb, "spriteSceneHash(r)",
			"%s must resolve its key through spriteSceneHash, or one of the two can drift back to "+
				"the plain hash and the sprite and its cues disagree about which scene they show", name)
	}
}
