//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/discovery"
	"github.com/stashapp/stash/pkg/sqlite"
)

// R015, R016, R017, R049, R067, R068: migration 119, against a real database.
//
// WHY THIS FILE IS THE STEP. internal/discovery has 23 exported non-test symbols
// and 16 of them had no non-test reference outside the package -- every line of
// feed.go. The PACKAGE was imported, by internal/acquisition, which used five
// other symbols from it. So the audit that found the collab and directory gaps
// stopped at the package boundary while the whole feed sat unreferenced inside it.

// THE BOARDS TABLE HAS NO RANK COLUMN, AND THAT IS THE POINT OF THE SCHEMA.
//
// Ground rule 4 and internal/discovery's TestABoardIsAnOrderingWithNoStoredRank
// both forbid a stored counter here. The positions live in a CHILD table keyed by
// position, and this asserts the parent has not quietly grown one -- because the
// failure mode is silent: someone adds `rank INTEGER` because sorting by it is
// easier, and every test keeps passing while the board has become a ranking.
//
// So it is read from PRAGMA rather than assumed, because "there is no rank column"
// is a claim about a file somebody can edit.
func TestTheBoardsTableHasNoRankColumn(t *testing.T) {
	runWithRollbackTxn(t, "discovery-no-rank-column", func(t *testing.T, ctx context.Context) {
		_, rows, err := db.QuerySQL(ctx, "PRAGMA table_info(discovery_boards)", nil)
		require.NoError(t, err)

		banned := []string{"rank", "score", "weight", "position", "ordinal", "sort_order"}
		var found []string
		for _, row := range rows {
			name, _ := row[1].(string)
			for _, b := range banned {
				if strings.EqualFold(name, b) {
					found = append(found, "discovery_boards."+name)
				}
			}
		}
		assert.Empty(t, found,
			"these columns make a board a STORED COUNTER rather than an editorial "+
				"ordering, which is what ground rule 4 forbids: %v. The positions "+
				"belong in discovery_board_items, where the ordering IS the primary "+
				"key and there is nothing to reconcile", found)

		// AND THE POSITIONS REALLY ARE THE PRIMARY KEY of the child table, so the
		// order cannot drift out of sync with a column that claims to be it.
		_, pkRows, err := db.QuerySQL(ctx, "PRAGMA table_info(discovery_board_items)", nil)
		require.NoError(t, err)
		var pkCols []string
		for _, row := range pkRows {
			if pk, _ := row[5].(int64); pk > 0 {
				pkCols = append(pkCols, fmt.Sprintf("%v", row[1]))
			}
		}
		assert.Equal(t, []string{"board_id", "position"}, pkCols,
			"(board_id, position) is the identity, so a duplicate position is not "+
				"representable and the ordering cannot disagree with itself")
	})
}

// A BOARD PERSISTS IN THE AUTHOR'S ORDER, AND DENSELY.
//
// The dense part is the reason SetBoard takes a LIST rather than (entity, rank)
// pairs: a caller passing pairs could write positions 0, 1, 7, and the hole has no
// meaning a reader could interpret.
func TestABoardPersistsInAuthorOrder(t *testing.T) {
	runWithRollbackTxn(t, "discovery-board-order", func(t *testing.T, ctx context.Context) {
		user := accessUser(ctx, t, "sfDiscoveryAuthor")
		store := sqlite.NewDiscoveryStore(db)

		// Deliberately NOT alphabetical: alphabetical would pass under a sort by
		// entity_id and under the author's order both, so it cannot tell them apart.
		order := []string{"zeta", "alpha", "mu"}
		require.NoError(t, store.SetBoard(ctx, discovery.WriteBoard{
			ID:        "board-order-1",
			Title:     "Deliberately not alphabetical",
			AuthorID:  user,
			EntityIDs: order,
		}))

		got, err := store.BoardFor(ctx, "board-order-1")
		require.NoError(t, err)
		assert.Equal(t, order, got.EntityIDs,
			"the author's order survives the round trip. Reading in entity_id order "+
				"would produce a board whose stored ordering is not what it shows")

		// AND THE POSITIONS ARE 0-BASED AND DENSE, which is what ResolveBoard's
		// documented 0-based numbering needs.
		_, rows, err := db.QuerySQL(ctx,
			"SELECT position FROM discovery_board_items WHERE board_id = ? ORDER BY position",
			[]interface{}{"board-order-1"})
		require.NoError(t, err)
		var positions []int
		for _, row := range rows {
			switch v := row[0].(type) {
			case int64:
				positions = append(positions, int(v))
			case int:
				positions = append(positions, v)
			}
		}
		assert.Equal(t, []int{0, 1, 2}, positions,
			"0-based because ResolveBoard numbers from 0, dense because a gap is an "+
				"ordering with a hole in it")
	})
}

// AN EDIT REPLACES THE ORDERING, AND DOES NOT MERGE.
//
// An author who removes an item means it gone. A merge cannot express that, and a
// merge that keeps the old positions produces a board with a hole.
func TestAnEditReplacesRatherThanMerges(t *testing.T) {
	runWithRollbackTxn(t, "discovery-board-edit", func(t *testing.T, ctx context.Context) {
		user := accessUser(ctx, t, "sfDiscoveryEditor")
		store := sqlite.NewDiscoveryStore(db)

		require.NoError(t, store.SetBoard(ctx, discovery.WriteBoard{
			ID: "board-edit-1", Title: "Before", AuthorID: user,
			EntityIDs: []string{"a", "b", "c", "d"},
		}))
		// Drop one from the middle AND reverse. A merge would leave b, c, d at their
		// old positions; a diff that renumbered only the tail would leave a gap.
		require.NoError(t, store.SetBoard(ctx, discovery.WriteBoard{
			ID: "board-edit-1", Title: "After", AuthorID: user,
			EntityIDs: []string{"d", "c", "a"},
		}))

		got, err := store.BoardFor(ctx, "board-edit-1")
		require.NoError(t, err)
		assert.Equal(t, []string{"d", "c", "a"}, got.EntityIDs,
			"an edit replaces the ordering wholesale")
		assert.Equal(t, "After", got.Title)

		// And the row count is the new length -- nothing was left behind.
		// curationScalar rather than a direct dbWrapper call: dbWrapper is
		// unexported and this is the sqlite_test package, so the count is read the
		// way the other tests in pkg/sqlite read one.
		// Read directly rather than through curationScalar, which requires a string
		// column and fails with "query returned a int64, want a string" on a COUNT --
		// a helper that insists on a shape the query does not have is a helper that
		// pushes the next reader toward CAST(... AS TEXT), which would hide the type.
		_, countRows, err := db.QuerySQL(ctx,
			"SELECT COUNT(*) FROM discovery_board_items WHERE board_id = ?",
			[]interface{}{"board-edit-1"})
		require.NoError(t, err)
		require.Len(t, countRows, 1)
		var count int64
		switch v := countRows[0][0].(type) {
		case int64:
			count = v
		case string:
			count, _ = strconv.ParseInt(v, 10, 64)
		}
		assert.Equal(t, int64(3), count, "no orphaned positions from the previous version")
	})
}

// DUPLICATE POSITIONS AND REPEATED ENTITIES ARE UNREPRESENTABLE.
//
// Both are things a board can be that a reader cannot be shown, and both are real
// defects a reader reports and an author cannot see.
func TestDuplicatePositionsAndRepeatedEntitiesAreRefused(t *testing.T) {
	runWithRollbackTxn(t, "discovery-duplicates", func(t *testing.T, ctx context.Context) {
		user := accessUser(ctx, t, "sfDiscoveryDup")
		store := sqlite.NewDiscoveryStore(db)

		// THE SCHEMA refuses a duplicate POSITION, which SetBoard cannot produce
		// (positions are dense by construction) but a direct query runner can.
		require.NoError(t, store.SetBoard(ctx, discovery.WriteBoard{
			ID: "board-dup-1", Title: "Base", AuthorID: user, EntityIDs: []string{"a", "b"},
		}))
		err := curationExec(ctx, t,
			"INSERT INTO discovery_board_items (board_id, position, entity_id) VALUES ('board-dup-1', 0, 'c')")
		require.Error(t, err,
			"two rows claiming position 0 is a board with a hole and a duplicate, "+
				"which PK (board_id, position) makes unrepresentable")

		// THE SCHEMA refuses the same ENTITY twice, and the STORE refuses it with a
		// named error -- because the author's fix is "drop one of the two" and
		// "constraint violation" does not say which.
		err = store.SetBoard(ctx, discovery.WriteBoard{
			ID: "board-dup-2", Title: "Repeated", AuthorID: user,
			EntityIDs: []string{"a", "b", "a"},
		})
		require.ErrorIs(t, err, discovery.ErrDuplicateEntity,
			"a list showing the same studio at positions 0 and 2 is a bug a reader "+
				"reports and an author cannot see")
		assert.Contains(t, err.Error(), "0", "the message names both positions, "+
			"because the fix is to drop one of them")
	})
}

// THE TWO SCHEMA CONSTRAINTS, FORCED THROUGH RAW SQL.
//
// Both of these were found by the mutation gate reporting SURVIVED, which is the
// interesting kind of finding: `UNIQUE (board_id, entity_id)` dropped, and
// `CHECK (position >= 0)` removed -- two survivors against a suite that was
// entirely green.
//
// The reason is a single blind spot and it is worth stating precisely, because it is
// easy to repeat. THE STORE REFUSES BOTH CASES IN GO. SetBoard rejects a repeated
// entity and it can only ever write a non-negative position, so through the store
// both constraints are invisible -- the store never produces a row they would
// reject. The tests exercised them only by way of SetBoard, and SetBoard is the one
// caller that cannot reach them.
//
// So the constraints were UNTESTED IN THE ONLY WAY THAT CAN REACH THEM: raw SQL,
// which is also the way anybody with a sqlite file reaches them. Two schema rules
// that the application never violates are precisely the two a hand-edited database
// gets wrong first.
func TestThePositionsAndEntityConstraintsHoldAgainstRawSQL(t *testing.T) {
	runWithRollbackTxn(t, "discovery-raw-constraints", func(t *testing.T, ctx context.Context) {
		user := accessUser(ctx, t, "sfDiscoveryRaw")
		store := sqlite.NewDiscoveryStore(db)

		require.NoError(t, store.SetBoard(ctx, discovery.WriteBoard{
			ID: "board-raw-1", Title: "Base", AuthorID: user, EntityIDs: []string{"x", "y"},
		}))

		// A NEGATIVE POSITION. SetBoard cannot produce one -- its positions are
		// slice indices -- so the only way to ask whether the CHECK is there is to
		// write one directly, and a negative position is an ordering that starts
		// before it begins.
		err := curationExec(ctx, t,
			"INSERT INTO discovery_board_items (board_id, position, entity_id) VALUES ('board-raw-1', -1, 'neg')")
		require.Error(t, err,
			"`CHECK (position >= 0)` is the rule that positions are 0-based, matching "+
				"ResolveBoard's numbering. Nothing in Go enforces it for a direct write")

		// THE SAME ENTITY TWICE IN ONE BOARD, at DIFFERENT valid positions, which is
		// the shape the store also refuses. Dropping UNIQUE (board_id, entity_id)
		// would let this in, and the resulting board shows one studio twice at two
		// ranks -- indistinguishable, to a reader, from a bug in the store.
		err = curationExec(ctx, t,
			"INSERT INTO discovery_board_items (board_id, position, entity_id) VALUES ('board-raw-1', 5, 'x')")
		require.Error(t, err,
			"UNIQUE (board_id, entity_id) is what makes a repeated entity "+
				"unrepresentable. The store refuses it in Go, so without this the "+
				"constraint was never exercised and dropping it changed nothing "+
				"observable -- which the mutation gate reported as a survivor")
	})
}

// A MISSING BOARD IS AN ERROR, NOT AN EMPTY BOARD.
//
// "There is no such board" and "the board is empty" are different answers and a
// resolver renders them differently -- one is a 404 and the other is a page.
func TestBoardForIsAbsentRatherThanEmpty(t *testing.T) {
	runWithRollbackTxn(t, "discovery-absent-board", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewDiscoveryStore(db)

		_, err := store.BoardFor(ctx, "no-such-board")
		require.ErrorIs(t, err, discovery.ErrNoBoard)

		// AND a board with NO items is not an error -- it is an empty board, and
		// the two must not be confused in either direction.
		user := accessUser(ctx, t, "sfDiscoveryEmpty")
		require.NoError(t, store.SetBoard(ctx, discovery.WriteBoard{
			ID: "board-empty-1", Title: "Nothing yet", AuthorID: user,
		}))
		got, err := store.BoardFor(ctx, "board-empty-1")
		require.NoError(t, err, "an empty board exists and reads as empty")
		assert.Empty(t, got.EntityIDs)
	})
}

// "MY LISTS", AND THE OTHER DIRECTION.
//
// BoardsByAuthor is the R049 view; BoardsContaining is the query the entity_id
// index exists for, and UNIQUE (board_id, entity_id) cannot serve it.
func TestBoardsByAuthorAndContaining(t *testing.T) {
	runWithRollbackTxn(t, "discovery-board-lists", func(t *testing.T, ctx context.Context) {
		author := accessUser(ctx, t, "sfDiscoveryLister")
		other := accessUser(ctx, t, "sfDiscoveryOther")
		store := sqlite.NewDiscoveryStore(db)

		for _, id := range []string{"board-l2", "board-l1", "board-l3"} {
			require.NoError(t, store.SetBoard(ctx, discovery.WriteBoard{
				ID: id, Title: "List " + id, AuthorID: author, EntityIDs: []string{"shared", id},
			}))
		}
		require.NoError(t, store.SetBoard(ctx, discovery.WriteBoard{
			ID: "board-l4", Title: "Not theirs", AuthorID: other, EntityIDs: []string{"shared"},
		}))

		mine, err := store.BoardsByAuthor(ctx, author)
		require.NoError(t, err)
		var ids []string
		for _, b := range mine {
			ids = append(ids, b.ID)
		}
		assert.Equal(t, []string{"board-l1", "board-l2", "board-l3"}, ids,
			"ORDERED BY board_id, so a list of someone's own boards does not "+
				"reshuffle between renders. Not by updated_at: recency is a product "+
				"decision this method should not make silently")

		containing, err := store.BoardsContaining(ctx, "shared")
		require.NoError(t, err)
		require.Len(t, containing, 4, "all four boards list it, including the other "+
			"author's -- 'which lists is this studio on' is not a question about who "+
			"wrote them")

		none, err := store.BoardsContaining(ctx, "not-on-any-board")
		require.NoError(t, err)
		assert.Empty(t, none, "and an entity on no board is an empty answer, not an error")
	})
}

// THE CALL SITE: THE FEED COMPOSES FROM STORED BOARDS.
//
// THIS TEST FAILS IF THE FEED BECOMES UNREACHABLE AGAIN, and that is its real job.
// Compose, Feed, FeedSection, FeedItem, Surface, KnownSurfaces, ResolveBoard,
// Board, BoardItem and BoardIsCuration were ten symbols with no non-test reference
// while their package was imported by somebody else -- a failure the
// package-level check cannot see.
func TestTheFeedComposesFromStoredBoards(t *testing.T) {
	runWithRollbackTxn(t, "discovery-feed-composes", func(t *testing.T, ctx context.Context) {
		user := accessUser(ctx, t, "sfDiscoveryFeeder")
		store := sqlite.NewDiscoveryStore(db)

		require.NoError(t, store.SetBoard(ctx, discovery.WriteBoard{
			ID: "board-feed-1", Title: "A list", AuthorID: user,
			EntityIDs: []string{"s-first", "s-second", "s-third"},
		}))
		// An EMPTY board must contribute no section: "the author cleared their
		// list" and "this author has a board" are different facts.
		require.NoError(t, store.SetBoard(ctx, discovery.WriteBoard{
			ID: "board-feed-empty", Title: "Cleared", AuthorID: user,
		}))

		boardIDs := []string{"s-first", "s-second", "s-third"}
		boards, err := store.BoardsByAuthor(ctx, user)
		require.NoError(t, err)

		feed, err := store.ComposeFeed(ctx, boards)
		require.NoError(t, err)

		require.Len(t, feed.Sections, 1, "one non-empty board is one section")
		section := feed.Sections[0]
		assert.Equal(t, discovery.SurfaceBoards, section.Surface,
			"and the item's source stays legible to the reader, which is §6a.7's "+
				"whole reason a feed item carries a Surface")
		require.Len(t, section.Items, 3)

		// THE AUTHOR'S ORDER IS CARRIED THROUGH UNCHANGED, as positions.
		var got []string
		for _, it := range section.Items {
			got = append(got, it.ID)
		}
		assert.Equal(t, []string{"s-first", "s-second", "s-third"}, got)
		for i, it := range section.Items {
			assert.Equal(t, i, it.Rank, "Rank is the position within the surface, "+
				"carried through unchanged and never sorted across surfaces")
		}

		// AND THE DESIGN POINT: a board is NOT a ranking, and Compose does not
		// become one by being handed a list.
		assert.True(t, discovery.BoardIsCuration(discovery.Board{}),
			"BoardIsCuration is always true by construction -- a reader must be able "+
				"to tell 'curated by a person' from 'recommended by a machine' in the "+
				"output, and that is only true if no code path sorts a board by score")

		// AND ResolveBoard adds nothing: same ids, same order, positions from the
		// slice index.
		resolved := discovery.ResolveBoard(boards[0].Board)
		require.Len(t, resolved, 3)
		for i, item := range resolved {
			assert.Equal(t, i, item.Position)
			assert.Equal(t, boards[0].EntityIDs[i], item.EntityID)
		}

		// AND BoardSection hands back the ONE surface's contribution, with ok false
		// when there is none -- so a caller cannot render a heading for a surface
		// that produced nothing.
		section2, ok, err := store.BoardSection(ctx, boards)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, discovery.SurfaceBoards, section2.Surface)
		assert.Len(t, section2.Items, 3)

		noneAtAll, ok, err := store.BoardSection(ctx, nil)
		require.NoError(t, err)
		assert.False(t, ok, "no boards is no section, not an empty one")
		assert.Empty(t, noneAtAll.Items, "and the returned section is inert rather "+
			"than a heading waiting to be rendered")

		// AND BoardItemsFor AGREES with ResolveBoard on the raw list, which is the
		// round-trip worth having: the store read the ordering back and resolved it
		// again, and the two spellings of "position 3" must be the same number.
		viaStore, err := store.BoardItemsFor(ctx, "board-feed-1")
		require.NoError(t, err)
		assert.Equal(t, resolved, viaStore,
			"a round-trip that disagreed would mean the stored order is not what "+
				"the domain's ordering says it is -- which is the failure the whole "+
				"primary-key-position shape exists to prevent")

		// Feed.Items flattens WITHOUT re-sorting, because a flat sort would
		// reintroduce the cross-surface comparison Compose exists to avoid.
		assert.Len(t, feed.Items(), 3)

		// AND THE RESOLVED BOARD, which is what a board page renders and what nothing
		// else in the tree can produce.
		items, err := store.BoardItemsFor(ctx, "board-feed-1")
		require.NoError(t, err)
		require.Len(t, items, 3)
		for i, it := range items {
			assert.Equal(t, i, it.Position,
				"ResolveBoard derives positions from slice order, and BoardItemsFor "+
					"asserts it rather than trusting it")
			assert.Equal(t, boardIDs[i], it.EntityID)
		}
	})
}

// EVERY FEED SYMBOL IS REFERENCED OUTSIDE ITS PACKAGE.
//
// THE FINER GUARD, and the third spelling of the same guard:
//
//  1. internal/collab    -- package imported by nothing
//  2. internal/directory -- package imported by nothing
//  3. internal/discovery -- PACKAGE IMPORTED, symbols not
//
// A check has no opinion about a failure one level below the one it tests, so this
// walks the source tree and asks about SYMBOLS.
//
// The scope is stated rather than total, and stating it is the honest part: a symbol
// is required to be referenced if it is part of the feature this step delivers. The
// rank.go helpers (Rank, Score, Cosine, DefaultGravity, DefaultWeights,
// SanitisePeerTaste) and NormaliseTitle are deliberately OUT of scope -- they are
// acquisition's, not the feed's, and requiring them here would make the guard a
// demand for work this step does not claim. A guard that names its own exemptions
// is checkable; one that silently passes on whatever it happens to accept is not.
func TestEveryFeedSymbolIsReferencedOutsideItsPackage(t *testing.T) {
	// The symbols this step delivers, and the reason each is required.
	required := map[string]string{
		"Board":           "§6a.4's curated list, read by BoardFor",
		"BoardItem":       "ResolveBoard's output type",
		"ResolveBoard":    "the only way an ordering becomes positions",
		"BoardIsCuration": "a reader must be able to tell curation from ranking",
		"Feed":            "Compose's return value",
		"Compose":         "nothing built a []FeedItem before ComposeFeed",
		"Surface":         "§6a.7's legible source",
		"SurfaceBoards":   "the surface a board item is labelled with",
		"FeedItem":        "the feed's unit",
		"FeedSection":     "the feed's grouping, which is the whole design",
	}

	// Every non-test .go file outside internal/discovery.
	blob := []string{}
	filepath.WalkDir("../../", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") || strings.Contains(path, "internal/discovery/") {
			return nil
		}
		if body, rerr := os.ReadFile(path); rerr == nil {
			blob = append(blob, string(body))
		}
		return nil
	})
	all := strings.Join(blob, "\n")

	orphans := []string{}
	for sym := range required {
		if !regexp.MustCompile(`\bdiscovery\.` + sym + `\b`).MatchString(all) {
			orphans = append(orphans, sym)
		}
	}
	assert.Empty(t, orphans,
		"these feed symbols have NO non-test reference outside internal/discovery: "+
			"%v. The package IS imported -- internal/acquisition uses five other "+
			"symbols from it -- so the package-level check passes while the feature "+
			"is unreachable. That is the third time this has happened and the third "+
			"time the guard has had to get finer", orphans)

	// AND THE SCOPE IS ASSERTED TOO, so an exemption cannot be added by deleting a
	// line here. If a required symbol is genuinely out of scope, removing it from
	// this map should FAIL rather than silently pass -- so the map is compared
	// against what ComposeFeed's path actually touches.
	assert.Contains(t, all, "discovery.Compose(",
		"Compose must be called from outside its package -- this is the call site "+
			"the whole step exists to create")
	assert.Contains(t, all, "discovery.ResolveBoard(",
		"and ResolveBoard, or the ordering never becomes positions")
}

// THE MUTATION GATE NAMES EVERY DISCOVERY TEST, AND IT CHECKS BY EXTRACTING.
//
// A mutation gate's entire output is the word SURVIVED, and a survivor that was
// never run is the one failure it cannot tell apart from a finding. The
// access-policy gate's pattern was written out in two places and the copies
// drifted, so two mutants were reported SURVIVED while the suite it ran could not
// have contained the test that kills them.
//
// So the pattern is EXTRACTED from the script and the test names are EXTRACTED
// from this file. A literal in two files is the same drift in a different hat.
func TestTheDiscoveryGateNamesEveryDiscoveryTest(t *testing.T) {
	source, err := os.ReadFile("mutate_discovery.py")
	require.NoError(t, err, "reading mutate_discovery.py -- without it the gate names nothing")

	// Cut returns (before, after, found), so the SEGMENT IS `before` on both
	// calls. The first version of this took `after` from the second Cut -- the text
	// PAST the closing paren, i.e. the rest of the script -- and so reported every
	// test missing from a pattern that named all of them. A guard that cries wolf
	// on a correct configuration trains its reader to ignore it.
	_, rest, ok := strings.Cut(string(source), "SUITE_PATTERN = (")
	require.True(t, ok, "no SUITE_PATTERN in mutate_discovery.py")
	pattern, _, ok := strings.Cut(rest, ")")
	require.True(t, ok, "unterminated SUITE_PATTERN")

	self, err := os.ReadFile("stashforge_discovery_test.go")
	require.NoError(t, err)

	missing := []string{}
	for _, m := range regexp.MustCompile(`(?m)^func (Test\w+)\(`).FindAllStringSubmatch(string(self), -1) {
		if !strings.Contains(pattern, m[1]) {
			missing = append(missing, m[1])
		}
	}
	assert.Empty(t, missing,
		"these tests exist but are not in SUITE_PATTERN, so the gate will never run "+
			"them and a mutant they would catch will read as a survivor: %v", missing)
	assert.Greater(t, len(pattern), 200, "SUITE_PATTERN looks too short to name a suite")
}
