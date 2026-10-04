package models

import (
	"fmt"
	"testing"
)

// stash#4233 -- rotation is parsed by ffprobe (pkg/ffmpeg/ffprobe.go:112 declares Rotation, :356
// assigns it from the stream side data) and then discarded: it was never a column, never a model
// field, and never reached the UI. The player compared width against height directly, so a phone video
// stored 1920x1080 with a 90-degree sidecar -- which DISPLAYS as 1080x1920 -- was laid out as
// landscape.
//
// These tests are here because the arithmetic is a sign-handling bug waiting to happen. 90 and 270
// swap the axes while 0 and 180 do not, so "is the rotation odd" is wrong; and ffprobe reports
// counter-clockwise as a NEGATIVE angle, so an unnormalised `rotation == 90` is wrong too.

func TestDisplayDimensionsAccountForRotation(t *testing.T) {
	tests := []struct {
		name                       string
		width, height, rotation    int
		wantWidth, wantHeight      int
		wantPortrait, wantLandmark bool
	}{
		// The reported bug: a portrait phone video stored landscape with a quarter turn.
		{"portrait phone video, quarter turn", 1920, 1080, 90, 1080, 1920, true, false},
		{"same video, counter-clockwise", 1920, 1080, -90, 1080, 1920, true, false},
		{"quarter turn the other way", 1920, 1080, 270, 1080, 1920, true, false},
		{"out of range, 450 == 90", 1920, 1080, 450, 1080, 1920, true, false},
		{"out of range, -270 == 90", 1920, 1080, -270, 1080, 1920, true, false},

		// A half turn leaves the axes where they are. This is the other half of the "is it odd"
		// mistake: 1920x1080 rotated 180 is still landscape.
		{"half turn does not swap", 1920, 1080, 180, 1920, 1080, false, true},
		{"negative half turn", 1920, 1080, -180, 1920, 1080, false, true},

		// No rotation: pre-existing behaviour, which must not change.
		{"plain landscape", 1920, 1080, 0, 1920, 1080, false, true},
		{"plain landscape, no rotation key equivalent", 1920, 1080, 360, 1920, 1080, false, true},

		// A genuinely portrait file is portrait with or without a sidecar. If this breaks, the fix
		// broke the common case to serve the rare one.
		{"plain portrait", 1080, 1920, 0, 1080, 1920, true, false},
		{"portrait with a half turn", 1080, 1920, 180, 1080, 1920, true, false},

		// A quarter turn on an already-portrait file makes it landscape on screen. Symmetric with
		// the first case, and a sign error would break exactly one of the two.
		{"portrait stored, quarter turn, displays landscape", 1080, 1920, 90, 1920, 1080, false, true},

		// Square: neither. > is strict, so this must stay that way -- the rotation change touched
		// this comparison and a <= slip would silently change it.
		{"square, no rotation", 1080, 1080, 0, 1080, 1080, false, false},
		{"square, quarter turn", 1080, 1080, 90, 1080, 1080, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &VideoFile{Width: tt.width, Height: tt.height, Rotation: tt.rotation}

			if got := v.DisplayWidth(); got != tt.wantWidth {
				t.Errorf("DisplayWidth() = %d, want %d", got, tt.wantWidth)
			}
			if got := v.DisplayHeight(); got != tt.wantHeight {
				t.Errorf("DisplayHeight() = %d, want %d", got, tt.wantHeight)
			}
			if got := v.DisplayOrientation(); got != tt.wantPortrait {
				t.Errorf("DisplayOrientation() = %v, want %v", got, tt.wantPortrait)
			}
		})
	}
}

// TestDisplayDimensionsIgnoreUnknownSize pins the "unknown is not evidence" rule.
//
// A scene whose metadata failed to scan has width and height of 0. Guessing portrait there would flip
// the layout of every such scene, which is a worse failure than defaulting to landscape -- so the
// helpers return zero and false rather than dividing by zero or treating 0 as the smaller dimension.
func TestDisplayDimensionsIgnoreUnknownSize(t *testing.T) {
	for _, v := range []*VideoFile{
		nil,
		{},
		{Width: 0, Height: 0, Rotation: 90},
		{Width: 1920, Height: 0, Rotation: 90},
		{Width: 0, Height: 1080, Rotation: 90},
		{Width: -1920, Height: -1080, Rotation: 90},
	} {
		if got := v.DisplayWidth(); got != 0 {
			t.Errorf("%s: DisplayWidth() = %d, want 0 for an unknown size", describe(v), got)
		}
		if got := v.DisplayHeight(); got != 0 {
			t.Errorf("%s: DisplayHeight() = %d, want 0 for an unknown size", describe(v), got)
		}
		if v.DisplayOrientation() {
			t.Errorf("%s: DisplayOrientation() = true, want false for an unknown size", describe(v))
		}
	}
}

func describe(v *VideoFile) string {
	if v == nil {
		return "nil file"
	}
	return fmt.Sprintf("%dx%d rot %d", v.Width, v.Height, v.Rotation)
}

// TestRotationDefaultsToZeroForUnscannedFiles is the migration's no-op guarantee.
//
// 123_video_rotation.up.sql adds the column NOT NULL DEFAULT 0, so every pre-existing row reads 0 and
// every helper above returns exactly what it did before the migration. If this test ever needs a
// different expectation, a migration has started changing existing rows, which is the specific failure
// 122_scene_time_range.up.sql was written to avoid.
func TestRotationDefaultsToZeroForUnscannedFiles(t *testing.T) {
	v := &VideoFile{Width: 1920, Height: 1080}
	if v.Rotation != 0 {
		t.Fatalf("the zero value of Rotation must be 0 so the DEFAULT 0 column is a true no-op, got %d", v.Rotation)
	}
	if v.DisplayWidth() != 1920 || v.DisplayHeight() != 1080 {
		t.Errorf("a rotation of 0 must leave the dimensions untouched, got %dx%d",
			v.DisplayWidth(), v.DisplayHeight())
	}
}
