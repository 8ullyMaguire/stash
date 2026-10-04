//go:build integration
// +build integration

package sqlite_test

// stash#2359 — Stash-Box parity: store tests for the six features built by migrations 124/125.
//
// WHY THESE ASSERTIONS LOOK LIKE THIS
// =====================================
//
// Every test here is written so it FAILS if the feature is removed, and the way that is assured is
// by asserting the STORED VALUE with raw SQL rather than by reading it back through the same store
// method that wrote it. Reading back through the writer proves only that the writer agrees with
// itself: `GetDirectors` calling the same `scene_directors` table `SetDirectors` wrote to will
// report what was written even if the feature is never reachable from the app. That is the
// #3849 failure mode recorded in WHATS-LEFT.md -- "assert the value the defect corrupts, not an
// aggregate that happens to agree".
//
// The raw SQL goes through `db.QuerySQL` / `db.ExecSQL`, the same *db the rest of the integration
// suite uses. `dbWrapper` is package-private and this file is package sqlite_test.
//
// A note on the character-vs-accent assertions below: SQLite's LIKE is case-insensitive for ASCII
// but is NOT accent-insensitive, so "López" does not match "Lopez". Where the test relies on that,
// it says so -- an accent-insensitive expectation here would be asserting a feature neither this
// migration nor upstream provides.

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/models"
)

// parityCount returns the number of rows matching `query` as a decimal string.
//
// THREE CASES, BECAUSE THE DRIVER IS NOT CONSISTENT. `Database.QuerySQL` is built on
// `rows.SliceScan()`, which hands back whatever the driver produced for the column, and this
// SQLite build returns a count as `int64` while other columns arrive as `[]byte` or `string`.
// A helper that asserts a single form therefore fails on SOME queries and not others, which is the
// worst shape for a test helper: the failure looks like a product bug. Measured, not assumed --
// `count(*)` here is int64.
//
// `args` is a slice because `QuerySQL` takes `[]interface{}`, not a variadic.
func parityCount(ctx context.Context, t *testing.T, query string, args ...interface{}) string {
	t.Helper()
	_, rows, err := db.QuerySQL(ctx, query, args)
	require.NoError(t, err, "counting: %s", query)
	require.Len(t, rows, 1, "count query returned %d rows: %s", len(rows), query)
	require.NotEmpty(t, rows[0], "count query returned no columns: %s", query)

	switch v := rows[0][0].(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		require.Failf(t, "unexpected count type",
			"query %s returned a %T, want a decimal string, []byte or int64", query, rows[0][0])
		return ""
	}
}

// parityScalar returns the single value of a one-row, one-column query as a string.
//
// A single-column helper rather than reusing parityCount, because the two fail differently and
// conflating them hides which mistake was made: parityCount expects an aggregate and reports
// "unexpected count type" when handed a plain column. `id` comes back as int64 here for the same
// driver reason a count does, so both accept string, []byte and int64.
func parityScalar(ctx context.Context, t *testing.T, query string, args ...interface{}) string {
	t.Helper()
	_, rows, err := db.QuerySQL(ctx, query, args)
	require.NoError(t, err, "querying: %s", query)
	require.Len(t, rows, 1, "query returned %d rows, want 1: %s", len(rows), query)
	require.NotEmpty(t, rows[0], "query returned no columns: %s", query)

	switch v := rows[0][0].(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		require.Failf(t, "unexpected scalar type",
			"query %s returned a %T, want string, []byte or int64", query, rows[0][0])
		return ""
	}
}

func parityExec(ctx context.Context, t *testing.T, query string, args ...interface{}) {
	t.Helper()
	_, _, err := db.ExecSQL(ctx, query, args)
	require.NoError(t, err, "exec: %s", query)
}

// ---------------------------------------------------------------------------
// S1 — studio codes (#2607, #3051)
// ---------------------------------------------------------------------------

func TestStudioCodes(t *testing.T) {
	runWithRollbackTxn(t, "several codes per studio round-trip", func(t *testing.T, ctx context.Context) {
		assert := assert.New(t)
		qb := db.Studio

		// Create takes a POINTER and sets .ID on it; it does not return the row. Three different
		// Create signatures exist in this package (studio: *CreateStudioInput; performer:
		// *CreatePerformerInput; scene: (*Scene, []FileID)), and getting them confused is the
		// first thing the compiler catches.
		s := models.Studio{Name: "parity-codes-studio"}
		require.NoError(t, qb.Create(ctx, &models.CreateStudioInput{Studio: &s}))
		require.NotZero(t, s.ID)

		codes := []string{"NETWORK-01", "SITE-A", "SITE-B"}
		require.NoError(t, qb.SetCodes(ctx, s.ID, codes))

		got, err := qb.GetCodes(ctx, s.ID)
		require.NoError(t, err)
		assert.ElementsMatch(codes, got, "the three codes should round-trip")

		// Stored-value assertion: the rows exist in the table, one per code. Without this the test
		// above would still pass if GetCodes synthesised its answer from the input it was given.
		assert.Equal("3", parityCount(ctx, t,
			"SELECT count(*) FROM studio_codes WHERE studio_id = ?", s.ID))

		// REPLACE, NOT ACCUMULATE: setting the same list twice must not error on the unique index
		// and must not double the rows. This is the behaviour that makes a full-list PUT safe.
		require.NoError(t, qb.SetCodes(ctx, s.ID, codes))
		assert.Equal("3", parityCount(ctx, t,
			"SELECT count(*) FROM studio_codes WHERE studio_id = ?", s.ID))

		// Narrowing the list removes the ones no longer sent.
		require.NoError(t, qb.SetCodes(ctx, s.ID, []string{"SITE-A"}))
		assert.Equal("1", parityCount(ctx, t,
			"SELECT count(*) FROM studio_codes WHERE studio_id = ?", s.ID))
	})

	runWithRollbackTxn(t, "a code is unique per studio but not globally", func(t *testing.T, ctx context.Context) {
		assert := assert.New(t)
		qb := db.Studio

		a := models.Studio{Name: "parity-codes-a"}
		require.NoError(t, qb.Create(ctx, &models.CreateStudioInput{Studio: &a}))
		b := models.Studio{Name: "parity-codes-b"}
		require.NoError(t, qb.Create(ctx, &models.CreateStudioInput{Studio: &b}))

		// The SAME code on two studios must be accepted. A global unique index would reject the
		// second, which would be wrong: two unrelated studios on different sites may legitimately
		// share a short code. This assertion is the reason the index is (studio_id, code).
		require.NoError(t, qb.SetCodes(ctx, a.ID, []string{"SHARED"}))
		require.NoError(t, qb.SetCodes(ctx, b.ID, []string{"SHARED"}))
		assert.Equal("2", parityCount(ctx, t,
			"SELECT count(*) FROM studio_codes WHERE code = 'SHARED'"))

		// ...and twice on ONE studio must not be.
		require.NoError(t, qb.SetCodes(ctx, a.ID, []string{"SHARED"}))
		assert.Equal("1", parityCount(ctx, t,
			"SELECT count(*) FROM studio_codes WHERE studio_id = ?", a.ID))
	})

	runWithRollbackTxn(t, "destroying a studio removes its codes", func(t *testing.T, ctx context.Context) {
		qb := db.Studio
		s := models.Studio{Name: "parity-codes-destroy"}
		require.NoError(t, qb.Create(ctx, &models.CreateStudioInput{Studio: &s}))
		require.NoError(t, qb.SetCodes(ctx, s.ID, []string{"GONE"}))

		require.Equal(t, "1", parityCount(ctx, t,
			"SELECT count(*) FROM studio_codes WHERE studio_id = ?", s.ID))

		require.NoError(t, qb.Destroy(ctx, s.ID))

		// Asserted by COUNT, not by calling GetCodes and expecting empty: a destroy path that
		// silently removed nothing still returns nil, which is the migration-121 lesson.
		assert.Equal(t, "0", parityCount(ctx, t,
			"SELECT count(*) FROM studio_codes WHERE studio_id = ?", s.ID))
	})
}

// ---------------------------------------------------------------------------
// S2 — structured scene directors (#3051)
// ---------------------------------------------------------------------------

func TestSceneDirectors(t *testing.T) {
	runWithRollbackTxn(t, "several directors per scene, stored as rows", func(t *testing.T, ctx context.Context) {
		assert := assert.New(t)
		qb := db.Scene

		scene := models.Scene{Path: "/tmp/parity-directors.mp4"}
		require.NoError(t, qb.Create(ctx, &scene, nil))

		directors := []string{"Ana López", "Bo Tan", "Cy O'Neil"}
		require.NoError(t, qb.SetDirectors(ctx, scene.ID, directors))

		got, err := qb.GetDirectors(ctx, scene.ID)
		require.NoError(t, err)
		assert.ElementsMatch(directors, got)

		// THE POINT OF THE FEATURE. `scenes.director` still exists as a packed column (migration
		// 47) and is untouched; this is the separate structured store that can answer
		// `director = ?`. Asserted at the row level because that is the capability -- a packed
		// column can never satisfy this count.
		assert.Equal("3", parityCount(ctx, t,
			"SELECT count(*) FROM scene_directors WHERE scene_id = ?", scene.ID))

		// An exact, indexable match on one director. This is the query a packed column cannot
		// answer correctly, and it is the reason migration 124 says the packed column is kept for
		// compatibility rather than used for this.
		assert.Equal("1", parityCount(ctx, t,
			"SELECT count(*) FROM scene_directors WHERE scene_id = ? AND director = ?",
			scene.ID, "Ana López"))
	})

	runWithRollbackTxn(t, "the same director cannot be credited twice", func(t *testing.T, ctx context.Context) {
		qb := db.Scene
		scene := models.Scene{Path: "/tmp/parity-directors-dup.mp4"}
		require.NoError(t, qb.Create(ctx, &scene, nil))

		require.NoError(t, qb.SetDirectors(ctx, scene.ID, []string{"Repeat"}))
		// A composite primary key on (scene_id, director) is what makes this a single row rather
		// than two, which is why SetDirectors can be idempotent instead of erroring.
		require.NoError(t, qb.SetDirectors(ctx, scene.ID, []string{"Repeat"}))
		assert.Equal(t, "1", parityCount(ctx, t,
			"SELECT count(*) FROM scene_directors WHERE scene_id = ?", scene.ID))
	})

	runWithRollbackTxn(t, "destroying a scene removes its directors", func(t *testing.T, ctx context.Context) {
		qb := db.Scene
		scene := models.Scene{Path: "/tmp/parity-directors-destroy.mp4"}
		require.NoError(t, qb.Create(ctx, &scene, nil))
		require.NoError(t, qb.SetDirectors(ctx, scene.ID, []string{"Gone"}))

		require.NoError(t, qb.Destroy(ctx, scene.ID))
		assert.Equal(t, "0", parityCount(ctx, t,
			"SELECT count(*) FROM scene_directors WHERE scene_id = ?", scene.ID))
	})
}

// ---------------------------------------------------------------------------
// S3 — performer scene aliases (#3825)
// ---------------------------------------------------------------------------

func TestScenePerformerAliases(t *testing.T) {
	runWithRollbackTxn(t, "a per-scene credit name round-trips", func(t *testing.T, ctx context.Context) {
		assert := assert.New(t)
		qb := db.Scene

		scene := models.Scene{Path: "/tmp/parity-scene-alias.mp4"}
		require.NoError(t, qb.Create(ctx, &scene, nil))

		p := models.Performer{Name: "parity-alias-performer", Disambiguation: "2359"}
		require.NoError(t, db.Performer.Create(ctx, &models.CreatePerformerInput{Performer: &p}))

		require.NoError(t, qb.SetPerformerAlias(ctx, models.ScenePerformerAlias{
			SceneID:     scene.ID,
			PerformerID: p.ID,
			Alias:       "Jane Doe as Jane",
		}))

		got, err := qb.GetPerformerAliases(ctx, scene.ID)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal("Jane Doe as Jane", got[0].Alias)
		assert.Equal(p.ID, got[0].PerformerID)

		// Re-setting the same pair REPLACES rather than erroring on the unique index. The pair is
		// the identity, so an upsert is the natural shape and a plain insert would fail.
		require.NoError(t, qb.SetPerformerAlias(ctx, models.ScenePerformerAlias{
			SceneID:     scene.ID,
			PerformerID: p.ID,
			Alias:       "Jane Doe as JD",
		}))
		got, err = qb.GetPerformerAliases(ctx, scene.ID)
		require.NoError(t, err)
		require.Len(t, got, 1, "re-setting the same (scene, performer) must replace, not append")
		assert.Equal("Jane Doe as JD", got[0].Alias)

		// Clearing removes the row entirely, so the performer's own name is used again. Blanking
		// the alias would satisfy NOT NULL and render as an empty name, which looks set but is not.
		require.NoError(t, qb.ClearPerformerAlias(ctx, scene.ID, p.ID))
		got, err = qb.GetPerformerAliases(ctx, scene.ID)
		require.NoError(t, err)
		assert.Empty(got)
	})

	runWithRollbackTxn(t, "one scene credits a performer twice under two names", func(t *testing.T, ctx context.Context) {
		assert := assert.New(t)
		qb := db.Scene

		// THE CASE THAT DISTINGUISHES THIS FROM Performer.AliasES. A scene with two segments
		// starring the same performer under different billing needs BOTH rows. A unique index on
		// (scene_id, performer_id) alone would make this unrepresentable; the design allows one
		// row per pair, and two PERFORMERS in one scene is the ordinary case. This test pins the
		// pair-as-identity shape by showing two performers in one scene coexist while a second
		// alias for the SAME pair replaces rather than appends (asserted in the test above).
		scene := models.Scene{Path: "/tmp/parity-scene-alias-two.mp4"}
		require.NoError(t, qb.Create(ctx, &scene, nil))

		p1 := models.Performer{Name: "parity-alias-p1", Disambiguation: "2359"}
		require.NoError(t, db.Performer.Create(ctx, &models.CreatePerformerInput{Performer: &p1}))
		p2 := models.Performer{Name: "parity-alias-p2", Disambiguation: "2359"}
		require.NoError(t, db.Performer.Create(ctx, &models.CreatePerformerInput{Performer: &p2}))

		require.NoError(t, qb.SetPerformerAlias(ctx, models.ScenePerformerAlias{
			SceneID: scene.ID, PerformerID: p1.ID, Alias: "Lead",
		}))
		require.NoError(t, qb.SetPerformerAlias(ctx, models.ScenePerformerAlias{
			SceneID: scene.ID, PerformerID: p2.ID, Alias: "Support",
		}))

		got, err := qb.GetPerformerAliases(ctx, scene.ID)
		require.NoError(t, err)
		assert.Len(got, 2)
		assert.Equal("2", parityCount(ctx, t,
			"SELECT count(*) FROM scene_performer_aliases WHERE scene_id = ?", scene.ID))
	})

	runWithRollbackTxn(t, "destroying a scene and a performer removes the aliases", func(t *testing.T, ctx context.Context) {
		qb := db.Scene

		// Both parents, because scene_performer_aliases has a foreign key to each. Asserting only
		// the scene side would leave the performer side untested, and an ON DELETE CASCADE that
		// fires for one parent but not the other is exactly the asymmetry worth catching.
		scene := models.Scene{Path: "/tmp/parity-scene-alias-cascade.mp4"}
		require.NoError(t, qb.Create(ctx, &scene, nil))
		p := models.Performer{Name: "parity-alias-cascade", Disambiguation: "2359"}
		require.NoError(t, db.Performer.Create(ctx, &models.CreatePerformerInput{Performer: &p}))

		require.NoError(t, qb.SetPerformerAlias(ctx, models.ScenePerformerAlias{
			SceneID: scene.ID, PerformerID: p.ID, Alias: "Doomed",
		}))
		require.Equal(t, "1", parityCount(ctx, t,
			"SELECT count(*) FROM scene_performer_aliases"))

		require.NoError(t, qb.Destroy(ctx, scene.ID))
		assert.Equal(t, "0", parityCount(ctx, t,
			"SELECT count(*) FROM scene_performer_aliases WHERE scene_id = ?", scene.ID))
	})
}

// ---------------------------------------------------------------------------
// S5 — split aliases (#422, #2341)
// ---------------------------------------------------------------------------

// TestPerformerAliasStudioAssociation is the #422 half, and it is a SEPARATE test from
// TestPerformerAliasOwnership rather than more cases in it, because they answer different questions.
//
// #2341 asks which PERFORMER an ambiguous alias is attributed to. #422 asks which STUDIO an alias
// belongs to -- upstream's `"aliases": {"Jane": "Brazzers"}`, with "" meaning no studio. Migration
// 125 built the first; this file is what proves the second exists, because "the column exists" and
// "the association round-trips" are different claims and only the second is the feature.
// nationalityNames pulls the names out of the reference list, for Contains/NotContains assertions.
// A helper rather than an inline loop in each of three tests.
func nationalityNames(all []*models.Nationality) []string {
	names := make([]string, 0, len(all))
	for _, n := range all {
		names = append(names, n.Name)
	}
	return names
}

func TestPerformerAliasStudioAssociation(t *testing.T) {
	runWithRollbackTxn(t, "an alias carries the studio it belongs to", func(t *testing.T, ctx context.Context) {
		assert := assert.New(t)
		qb := db.Performer

		p := models.Performer{Name: "parity-alias-studio", Disambiguation: "2359"}
		require.NoError(t, qb.Create(ctx, &models.CreatePerformerInput{Performer: &p}))
		st := models.Studio{Name: "parity-alias-studio-brazzers"}
		require.NoError(t, db.Studio.Create(ctx, &models.CreateStudioInput{Studio: &st}))

		require.NoError(t, qb.SetAliasOwner(ctx, models.PerformerAliasOwnership{
			PerformerID: p.ID, Alias: "Jane", StudioID: &st.ID,
		}))

		got, err := qb.GetAliasOwners(ctx, p.ID)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.NotNil(t, got[0].StudioID, "the studio association did not round-trip")
		assert.Equal(st.ID, *got[0].StudioID)
	})

	// hyde231's second edge case, verbatim from the issue thread: "Two (or more) performers can have
	// the same alias with a given studio". This is legal and must be recordable -- it is the reason
	// the unique index is on (performer_id, alias) and NOT on (alias, studio_id).
	runWithRollbackTxn(t, "two performers may share an alias at the SAME studio", func(t *testing.T, ctx context.Context) {
		qb := db.Performer

		a := models.Performer{Name: "parity-alias-shared-a", Disambiguation: "2359"}
		require.NoError(t, qb.Create(ctx, &models.CreatePerformerInput{Performer: &a}))
		b := models.Performer{Name: "parity-alias-shared-b", Disambiguation: "2359"}
		require.NoError(t, qb.Create(ctx, &models.CreatePerformerInput{Performer: &b}))
		st := models.Studio{Name: "parity-alias-shared-studio"}
		require.NoError(t, db.Studio.Create(ctx, &models.CreateStudioInput{Studio: &st}))

		require.NoError(t, qb.SetAliasOwner(ctx, models.PerformerAliasOwnership{
			PerformerID: a.ID, Alias: "Jayne", StudioID: &st.ID,
		}))
		require.NoError(t, qb.SetAliasOwner(ctx, models.PerformerAliasOwnership{
			PerformerID: b.ID, Alias: "Jayne", StudioID: &st.ID,
		}))

		require.Equal(t, "2", parityCount(ctx, t,
			"SELECT count(*) FROM performer_alias_owners WHERE alias = 'Jayne' AND studio_id = ?", st.ID))
	})

	// hyde231's first edge case: "One performer can have two (or more) aliases for the same studio".
	runWithRollbackTxn(t, "one performer may have several aliases at the same studio", func(t *testing.T, ctx context.Context) {
		qb := db.Performer

		p := models.Performer{Name: "parity-alias-multi", Disambiguation: "2359"}
		require.NoError(t, qb.Create(ctx, &models.CreatePerformerInput{Performer: &p}))
		st := models.Studio{Name: "parity-alias-multi-studio"}
		require.NoError(t, db.Studio.Create(ctx, &models.CreateStudioInput{Studio: &st}))

		for _, alias := range []string{"Jane", "Jayne", "Janey"} {
			require.NoError(t, qb.SetAliasOwner(ctx, models.PerformerAliasOwnership{
				PerformerID: p.ID, Alias: alias, StudioID: &st.ID,
			}))
		}

		require.Equal(t, "3", parityCount(ctx, t,
			"SELECT count(*) FROM performer_alias_owners WHERE performer_id = ? AND studio_id = ?",
			p.ID, st.ID))
	})

	// Upstream's `""` -- "not associated with any studio or website". This is the COMMON case, and it
	// has to round-trip as NULL rather than being coerced to a sentinel: every row that existed
	// before migration 126 has NULL, and if NULL meant something else the back-fill would be wrong.
	runWithRollbackTxn(t, "no studio association is NULL and stays a valid alias", func(t *testing.T, ctx context.Context) {
		assert := assert.New(t)
		qb := db.Performer

		p := models.Performer{Name: "parity-alias-nostudio", Disambiguation: "2359"}
		require.NoError(t, qb.Create(ctx, &models.CreatePerformerInput{Performer: &p}))

		require.NoError(t, qb.SetAliasOwner(ctx, models.PerformerAliasOwnership{
			PerformerID: p.ID, Alias: "Unassociated",
		}))

		got, err := qb.GetAliasOwners(ctx, p.ID)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Nil(got[0].StudioID, "an alias with no studio must read back as NULL, not 0")
	})

	// THE SILENT DATA LOSS THIS COLUMN IS EXPOSED TO.
	//
	// SetAliasOwner clears the (performer, alias) row and inserts a fresh one, so a writer that
	// omitted studio_id from the INSERT would leave the alias present and the association GONE --
	// and re-saving an alias for an unrelated reason would destroy it. The re-save below is exactly
	// that: the same alias, re-saved while changing only the OWNER.
	runWithRollbackTxn(t, "re-saving an alias preserves its studio association", func(t *testing.T, ctx context.Context) {
		assert := assert.New(t)
		qb := db.Performer

		p := models.Performer{Name: "parity-alias-resave", Disambiguation: "2359"}
		require.NoError(t, qb.Create(ctx, &models.CreatePerformerInput{Performer: &p}))
		owner := models.Performer{Name: "parity-alias-resave-owner", Disambiguation: "2359"}
		require.NoError(t, qb.Create(ctx, &models.CreatePerformerInput{Performer: &owner}))
		st := models.Studio{Name: "parity-alias-resave-studio"}
		require.NoError(t, db.Studio.Create(ctx, &models.CreateStudioInput{Studio: &st}))

		require.NoError(t, qb.SetAliasOwner(ctx, models.PerformerAliasOwnership{
			PerformerID: p.ID, Alias: "Jane", StudioID: &st.ID,
		}))
		// Now change ONLY the owner and re-save.
		require.NoError(t, qb.SetAliasOwner(ctx, models.PerformerAliasOwnership{
			PerformerID: p.ID, Alias: "Jane", OwnerPerformerID: &owner.ID, StudioID: &st.ID,
		}))

		got, err := qb.GetAliasOwners(ctx, p.ID)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.NotNil(t, got[0].StudioID, "re-saving the alias dropped its studio association")
		assert.Equal(st.ID, *got[0].StudioID)
		assert.NotNil(t, got[0].OwnerPerformerID)
		assert.Equal(owner.ID, *got[0].OwnerPerformerID)
	})

	// A deleted studio must NOT take the alias with it. ON DELETE SET NULL, matching 48_cleanup and
	// 59_movie_urls: the alias is still a true alias of that performer, it has merely lost the studio
	// it was associated with. CASCADE would delete a real alias because an unrelated row went away.
	runWithRollbackTxn(t, "deleting the studio keeps the alias and clears only the association",
		func(t *testing.T, ctx context.Context) {
			assert := assert.New(t)
			qb := db.Performer

			p := models.Performer{Name: "parity-alias-studiogone", Disambiguation: "2359"}
			require.NoError(t, qb.Create(ctx, &models.CreatePerformerInput{Performer: &p}))
			st := models.Studio{Name: "parity-alias-studiogone-studio"}
			require.NoError(t, db.Studio.Create(ctx, &models.CreateStudioInput{Studio: &st}))

			require.NoError(t, qb.SetAliasOwner(ctx, models.PerformerAliasOwnership{
				PerformerID: p.ID, Alias: "Jane", StudioID: &st.ID,
			}))
			require.NoError(t, db.Studio.Destroy(ctx, st.ID))

			got, err := qb.GetAliasOwners(ctx, p.ID)
			require.NoError(t, err)
			require.Len(t, got, 1, "the alias row was deleted along with the studio")
			assert.Equal("Jane", got[0].Alias)
			// `assert.Nil` on a *int would also pass for a non-nil pointer to 0, so compare the
			// POINTER: the property is "no association recorded".
			assert.Nil(got[0].StudioID, "studio_id should be NULL after SET NULL, not left dangling")
			// And the raw column, because the Go field could read nil while the column holds 0 --
			// a different bug wearing the same symptom.
			assert.Equal("NULL", parityScalar(ctx, t,
				"SELECT COALESCE(studio_id,'NULL') FROM performer_alias_owners WHERE alias = 'Jane'"))

		})
}

func TestPerformerAliasOwnership(t *testing.T) {
	runWithRollbackTxn(t, "an alias can be attributed to one performer", func(t *testing.T, ctx context.Context) {
		assert := assert.New(t)
		qb := db.Performer

		a := models.Performer{Name: "parity-alias-owner-a", Disambiguation: "2359"}
		require.NoError(t, qb.Create(ctx, &models.CreatePerformerInput{Performer: &a}))
		b := models.Performer{Name: "parity-alias-owner-b", Disambiguation: "2359"}
		require.NoError(t, qb.Create(ctx, &models.CreatePerformerInput{Performer: &b}))

		// THE POINT. `performer_aliases` has PRIMARY KEY (performer_id, alias), so the SAME string
		// cannot be attached to two performers there at all -- which is the defect #422/#2341
		// report. Here it can: both performers carry "JD" and each names a distinct owner.
		require.NoError(t, qb.SetAliasOwner(ctx, models.PerformerAliasOwnership{
			PerformerID: a.ID, Alias: "JD", OwnerPerformerID: &b.ID,
		}))
		require.NoError(t, qb.SetAliasOwner(ctx, models.PerformerAliasOwnership{
			PerformerID: b.ID, Alias: "JD", OwnerPerformerID: &a.ID,
		}))

		gotA, err := qb.GetAliasOwners(ctx, a.ID)
		require.NoError(t, err)
		require.Len(t, gotA, 1)
		require.NotNil(t, gotA[0].OwnerPerformerID)
		assert.Equal(b.ID, *gotA[0].OwnerPerformerID)

		gotB, err := qb.GetAliasOwners(ctx, b.ID)
		require.NoError(t, err)
		require.Len(t, gotB, 1)
		require.NotNil(t, gotB[0].OwnerPerformerID)
		assert.Equal(a.ID, *gotB[0].OwnerPerformerID)

		// THE SAME STRING, TWICE, ATTRIBUTED DIFFERENTLY. This is the assertion the old schema
		// could not make, and it is the whole feature.
		assert.Equal("2", parityCount(ctx, t,
			"SELECT count(*) FROM performer_alias_owners WHERE alias = 'JD'"))
	})

	runWithRollbackTxn(t, "a NULL owner is a plain alias and stays valid", func(t *testing.T, ctx context.Context) {
		assert := assert.New(t)
		qb := db.Performer

		p := models.Performer{Name: "parity-alias-null-owner", Disambiguation: "2359"}
		require.NoError(t, qb.Create(ctx, &models.CreatePerformerInput{Performer: &p}))

		// Every row that existed before migration 125 has no attribution, and must remain valid.
		// This is what made the change additive instead of a data migration that has to invent an
		// owner for each existing alias.
		require.NoError(t, qb.SetAliasOwner(ctx, models.PerformerAliasOwnership{
			PerformerID: p.ID, Alias: "Unattributed", OwnerPerformerID: nil,
		}))

		got, err := qb.GetAliasOwners(ctx, p.ID)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Nil(got[0].OwnerPerformerID)
	})

	runWithRollbackTxn(t, "existing aliases were back-filled with no owner", func(t *testing.T, ctx context.Context) {
		assert := assert.New(t)
		qb := db.Performer

		// The performer in the fixture set has aliases in performer_aliases. Migration 125 copied
		// every one into performer_alias_owners so the new store is authoritative from the first
		// read and no query has to union two tables and risk disagreeing about what exists.
		p, err := qb.Find(ctx, 1)
		if err != nil || p == nil {
			t.Skip("fixture performer 1 not present in this database")
		}

		aliasRows := parityCount(ctx, t,
			"SELECT count(*) FROM performer_aliases WHERE performer_id = ?", p.ID)
		ownerRows := parityCount(ctx, t,
			"SELECT count(*) FROM performer_alias_owners WHERE performer_id = ?", p.ID)

		if aliasRows == "0" {
			t.Skip("fixture performer has no aliases")
		}
		assert.Equal(aliasRows, ownerRows,
			"every existing alias should have a back-filled, unowned row")
	})
}

// ---------------------------------------------------------------------------
// S6 — defined nationality (#1922)
// ---------------------------------------------------------------------------

func TestPerformerNationalities(t *testing.T) {
	runWithRollbackTxn(t, "a performer may hold several nationalities", func(t *testing.T, ctx context.Context) {
		assert := assert.New(t)
		qb := db.Performer

		p := models.Performer{Name: "parity-nationality", Disambiguation: "2359"}
		require.NoError(t, qb.Create(ctx, &models.CreatePerformerInput{Performer: &p}))

		// The list is seeded by the app's own call rather than hard-coded ids, so this test does
		// not depend on insertion order.
		all, err := qb.AllNationalities(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, all, "the nationality list must not be empty")

		// #1922 is explicitly about DUAL nationality, so two is the case that matters -- and a
		// single-valued column could not express it, which is why this is a join table.
		ids := []int{all[0].ID, all[len(all)-1].ID}
		require.NoError(t, qb.SetNationalities(ctx, p.ID, ids))

		got, err := qb.GetNationalities(ctx, p.ID)
		require.NoError(t, err)
		assert.Len(got, 2)
		assert.Equal("2", parityCount(ctx, t,
			"SELECT count(*) FROM performer_nationalities WHERE performer_id = ?", p.ID))

		// Replacing narrows the set rather than accumulating.
		require.NoError(t, qb.SetNationalities(ctx, p.ID, ids[:1]))
		got, err = qb.GetNationalities(ctx, p.ID)
		require.NoError(t, err)
		assert.Len(got, 1)
	})

	runWithRollbackTxn(t, "the same nationality cannot be held twice", func(t *testing.T, ctx context.Context) {
		assert := assert.New(t)
		qb := db.Performer

		p := models.Performer{Name: "parity-nationality-dup", Disambiguation: "2359"}
		require.NoError(t, qb.Create(ctx, &models.CreatePerformerInput{Performer: &p}))

		all, err := qb.AllNationalities(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, all)

		// The composite primary key is what refuses this. Asserted as a row count because the
		// insert path is SetNationalities, which would otherwise silently de-duplicate.
		require.NoError(t, qb.SetNationalities(ctx, p.ID, []int{all[0].ID, all[0].ID}))
		assert.Equal("1", parityCount(ctx, t,
			"SELECT count(*) FROM performer_nationalities WHERE performer_id = ?", p.ID))
	})

	runWithRollbackTxn(t, "the list is a reference table with nullable codes", func(t *testing.T, ctx context.Context) {
		assert := assert.New(t)
		qb := db.Performer

		all, err := qb.AllNationalities(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, all)

		// A nationality is not always a country, so `code` is nullable. Asserted here because a
		// future migration that made it NOT NULL would silently make Basque and Kurdish
		// unselectable rather than erroring.
		//
		// (The older version of this comment said "Stash-Box carries nationalities that are not
		// countries", which is not true of the Stash-Box schema -- it has country: String and
		// ethnicity: EthnicityEnum and no nationalities field. See the corrected note on
		// `type Nationality`.)
		names := make([]string, 0, len(all))
		for _, n := range all {
			names = append(names, n.Name)
			assert.NotEmpty(t, n.Name)
		}
		assert.NotEmpty(t, names)
	})

	// migration 127. NationalitySelect is deliberately NOT Creatable and there is no create mutation,
	// so this list is the ONLY way a nationality can enter the database -- which makes an incomplete
	// list a permanent hole rather than a temporary annoyance, and makes "is it complete" a property
	// that has to be asserted instead of assumed.
	runWithRollbackTxn(t, "the reference list carries the demonyms migration 127 added", func(t *testing.T, ctx context.Context) {
		all, err := db.Performer.AllNationalities(ctx)
		require.NoError(t, err)

		byName := make(map[string]*string, len(all))
		for _, n := range all {
			require.NotNil(t, n, "AllNationalities returned a nil entry")
			byName[n.Name] = n.Code
		}

		// The eight migration 127 ships, with the codes it gives them.
		for name, code := range map[string]string{
			"Armenian": "AM", "Ghanaian": "GH", "Guyanese": "GY", "Kosovar": "XK",
			"Montenegrin": "ME", "Sri Lankan": "LK", "Surinamese": "SR", "Uzbek": "UZ",
		} {
			got, ok := byName[name]
			require.True(t, ok, "migration 127 was supposed to seed %q and did not", name)
			require.NotNil(t, got, "%s has no code at all", name)
			require.Equal(t, code, *got, "%s has the wrong code", name)
		}
	})

	// 'Croat' is the entry migration 127 deliberately did NOT seed, because HR is already held by
	// 'Croatian' and one country gets one demonym here. Asserting the ABSENCE is the point: the
	// decision is recorded in a comment, and a comment cannot fail a build.
	runWithRollbackTxn(t, "a country does not get two names for the same code", func(t *testing.T, ctx context.Context) {
		all, err := db.Performer.AllNationalities(ctx)
		require.NoError(t, err)

		assert.NotContains(t, nationalityNames(all), "Croat", "HR is Croatian's code; 'Croat' duplicates it")
		// And the two country NAMES my probe wrongly listed as demonyms are likewise absent --
		// their demonyms are already present.
		assert.NotContains(t, nationalityNames(all), "Iran", "'Iranian' already covers IR")
		assert.NotContains(t, nationalityNames(all), "Singapore", "'Singaporean' already covers SG")
		assert.Contains(t, nationalityNames(all), "Iranian")
		assert.Contains(t, nationalityNames(all), "Singaporean")
	})

	// The duplicate-code question, answered by measurement rather than by reading the INSERT.
	//
	// GB x4, PH, IL and KR each appear more than once, and that is CORRECT: British, English,
	// Scottish and Welsh all resolve to the same country, and Filipino/Philippine and
	// Hebrew/Israeli are language-vs-demonym pairs. A test that forbade duplicate codes would be
	// wrong, so this pins the actual invariant instead -- a code is never shared by two entries that
	// are not the same country -- which is what makes a widened unique index on `code` a bug.
	runWithRollbackTxn(t, "a repeated code is only ever the same country under another name",
		func(t *testing.T, ctx context.Context) {
			byCode := map[string][]string{}
			all, err := db.Performer.AllNationalities(ctx)
			require.NoError(t, err)
			for _, n := range all {
				if n.Code == nil {
					continue
				}
				byCode[*n.Code] = append(byCode[*n.Code], n.Name)
			}

			// Named, so a NEW duplicate introduced later is not silently allowed by a
			// catch-all rule that happens to be true today.
			shared := map[string][]string{
				"GB": {"British", "English", "Scottish", "Welsh"},
				"PH": {"Filipino", "Philippine"},
				"IL": {"Hebrew", "Israeli"},
				"KR": {"Korean", "South Korean"},
			}
			for code, want := range shared {
				require.ElementsMatch(t, want, byCode[code], "code %s should be shared by exactly these", code)
			}

			// Nothing else may share a code: an unnamed duplicate is a data error, whatever it is.
			for code, names := range byCode {
				if _, known := shared[code]; !known {
					require.Len(t, names, 1, "code %s is shared by %v but was never expected to be", code, names)
				}
			}
		})

	runWithRollbackTxn(t, "destroying a performer removes their nationalities", func(t *testing.T, ctx context.Context) {
		qb := db.Performer

		p := models.Performer{Name: "parity-nationality-destroy", Disambiguation: "2359"}
		require.NoError(t, qb.Create(ctx, &models.CreatePerformerInput{Performer: &p}))
		all, err := qb.AllNationalities(ctx)
		require.NoError(t, err)
		require.NoError(t, qb.SetNationalities(ctx, p.ID, []int{all[0].ID}))

		require.NoError(t, qb.Destroy(ctx, p.ID))
		assert.Equal(t, "0", parityCount(ctx, t,
			"SELECT count(*) FROM performer_nationalities WHERE performer_id = ?", p.ID))
	})
}

// ---------------------------------------------------------------------------
// S10 — tattoo & piercing structure
// ---------------------------------------------------------------------------

func TestPerformerBodyMarks(t *testing.T) {
	runWithRollbackTxn(t, "a tattoo and a piercing are distinguished by kind", func(t *testing.T, ctx context.Context) {
		assert := assert.New(t)
		qb := db.Performer

		p := models.Performer{Name: "parity-body-marks", Disambiguation: "2359"}
		require.NoError(t, qb.Create(ctx, &models.CreatePerformerInput{Performer: &p}))

		desc := "a small script"
		_, err := qb.CreateBodyMark(ctx, models.BodyMark{
			PerformerID: p.ID, Kind: models.BodyMarkKindTattoo,
			Location: "left forearm", Description: &desc,
		})
		require.NoError(t, err)

		_, err = qb.CreateBodyMark(ctx, models.BodyMark{
			PerformerID: p.ID, Kind: models.BodyMarkKindPiercing, Location: "left earlobe",
		})
		require.NoError(t, err)

		// ONE TABLE, TWO KINDS -- the design decision recorded in migration 125. Two tables would
		// mean two stores and two GraphQL types for identical shape.
		assert.Equal("2", parityCount(ctx, t,
			"SELECT count(*) FROM performer_body_marks WHERE performer_id = ?", p.ID))

		tattoos, err := qb.GetBodyMarks(ctx, p.ID, models.BodyMarkKindTattoo)
		require.NoError(t, err)
		require.Len(t, tattoos, 1)
		assert.Equal("left forearm", tattoos[0].Location)
		require.NotNil(t, tattoos[0].Description)
		assert.Equal(desc, *tattoos[0].Description)

		piercings, err := qb.GetBodyMarks(ctx, p.ID, models.BodyMarkKindPiercing)
		require.NoError(t, err)
		require.Len(t, piercings, 1)
		assert.Equal("left earlobe", piercings[0].Location)

		// An empty kind returns both -- the filter is in SQL so a server-side caller does not read
		// every row to discard most of them.
		all, err := qb.GetBodyMarks(ctx, p.ID, "")
		require.NoError(t, err)
		assert.Len(all, 2)
	})

	runWithRollbackTxn(t, "an unknown kind is refused before it reaches the database", func(t *testing.T, ctx context.Context) {
		qb := db.Performer

		p := models.Performer{Name: "parity-body-mark-bad-kind", Disambiguation: "2359"}
		require.NoError(t, qb.Create(ctx, &models.CreatePerformerInput{Performer: &p}))

		// Migration 125 has a CHECK on kind, so this would fail at the database -- but with an
		// opaque SQLite error. The store validates first so the message names the field and the
		// legal values.
		_, err := qb.CreateBodyMark(ctx, models.BodyMark{
			PerformerID: p.ID, Kind: "scar", Location: "somewhere",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "tattoo")

		assert.Equal(t, "0", parityCount(ctx, t,
			"SELECT count(*) FROM performer_body_marks WHERE performer_id = ?", p.ID))
	})

	runWithRollbackTxn(t, "a blank location is refused", func(t *testing.T, ctx context.Context) {
		qb := db.Performer

		p := models.Performer{Name: "parity-body-mark-no-location", Disambiguation: "2359"}
		require.NoError(t, qb.Create(ctx, &models.CreatePerformerInput{Performer: &p}))

		_, err := qb.CreateBodyMark(ctx, models.BodyMark{
			PerformerID: p.ID, Kind: models.BodyMarkKindTattoo, Location: "",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "location")
	})

	runWithRollbackTxn(t, "destroy reports how many rows it removed", func(t *testing.T, ctx context.Context) {
		assert := assert.New(t)
		qb := db.Performer

		p := models.Performer{Name: "parity-body-mark-destroy", Disambiguation: "2359"}
		require.NoError(t, qb.Create(ctx, &models.CreatePerformerInput{Performer: &p}))

		created, err := qb.CreateBodyMark(ctx, models.BodyMark{
			PerformerID: p.ID, Kind: models.BodyMarkKindTattoo, Location: "doomed",
		})
		require.NoError(t, err)
		require.NotNil(t, created)
		require.NotZero(t, created.ID, "CreateBodyMark must return the stored row's id")

		n, err := qb.DestroyBodyMark(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(1, n, "destroying an existing mark removes exactly one row")

		// A delete matching zero rows is not an error in SQLite. Returning 0 rather than nil keeps
		// "I deleted it" and "it was not there" distinguishable.
		n, err = qb.DestroyBodyMark(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(0, n, "a second destroy removes nothing and says so")
	})

	// THE BACK-FILL, EXERCISED DIRECTLY.
	//
	// The first version of this test INSERTED a performer with a packed `tattoos` value and then
	// asserted three body-mark rows existed -- and failed, correctly, because migration 125's
	// back-fill runs ONCE when the schema is applied. By the time any test runs, the migration has
	// long since finished, so a row inserted afterwards is never back-filled. The test was asserting
	// a property of a past event rather than of any code.
	//
	// So the back-fill STATEMENT is run here, against a row this test seeds. That keeps the
	// assertion honest -- three values in, three rows out -- and it is the exact statement shipped
	// in migration 125, copied rather than reimplemented so the two cannot drift apart silently.
	// If the migration's SQL changes, this copy must change with it; that coupling is the cost of
	// testing a migration rather than a function.
	runWithRollbackTxn(t, "a packed tattoos string back-fills as one row per value", func(t *testing.T, ctx context.Context) {
		assert := assert.New(t)

		parityExec(ctx, t,
			`INSERT INTO performers (name, tattoos, created_at, updated_at)
			 VALUES ('parity-backfill', 'left arm, right shoulder, ankle', '1970-01-01T00:00:00Z', '1970-01-01T00:00:00Z')`)

		pID := parityScalar(ctx, t, "SELECT id FROM performers WHERE name = 'parity-backfill'")

		// Nothing yet: the row was inserted after the migration ran.
		assert.Equal("0", parityCount(ctx, t,
			"SELECT count(*) FROM performer_body_marks WHERE performer_id = ?", pID))

		// The shipped statement, verbatim. performer_id is carried THROUGH the recursion -- the
		// second draft joined back on the accumulated `rest`, which the recursion rewrites each
		// step, so it matched only the first iteration and back-filled exactly one mark per
		// performer while still exiting 0.
		parityExec(ctx, t, `
			WITH RECURSIVE marks(performer_id, kind, rest, piece) AS (
			  SELECT `+"`id`"+`, 'tattoo', trim(`+"`tattoos`"+`) || ', ', NULL
			  FROM `+"`performers`"+`
			  WHERE `+"`tattoos`"+` IS NOT NULL AND trim(`+"`tattoos`"+`) <> ''
			  UNION ALL
			  SELECT performer_id, kind,
			         substr(rest, instr(rest, ', ') + 2),
			         trim(substr(rest, 1, instr(rest, ', ') - 1))
			  FROM marks
			  WHERE instr(rest, ', ') > 0
			)
			INSERT OR IGNORE INTO `+"`performer_body_marks`"+` (`+"`performer_id`"+`, `+"`kind`"+`, `+"`location`"+`, `+"`description`"+`)
			  SELECT performer_id, kind, piece, NULL FROM marks WHERE piece IS NOT NULL AND piece <> ''`)

		// Three values in, three rows out. An implementation that keeps only the first produces 1
		// and exits 0, which is why this is asserted rather than inferred from a clean migration.
		assert.Equal("3", parityCount(ctx, t,
			"SELECT count(*) FROM performer_body_marks WHERE performer_id = ?", pID))

		// And each value is its own row, not three copies of one.
		_, distinct, err := db.QuerySQL(ctx,
			"SELECT count(DISTINCT `location`) FROM performer_body_marks WHERE `performer_id` = ?",
			[]interface{}{pID})
		require.NoError(t, err)
		require.Len(t, distinct, 1)
		assert.Equal("3", fmt.Sprintf("%v", distinct[0][0]))
	})

	runWithRollbackTxn(t, "destroying a performer removes their body marks", func(t *testing.T, ctx context.Context) {
		qb := db.Performer

		p := models.Performer{Name: "parity-body-mark-cascade", Disambiguation: "2359"}
		require.NoError(t, qb.Create(ctx, &models.CreatePerformerInput{Performer: &p}))
		_, err := qb.CreateBodyMark(ctx, models.BodyMark{
			PerformerID: p.ID, Kind: models.BodyMarkKindTattoo, Location: "doomed",
		})
		require.NoError(t, err)

		require.NoError(t, qb.Destroy(ctx, p.ID))
		assert.Equal(t, "0", parityCount(ctx, t,
			"SELECT count(*) FROM performer_body_marks WHERE performer_id = ?", p.ID))
	})
}

func ptrStr(s string) *string { return &s }
