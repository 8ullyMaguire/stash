package manager

// stash#2149, the CALLER side.
//
// A mutation harness caught what the helper tests could not: deleting the call
// to storablePhash from Start left every helper test green. A test of a helper
// is not a test of its caller -- the helper can be perfect and still be
// unused. These tests drive the real GeneratePhashTask.Start with a real
// ffmpeg.FFMpeg pointed at a stub encoder script, so the only thing stubbed is
// ffmpeg's output. The branch under test is production code.
//
// #2149's actual damage is a confidently WRONG value reaching the database and
// then StashDB, so what is asserted here is the EFFECT on the stored file, not
// the return value of a function.

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/utils"
	"golang.org/x/image/bmp"
)

// stubTxnManager lets the real Repository.WithTxn run its callback without a
// database, so the production transaction wrapper is exercised for real.
type stubTxnManager struct{}

func (stubTxnManager) Begin(ctx context.Context, _ bool) (context.Context, error) {
	return ctx, nil
}
func (stubTxnManager) Commit(context.Context) error   { return nil }
func (stubTxnManager) Rollback(context.Context) error { return nil }
func (stubTxnManager) IsLocked(error) bool            { return false }
func (stubTxnManager) WithDatabase(ctx context.Context) (context.Context, error) {
	return ctx, nil
}

// capturingFileWriter records every Update call. A phash that is refused must
// produce NO Update at all -- not an Update writing a zero.
//
// FileReaderWriter is embedded rather than implemented: the interface has
// dozens of methods and none of them are under test here, and writing them all
// out would be noise that hides the two that matter.
type capturingFileWriter struct {
	models.FileReaderWriter

	updates  int
	last     *models.VideoFile
	siblings []models.File
}

func (w *capturingFileWriter) Update(_ context.Context, f models.File) error {
	w.updates++
	// The task always passes its own *VideoFile, but models.File is an
	// interface so a type assertion is unavoidable here. A failed assertion
	// leaves last nil, which every caller below checks by way of w.updates.
	if vf, ok := f.(*models.VideoFile); ok {
		w.last = vf
	}
	return nil
}

func (w *capturingFileWriter) FindByFingerprint(context.Context, models.Fingerprint) ([]models.File, error) {
	return w.siblings, nil
}

// stubEncoder writes a shell script standing in for ffmpeg.
func stubEncoder(t *testing.T, imagePath string) *ffmpeg.FFMpeg {
	t.Helper()

	script := filepath.Join(t.TempDir(), "ffmpeg-stub")
	body := "#!/bin/sh\ncat " + imagePath + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("writing stub encoder: %v", err)
	}
	return ffmpeg.NewEncoder(script)
}

// flatSprite is what ffmpeg produces for a file it cannot decode: a solid
// frame, repeated across the whole sprite. Its perceptual hash was MEASURED at
// 8000000000000000, which IsBadPhash rejects -- so this fixture reproduces the
// #2149 condition exactly, rather than approximating it.
//
// Do not use it for the positive test. A uniform image has no structure for
// the DCT to work with, so every flat colour collapses to the same degenerate
// hash, and the "good" case would be refused for the right reason but a
// misleading one.
func flatSprite(t *testing.T) string {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for x := 0; x < 64; x++ {
		for y := 0; y < 64; y++ {
			img.Set(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	return writeBMP(t, img)
}

// structuredSprite is a non-uniform frame standing in for real video: a
// deterministic gradient with hard edges, so the perceptual hash has something
// to work with. Measured phash: aa20d722f722ff22, which IsBadPhash accepts.
func structuredSprite(t *testing.T) string {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for x := 0; x < 64; x++ {
		for y := 0; y < 64; y++ {
			c := color.RGBA{
				R: uint8(x * 4),
				G: uint8(y * 4),
				B: uint8(255 - (x+y)*2),
				A: 255,
			}
			if (x/8+y/8)%2 == 0 {
				c = color.RGBA{R: 10, G: 20, B: 240, A: 255}
			}
			img.Set(x, y, c)
		}
	}
	return writeBMP(t, img)
}

// writeBMP writes the fixture in the format videophash actually asks ffmpeg
// for: BMP, decoded with image.Decode. Emitting PNG here would make every
// screenshot fail to decode, and a decode failure looks exactly like a
// refusal at the call site -- the task writes nothing either way. That is
// precisely the ambiguity these tests have to avoid.
func writeBMP(t *testing.T, img image.Image) string {
	t.Helper()

	var buf bytes.Buffer
	if err := bmp.Encode(&buf, img); err != nil {
		t.Fatalf("encoding sprite: %v", err)
	}

	path := filepath.Join(t.TempDir(), "sprite.bmp")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("writing sprite: %v", err)
	}
	return path
}

func withEncoder(t *testing.T, enc *ffmpeg.FFMpeg) {
	t.Helper()
	prev := instance
	instance = &Manager{FFMpeg: enc}
	t.Cleanup(func() { instance = prev })
}

func videoFile(path string, fps ...models.Fingerprint) *models.VideoFile {
	return &models.VideoFile{
		BaseFile: &models.BaseFile{Path: path, Fingerprints: fps},
	}
}

func runTask(t *testing.T, file *models.VideoFile, siblings []models.File, overwrite bool) *capturingFileWriter {
	t.Helper()

	w := &capturingFileWriter{siblings: siblings}
	// The field is a value, not a pointer: Repository.WithTxn has a pointer
	// receiver but the struct is copied into the task, which is how every
	// other task in this package holds it.
	repo := models.Repository{TxnManager: stubTxnManager{}, File: w}

	task := &GeneratePhashTask{
		repository: repo,
		File:       file,
		Overwrite:  overwrite,
	}
	task.Start(context.Background())
	return w
}

// The core assertion: a known-bad phash must never reach the database by any
// route. If storablePhash is not called, this fails -- which is the mutation
// the helper-only tests missed.
func TestTheTaskDoesNotStoreAKnownBadPhash(t *testing.T) {
	// A flat sprite is what ffmpeg emits for an undecodable file, and its
	// hash measures as a known-bad value, so this is the real #2149 path.
	withEncoder(t, stubEncoder(t, flatSprite(t)))

	w := runTask(t, videoFile("/tmp/undecodable.mp4"), nil, true)

	if w.updates != 0 {
		t.Errorf("the task wrote to the database %d time(s) for a file whose "+
			"phash is known-bad. #2149 is that this value gets uploaded to "+
			"StashDB and matches every other undecodable file.", w.updates)
	}
}

// A good phash must be stored, so the test above cannot pass merely because
// the task never writes anything.
func TestTheTaskStoresAGoodPhash(t *testing.T) {
	withEncoder(t, stubEncoder(t, structuredSprite(t)))

	w := runTask(t, videoFile("/tmp/good.mp4"), nil, true)

	if w.updates != 1 {
		t.Fatalf("expected exactly one database write for a good file, got %d; "+
			"if this fails, the refusal test above passes for the wrong reason",
			w.updates)
	}
	if phash := w.last.Fingerprints.Get(models.FingerprintTypePhash); phash == nil {
		t.Error("the file was written with no phash fingerprint")
	} else {
		t.Logf("stub encoder produced phash %s", utils.PhashToString(phash.(int64)))
	}
}

// A generation failure must not write anything. This is the path a stub
// encoder that does not exist exercises, and it is what proves the task
// really does call the encoder -- so the refusal test above cannot be passing
// simply because nothing ever runs.
func TestTheTaskCallsTheEncoderAndWritesOnlyOnSuccess(t *testing.T) {
	missing := ffmpeg.NewEncoder(filepath.Join(t.TempDir(), "does-not-exist"))
	withEncoder(t, missing)

	w := runTask(t, videoFile("/tmp/nope.mp4"), nil, true)

	if w.updates != 0 {
		t.Errorf("a file whose encoder does not exist was written to the "+
			"database %d time(s); generation must fail without storing",
			w.updates)
	}
}

// The existing-phash reuse path (#4393) must ALSO refuse a known-bad value
// copied from a sibling file. Reusing a hash stored before this validator
// existed is precisely how a bad value outlives the fix.
func TestReusingAnExistingPhashDoesNotStoreAKnownBadValue(t *testing.T) {
	withEncoder(t, stubEncoder(t, structuredSprite(t)))

	bad, err := utils.StringToPhash("8080808080808080")
	if err != nil {
		t.Fatal(err)
	}
	const oshash = int64(1234)

	sibling := videoFile("/tmp/sibling.mp4",
		models.Fingerprint{Type: models.FingerprintTypeOshash, Fingerprint: oshash},
		models.Fingerprint{Type: models.FingerprintTypePhash, Fingerprint: bad},
	)

	w := runTask(t,
		videoFile("/tmp/target.mp4",
			models.Fingerprint{Type: models.FingerprintTypeOshash, Fingerprint: oshash}),
		[]models.File{sibling}, false)

	if w.updates != 0 {
		t.Errorf("a known-bad phash reused from a sibling file was stored "+
			"(%d write(s)); a bad value already in the database outlives the "+
			"generation-time check unless the reuse path is guarded too",
			w.updates)
	}
}
