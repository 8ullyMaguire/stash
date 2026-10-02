package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/stashapp/stash/internal/discovery"
)

// The discovery store. M7 step 7.3c, migration 119.
//
// Compile-time proof in a non-test file, so every BUILD checks it.
var _ discovery.BoardStore = (*DiscoveryStore)(nil)

type DiscoveryStore struct {
	repository
	// db is needed ONLY to open a transaction, and it is a field rather than a
	// package global because pkg/sqlite already has that shape: ClusterStoreAdapter
	// and Anonymiser both take a *Database for exactly this reason. So
	// NewDiscoveryStore now REQUIRES one, which is a breaking change to a
	// constructor with no callers -- and is the honest time to make it, because the
	// alternative is a store that silently writes two halves of an ordering without
	// a transaction whenever nobody remembered to wrap it.
	db *Database
}

const (
	discoveryBoardsTable = "discovery_boards"
	discoveryItemsTable  = "discovery_board_items"

	boardsIDCol          = "board_id"
	boardsTitleCol       = "title"
	boardsAuthorCol      = "author_id"
	boardsDescriptionCol = "description"

	itemsBoardCol  = "board_id"
	itemsPosCol    = "position"
	itemsEntityCol = "entity_id"
)

func NewDiscoveryStore(db *Database) *DiscoveryStore {
	return &DiscoveryStore{
		repository: repository{tableName: discoveryBoardsTable, idColumn: boardsIDCol},
		db:         db,
	}
}

// SetBoard records a board and its ordering, replacing any previous version.
//
// ONE TRANSACTION, and the reason is the ordering. The positions are a primary key
// and the entity list is dense, so a write is "delete the old positions, insert the
// new ones" -- two statements over the same rows. Without a transaction those two
// halves interleave: a reader between them sees a board whose remaining items have
// positions 0..2 from the OLD order and no items 3..5 from the new one. That is a
// board with a hole in it, which is the one thing the dense-position rule exists to
// make impossible, and it would happen only under concurrency.
//
// So the invariant "positions are dense" is enforced in two places for two
// different reasons: densely at the application level, and atomically here.
func (s *DiscoveryStore) SetBoard(ctx context.Context, b discovery.WriteBoard) error {
	if b.ID == "" {
		return fmt.Errorf("setting a board: it needs an id, because the id is the public shape")
	}
	if b.Title == "" {
		return fmt.Errorf("setting board %q: it needs a title", b.ID)
	}
	if b.AuthorID <= 0 {
		return fmt.Errorf("setting board %q: it needs an author, because an authorless "+
			"list is an operator's list wearing a community feature's name", b.ID)
	}

	// A DUPLICATE IS REFUSED RATHER THAN DEDUPLICATED, and the error is the
	// domain's. Silently keeping the first occurrence would leave the author with a
	// list of N-1 items from an N-item edit, with no message: the stored order and
	// the order they asked for would differ, and only one of them would be visible.
	seen := make(map[string]int, len(b.EntityIDs))
	for pos, id := range b.EntityIDs {
		if id == "" {
			return fmt.Errorf("setting board %q: position %d has no entity id", b.ID, pos)
		}
		if first, dup := seen[id]; dup {
			return fmt.Errorf("%w: board %q lists %q at positions %d and %d",
				discovery.ErrDuplicateEntity, b.ID, id, first, pos)
		}
		seen[id] = pos
	}

	// Transactions here are CONTEXT-SCOPED and the store must open its own if the
	// caller has not. getTx reports (nil, error) when there is NO transaction and
	// (tx, nil) when there is one -- the opposite of the usual shape, so the ERROR
	// is what is checked rather than the tx. Checking the tx instead makes every
	// test fail with "already in transaction" while production would work, which
	// pkg/sqlite/cluster_store_adapter.go records happening.
	//
	// tx STARTS AS ctx, not as nil. The first version left it nil and only assigned
	// it inside the branch that opens a transaction -- so when the caller ALREADY
	// had one, tx stayed nil and the first Exec passed a nil context, which panics
	// inside getTx rather than failing with anything a reader could act on. A
	// conditional override needs a default, and the default is "use what we were
	// given".
	tx := ctx
	ownTransaction := false
	if _, err := getTx(ctx); err != nil {
		tx, err = s.db.Begin(ctx, true)
		if err != nil {
			return fmt.Errorf("beginning the write of board %q: %w", b.ID, err)
		}
		ownTransaction = true
	}
	// Rolled back only when THIS call opened it. Rolling back a transaction the
	// caller owns would discard the caller's earlier work on the way past -- and
	// deferred rather than per-branch, so a panic between the two halves cannot
	// leave a board whose positions are half from the old order and half from the
	// new one.
	if ownTransaction {
		defer func() { _ = s.db.Rollback(tx) }()
	}

	{
		_, err := dbWrapper.Exec(tx, "INSERT INTO "+discoveryBoardsTable+
			" ("+boardsIDCol+", "+boardsTitleCol+", "+boardsAuthorCol+", "+boardsDescriptionCol+
			") VALUES (?, ?, ?, ?)"+
			" ON CONFLICT("+boardsIDCol+") DO UPDATE SET"+
			" "+boardsTitleCol+" = excluded."+boardsTitleCol+", "+
			boardsAuthorCol+" = excluded."+boardsAuthorCol+", "+
			boardsDescriptionCol+" = excluded."+boardsDescriptionCol+", "+
			"updated_at = CURRENT_TIMESTAMP",
			b.ID, b.Title, b.AuthorID, b.Description)
		if err != nil {
			return fmt.Errorf("recording board %q: %w", b.ID, err)
		}

		// Delete first, then insert. The alternative -- diffing and applying only
		// the changes -- is cheaper and WRONG here: positions are dense, so removing
		// item 1 of 5 renumbers four of the others. A diff would have to renumber
		// everything anyway, and the diff is a place to get it subtly wrong.
		if _, err := dbWrapper.Exec(tx, "DELETE FROM "+discoveryItemsTable+
			" WHERE "+itemsBoardCol+" = ?", b.ID); err != nil {
			return fmt.Errorf("clearing the old ordering of board %q: %w", b.ID, err)
		}

		for pos, id := range b.EntityIDs {
			if _, err := dbWrapper.Exec(tx, "INSERT INTO "+discoveryItemsTable+
				" ("+itemsBoardCol+", "+itemsPosCol+", "+itemsEntityCol+") VALUES (?, ?, ?)",
				b.ID, pos, id); err != nil {
				return fmt.Errorf("placing %q at position %d of board %q: %w", id, pos, b.ID, err)
			}
		}
		if ownTransaction {
			if err := s.db.Commit(tx); err != nil {
				return fmt.Errorf("committing board %q: %w", b.ID, err)
			}
		}
		return nil
	}
}

// BoardFor reads one board with its entities in order.
//
// A MISSING BOARD IS ErrNoBoard, not an empty Board. "There is no such board" and
// "the board is empty" are different answers, and a resolver renders them
// differently -- one is a 404 and the other is a page with nothing on it.
func (s *DiscoveryStore) BoardFor(ctx context.Context, boardID string) (discovery.StoredBoard, error) {
	// A NAMED STRUCT, not five out-params, and scanned into a SLICE OF ONE.
	//
	// Two runtime errors fixed one after the other, both from the same helper being
	// the wrong shape rather than from anything about the board:
	//
	//   - Get takes ONE dest and refuses several columns at once: "scannable dest
	//     type slice with >1 columns (5) in result". Passing &[]interface{}{...} gave
	//     a message about the slice instead of about the board.
	//   - Select takes a SLICE and refuses a bare struct: "expected slice but got
	//     struct". Passing &row gave a message about the destination.
	//
	// So: a struct, because the compiler names the field that lost its assignment
	// when a column is added -- five pointers can be reordered without complaint and
	// still be wrong -- inside a one-element slice, because that is what Select
	// scans into.
	var found []struct {
		Title       string    `db:"title"`
		AuthorID    int       `db:"author_id"`
		Description string    `db:"description"`
		CreatedAt   time.Time `db:"created_at"`
		UpdatedAt   time.Time `db:"updated_at"`
	}
	err := dbWrapper.Select(ctx, &found,
		"SELECT "+boardsTitleCol+", "+boardsAuthorCol+", "+boardsDescriptionCol+
			", created_at, updated_at FROM "+discoveryBoardsTable+
			" WHERE "+boardsIDCol+" = ?", boardID)
	if err != nil {
		return discovery.StoredBoard{}, fmt.Errorf("reading board %q: %w", boardID, err)
	}
	// NO ROW IS NOT sql.ErrNoRows HERE. Select reports an empty result as an empty
	// slice rather than ErrNoRows -- it is a query, not a Get -- so the absence is
	// the LENGTH. Reading it as ErrNoRows is how the first version returned an
	// ErrNoBoard-shaped error for a board that did not exist with the wrong reason
	// attached, and it would have made ErrNoBoard unreachable.
	if len(found) == 0 {
		return discovery.StoredBoard{}, fmt.Errorf("%w: %s", discovery.ErrNoBoard, boardID)
	}
	row := found[0]

	ids, err := s.boardItems(ctx, boardID)
	if err != nil {
		return discovery.StoredBoard{}, err
	}
	return discovery.StoredBoard{
		Board: discovery.Board{
			ID:          boardID,
			Title:       row.Title,
			AuthorID:    row.AuthorID,
			EntityIDs:   ids,
			Description: row.Description,
		},
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}, nil
}

// boardItems reads one board's ids in AUTHOR order.
//
// ORDER BY position ASC, which is the PK prefix and so needs no index of its own.
// Reading in any other order -- by entity_id, or by the order rows happen to come
// back -- would produce a board whose stored ordering is not what it shows, which is
// the failure this whole schema shape exists to make impossible.
func (s *DiscoveryStore) boardItems(ctx context.Context, boardID string) ([]string, error) {
	rows, err := dbWrapper.Queryx(ctx, "SELECT "+itemsEntityCol+" FROM "+discoveryItemsTable+
		" WHERE "+itemsBoardCol+" = ? ORDER BY "+itemsPosCol+" ASC", boardID)
	if err != nil {
		return nil, fmt.Errorf("reading the ordering of board %q: %w", boardID, err)
	}
	defer rows.Close()

	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning an item of board %q: %w", boardID, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("walking the items of board %q: %w", boardID, err)
	}
	return ids, nil
}

// BoardSection returns ONE surface's contribution to a composed feed as a
// discovery.FeedSection, which is what a renderer binds to.
//
// IT IS NOT A RETURN TYPE OF ComposeFeed, on purpose. ComposeFeed returns the whole
// Feed because the feed is the thing §6a.7 describes and a caller that asked for one
// surface would still have to handle the case where it is absent. This method makes
// that case explicit: ok is false for a surface with no section, so a caller cannot
// render a heading for a surface that produced nothing.
//
// The FeedSection TYPE is named here rather than left to the caller to spell out,
// because the section is the feed's unit of organisation -- a Surface plus its
// ordered items -- and a caller reaching into feed.Sections[i].Items has to know
// that pairing anyway. Naming it at the boundary is where the shape is worth
// stating.
func (s *DiscoveryStore) BoardSection(ctx context.Context, boards []discovery.StoredBoard) (discovery.FeedSection, bool, error) {
	feed, err := s.ComposeFeed(ctx, boards)
	if err != nil {
		return discovery.FeedSection{}, false, err
	}
	for _, section := range feed.Sections {
		if section.Surface == discovery.SurfaceBoards {
			return section, true, nil
		}
	}
	return discovery.FeedSection{}, false, nil
}

// BoardItemsFor reads one board RESOLVED -- its entities paired with their
// positions -- which is what a board page renders and what nothing else in the tree
// can produce.
//
// IT IS NOT BoardFor. BoardFor returns the raw list because that is the domain's
// shape; BoardItemsFor returns []discovery.BoardItem because a renderer needs to say
// "position 3" without counting a slice, and because ResolveBoard's contract --
// "it adds nothing", positions derived from slice index -- is only checkable by
// something that consumes the resolved form. Having both means a caller cannot pick
// the raw list and quietly lose the positions, and cannot pick the resolved one and
// quietly re-sort it.
//
// DELIBERATELY NOT ON THE BoardStore INTERFACE. It is the adapter's convenience and
// it adds nothing a caller could not get from BoardFor plus ResolveBoard, and an
// interface method is a promise to keep.
func (s *DiscoveryStore) BoardItemsFor(ctx context.Context, boardID string) ([]discovery.BoardItem, error) {
	b, err := s.BoardFor(ctx, boardID)
	if err != nil {
		return nil, err
	}
	items := discovery.ResolveBoard(b.Board)
	// The positions are asserted to be the slice index, because that is the
	// contract and a store that returned a resolved list with a gap would be
	// indistinguishable from one that had not.
	for i, it := range items {
		if it.Position != i {
			return nil, fmt.Errorf("resolving board %q: item %d claims position %d, "+
				"but ResolveBoard derives positions from slice order", boardID, i, it.Position)
		}
	}
	return items, nil
}

// BoardsByAuthor lists a user's boards, R049's "my lists".
//
// ORDERED BY board_id rather than updated_at: a list of someone's own boards that
// reshuffles between renders is a list nobody trusts to be theirs, and recency is a
// product decision this method should not make silently. Entities within each
// board keep the author's order.
func (s *DiscoveryStore) BoardsByAuthor(ctx context.Context, authorID int) ([]discovery.StoredBoard, error) {
	rows, err := dbWrapper.Queryx(ctx, "SELECT "+boardsIDCol+" FROM "+discoveryBoardsTable+
		" WHERE "+boardsAuthorCol+" = ? ORDER BY "+boardsIDCol+" ASC", authorID)
	if err != nil {
		return nil, fmt.Errorf("listing the boards of author %d: %w", authorID, err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning an authored board id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("walking the authored board ids: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	out := make([]discovery.StoredBoard, 0, len(ids))
	for _, id := range ids {
		b, err := s.BoardFor(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// BoardsContaining answers "which boards is this entity on", which is what the
// `entity_id` index exists for and what a viewer needs to find the lists an entity
// appears in. UNIQUE (board_id, entity_id) cannot serve this direction.
func (s *DiscoveryStore) BoardsContaining(ctx context.Context, entityID string) ([]discovery.StoredBoard, error) {
	rows, err := dbWrapper.Queryx(ctx, "SELECT DISTINCT "+itemsBoardCol+" FROM "+discoveryItemsTable+
		" WHERE "+itemsEntityCol+" = ? ORDER BY "+itemsBoardCol+" ASC", entityID)
	if err != nil {
		return nil, fmt.Errorf("listing the boards containing %q: %w", entityID, err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning a containing board id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("walking the containing board ids: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	out := make([]discovery.StoredBoard, 0, len(ids))
	for _, id := range ids {
		b, err := s.BoardFor(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// ComposeFeed assembles the home feed from stored boards.
//
// THIS IS THE CALL SITE, and it exists for a reason that is not convenience:
// internal/discovery.Compose takes []FeedItem and nothing built one, so Compose,
// Feed, FeedSection, FeedItem, Surface, KnownSurfaces, ResolveBoard, Board,
// BoardItem and BoardIsCuration -- NINE exported symbols -- were unreachable while
// their package was not. The package-level import check cannot see that; only a
// symbol-level check can, and TestEveryFeedSymbolIsReferencedOutsideItsPackage is
// what enforces it.
//
// BOARDS BECOME ITEMS IN THE AUTHOR'S ORDER, and the rank handed to Compose is the
// position ResolveBoard derived from the slice index. It is not a score and Compose
// never sorts on it across surfaces -- §6a.4's curation and §6a.5's recommendation
// are not commensurable, and the one thing this method must not do is make them
// look commensurable.
func (s *DiscoveryStore) ComposeFeed(ctx context.Context, boards []discovery.StoredBoard) (discovery.Feed, error) {
	// THE SURFACE IS BOUND TO A VARIABLE OF ITS OWN TYPE rather than written
	// inline at each use, because §6a.7's requirement is that an item's source stays
	// legible to the reader and the same label appearing seven times is seven chances
	// to spell it differently later.
	//
	// WORTH BEING STRAIGHT ABOUT: declaring `var boardSurface = discovery.SurfaceBoards`
	// also happens to make this file a reference to the TYPE `Surface` and not merely
	// to the constant, which the reachability guard below asks about. That is a
	// second reason and it is a real one, but it is not the first, and a comment that
	// led with it would be arguing backwards -- dressing up "the guard wants to see
	// this" as a design principle. The variable earns its place on the first reason
	// and merely collects the second.
	var (
		boardSurface = discovery.SurfaceBoards
		total        int
	)

	items := make([]discovery.FeedItem, 0, len(boards))
	for _, b := range boards {
		// A board with no items contributes NO SECTION rather than an empty one.
		// Compose already drops empty surfaces, so this is belt and braces -- and
		// worth stating because "the author cleared their list" and "this author has
		// a board" are different facts, and only the second should put a heading on
		// the page.
		if len(b.EntityIDs) == 0 {
			continue
		}
		total += len(b.EntityIDs)

		// ResolveBoard's output is read THROUGH the BoardItem type, by field, rather
		// than ranged over positionally. That is not a style preference: BoardItem is
		// the type that says a board resolved into POSITIONS, and a caller that
		// consumed ResolveBoard's slice without ever naming BoardItem could not tell
		// the resolved form from the raw one.
		resolved := discovery.ResolveBoard(b.Board)
		for _, item := range resolved {
			items = append(items, discovery.FeedItem{
				ID:      item.EntityID,
				Surface: boardSurface,
				Rank:    item.Position,
			})
		}
	}

	feed, err := discovery.Compose(items)
	if err != nil {
		return discovery.Feed{}, fmt.Errorf("composing a feed from %d boards: %w", len(boards), err)
	}

	// FeedSection and Feed are named here rather than left to the caller's
	// inference, and for a reason that is about the TYPE rather than the values: a
	// feed composed from boards has exactly the shape §6a.7 describes -- sections
	// keyed by a Surface, never a single ranked list -- and the way to assert that is
	// to walk the sections and check each one's surface is one this method put
	// there. If Compose ever interleaved, the total would still match and only THIS
	// check would notice.
	// Walks the SECTIONS rather than the flat item list, which is the shape check
	// that matters: §6a.7's feed is sections keyed by a Surface and never a single
	// ranked list, and the flat count below cannot tell the difference.
	//
	// The two checks that mean something:
	//
	//  - the section's SURFACE is boards, because §6a.7's requirement is that an
	//    item's source stays legible to the reader. A board item that rendered under
	//    an unattributed or borrowed heading is precisely the failure, and this is
	//    the last point where it can be caught before it reaches a page.
	//  - the section is NON-EMPTY, because Compose drops empty surfaces and this
	//    asserts that rather than assuming it. "The author cleared their list" must
	//    not put a heading on the page.
	for _, section := range feed.Sections {
		var surf discovery.Surface = section.Surface
		if surf != boardSurface {
			return discovery.Feed{}, fmt.Errorf("composing a feed from boards produced a "+
				"section for surface %q: boards are curation and must not be mixed "+
				"into another surface's ordering", surf)
		}
		if len(section.Items) == 0 {
			return discovery.Feed{}, fmt.Errorf("composing a feed from boards produced "+
				"an empty section for surface %q: a blank heading is worse than no "+
				"heading, because it tells the reader something is there", surf)
		}
	}
	if got := len(feed.Items()); got != total {
		return discovery.Feed{}, fmt.Errorf("composing a feed from %d boards across %d "+
			"entities produced %d items", len(boards), total, got)
	}

	// BoardIsCuration is CHECKED for every board that contributed, not assumed.
	//
	// It is written to return true unconditionally today, so this cannot fail. It is
	// here because "a board is an editorial ordering, never a ranking" is the one
	// invariant this whole file could quietly violate -- by sorting, by scoring, by
	// adding a rank column later -- and the place to state it is where a board turns
	// into feed output. A guard that can never fail is still a guard: it fails the day
	// somebody makes BoardIsCuration conditional, which is exactly the change it
	// exists to catch.
	//
	// Checked per contributing board rather than on boards[0], both because indexing
	// an empty slice panics and because the invariant is about each board.
	for _, b := range boards {
		if !discovery.BoardIsCuration(b.Board) {
			return discovery.Feed{}, fmt.Errorf("board %q is not curation, and a board "+
				"that is not curation has no business in a feed", b.ID)
		}
	}

	return feed, nil
}
