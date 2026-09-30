package generate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stash#7229: "Video preview generation fails for files whose video stream starts
// at a non-zero timestamp while the container reports `start: 0`."
//
// THE FAILURE, reproduced against the real ffmpeg:
//
//	ffmpeg -ss 0 -i video_offset.mp4 -t 1.0 -c:v libx264 -an seg0.mp4
//	  exit 0, 261 bytes, and ffprobe reports VideoCodec="" and no streams
//
// ffmpeg treats seeking into a range with no frames as a CLEAN NO-OP. It exits 0
// and leaves a file behind, and `-xerror` does not help because there is no warning
// to escalate. `previewVideoChunk` sets `XError: !fallback` and that flag is
// correctly applied (transcode.go:69-71), so this is not a missing flag.
//
// The stream-less file then poisons the concat: the demuxer takes the stream layout
// from the FIRST entry of the list, so one bad segment aborts the whole preview with
// "Output file does not contain any stream" even though every other segment encoded
// correctly. The reporter's note that "the fallback path fails identically" follows
// from this too: fallback only changes SlowSeek and drops XError.
//
// These tests generate real media with real ffmpeg when it is available, because
// the whole defect is a property of ffmpeg's output -- a mocked encoder would only
// prove that the code trusts what it is told, which was never in doubt.

// mustFFProbe resolves ffprobe from PATH. NewFFProbe takes a path and does not
// look one up itself, so a bare "" silently yields "exec: no command" -- which
// would make every segment test fail for a reason unrelated to the defect.
func mustFFProbe(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not available")
	}
	return p
}

// stash7229Fixture builds the reporter's shape: container start_time 0, video
// stream start_time non-zero. Returns the path and skips if ffmpeg is absent.
func stash7229Fixture(t *testing.T) (dir string, offsetVideo string) {
	t.Helper()

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available; the defect is a property of ffmpeg's output")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not available")
	}

	dir = t.TempDir()

	// A plain source with real video frames AND audio. The audio is not incidental:
	// a container's start_time follows its EARLIEST stream, so with a video-only
	// mux the container start simply becomes the video's 3s and there is no
	// discrepancy to hit. Audio sitting at 0 is what holds the container at 0 while
	// the video starts later, which is the reporter's file and the real one.
	src := filepath.Join(dir, "src.mp4")
	require.NoError(t, exec.Command("ffmpeg", "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=10",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=10",
		"-map", "0:v", "-map", "1:a",
		"-c:v", "libx264", "-c:a", "aac", "-shortest", src).Run())

	// Delay the VIDEO stream by 3s while the CONTAINER starts at 0 -- the reporter's
	// shape, and what HandBrake produces when the original recording had audio
	// first.
	//
	// Two things are load-bearing here, and getting either wrong yields a different
	// and much more benign file that quietly stops reproducing the defect:
	//
	//   - the audio mapped at 0, because a container's start_time follows its
	//     EARLIEST stream; video-only would drag the container to 3s too
	//   - the `-itsoffset 3` second input, without which a sole delayed stream
	//     also sets the container start
	//
	// The required StartTime assertion below is what catches either regression.
	offsetVideo = filepath.Join(dir, "offset.mp4")
	require.NoError(t, exec.Command("ffmpeg", "-y", "-loglevel", "error",
		"-i", src, "-itsoffset", "3", "-i", src,
		"-filter_complex", "[1:v]trim=0:5,setpts=PTS-STARTPTS+3/TB[v]",
		"-map", "[v]", "-map", "0:a",
		"-c:v", "libx264", "-c:a", "aac", "-shortest", offsetVideo).Run())

	return dir, offsetVideo
}

// cutSegment runs the same shape of command previewVideoChunk builds.
func cutSegment(t *testing.T, input string, start string, dur string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "seg.mp4")
	cmd := exec.Command("ffmpeg", "-y", "-loglevel", "error",
		"-ss", start, "-i", input, "-t", dur,
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-an", out)
	require.NoError(t, cmd.Run(), "ffmpeg must exit 0 even for a frameless range")
	return out
}

// THE PREMISE. This is the behaviour the whole fix rests on: ffmpeg SUCCEEDS on a
// range with no video frames and writes a small, non-empty, stream-less file.
//
// Asserting it means a future ffmpeg that starts failing here would be a visible
// behaviour change rather than a silent one. It also documents why a size check
// cannot be the fix: the file is 261 bytes, not 0.
func TestFfmpegExitsZeroAndWritesAStreamlessFileForAFramelessRange(t *testing.T) {
	dir, offsetVideo := stash7229Fixture(t)

	// Confirm the fixture really has the reported shape. Asserting the container
	// start is what makes a degraded fixture a failure rather than a test that
	// quietly stops testing the defect.
	vf, err := ffmpeg.NewFFProbe(mustFFProbe(t)).NewVideoFile(offsetVideo)
	require.NoError(t, err)
	require.NotEmpty(t, vf.VideoCodec, "the fixture must have a video stream overall")
	require.Less(t, vf.StartTime, 0.001,
		"the CONTAINER must start at 0 -- that is the discrepancy the defect needs")

	seg := cutSegment(t, offsetVideo, "0", "1.0")

	fi, err := os.Stat(seg)
	require.NoError(t, err)
	assert.Greater(t, fi.Size(), int64(0),
		"the file is NOT empty, which is why a size check cannot be the fix")

	segProbe, err := ffmpeg.NewFFProbe(mustFFProbe(t)).NewVideoFile(seg)
	require.NoError(t, err)
	assert.Empty(t, segProbe.VideoCodec,
		"the segment at t=0 has no video stream: no frames exist before the offset")
	_ = dir
}

// THE FIX. A segment with no video stream is rejected.
func TestASegmentWithNoVideoStreamIsNotUsable(t *testing.T) {
	_, offsetVideo := stash7229Fixture(t)
	g := Generator{FFProbe: ffmpeg.NewFFProbe(mustFFProbe(t))}

	// Before the offset: no frames, so not usable.
	assert.False(t, g.segmentIsUsable(cutSegment(t, offsetVideo, "0", "1.0")),
		"a segment cut before the video stream starts must be rejected")

	// After the offset: real frames, so usable.
	assert.True(t, g.segmentIsUsable(cutSegment(t, offsetVideo, "3.2", "1.0")),
		"a segment cut inside the video stream must be accepted")
}

// A missing or zero-length file is rejected without needing ffprobe.
func TestAMissingOrEmptySegmentIsNotUsable(t *testing.T) {
	g := Generator{FFProbe: ffmpeg.NewFFProbe(mustFFProbe(t))}

	assert.False(t, g.segmentIsUsable(filepath.Join(t.TempDir(), "nope.mp4")),
		"a missing segment must not be usable")

	empty := filepath.Join(t.TempDir(), "empty.mp4")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))
	assert.False(t, g.segmentIsUsable(empty), "a 0-byte segment must not be usable")
}

// THE POSITIVE CONTROL, and the guard against an over-broad fix. Ordinary segments
// must still be accepted -- if this fails, the filter rejects everything and every
// preview fails differently.
func TestAnOrdinarySegmentIsStillUsable(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp4")
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	require.NoError(t, exec.Command("ffmpeg", "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=5",
		"-c:v", "libx264", src).Run())

	g := Generator{FFProbe: ffmpeg.NewFFProbe(mustFFProbe(t))}
	assert.True(t, g.segmentIsUsable(cutSegment(t, src, "0", "1.0")),
		"a normal video's first segment must remain usable")
	assert.True(t, g.segmentIsUsable(cutSegment(t, src, "2.0", "1.0")),
		"a normal video's middle segment must remain usable")
}

// THE CONCAT LIST. What matters is not that a segment is filtered but that the
// generated list names only usable files -- and that the files it names still
// exist when the concat runs.
func TestTheConcatListNamesOnlyUsableSegments(t *testing.T) {
	_, offsetVideo := stash7229Fixture(t)
	g := Generator{FFProbe: ffmpeg.NewFFProbe(mustFFProbe(t))}

	bad := cutSegment(t, offsetVideo, "0", "1.0")
	good := cutSegment(t, offsetVideo, "3.2", "1.0")

	// Stand in for the loop: keep only what segmentIsUsable accepts, exactly as
	// previewVideo does.
	var usable []string
	for _, f := range []string{bad, good} {
		if g.segmentIsUsable(f) {
			usable = append(usable, f)
		}
	}
	require.Len(t, usable, 1, "exactly the good segment survives")

	// The surviving name must be a file that exists.
	_, err := os.Stat(usable[0])
	assert.NoError(t, err,
		"the concat list must never name a file the reader will not find")
}

// -xerror is NOT the answer, and asserting that prevents a future reader from
// "fixing" this by wiring the flag that is already wired.
func TestXErrorWouldNotHaveCaughtIt(t *testing.T) {
	_, offsetVideo := stash7229Fixture(t)
	out := filepath.Join(t.TempDir(), "seg.mp4")

	cmd := exec.Command("ffmpeg", "-y", "-xerror", "-loglevel", "error",
		"-ss", "0", "-i", offsetVideo, "-t", "1.0",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-an", out)
	err := cmd.Run()
	assert.NoError(t, err, "-xerror does not catch a frameless range: there is no warning")

	if err == nil {
		vf, perr := ffmpeg.NewFFProbe(mustFFProbe(t)).NewVideoFile(out)
		require.NoError(t, perr)
		assert.Empty(t, vf.VideoCodec,
			"so the stream-less file is produced even on the non-fallback path")
	}
}

// ---------------------------------------------------------------------------
// The tests above drive segmentIsUsable directly. These drive the SHIPPED
// PreviewVideo end to end, because the defect is not in the predicate -- it is in
// the loop trusting every segment and the concat list naming whatever it built.
// A mutation that reverted the filter, or dropped the tmpFiles reassignment, or
// removed the all-empty error SURVIVED when only the predicate was tested, because
// the test re-implemented the loop instead of running it. That is the lesson from
// the characterisation-test-fixture-fidelity skill, and it is why these exist.
// ---------------------------------------------------------------------------

// realScenePaths is a minimal ScenePaths: the only method the preview path calls
// besides the output paths is TempFile.
type realScenePaths struct {
	dir string
}

// outDir is created by stash7229Generator before use.
const outDir = "out"

func (p realScenePaths) TempFile(pattern string) (*os.File, error) {
	return os.CreateTemp(p.dir, "seg-*.mp4")
}

func (p realScenePaths) GetVideoPreviewPath(string) string {
	return filepath.Join(p.dir, "out", "preview.mp4")
}
func (p realScenePaths) GetWebpPreviewPath(string) string {
	return filepath.Join(p.dir, "out", "preview.webp")
}
func (p realScenePaths) GetSpriteImageFilePath(string) string {
	return filepath.Join(p.dir, "out", "sprite.jpg")
}
func (p realScenePaths) GetSpriteVttFilePath(string) string {
	return filepath.Join(p.dir, "out", "sprite.vtt")
}
func (p realScenePaths) GetTranscodePath(string) string {
	return filepath.Join(p.dir, outDir, "transcode.mp4")
}

// nullFFmpegConfig supplies the two arg lists. previewVideoChunk calls them
// unconditionally, so a nil FFMpegConfig panics rather than failing cleanly -- worth
// knowing if anyone writes another test in this package.
type nullFFmpegConfig struct{}

func (nullFFmpegConfig) GetTranscodeInputArgs() []string  { return nil }
func (nullFFmpegConfig) GetTranscodeOutputArgs() []string { return nil }

func stash7229Generator(t *testing.T) Generator {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, outDir), 0o750); err != nil {
		t.Fatal(err)
	}

	ffmpegBin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available")
	}
	// NewEncoder does not verify the binary is runnable, and a broken path would
	// otherwise surface as a confusing per-segment failure.
	enc := ffmpeg.NewEncoder(ffmpegBin)
	if out, err := exec.Command(ffmpegBin, "-version").CombinedOutput(); err != nil {
		t.Skipf("ffmpeg at %s is not runnable: %v", ffmpegBin, err)
	} else if len(out) == 0 {
		t.Skipf("ffmpeg at %s produced no output", ffmpegBin)
	}

	return Generator{
		Encoder:      enc,
		FFProbe:      ffmpeg.NewFFProbe(mustFFProbe(t)),
		ScenePaths:   realScenePaths{dir: dir},
		FFMpegConfig: nullFFmpegConfig{},
		LockManager:  fsutil.NewReadLockManager(),
		Overwrite:    true,
	}
}

// THE END-TO-END FIX. A file with a non-zero video start offset must produce a
// real preview, not "Output file does not contain any stream".
//
// Segments are laid out across the CONTAINER duration, so with the video starting
// at 3s in a 5s file the first segment lands entirely before any frame exists.
// Pre-fix that stream-less file was concatenated and killed the whole preview.
func TestPreviewVideoSucceedsWhenTheVideoStreamStartsLate(t *testing.T) {
	_, offsetVideo := stash7229Fixture(t)
	g := stash7229Generator(t)

	// Long enough to take the segmented path, not previewVideoSingle.
	opts := PreviewOptions{Segments: 4, SegmentDuration: 1.0, Audio: false, Preset: "ultrafast"}

	err := g.PreviewVideo(context.Background(), offsetVideo, 5.0, "abc123", opts, false, false)
	require.NoError(t, err, "a video whose stream starts late must still get a preview")

	out := g.ScenePaths.GetVideoPreviewPath("abc123")
	fi, statErr := os.Stat(out)
	require.NoError(t, statErr, "a preview file must actually be produced")
	assert.Greater(t, fi.Size(), int64(0), "and it must not be empty")
}

// The ALL-EMPTY case: every segment has no video stream, so there is nothing to
// concatenate. That must be a reported error, not a silent empty preview -- which
// is what the concat demuxer would otherwise produce while the task records
// success.
func TestPreviewVideoErrorsWhenEverySegmentIsUnusable(t *testing.T) {
	_, offsetVideo := stash7229Fixture(t)
	g := stash7229Generator(t)

	// A duration wholly inside the pre-offset gap: every segment has no frames.
	opts := PreviewOptions{Segments: 2, SegmentDuration: 0.5, Audio: false, Preset: "ultrafast"}

	err := g.PreviewVideo(context.Background(), offsetVideo, 1.0, "deadbeef", opts, false, false)
	require.Error(t, err, "an entirely unusable segment set must be an error, not a silent empty preview")
	assert.Contains(t, err.Error(), "no preview segment",
		"the error must say what happened, not just that something did")
}

// THE POSITIVE CONTROL on the shipped path: an ordinary video must still get a
// preview. If the filter were over-broad it would reject every segment and this
// would fail differently.
func TestPreviewVideoStillSucceedsForAnOrdinaryVideo(t *testing.T) {
	dir := t.TempDir()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	plain := filepath.Join(dir, "plain.mp4")
	require.NoError(t, exec.Command("ffmpeg", "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=6",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", plain).Run())

	g := stash7229Generator(t)
	opts := PreviewOptions{Segments: 4, SegmentDuration: 1.0, Audio: false, Preset: "ultrafast"}

	require.NoError(t, g.PreviewVideo(context.Background(), plain, 6.0, "feedface", opts, false, false),
		"an ordinary video must still get a preview")

	fi, err := os.Stat(g.ScenePaths.GetVideoPreviewPath("feedface"))
	require.NoError(t, err)
	assert.Greater(t, fi.Size(), int64(0))
}
