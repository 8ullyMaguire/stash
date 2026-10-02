package models

import (
	"context"
	"time"
)

type SceneMarker struct {
	ID         int      `json:"id"`
	Title      string   `json:"title"`
	Seconds    float64  `json:"seconds"`
	EndSeconds *float64 `json:"end_seconds"`

	// PrimaryTagID is the tag this marker is DISPLAYED AND SORTED BY -- two ORDER BY
	// clauses in pkg/sqlite/scene_marker.go join on `scene_markers.primary_tag_id =
	// tags.id`. It is the same relationship that TagIDs holds in full, stored twice, so
	// the primary tag is expected to be among TagIDs once loaded.
	PrimaryTagID int `json:"primary_tag_id"`

	// TagIDs is the marker's full tag set. #1253: the `scene_markers_tags` join table and
	// SceneMarkerStore.GetTagIDs both already existed; what was missing was this field and
	// the loader below, so a caller could not reach a marker's tags through the model at
	// all -- the capability existed at the store layer and stopped there.
	TagIDs RelatedIDs `json:"tag_ids"`

	SceneID   int       `json:"scene_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// LoadTagIDs loads the marker's tag ids via l. #1253.
//
// The delegation is two lines because `RelatedIDs.load` already does the two things that
// matter and that are easy to get wrong by hand:
//
//   - it CACHES, so a second call returns without re-querying and without appending a
//     second copy of the same ids;
//   - it propagates a loader error and leaves the field UNLOADED. That is the property that
//     prevents silent data loss: a caller that swallowed the error and saw an empty list
//     would conclude the marker has no tags, and clear them.
func (s *SceneMarker) LoadTagIDs(ctx context.Context, l TagIDLoader) error {
	return s.TagIDs.load(func() ([]int, error) {
		return l.GetTagIDs(ctx, s.ID)
	})
}

func NewSceneMarker() SceneMarker {
	currentTime := time.Now()
	return SceneMarker{
		CreatedAt: currentTime,
		UpdatedAt: currentTime,
	}
}

// SceneMarkerPartial represents part of a SceneMarker object.
// It is used to update the database entry.
type SceneMarkerPartial struct {
	Title        OptionalString
	Seconds      OptionalFloat64
	EndSeconds   OptionalFloat64
	PrimaryTagID OptionalInt
	TagIDs       *UpdateIDs
	SceneID      OptionalInt
	CreatedAt    OptionalTime
	UpdatedAt    OptionalTime
}

func NewSceneMarkerPartial() SceneMarkerPartial {
	currentTime := time.Now()
	return SceneMarkerPartial{
		UpdatedAt: NewOptionalTime(currentTime),
	}
}
