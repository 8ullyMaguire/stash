package api

// stash#3530 — the DIRECT stream endpoint must not serve a window's file and call it a day.
//
// StreamSceneDirect ends in http.ServeFile(w, r, fp), which serves the whole file. An HTTP Range
// header does not help: it addresses BYTES, and an MP4's byte position for time T depends on the
// container's variable-bitrate layout, so any time-to-byte guess lands mid-GOP.
//
// So a ranged scene either transcodes (307 to /stream.mp4) or is refused (409). These tests cover
// the DECISION, which is the part that can silently regress into "serve the whole file".

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stashapp/stash/pkg/models"
)

// sceneWith builds a scene whose primary file carries the given window.
func sceneWith(start, end *float64) *models.Scene {
	return &models.Scene{
		ID: 7,
		Files: models.NewRelatedVideoFiles([]*models.VideoFile{{
			BaseFile:  &models.BaseFile{Path: "/media/x.mp4"},
			Duration:  7200,
			StartTime: start,
			EndTime:   end,
		}}),
	}
}

// TestOnlyAWindowedSceneIsRefusedFromDirectStreaming — the shape of the whole decision. An
// unranged scene must keep working, INCLUDING with transcoding disabled: that is the common case
// and it must never be refused.
func TestOnlyAWindowedSceneIsRefusedFromDirectStreaming(t *testing.T) {
	for _, tc := range []struct {
		name       string
		start, end *float64
		wantRanged bool
	}{
		{"no window at all", nil, nil, false},
		{"both ends", f(60), f(300), true},
		{"open at the end", f(60), nil, true},
		{"open at the start", nil, f(300), true},
		// A window starting at 0 is still a window. The nil-means-no-window rule means this
		// cannot be written as `*start > 0`, and this case is why.
		{"a window starting exactly at zero", f(0), f(300), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantRanged, isRangedScene(sceneWith(tc.start, tc.end)))
		})
	}
}

// TestASceneWithNoFileIsNotWindowed — Primary() is nil when the file is missing, and the decision
// must not panic on it.
func TestASceneWithNoFileIsNotWindowed(t *testing.T) {
	assert.NotPanics(t, func() {
		empty := &models.Scene{ID: 7}
		assert.False(t, isRangedScene(empty),
			"a scene whose file vanished has no window to honour; it is #3526's 404 case")
		assert.Equal(t, "unknown", describeWindow(empty))
	})
}

// TestTheRefusalBodyNamesTheWindow — a bare "not possible" leaves a user with no way to find the
// offending scene, and the body is the only place they can.
func TestTheRefusalBodyNamesTheWindow(t *testing.T) {
	assert.Equal(t, "60.0s-300.0s", describeWindow(sceneWith(f(60), f(300))),
		"a window with both ends must show both")
	assert.Equal(t, "from 60.0s", describeWindow(sceneWith(f(60), nil)),
		"an open-ended window has no end to print, so it must say so rather than print 0")
	assert.Equal(t, "until 300.0s", describeWindow(sceneWith(nil, f(300))),
		"likewise for one with no start")
}
