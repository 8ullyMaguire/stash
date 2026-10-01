package sqlite

import (
	"context"
	"fmt"

	"github.com/doug-martin/goqu/v9"
	"github.com/doug-martin/goqu/v9/exp"
	"github.com/jmoiron/sqlx"

	"github.com/stashapp/stash/internal/collab"
)

// Field reputation persistence. StashForge M2b.
//
// The row store. The collab-facing adapter is CollabReputationStore at the
// bottom of this file, which is the split stashforge_proposals.go has too:
// goqu and models here, collab's own view in the adapter, so governance logic
// stays free of SQL and testable without a database.

const fieldReputationTable = "field_reputation"

// Compile-time proof the adapter satisfies collab's interface. A signature drift
// should fail at build time, not at the first call from a resolver.
var _ collab.ReputationStore = (*CollabReputationStore)(nil)

type FieldReputationStore struct {
	repository
	tableMgr *table
}

func NewFieldReputationStore() *FieldReputationStore {
	return &FieldReputationStore{
		repository: repository{tableName: fieldReputationTable},
		tableMgr:   fieldReputationTableMgr,
	}
}

func (s *FieldReputationStore) table() exp.IdentifierExpression { return s.tableMgr.table }

func (s *FieldReputationStore) selectDataset() *goqu.SelectDataset {
	return dialect.From(s.table()).Select(s.table().All())
}

// fieldReputationRow is the database row, kept separate from
// collab.FieldStanding.
//
// The separation is the same one stashforge_proposals.go makes with
// proposalRow, and for the same reason: the db tags are a schema concern, and
// putting them on collab.FieldStanding would make the pure governance package
// depend on the database's column names. That is a small coupling until a
// column is renamed, at which point a change to the schema stops compiling in
// the package that was supposed to know nothing about it -- and the fix is to
// edit the governance layer, which is the wrong place to be making the change.
type fieldReputationRow struct {
	UserID     int       `db:"user_id"`
	TargetType string    `db:"target_type"`
	Field      string    `db:"field"`
	Reputation int       `db:"reputation"`
	Rejections int       `db:"rejections"`
	// UpdatedAt is read but not carried into collab's view. It is here because
	// the SELECT is table.All(): sqlx requires a destination for every returned
	// column, so omitting it fails the scan at runtime rather than at compile
	// time. The same is true of every other column, which is why this struct has
	// exactly the table's columns and not the ones the service happens to want.
	UpdatedAt Timestamp `db:"updated_at"`
}

// toStanding converts the row into collab's view.
func (r fieldReputationRow) toStanding() *collab.FieldStanding {
	return &collab.FieldStanding{
		UserID:     r.UserID,
		TargetType: r.TargetType,
		Field:      r.Field,
		Reputation: r.Reputation,
		Rejections: r.Rejections,
	}
}

// standingKey is the (user, field) triple every read and write is addressed by.
func standingKey(userID int, targetType, field string) []goqu.Expression {
	return []goqu.Expression{
		goqu.C("user_id").Eq(userID),
		goqu.C("target_type").Eq(targetType),
		goqu.C("field").Eq(field),
	}
}

// findFor returns one standing, or nil when there is no row.
//
// nil rather than an error: no row means this user has never been elected on
// this field, which is the normal state for almost every user and almost every
// field. Returning an error would put a branch in every caller for a case that
// is not a fault.
func (s *FieldReputationStore) findFor(ctx context.Context, userID int, targetType, field string) (*collab.FieldStanding, error) {
	ds := s.selectDataset().Where(standingKey(userID, targetType, field)...)

	rows, err := s.getMany(ctx, ds)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

// getMany reads into a slice, scanning exactly once per callback.
//
// The single Scan is load-bearing and is the same note that appears on
// EditProposalStore.getMany: queryFunc advances the cursor before calling f, so
// calling r.Next() again inside f would skip the row just advanced to. That
// yields zero rows and NO error, which is the worst possible failure shape -- a
// test asserting "the update worked" would pass.
func (s *FieldReputationStore) getMany(ctx context.Context, q *goqu.SelectDataset) ([]*collab.FieldStanding, error) {
	const single = false
	var ret []*collab.FieldStanding
	if err := queryFunc(ctx, q, single, func(r *sqlx.Rows) error {
		var row fieldReputationRow
		if err := r.StructScan(&row); err != nil {
			return err
		}
		ret = append(ret, row.toStanding())
		return nil
	}); err != nil {
		return nil, err
	}
	return ret, nil
}

// findManyFor returns standings for a set of users on one field, in one query.
//
// One query rather than a loop over findFor on purpose. A tally on a busy
// proposal would otherwise do a database round trip per ballot, inside
// arithmetic that should be pure. The batch form also guarantees every reader
// sees the same snapshot, so one tally cannot observe two different reputation
// values for the same user.
func (s *FieldReputationStore) findManyFor(ctx context.Context, userIDs []int, targetType, field string) (map[int]collab.FieldStanding, error) {
	out := make(map[int]collab.FieldStanding, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}

	ds := s.selectDataset().Where(
		goqu.C("user_id").In(userIDs),
		goqu.C("target_type").Eq(targetType),
		goqu.C("field").Eq(field),
	)

	rows, err := s.getMany(ctx, ds)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.UserID] = *r
	}
	// Users with no row are ABSENT from the map, not present with zeros. The
	// caller substitutes a fresh voter's weight, which computes the same number
	// while keeping "no row" distinguishable from "a row of zero" all the way
	// through. Only the second is a signal about the user.
	return out, nil
}

// recordOutcome applies one settled proposal's effect on one user's standing,
// and returns the row as stored.
//
// agreed credits reputation; !agreed increments rejections. Two counters rather
// than one signed value, because they answer different questions: reputation
// feeds the weight, rejections feed decay. Decaying someone toward a floor is a
// judgement about a person; the weight itself is arithmetic.
//
// The floor at zero on a debit is not a nicety. reputation is CHECK (>= 0), so
// without the clamp a losing streak ends in a constraint violation the user
// would read as a server fault rather than as "you have been overruled here a
// few times". Flooring is also the point: decay discounts a vote, it does not
// silence the voter.
func (s *FieldReputationStore) recordOutcome(ctx context.Context, standing collab.FieldStanding, agreed bool) (*collab.FieldStanding, error) {
	reputation := goqu.L("MAX(reputation - 1, 0)")
	rejections := goqu.L("rejections")
	if agreed {
		reputation = goqu.L("reputation + 1")
		rejections = goqu.L("rejections + 0")
	} else {
		rejections = goqu.L("rejections + 1")
	}

	ds := dialect.Update(s.table()).
		Set(goqu.Record{
			"reputation": reputation,
			"rejections": rejections,
			"updated_at": goqu.L("CURRENT_TIMESTAMP"),
		}).
		Where(standingKey(standing.UserID, standing.TargetType, standing.Field)...)

	res, err := exec(ctx, ds)
	if err != nil {
		return nil, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}

	if affected == 0 {
		// No row to update, so this is a debit against a user who has no
		// reputation on this field yet. There is nothing to debit: inserting a
		// row with rejections=1, reputation=0 would record a judgement about
		// someone who has never been judged here, and would make their first
		// ever election on a field indistinguishable from their fourth loss.
		if !agreed {
			return nil, collab.ErrNoReputation
		}
		if err := s.insertFirstCredit(ctx, standing); err != nil {
			return nil, err
		}
	}

	// Read the row back rather than echoing the computed values, so the caller
	// sees what the database stored. The clamp and the defaults are applied by
	// SQLite, and a caller that reimplemented them in Go would be a second
	// source of truth for the same arithmetic.
	return s.findFor(ctx, standing.UserID, standing.TargetType, standing.Field)
}

// insertFirstCredit creates the first reputation row for a (user, field).
//
// Called only when a credit lands on a user with no row, so the reputation is
// known to be 1. The rejections column is left to its DEFAULT of 0 rather than
// written, so a schema default change is not silently overridden here.
func (s *FieldReputationStore) insertFirstCredit(ctx context.Context, standing collab.FieldStanding) error {
	// Prepared(true) is the package idiom for inserts (blob.go,
	// custom_fields.go, fingerprint.go): it binds values rather than inlining
	// them, so a target_type or field containing a quote is data rather than
	// syntax. The vocabulary is a closed set in Go, but the database does not
	// know that, and an unparameterised write is a latent injection.
	ds := dialect.Insert(s.table()).Prepared(true).
		Cols("user_id", "target_type", "field", "reputation").
		Vals([]interface{}{standing.UserID, standing.TargetType, standing.Field, 1})

	if _, err := exec(ctx, ds); err != nil {
		return err
	}
	return nil
}

// CollabReputationStore adapts the row store to collab's interface.
//
// A separate type from FieldReputationStore for the reason
// CollabProposalStore is separate from EditProposalStore: the two disagree
// about what a reputation IS. This one carries no SQL concepts at all -- no
// rows, no columns, no "affected" counts -- so the governance code above it
// cannot accidentally depend on the schema.
type CollabReputationStore struct {
	rep *FieldReputationStore
}

func NewCollabReputationStore() *CollabReputationStore {
	return &CollabReputationStore{rep: NewFieldReputationStore()}
}

// Standing returns one user's reputation on one field, translating a miss into
// collab.ErrNoReputation.
//
// The translation is the point of the two-value distinction: collab needs to
// tell "never voted here" from "voted here and lost", and the row store's nil is
// the same nil that would mean "the database failed" everywhere else in this
// package. ErrNoReputation is a different error from any SQL failure, so a
// caller can branch on it without swallowing real faults.
func (s *CollabReputationStore) Standing(ctx context.Context, userID int, targetType, field string) (*collab.FieldStanding, error) {
	standing, err := s.rep.findFor(ctx, userID, targetType, field)
	if err != nil {
		return nil, fmt.Errorf("reading field reputation: %w", err)
	}
	if standing == nil {
		return nil, collab.ErrNoReputation
	}
	return standing, nil
}

func (s *CollabReputationStore) StandingsForMany(ctx context.Context, userIDs []int, targetType, field string) (map[int]collab.FieldStanding, error) {
	out, err := s.rep.findManyFor(ctx, userIDs, targetType, field)
	if err != nil {
		return nil, fmt.Errorf("reading field reputations: %w", err)
	}
	return out, nil
}

func (s *CollabReputationStore) RecordOutcome(ctx context.Context, standing collab.FieldStanding, agreed bool) (*collab.FieldStanding, error) {
	out, err := s.rep.recordOutcome(ctx, standing, agreed)
	if err != nil {
		// Wrapping with %w so errors.Is still finds ErrNoReputation through the
		// adapter, which is what lets the debit-against-nothing case be
		// distinguished from a database fault at the call site.
		return nil, fmt.Errorf("recording field reputation outcome: %w", err)
	}
	return out, nil
}
