//go:build integration
// +build integration

package sqlite_test

// stash#3530 — SetSceneRange, the WRITE side of a scene's window.
//
// The read side (sceneFileRanges) has extensive coverage. The write side had none, and it is
// where the two silent failure modes live:
//
//   - an UPDATE that matches no row is reported by SQLite as SUCCESS. So writing a range onto a
//     file that is not attached to the scene looks like it worked, and the window does not exist.
//     TestARangeOnAnUnattachedFileIsRefused pins the RowsAffected check.
//   - a nil pointer must become NULL, not 0. If a nil became 0 then "clear the window" would
//     write a zero-length window, which the CHECKs refuse -- so the clear would fail loudly,
//     which is the good case. But if nil were dropped from the SET clause instead, the clear
//     would silently leave the OLD window in place and report success. TestBothNilClearsTheWindow
//     is the only thing that tells those two apart.
//
// Everything here goes through the STORE, not raw SQL, because the store is what the API calls.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/models"
)

func fptr(v float64) *float64 { return &v }

// TestTheStoreWritesAndReadsBackTheSameWindow — the round trip, in the store's own terms.
func TestTheStoreWritesAndReadsBackTheSameWindow(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		sceneID := mkRangeVideo(t, ctx, "write-round-trip.mp4", 1800)
		fileID := models.FileID(mkFileIDForScene(t, ctx, sceneID))

		require.NoError(t, db.Scene.SetSceneRange(ctx, sceneID, fileID, fptr(120.0), fptr(480.0)))

		assert.Equal(t, 360.0, rangeVideoDuration(t, ctx, sceneID),
			"a 120..480 window must read back as 360 seconds")

		// And the raw columns, because Duration is a DERIVED value and this test should not
		// pass merely because two wrongs cancelled.
		assert.Equal(t, 120.0, scalar(t, ctx,
			"SELECT start_time FROM scenes_files WHERE scene_id = ?", sceneID),
			"start_time must be stored verbatim")
		assert.Equal(t, 480.0, scalar(t, ctx,
			"SELECT end_time FROM scenes_files WHERE scene_id = ?", sceneID),
			"end_time must be stored verbatim")
	})
}

// TestAnOpenEndedWindowIsAStartWithNoEnd — the form the API must be able to express.
func TestAnOpenEndedWindowIsAStartWithNoEnd(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		sceneID := mkRangeVideo(t, ctx, "write-open-ended.mp4", 1800)
		fileID := models.FileID(mkFileIDForScene(t, ctx, sceneID))

		require.NoError(t, db.Scene.SetSceneRange(ctx, sceneID, fileID, fptr(600.0), nil))

		assert.Equal(t, 1200.0, rangeVideoDuration(t, ctx, sceneID),
			"an open-ended window from 600s runs to the end of a 1800s file")
	})
}

// TestBothNilClearsTheWindow — and it must be a REAL clear, not a no-op.
//
// The distinction this pins: an implementation that DROPS nil values from the SET clause leaves
// the previous window in place and returns success. Same observable "no error", completely
// different meaning.
func TestBothNilClearsTheWindow(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		sceneID := mkRangeVideo(t, ctx, "write-clear.mp4", 1800)
		fileID := models.FileID(mkFileIDForScene(t, ctx, sceneID))

		require.NoError(t, db.Scene.SetSceneRange(ctx, sceneID, fileID, fptr(10.0), fptr(20.0)))
		require.Equal(t, 10.0, rangeVideoDuration(t, ctx, sceneID), "precondition: the window is set")

		require.NoError(t, db.Scene.SetSceneRange(ctx, sceneID, fileID, nil, nil))

		assert.Equal(t, 1800.0, rangeVideoDuration(t, ctx, sceneID),
			"clearing the window must restore the whole file, not leave 10..20 in place")
	})
}

// TestARangeOnAnUnattachedFileIsRefused — the RowsAffected check.
//
// Without it this returns nil and the caller believes it set a window on a scene that has no
// such file. The window then does not exist and nothing reports that.
func TestARangeOnAnUnattachedFileIsRefused(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		sceneID := mkRangeVideo(t, ctx, "write-unattached-target.mp4", 1800)
		other := mkRangeVideoFile(t, ctx, "write-unattached-other.mp4", 600)

		err := db.Scene.SetSceneRange(ctx, sceneID, other.ID, fptr(10.0), fptr(20.0))
		require.Error(t, err,
			"writing a range onto a file this scene does not have must be an error, not a silent no-op")
		assert.Contains(t, err.Error(), "not attached",
			"the message must say what is wrong; a caller cannot act on 'constraint failed'")
	})
}

// TestTheWindowIsPerSceneFilePair — the grain, and the reason SetSceneRange takes a fileID.
//
// A fix that preserved windows by caching one per FILE would pass every other test here and
// then overwrite one scene's window with another's. This drives two scenes over one file.
func TestTheWindowIsPerSceneFilePair(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		f := mkRangeVideoFile(t, ctx, "write-two-scenes.mp4", 1800)

		first := newSceneOver(t, ctx, f)
		second := newSceneOver(t, ctx, f)
		setRange(t, ctx, second, 900.0, 1200.0)
		require.NoError(t, exec(t, ctx,
			"UPDATE scenes_files SET `primary` = 0 WHERE scene_id = ?", second))

		require.NoError(t, db.Scene.SetSceneRange(ctx, first, f.ID, fptr(0.0), fptr(300.0)))

		secondFiles, err := db.Scene.GetFiles(ctx, second)
		require.NoError(t, err)
		require.Len(t, secondFiles, 1)
		assert.Equal(t, 300.0, secondFiles[0].Duration,
			"writing the first scene's window must not touch the second scene of the same file")
	})
}

// TestTheStoreDoesNotClampAWindowItIsGiven — the deliberate non-clamp, pinned.
//
// sceneFileRanges clamps on READ so that hand-written SQL cannot break a reader. The WRITE
// deliberately does not: clamping here would save a number the caller never sent, so a read-back
// would disagree with the request. The API refuses an overrunning window instead
// (mutationResolver.validateSceneWindow), which keeps the two layers' jobs distinct.
func TestTheStoreDoesNotClampAWindowItIsGiven(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		sceneID := mkRangeVideo(t, ctx, "write-no-clamp.mp4", 1800)
		fileID := models.FileID(mkFileIDForScene(t, ctx, sceneID))

		// 9999 is past the end of a 1800s file. The store writes it verbatim.
		require.NoError(t, db.Scene.SetSceneRange(ctx, sceneID, fileID, fptr(0.0), fptr(9999.0)))

		stored := scalar(t, ctx,
			"SELECT end_time FROM scenes_files WHERE scene_id = ?", sceneID)
		require.NotNil(t, stored, "the value must be stored, not dropped")
		assert.Equal(t, 9999.0, stored,
			"the store writes what it was given; refusing an overrunning window is the API's job")

		// ...and the READ clamps it, which is the other half of the same contract.
		assert.Equal(t, 1800.0, rangeVideoDuration(t, ctx, sceneID),
			"the reader clamps an overrunning window so a scene can never report past its file")
	})
}
