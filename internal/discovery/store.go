// The persistence the feed and boards need. M7 step 7.3c.
//
// WHY THIS FILE EXISTS, and it is the third time this shape has been needed.
//
//  1. internal/collab -- a domain model with no caller.
//  2. internal/directory -- 319 lines, thirteen exported symbols, imported by
//     NOTHING.
//  3. internal/discovery -- the package WAS imported, by internal/acquisition,
//     which used five other symbols from it. All 225 lines of feed.go had no
//     non-test reference at all: 16 of 23 exported symbols were unreachable
//     while their package was not.
//
// The third is the interesting one, because the cheap check -- "is the package
// imported" -- cannot see it. Reachable package, unreachable symbol is a finer
// failure than the one the cheap check tests for, and a check has no opinion
// about failures one level below the one it is looking for. So the store goes HERE,
// next to the model, and the guard walks the source tree.
//
// NOTE WHAT THE INTERFACE DOES NOT HAVE. A board is an EDITORIAL ORDERING and not
// a score (ground rule 4), so there is deliberately:
//
//   - no SetBoardRank(ctx, boardID, entityID, rank) -- the one method that would
//     let an ordering be edited as a set of counters, which is how a curated list
//     silently becomes a stored ranking;
//   - no method taking a score, a weight or a position pair;
//   - no ReorderByRecommendation(ctx, boardID), and the absence is the point:
//     internal/discovery explicitly refuses to sort a board by score, because a
//     reader cannot then tell "curated by a person" from "recommended by a
//     machine" in the output.
//
// An ordering is a LIST. A store method that took pairs would be a rank column in
// Go, which is the same defect wearing better clothes.

package discovery

import (
	"context"
	"errors"
	"time"
)

// BoardStore persists boards and reads them back in the author's order.
type BoardStore interface {
	// SetBoard records a board and its ordering, REPLACING any previous version.
	//
	// IT TAKES THE LIST, NOT PAIRS, and that is not a convenience. Positions are
	// DENSE and are assigned here, because a caller that could pass (entity, rank)
	// pairs could pass 0, 1, 7 -- an ordering with a hole in it, and the hole has
	// no meaning a reader could interpret. Taking []string makes a gap
	// unrepresentable rather than merely discouraged.
	//
	// Replacing rather than merging is what makes a board EDITABLE: an author who
	// removes an item means it gone, and a merge cannot express that.
	SetBoard(ctx context.Context, b WriteBoard) error

	// BoardFor reads one board with its entities in order.
	//
	// A missing board is ErrNoBoard and NOT an empty Board. "There is no such
	// board" and "the board is empty" are different answers, and a resolver
	// renders them differently -- one is a 404 and the other is a page.
	BoardFor(ctx context.Context, boardID string) (StoredBoard, error)

	// BoardsByAuthor lists a user's boards, R049's "my lists".
	//
	// ORDERED BY board_id, and the ordering is part of the contract: a list of
	// someone's boards that reshuffles between renders is a list nobody trusts to
	// be theirs. NOT ordered by updated_at, because recency is a product decision
	// this interface is not the place to make silently.
	BoardsByAuthor(ctx context.Context, authorID int) ([]StoredBoard, error)

	// BoardsContaining answers "which boards is this entity on", which is the
	// query the `entity_id` index exists for and the one a viewer needs to find the
	// lists an entity appears in.
	BoardsContaining(ctx context.Context, entityID string) ([]StoredBoard, error)
}

// ErrNoBoard is a missing board, kept distinct from an empty one.
var ErrNoBoard = errors.New("discovery: no such board")

// ErrDuplicateEntity is returned when a board lists the same entity twice.
//
// It is a NAMED error rather than a bare failure because it is an authoring
// mistake with a specific fix -- drop one of the two -- and "constraint violation"
// does not say which. A board showing the same studio at positions 2 and 7 is a
// bug a reader reports and an author cannot see.
var ErrDuplicateEntity = errors.New("discovery: the same entity appears twice in one board")

// StoredBoard is a board as the DATABASE holds it, which is not the same thing as
// the domain's Board.
//
// THE TIMESTAMPS ARE HERE AND NOT ON Board. They are facts ABOUT the stored record
// rather than parts of the thing being asserted -- a board's ordering is the
// content, and its created_at is a consequence of when somebody filed it. An
// earlier version of this file added unexported createdAt/updatedAt fields to Board
// with accessors, which does not compile: the sqlite adapter is in another package
// and cannot set them, so the timestamps had nowhere to live. A ReadResult is the
// honest place for them -- it is what a read produces, as against what a caller
// submitted.
//
// NOT caller-settable on the way IN, and that is the point: CURRENT_TIMESTAMP on
// write and nothing else. A caller-supplied created_at is a claim about the past
// that nothing in this system can verify, and the WriteBoard input below has no
// field for one.
type StoredBoard struct {
	Board
	CreatedAt time.Time
	UpdatedAt time.Time
}

// WriteBoard is what SetBoard accepts: the author's contribution and nothing else.
// No CreatedAt, no UpdatedAt, no rank -- see the type comment.
type WriteBoard struct {
	ID          string
	Title       string
	AuthorID    int
	EntityIDs   []string
	Description string
}

// NewWriteBoard converts a domain Board into the write shape. It exists so the
// conversion is named and testable rather than scattered, and so the ONLY place a
// Board becomes a WriteBoard is one a reader can find.
func NewWriteBoard(b Board) WriteBoard {
	return WriteBoard{
		ID:          b.ID,
		Title:       b.Title,
		AuthorID:    b.AuthorID,
		EntityIDs:   append([]string(nil), b.EntityIDs...),
		Description: b.Description,
	}
}
