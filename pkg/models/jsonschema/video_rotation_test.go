package jsonschema

import (
	"encoding/json"
	"strings"
	"testing"
)

// stash#4233 -- rotation has to survive a metadata-only export/import.
//
// The JSON export is a METADATA export: it carries the file's measured properties rather than the file
// itself, and the import path reconstructs a models.VideoFile from it (pkg/file/import.go). Adding
// Rotation to models.VideoFile without adding it to the JSON shape would therefore compile, pass every
// test, and silently flatten a rotated video back to its encoded orientation on every export/import
// cycle -- with no error anywhere, because a missing field is indistinguishable from a zero one.
//
// That is why these are tests rather than a code-reading convention: the failure mode is a field that
// is absent, and absence is exactly what a compile check cannot see.

func TestVideoFileJSONCarriesRotation(t *testing.T) {
	v := VideoFile{
		Width:    1920,
		Height:   1080,
		Rotation: 90,
	}

	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if !strings.Contains(string(raw), `"rotation":90`) {
		t.Errorf("rotation missing from the exported JSON: %s", raw)
	}
}

func TestVideoFileJSONOmitsZeroRotation(t *testing.T) {
	// The other half, and the reason for omitempty rather than a bare tag: a file with no rotation
	// must serialise exactly as it did before #4233, so exports stay byte-identical for the
	// overwhelming majority of files and the field does not become noise in every export.
	v := VideoFile{Width: 1920, Height: 1080}

	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if strings.Contains(string(raw), "rotation") {
		t.Errorf("a zero rotation must be omitted, not written: %s", raw)
	}
}

func TestVideoFileJSONRoundTripsRotation(t *testing.T) {
	// The actual property that matters: a value that goes out must come back. A field present in the
	// struct but absent from the round trip is the silent-flattening bug in miniature.
	for _, rotation := range []int{0, 90, -90, 180, 270} {
		original := VideoFile{Width: 1920, Height: 1080, Rotation: rotation}

		raw, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("rotation %d: marshal: %v", rotation, err)
		}

		var restored VideoFile
		if err := json.Unmarshal(raw, &restored); err != nil {
			t.Fatalf("rotation %d: unmarshal: %v", rotation, err)
		}

		if restored.Rotation != rotation {
			t.Errorf("rotation %d round-tripped to %d", rotation, restored.Rotation)
		}
	}
}

// TestVideoFileJSONDecodesLegacyExports is the compatibility direction.
//
// An export written by a version predating #4233 has no "rotation" key at all, and there are years of
// them in the wild. Decoding must yield 0 -- "not rotated" -- rather than failing, because the file
// is otherwise perfectly valid.
func TestVideoFileJSONDecodesLegacyExports(t *testing.T) {
	legacy := `{"format":"mp4","width":1920,"height":1080,"duration":120.5,"video_codec":"h264"}`

	var v VideoFile
	if err := json.Unmarshal([]byte(legacy), &v); err != nil {
		t.Fatalf("a pre-#4233 export must still decode: %v", err)
	}

	if v.Rotation != 0 {
		t.Errorf("a legacy export should decode to rotation 0, got %d", v.Rotation)
	}
	if v.Width != 1920 || v.Height != 1080 {
		t.Errorf("the rest of the legacy export must be unaffected, got %dx%d", v.Width, v.Height)
	}
}
