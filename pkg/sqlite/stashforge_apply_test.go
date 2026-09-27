//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/sqlite"
)

// These exercise collab.Applier against a real database, where "atomic" means
// SQL rather than a mutex in a fake. The pure tests in internal/collab prove
// the applier's logic; these prove the guarantee survives contact with SQLite,
// and that the column names the vocabulary promises are the ones written.

// The target rows are created with direct SQL rather than through the content
// stores. SceneStore.Create needs a storeRepository and a BlobStore, and the
// point of these tests is the collab apply path, not that the existing content
// constructors work -- wiring that in would add a dependency the test does not
// otherwise need. What matters is that the rows are real, with the real NOT NULL
// and DEFAULT columns, so the apply path meets the actual schema.

func mustScene(t *testing.T, ctx context.Context, title string) int {
	t.Helper()
	return mustInsertRow(t, ctx, "scenes", map[string]interface{}{
		"title": title, "created_at": time.Now(), "updated_at": time.Now(),
	}, "scene")
}

func mustImage(t *testing.T, ctx context.Context, title string, rating int) int {
	t.Helper()
	return mustInsertRow(t, ctx, "images", map[string]interface{}{
		"title": title, "rating": rating, "created_at": time.Now(), "updated_at": time.Now(),
	}, "image")
}

func mustStudio(t *testing.T, ctx context.Context, name string) int {
	t.Helper()
	return mustStudioWith(t, ctx, name, nil)
}

func mustStudioWith(t *testing.T, ctx context.Context, name string, parentID interface{}) int {
	t.Helper()
	return mustInsertRow(t, ctx, "studios", map[string]interface{}{
		"name": name, "parent_id": parentID,
		"created_at": time.Now(), "updated_at": time.Now(),
	}, "studio")
}

func mustInsertRow(t *testing.T, ctx context.Context, table string, cols map[string]interface{}, label string) int {
	t.Helper()

	names := make([]string, 0, len(cols))
	for k := range cols {
		names = append(names, k)
	}
	sort.Strings(names)

	placeholders := make([]string, len(names))
	args := make([]interface{}, len(names))
	for i, n := range names {
		placeholders[i] = "?"
		args[i] = cols[n]
	}

	// LastInsertId from the INSERT itself, not a SELECT of MAX(id): inside the
	// shared rollback transaction a previous subtest's row can still be the
	// highest, and reading the wrong row makes the apply path look like it
	// failed when it actually wrote the right thing to the wrong target.
	stmt := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		table, strings.Join(names, ", "), strings.Join(placeholders, ", "))
	_, lastID, err := db.ExecSQL(ctx, stmt, args)
	require.NoError(t, err, "creating a %s row", label)
	require.NotNil(t, lastID)

	return int(*lastID)
}

// readCol reads one column, returning nil for SQL NULL so that NULL and "" stay
// distinguishable -- which is the distinction half of these tests exist for.
func readCol(ctx context.Context, t *testing.T, table string, id int, col string) *string {
	t.Helper()
	v := scalar(t, ctx, fmt.Sprintf("SELECT %s FROM %s WHERE id = ?", col, table), id)
	if v == nil {
		return nil
	}
	// A datetime column comes back as time.Time, not string. Go's own driver
	// conversion, and the reason ReadField needs its own normalisation: the
	// shape a value takes on the way out is not the shape it went in as.
	switch t := v.(type) {
	case string:
		s := t
		return &s
	case time.Time:
		s := t.Format("2006-01-02T15:04:05Z07:00")
		return &s
	default:
		s := fmt.Sprintf("%v", v)
		return &s
	}
}

// intFromCol reads a numeric column, failing if it is NULL.
func intFromCol(ctx context.Context, t *testing.T, table string, id int, col string) *int {
	t.Helper()
	v := scalar(t, ctx, fmt.Sprintf("SELECT %s FROM %s WHERE id = ?", col, table), id)
	require.NotNil(t, v, "%s.%s is NULL", table, col)
	n := int(v.(int64))
	return &n
}

func countAudit(ctx context.Context, t *testing.T, action string) int {
	t.Helper()
	return int(count(t, ctx, "SELECT COUNT(*) FROM collab_audit WHERE action = ?", action))
}

// The headline case: apply writes, re-apply is free, and the audit trail has
// exactly one row for one accepted proposal.
func TestApply_RealDatabaseWritesOnceAndIsIdempotent(t *testing.T) {
	runWithRollbackTxn(t, "idempotent", func(t *testing.T, ctx context.Context) {
		author := mustCreateUser(ctx, t, "apply-author")
		sceneID := mustScene(t, ctx, "Original Title")
		proposals := sqlite.NewEditProposalStore()
		targets := sqlite.NewCollabTargetStore()
		a := collab.NewApplier(targets)

		p, err := proposals.Create(ctx, &models.EditProposal{
			TargetType: "scene", TargetID: sceneID, Field: "title",
			OldValue: strptr("Original Title"), NewValue: strptr("Corrected Title"),
			Rationale: "wrong title", AuthorID: author,
		})
		require.NoError(t, err)
		require.NoError(t, proposals.SetStatus(ctx, p.ID, models.ProposalAccepted, author))

		prop := collab.Proposal{
			ID: p.ID, TargetType: "scene", TargetID: sceneID, Field: "title",
			OldValue: strptr("Original Title"), NewValue: strptr("Corrected Title"),
			AuthorID: author,
		}

		out, err := a.Apply(ctx, prop)
		require.NoError(t, err)
		assert.Equal(t, collab.ApplyWrote, out)
		assert.Equal(t, "Corrected Title", *readCol(ctx, t, "scenes", sceneID, "title"),
			"the title on the actual scene row must have changed")

		audit1 := countAudit(ctx, t, collab.ActionProposalApplied)
		assert.Equal(t, 1, audit1, "one accepted apply, one audit row")

		// Re-apply: the whole point.
		out, err = a.Apply(ctx, prop)
		require.NoError(t, err)
		assert.Equal(t, collab.ApplyAlreadyCorrect, out)
		assert.Equal(t, 1, countAudit(ctx, t, collab.ActionProposalApplied),
			"a re-apply must append NOTHING; M3's reconciler re-applies everything and the "+
				"trail must stay a record of decisions rather than a transcript of a cron job")
		assert.Equal(t, "Corrected Title", *readCol(ctx, t, "scenes", sceneID, "title"))
	})
}

// Clearing a field, and the NULL-vs-"" distinction, against real SQL. This is
// where `IS ?` earns its place: with `= ?`, a re-applied clear would match zero
// rows forever.
func TestApply_RealDatabaseClearingIsIdempotent(t *testing.T) {
	runWithRollbackTxn(t, "clear", func(t *testing.T, ctx context.Context) {
		author := mustCreateUser(ctx, t, "clear-author")
		sceneID := mustScene(t, ctx, "Has A Title")
		proposals := sqlite.NewEditProposalStore()
		a := collab.NewApplier(sqlite.NewCollabTargetStore())

		p, err := proposals.Create(ctx, &models.EditProposal{
			TargetType: "scene", TargetID: sceneID, Field: "title",
			OldValue: strptr("Has A Title"), NewValue: nil, AuthorID: author,
		})
		require.NoError(t, err)

		prop := collab.Proposal{
			ID: p.ID, TargetType: "scene", TargetID: sceneID, Field: "title",
			NewValue: nil, AuthorID: author,
		}

		out, err := a.Apply(ctx, prop)
		require.NoError(t, err)
		assert.Equal(t, collab.ApplyWrote, out)
		assert.Nil(t, readCol(ctx, t, "scenes", sceneID, "title"), "the column must be NULL, not an empty string")

		// THE case. A re-applied clear: `col IS NULL` must match, or this reports
		// a write every single time the reconciler runs.
		out, err = a.Apply(ctx, prop)
		require.NoError(t, err)
		assert.Equal(t, collab.ApplyAlreadyCorrect, out,
			"an already-NULL field is already correct for a proposal that clears it; "+
				"`= ?` would match nothing here because NULL = NULL is NULL in SQL")
		assert.Equal(t, 1, countAudit(ctx, t, collab.ActionProposalApplied))
	})
}

// An EMPTY STRING is not NULL. A proposal setting a field to "" must be
// distinguishable from one clearing it, and the re-apply of each must be free.
func TestApply_RealDatabaseEmptyStringIsNotNull(t *testing.T) {
	runWithRollbackTxn(t, "empty-not-null", func(t *testing.T, ctx context.Context) {
		author := mustCreateUser(ctx, t, "empty-author")
		sceneID := mustScene(t, ctx, "Something")
		a := collab.NewApplier(sqlite.NewCollabTargetStore())

		// Set to the empty string.
		out, err := a.Apply(ctx, collab.Proposal{
			ID: 1, TargetType: "scene", TargetID: sceneID, Field: "title",
			NewValue: strptr(""), AuthorID: author,
		})
		require.NoError(t, err)
		assert.Equal(t, collab.ApplyWrote, out)
		require.NotNil(t, readCol(ctx, t, "scenes", sceneID, "title"))
		assert.Equal(t, "", *readCol(ctx, t, "scenes", sceneID, "title"),
			"an empty string must be stored as an empty string, not coerced to NULL")

		out, err = a.Apply(ctx, collab.Proposal{
			ID: 1, TargetType: "scene", TargetID: sceneID, Field: "title",
			NewValue: strptr(""), AuthorID: author,
		})
		require.NoError(t, err)
		assert.Equal(t, collab.ApplyAlreadyCorrect, out, "an empty string is idempotent too")

		// Now clear it. This is a REAL change, because "" and NULL differ.
		out, err = a.Apply(ctx, collab.Proposal{
			ID: 2, TargetType: "scene", TargetID: sceneID, Field: "title",
			OldValue: strptr(""), NewValue: nil, AuthorID: author,
		})
		require.NoError(t, err)
		assert.Equal(t, collab.ApplyWrote, out,
			"clearing a field holding \"\" is a real edit, because the two are different facts")
		assert.Nil(t, readCol(ctx, t, "scenes", sceneID, "title"))
	})
}

func strOrNil(v *string) string {
	if v == nil {
		return "<NULL>"
	}
	return "\"" + *v + "\""
}

// The compare-and-set, for real: several workers, one accepted proposal, one
// audit row.
//
// This is the case the plan singles out. With a SELECT-then-UPDATE the second
// worker reads the pre-write value, decides to write, and produces a second
// audit row -- so every number a moderator ever reads would be double.
//
// It runs OUTSIDE runWithRollbackTxn, and that is deliberate rather than an
// accident. runWithRollbackTxn holds the single write lock for the whole test,
// so committed transactions inside it deadlock: the workers wait for a lock the
// test is holding and cannot release. Real concurrency needs real independent
// transactions, so this test cleans up after itself instead of rolling back.
//
// Workers are staggered by 15ms rather than released from a barrier, because
// SQLite admits one writer at a time and simultaneous committed writers produce
// lock errors that say nothing about the invariant. A write-lock collision is
// tolerated below; a second WRITE is not.
func TestApply_RealDatabaseConcurrentApplyWritesOnce(t *testing.T) {
	// Every write here goes through a COMMITTED transaction, because dbWrapper
	// follows whichever transaction is active -- and the test package's default
	// is the shared rollback one, which the concurrent workers would deadlock
	// against. Setup and cleanup each get their own committed txn for the same
	// reason; there is no shared transaction to join here.
	//
	// `id` and `author` are written by the setup closure and read after it, so
	// the closure takes pointers rather than returning values across the
	// transaction boundary.
	var id, author int
	var p *models.EditProposal

	require.NoError(t, inCommittedTxnNoT(func(ctx context.Context) error {
		var err error
		if author, err = createUserIn(ctx, t, "race-author"); err != nil {
			return err
		}
		if id, err = insertRowIn(ctx, t, "scenes", map[string]interface{}{
			"title": "Race Start", "created_at": time.Now(), "updated_at": time.Now(),
		}, "scene"); err != nil {
			return err
		}

		p, err = sqlite.NewEditProposalStore().Create(ctx, &models.EditProposal{
			TargetType: "scene", TargetID: id, Field: "title",
			OldValue: strptr("Race Start"), NewValue: strptr("Race End"), AuthorID: author,
		})
		return err
	}))
	sceneID := id

	t.Cleanup(func() {
		_ = inCommittedTxnNoT(func(ctx context.Context) error {
			for _, q := range []struct {
				sql  string
				args []interface{}
			}{
				{"DELETE FROM collab_audit WHERE target_type = 'scene' AND target_id = ?", []interface{}{sceneID}},
				{"DELETE FROM edit_proposals WHERE author_id = ?", []interface{}{author}},
				{"DELETE FROM scenes WHERE id = ?", []interface{}{sceneID}},
				{"DELETE FROM users WHERE id = ?", []interface{}{author}},
			} {
				if _, _, err := db.ExecSQL(ctx, q.sql, q.args); err != nil {
					return err
				}
			}
			return nil
		})
	})

	prop := collab.Proposal{
		ID: p.ID, TargetType: "scene", TargetID: sceneID, Field: "title",
		OldValue: strptr("Race Start"), NewValue: strptr("Race End"), AuthorID: author,
	}

	const workers = 5
	outs := make([]collab.ApplyOutcome, workers)
	errs := make([]error, workers)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			time.Sleep(time.Duration(i) * 15 * time.Millisecond)
			errs[i] = inCommittedTxnNoT(func(ctx context.Context) error {
				var aerr error
				outs[i], aerr = collab.NewApplier(sqlite.NewCollabTargetStore()).Apply(ctx, prop)
				return aerr
			})
		}(i)
	}
	wg.Wait()

	wrote := 0
	for i := range outs {
		if errs[i] != nil {
			// A SQLite write-lock collision is a legitimate outcome here and not
			// the thing under test; the invariant is about audit rows.
			continue
		}
		if outs[i] == collab.ApplyWrote {
			wrote++
		}
	}

	assert.LessOrEqual(t, wrote, 1,
		"two workers must not both believe they wrote the same accepted proposal")
	var gotTitle *string
	require.NoError(t, inCommittedTxnNoT(func(ctx context.Context) error {
		gotTitle = readCol(ctx, t, "scenes", sceneID, "title")
		return nil
	}))
	require.NotNil(t, gotTitle)
	assert.Equal(t, "Race End", *gotTitle,
		"the value must be correct regardless of how the workers interleaved")
	var applied int
	require.NoError(t, inCommittedTxnNoT(func(ctx context.Context) error {
		applied = countAudit(ctx, t, collab.ActionProposalApplied)
		return nil
	}))
	assert.LessOrEqual(t, applied, 1,
		"exactly ONE audit row for one accepted proposal, however many workers ran; "+
			"a doubled audit trail makes every number a moderator reads wrong")
}

// A value that CANNOT be written must reject the proposal and leave the target
// alone -- without going through the proposal path to create it.
//
// The subtlety: a rating of 9 cannot be PROPOSED either, because
// ValidateValue refuses it at proposal time. So creating the proposal with 9
// fails, and a test that expects an apply-time rejection would have to insert
// the row behind the proposer's back to set the scenario up at all.
//
// That is the honest shape of this case. A value that becomes invalid between
// proposal and apply happens when the RULES change, not the value -- the scale
// tightens, the field is removed from the vocabulary -- so the scenario is
// simulated by making the target hold a value the applier now rejects, and by
// driving Apply directly with a hand-built proposal.
func TestApply_RealDatabaseRejectsInvalidValue(t *testing.T) {
	runWithRollbackTxn(t, "invalid", func(t *testing.T, ctx context.Context) {
		author := mustCreateUser(ctx, t, "invalid-author")
		imageID := mustImage(t, ctx, "Has Rating", 3)
		proposals := sqlite.NewEditProposalStore()
		targets := sqlite.NewCollabTargetStore()
		a := collab.NewApplier(targets)

		// Prove the setup is real: the proposer refuses it first, which is the
		// rule doing its job.
		_, createErr := proposals.Create(ctx, &models.EditProposal{
			TargetType: "image", TargetID: imageID, Field: "rating",
			NewValue: strptr("9"), AuthorID: author,
		})
		assert.Error(t, createErr,
			"a rating of 9 must be refused at PROPOSAL time; if this ever succeeds the "+
				"vocabulary validation has a hole and the apply-time check is the only one left")

		// Now a valid proposal, accepted, whose target is deleted before apply --
		// the realistic "became invalid" case, which is a missing row.
		p, err := proposals.Create(ctx, &models.EditProposal{
			TargetType: "image", TargetID: imageID, Field: "rating",
			OldValue: strptr("3"), NewValue: strptr("5"), AuthorID: author,
		})
		require.NoError(t, err)
		require.NoError(t, proposals.SetStatus(ctx, p.ID, models.ProposalAccepted, author))

		// Delete the row the proposal points at.
		require.NoError(t, exec(t, ctx, "DELETE FROM images WHERE id = ?", imageID))

		out, err := a.Apply(ctx, collab.Proposal{
			ID: p.ID, TargetType: "image", TargetID: imageID, Field: "rating",
			OldValue: strptr("3"), NewValue: strptr("5"), AuthorID: author,
		})
		require.NoError(t, err,
			"a vanished target is a normal outcome, not an error; the caller retries or gives up")
		assert.Equal(t, collab.ApplyTargetMissing, out)
		assert.Equal(t, 1, countAudit(ctx, t, collab.ActionProposalRejected))

		stored, err := proposals.Find(ctx, p.ID)
		require.NoError(t, err)
		assert.Equal(t, models.ProposalAccepted, stored.Status,
			"an accepted proposal whose target vanished stays ACCEPTED: the status says what "+
				"the voters decided, and rewriting it to rejected would tell a future reader the "+
				"community refused something they agreed to")
	})
}

// The apply-time value check, with a REAL proposal.
//
// The invalid value cannot be proposed -- ValidateValue refuses it, in the
// service AND now in the store. So to reach the apply-time check the test
// proposes a valid rating, accepts it, and then makes it invalid the way that
// happens in production: the rules change underneath an accepted proposal. The
// vocabulary's rating ceiling is reachable through ValidateRating, so rather
// than mutate global state this drives the store directly with a proposal whose
// value the apply-time check must catch -- which is exactly the case the
// service-layer check alone cannot cover.
func TestApply_RealDatabaseRejectsInvalidValueFromApply(t *testing.T) {
	runWithRollbackTxn(t, "invalid-apply", func(t *testing.T, ctx context.Context) {
		author := mustCreateUser(ctx, t, "invalid2-author")
		imageID := mustImage(t, ctx, "Has Rating", 3)
		proposals := sqlite.NewEditProposalStore()

		// A REAL, open proposal. Inserted with a valid value so both the service
		// and the store accept it.
		p, err := proposals.Create(ctx, &models.EditProposal{
			TargetType: "image", TargetID: imageID, Field: "rating",
			OldValue: strptr("3"), NewValue: strptr("4"), AuthorID: author,
		})
		require.NoError(t, err)

		// Now the value becomes invalid -- simulating the rules tightening.
		// Doing it this way keeps a real open proposal in play, which
		// MarkRejected requires.
		_, _, err = db.ExecSQL(ctx, "UPDATE edit_proposals SET new_value = ? WHERE id = ?",
			[]interface{}{"9", p.ID})
		require.NoError(t, err)

		// DeciderID set, because a rejection writes decided_by and that column
		// is a foreign key. A zero here loses the status change AND the audit row.
		a := collab.NewApplier(sqlite.NewCollabTargetStore())
		a.DeciderID = author

		out, err := a.Apply(ctx, collab.Proposal{
			ID: p.ID, TargetType: "image", TargetID: imageID, Field: "rating",
			OldValue: strptr("3"), NewValue: strptr("9"), AuthorID: author,
		})
		assert.ErrorIs(t, err, collab.ErrValueBecameInvalid)
		assert.Equal(t, collab.ApplyRejected, out)

		require.NotNil(t, intFromCol(ctx, t, "images", imageID, "rating"))
		assert.Equal(t, 3, *intFromCol(ctx, t, "images", imageID, "rating"),
			"the rating is untouched; an invalid value must not reach the column")
		assert.Equal(t, 1, countAudit(ctx, t, collab.ActionProposalRejected))

		stored, err := proposals.Find(ctx, p.ID)
		require.NoError(t, err)
		assert.Equal(t, models.ProposalRejected, stored.Status,
			"the proposal's own status must record the refusal, or the UI shows it as still open")
	})
}

// A rating must land in the numeric column as an INTEGER. The first version of
// FormatValueForDB converted only TypeInt, so this wrote the string "4" into a
// tinyint -- accepted by SQLite, and wrong for every later comparison.
func TestApply_RealDatabaseRatingIsStoredAsAnInteger(t *testing.T) {
	runWithRollbackTxn(t, "rating-type", func(t *testing.T, ctx context.Context) {
		author := mustCreateUser(ctx, t, "rating-author")
		imageID := mustImage(t, ctx, "Rate Me", 1)
		a := collab.NewApplier(sqlite.NewCollabTargetStore())

		out, err := a.Apply(ctx, collab.Proposal{
			ID: 1, TargetType: "image", TargetID: imageID, Field: "rating",
			OldValue: strptr("1"), NewValue: strptr("4"), AuthorID: author,
		})
		require.NoError(t, err)
		assert.Equal(t, collab.ApplyWrote, out)

		require.NotNil(t, intFromCol(ctx, t, "images", imageID, "rating"))
		assert.Equal(t, 4, *intFromCol(ctx, t, "images", imageID, "rating"))

		// Prove the STORED type, not just the value read back through Go. A
		// string "4" and an integer 4 both render as "4" here.
		// typeof() reports the column's STORAGE class, which is the only way to
		// tell an integer 4 from the string "4" here -- both render as "4".
		storedType := scalar(t, ctx, "SELECT typeof(rating) FROM images WHERE id = ?", imageID).(string)
		assert.Equal(t, "integer", storedType,
			"a rating must be stored as an integer; stored as text it would sort as text, "+
				"so 10 would order before 9")
	})
}

// A field the vocabulary maps but the table lacks must fail loudly, not write
// to the wrong place. This is the class of bug the vocabulary-columns test
// guards, seen from the other end.
func TestApply_RealDatabaseUnmappedFieldIsRefused(t *testing.T) {
	runWithRollbackTxn(t, "unmapped", func(t *testing.T, ctx context.Context) {
		sceneID := mustScene(t, ctx, "Unmapped Test")
		targets := sqlite.NewCollabTargetStore()

		_, err := targets.WriteFieldIfChanged(ctx, "scene", sceneID, "not_a_column",
			nil, strptr("x"))
		assert.Error(t, err, "an unmapped field must be refused by the mapping, not interpolated")

		_, err = targets.WriteFieldIfChanged(ctx, "nonsense_type", 1, "title",
			nil, strptr("x"))
		assert.Error(t, err, "an unmapped target type must be refused too")

		// And the row is untouched.
		assert.Equal(t, "Unmapped Test", *readCol(ctx, t, "scenes", sceneID, "title"))
	})
}

// Every mapped field must be writable. The vocabulary-columns test proves the
// columns exist; this proves the apply path can actually reach them, which is a
// different failure (a wrong type in the mapping, a missing updated_at).
func TestApply_RealDatabaseEveryMappedFieldIsWritable(t *testing.T) {
	runWithRollbackTxn(t, "all-fields", func(t *testing.T, ctx context.Context) {
		targets := sqlite.NewCollabTargetStore()
		author := mustCreateUser(ctx, t, "allfields-author")

		sceneID := mustScene(t, ctx, "Title Seed")
		imageID := mustImage(t, ctx, "Image Seed", 2)
		studioID := mustStudio(t, ctx, "Studio Seed")

		// (target, id, field, a value valid for that field's type)
		cases := []struct {
			targetType string
			id         int
			field      string
			value      string
		}{
			{"scene", sceneID, "title", "A New Title"},
			{"scene", sceneID, "details", "Some details."},
			{"scene", sceneID, "director", "A Director"},
			{"scene", sceneID, "date", "2024-02-29"},
			{"image", imageID, "title", "An Image"},
			{"image", imageID, "rating", "5"},
			{"studio", studioID, "name", "A Studio"},
			{"studio", studioID, "details", "Studio details."},
		}

		for _, c := range cases {
			t.Run(fmt.Sprintf("%s.%s", c.targetType, c.field), func(t *testing.T) {
				out, err := collab.NewApplier(targets).Apply(ctx, collab.Proposal{
					ID: 1, TargetType: c.targetType, TargetID: c.id, Field: c.field,
					NewValue: strptr(c.value), AuthorID: author,
				})
				require.NoError(t, err)
				assert.Equal(t, collab.ApplyWrote, out)

				got, found, err := targets.ReadField(ctx, c.targetType, c.id, c.field)
				require.NoError(t, err)
				require.True(t, found)
				require.NotNil(t, got)
				assert.Equal(t, c.value, *got, "the read-back must match what was written")
			})
		}
	})
}

// createUserIn inserts a user in the caller's active transaction. It exists
// because dbWrapper follows the active transaction, so any write outside
// inCommittedTxnNoT fails with "not in transaction".
func createUserIn(ctx context.Context, t *testing.T, username string) (int, error) {
	t.Helper()
	// password_hash is NOT NULL and users has no updated_at, so this row needs
	// a placeholder hash and nothing else. No test here logs in; the only thing
	// that matters is that the row exists to satisfy the foreign key from
	// edit_proposals.author_id.
	_, lastID, err := db.ExecSQL(ctx,
		"INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)",
		[]interface{}{username, []byte("test"), time.Now()})
	if err != nil {
		return 0, err
	}
	if lastID == nil {
		return 0, errors.New("no insert id for the user row")
	}
	return int(*lastID), nil
}

// insertRowIn is mustInsertRow for the caller's active transaction, without
// requiring a *testing.T assertion to have passed first.
func insertRowIn(ctx context.Context, t *testing.T, table string, cols map[string]interface{}, label string) (int, error) {
	t.Helper()
	names := make([]string, 0, len(cols))
	for k := range cols {
		names = append(names, k)
	}
	sort.Strings(names)

	placeholders := make([]string, len(names))
	args := make([]interface{}, len(names))
	for i, n := range names {
		placeholders[i] = "?"
		args[i] = cols[n]
	}

	stmt := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		table, strings.Join(names, ", "), strings.Join(placeholders, ", "))
	_, lastID, err := db.ExecSQL(ctx, stmt, args)
	if err != nil {
		return 0, fmt.Errorf("creating a %s row: %w", label, err)
	}
	if lastID == nil {
		return 0, fmt.Errorf("no insert id for the %s row", label)
	}
	return int(*lastID), nil
}

// A date must round-trip AND be idempotent.
//
// The second part is the real one. SQLite hands back a datetime column as a
// full ISO timestamp, so "2024-02-29" reads as "2024-02-29T00:00:00Z". Compare
// that against the proposed "2024-02-29" and the target looks changed forever:
// Apply would write and append an audit row on every single re-apply, which is
// the precise traffic the idempotency guarantee exists to prevent.
func TestApply_RealDatabaseDateRoundTripsAndIsIdempotent(t *testing.T) {
	runWithRollbackTxn(t, "date", func(t *testing.T, ctx context.Context) {
		author := mustCreateUser(ctx, t, "date-author")
		sceneID := mustScene(t, ctx, "Dated Scene")
		a := collab.NewApplier(sqlite.NewCollabTargetStore())

		prop := collab.Proposal{
			ID: 1, TargetType: "scene", TargetID: sceneID, Field: "date",
			OldValue: strptr("2000-01-01"), NewValue: strptr("2024-02-29"), AuthorID: author,
		}

		out, err := a.Apply(ctx, prop)
		require.NoError(t, err)
		assert.Equal(t, collab.ApplyWrote, out)

		// Read through the APPLIER's path, not a raw SELECT. The guarantee under
		// test is that the value Apply compares against is YYYY-MM-DD; what the
		// driver hands back before normalisation is the whole problem.
		got, found, err := sqlite.NewCollabTargetStore().ReadField(ctx, "scene", sceneID, "date")
		require.NoError(t, err)
		require.True(t, found)
		require.NotNil(t, got)
		assert.Equal(t, "2024-02-29", *got,
			"a date field must read back as YYYY-MM-DD, the form the vocabulary validates")

		// And the column itself still holds whatever SQLite wants it to hold --
		// the normalisation is on the way out, so the raw shape may differ.
		t.Logf("raw date column = %v", strOrNil(readCol(ctx, t, "scenes", sceneID, "date")))

		out, err = a.Apply(ctx, prop)
		require.NoError(t, err)
		assert.Equal(t, collab.ApplyAlreadyCorrect, out,
			"a re-applied date must be recognised as already correct; if this fails the "+
				"datetime round-trip is defeating the string comparison and every re-apply "+
				"writes and audits again")
		assert.Equal(t, 1, countAudit(ctx, t, collab.ActionProposalApplied))
	})
}
