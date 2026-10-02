//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/models"
)

// These cover the FOUR remaining sites of the five that were rewired to
// sqlite.SceneRangeDurationSQL. They existed because a per-site mutation sweep showed the
// rewiring was UNTESTED: reverting studio.go, tag.go or scene.go's Duration() to
// `video_files.duration` changed nothing any test could see.
//
// That is the failure this suite exists to prevent, and it is worth naming precisely. The
// fragment's own mutation sweep was 5/5 killed and proved the ARITHMETIC is exercised, while
// four of the five call sites were not exercised at all. A correct constant wired into dead
// call sites looks exactly like a finished change from every angle a mutation sweep of the
// constant can see. **Sweeping the constant proves the rule; only calling it proves the wiring.**

// TestTheLibraryDurationAndSceneQueriesSeeTheRange — scene.go's two sites, `Duration()` and
// the per-row `as duration` behind FindScenes.duration.
func TestTheLibraryDurationAndSceneQueriesSeeTheRange(t *testing.T) {
	sfTxn(t, func(ctx context.Context) {
		baseline, err := db.Scene.Duration(ctx)
		require.NoError(t, err)

		// FindScenes' aggregate is over the WHOLE library too, so it needs the same baseline
		// treatment Duration() got. Asserting an absolute here would measure the fixture.
		perPage0 := -1
		baseResult, err := db.Scene.Query(ctx, models.SceneQueryOptions{
			QueryOptions:  models.QueryOptions{FindFilter: &models.FindFilterType{PerPage: &perPage0}},
			TotalDuration: true,
		})
		require.NoError(t, err)
		baseTotal := baseResult.TotalDuration

		// One 1800s file split into 600s + 1200s: the library must grow by 1800, once.
		f := mkRangeVideoFile(t, ctx, "dur-split.mp4", 1800)
		first := newSceneOver(t, ctx, f)
		require.NoError(t, exec(t, ctx,
			"UPDATE scenes_files SET start_time = 0, end_time = 600 WHERE scene_id = ?", first))
		second := newSceneOver(t, ctx, f)
		require.NoError(t, exec(t, ctx,
			"UPDATE scenes_files SET start_time = 600, end_time = 1800 WHERE scene_id = ?", second))

		after, err := db.Scene.Duration(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1800.0, after-baseline,
			"a 1800s file split 600+1200 must add 1800s to the library total, not 3600s")

		// And the per-row column that FindScenes sums.
		perPage := -1 // all results
		result, err := db.Scene.Query(ctx, models.SceneQueryOptions{
			QueryOptions: models.QueryOptions{
				FindFilter: &models.FindFilterType{PerPage: &perPage},
			},
			TotalDuration: true,
		})
		require.NoError(t, err)
		require.NotNil(t, result)

		scenes, err := result.Resolve(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, scenes, "the query returned no scenes, so the assertions below "+
			"would hold vacuously")

		// models.Scene has NO Duration field -- the compiler refuses `s.Duration`, which is
		// the same fact spec §8's recount established by reading the GraphQL schema. So the
		// per-row `as duration` column exists ONLY to be summed, and the sole observable is
		// result.TotalDuration. That makes this the only place the column can be tested, and
		// it is why a reverted column shows up here and nowhere else.
		assert.InDelta(t, 1800.0, result.TotalDuration-baseTotal, 0.001,
			"FindScenesResult.duration is documented as the TOTAL, and it must be the sum of "+
				"the ranges rather than of the files: a 1800s file split 600+1200 would "+
				"otherwise report 3600")
	})
}

// ---------------------------------------------------------------------------
// THE THREE `scenes_duration` SORT CLAUSES.
//
// The shape is always the same, so one fixture serves all three: ONE file of 1800s, split into
// two windows -- 600s and 1200s -- each on its own scene. Entity A is attached to the 600s
// scene, entity B to the 1200s one.
//
// Under the bug each side sums the FILE's full 1800s, both tie, and `ORDER BY <tie>` is
// whatever SQLite happens to emit. So "B came first" is not provable from a single run -- the
// assertion has to be that the ORDER CHANGES when the ranges are, which is why each test
// sorts twice: once as split, once with the ranges removed.
//
// **Why a DIFFERENTIAL assertion, and not "B comes first".** The first draft of these three
// tests asserted only that the 1200s entity sorted before the 600s one. Reverting studio.go
// and tag.go to `video_files.duration` did NOT fail them -- both sides tied on 1800, and the
// tie-break happened to agree with the expected order. So the assertion was satisfied by the
// fallback, not by the sort, and a name that correlated with the answer would have done the
// same. The test now sorts TWICE with the attachments swapped and requires the ORDER TO CHANGE.
// A tie cannot change its own order, so only the range can produce the flip.
// ---------------------------------------------------------------------------

// splitFileScenes creates one 1800s file split into a 600s scene and a 1200s scene, and
// returns both scene ids in that order.
func splitFileScenes(t *testing.T, ctx context.Context, name string) (shortScene, longScene int) {
	t.Helper()
	f := mkRangeVideoFile(t, ctx, name, 1800)

	shortScene = newSceneOver(t, ctx, f)
	require.NoError(t, exec(t, ctx,
		"UPDATE scenes_files SET start_time = 0, end_time = 600 WHERE scene_id = ?", shortScene))
	longScene = newSceneOver(t, ctx, f)
	require.NoError(t, exec(t, ctx,
		"UPDATE scenes_files SET start_time = 600, end_time = 1800 WHERE scene_id = ?", longScene))
	return shortScene, longScene
}

// sortByScenesDurationDesc returns the ids of the given entities, ordered by their scenes'
// total duration, descending -- the direction that puts the 1200s one first when the ranges
// are honoured.
func sortedIDsDesc(t *testing.T, ctx context.Context, query func() ([]int, error)) []int {
	t.Helper()
	ids, err := query()
	require.NoError(t, err)
	return ids
}

// sortCase is one store's scenes_duration sort: how to create an entity, how to attach a
// scene to it, and how to read the sorted order.
type sortCase struct {
	label  string
	create func(ctx context.Context, name string) int
	attach func(ctx context.Context, entityID, sceneID int) error
	sorted func(ctx context.Context, sort string, dir models.SortDirectionEnum, perPage int) []int
}

func TestSortingByScenesDurationUsesTheRange(t *testing.T) {
	sort := "scenes_duration"
	desc := models.SortDirectionEnumDesc
	all := -1

	cases := []sortCase{
		{
			label: "performers",
			create: func(ctx context.Context, name string) int {
				p := &models.Performer{Name: name}
				require.NoError(t, db.Performer.Create(ctx, &models.CreatePerformerInput{Performer: p}))
				return p.ID
			},
			attach: func(ctx context.Context, entityID, sceneID int) error {
				return exec(t, ctx,
					"INSERT INTO performers_scenes (performer_id, scene_id) VALUES (?, ?)",
					entityID, sceneID)
			},
			sorted: func(ctx context.Context, s string, d models.SortDirectionEnum, pp int) []int {
				got, _, err := db.Performer.Query(ctx, nil, &models.FindFilterType{
					Sort: &s, Direction: &d, PerPage: &pp,
				})
				require.NoError(t, err)
				out := make([]int, 0, len(got))
				for _, p := range got {
					out = append(out, p.ID)
				}
				return out
			},
		},
		{
			label: "tags",
			create: func(ctx context.Context, name string) int {
				tg := &models.Tag{Name: name}
				require.NoError(t, db.Tag.Create(ctx, &models.CreateTagInput{Tag: tg}))
				return tg.ID
			},
			attach: func(ctx context.Context, entityID, sceneID int) error {
				return exec(t, ctx,
					"INSERT INTO scenes_tags (scene_id, tag_id) VALUES (?, ?)", sceneID, entityID)
			},
			sorted: func(ctx context.Context, s string, d models.SortDirectionEnum, pp int) []int {
				got, _, err := db.Tag.Query(ctx, nil, &models.FindFilterType{
					Sort: &s, Direction: &d, PerPage: &pp,
				})
				require.NoError(t, err)
				out := make([]int, 0, len(got))
				for _, tg := range got {
					out = append(out, tg.ID)
				}
				return out
			},
		},
		{
			label: "studios",
			create: func(ctx context.Context, name string) int {
				st := &models.Studio{Name: name}
				require.NoError(t, db.Studio.Create(ctx, &models.CreateStudioInput{Studio: st}))
				return st.ID
			},
			// studio.go's clause joins `scenes.studio_id = studios.id`, so the link is a
			// COLUMN on scenes, not a join table (measured from the clause itself: there is
			// no studios_scenes table, and the first draft of this test tried one).
			attach: func(ctx context.Context, entityID, sceneID int) error {
				return exec(t, ctx, "UPDATE scenes SET studio_id = ? WHERE id = ?", entityID, sceneID)
			},
			sorted: func(ctx context.Context, s string, d models.SortDirectionEnum, pp int) []int {
				got, _, err := db.Studio.Query(ctx, nil, &models.FindFilterType{
					Sort: &s, Direction: &d, PerPage: &pp,
				})
				require.NoError(t, err)
				out := make([]int, 0, len(got))
				for _, st := range got {
					out = append(out, st.ID)
				}
				return out
			},
		},
	}

	for _, c := range cases {
		c := c
		// One transaction PER CASE. Sharing one across the three left the earlier cases'
		// entities in the library while the later ones sorted, and they landed in the same
		// index band -- which is how this test passed once and failed the next run. A sort
		// assertion needs a library containing only what that case put there.
		sfTxn(t, func(ctx context.Context) {
			shortScene, longScene := splitFileScenes(t, ctx, "sort-"+c.label+".mp4")

			// Names contradict the expected order on purpose: every one of these three
			// sorts appends a trailing COALESCE(sort_name, name, id) tiebreak (measured at
			// tag.go:894), so the short-window entity must be the one that sorts first BY
			// NAME for a tie to be visibly wrong.
			aID := c.create(ctx, "aaa-"+c.label)
			bID := c.create(ctx, "zzz-"+c.label)
			require.NoError(t, c.attach(ctx, aID, shortScene))
			require.NoError(t, c.attach(ctx, bID, longScene))

			first := relativePositions(c.sorted(ctx, sort, desc, all), aID, bID)
			require.True(t, first.found, "%s: neither entity appeared in the sorted result", c.label)

			// Swap ONLY the windows, on the SAME entities in the SAME library, so the two
			// runs are directly comparable.
			require.NoError(t, exec(t, ctx,
				"UPDATE scenes_files SET start_time = 600, end_time = 1800 WHERE scene_id = ?",
				shortScene))
			require.NoError(t, exec(t, ctx,
				"UPDATE scenes_files SET start_time = 0, end_time = 600 WHERE scene_id = ?",
				longScene))

			second := relativePositions(c.sorted(ctx, sort, desc, all), aID, bID)
			require.True(t, second.found, "%s: neither entity appeared after the swap", c.label)

			assert.NotEqual(t, first.whoFirst, second.whoFirst,
				"swapping which window each %s is on must REVERSE the scenes_duration order; "+
					"an unchanged order means the sort key is not the range (both sides tied on "+
					"the file's full length), so this clause is not using "+
					"SceneRangeDurationSQL. The names contradict the order, so the trailing "+
					"name tiebreak cannot explain it.", c.label)
		})
	}
}

// positions pairs the index of aID and bID within a sorted id list.
type positions struct {
	a, b      int
	found     bool
	whoFirst  string
	equalSort bool
}

// relativePositions locates two ids in a sorted result and reduces them to an ORDER, dropping
// the absolute indices -- so two runs over the same library compare even though other
// entities share the page. A tie is the bug's signature, and `equalSort` records one.
func relativePositions(sorted []int, aID, bID int) positions {
	p := positions{a: -1, b: -1}
	for i, id := range sorted {
		switch id {
		case aID:
			p.a = i
		case bID:
			p.b = i
		}
	}
	if p.a == -1 || p.b == -1 {
		return p
	}
	p.found = true
	switch {
	case p.a < p.b:
		p.whoFirst = "A"
	case p.b < p.a:
		p.whoFirst = "B"
	default:
		p.whoFirst = "same-position"
		p.equalSort = true
	}
	return p
}
