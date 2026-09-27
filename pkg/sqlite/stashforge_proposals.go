package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/doug-martin/goqu/v9"
	"github.com/doug-martin/goqu/v9/exp"
	"github.com/jmoiron/sqlx"
	"gopkg.in/guregu/null.v4"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/pkg/models"
)

const (
	editProposalsTable = "edit_proposals"
	proposalVotesTable = "proposal_votes"
)

// EditProposalStore is the write path to shared content.
//
// Every method here is deliberately narrow. There is no Update method that
// writes new_value, because a proposal's value is what voters agreed on: letting
// it change after the fact means the tally describes a value nobody voted for.
// Changing a proposal means superseding it and opening a new one.
type EditProposalStore struct {
	repository
	tableMgr *table
}

func NewEditProposalStore() *EditProposalStore {
	return &EditProposalStore{
		repository: repository{tableName: editProposalsTable, idColumn: "id"},
		tableMgr:   editProposalTableMgr,
	}
}

func (qb *EditProposalStore) table() exp.IdentifierExpression { return qb.tableMgr.table }

func (qb *EditProposalStore) selectDataset() *goqu.SelectDataset {
	return dialect.From(qb.table()).Select(qb.table().All())
}

// Create inserts a proposal and returns it as the database stored it.
//
// Re-read after the insert rather than echoing the argument back: the id, the
// created_at default and the status CHECK default all come from the schema, and
// a caller that guessed at them would be wrong about its own write.
func (qb *EditProposalStore) Create(ctx context.Context, p *models.EditProposal) (*models.EditProposal, error) {
	if p.Status == "" {
		p.Status = models.ProposalOpen
	}

	// Validate HERE as well as in the collab service. The service is the
	// user-facing path and validates there, but the store is the last thing
	// before SQL, and anything that reaches it directly -- an importer, a
	// migration, a test, a future caller -- would otherwise write a row no
	// proposer is allowed to write. A rule enforced only one layer up is a rule
	// one call path away from being skipped; Found by
	// TestApply_RealDatabaseRejectsInvalidValue, which could create a rating
	// of 9 that the service would have refused.
	if err := collab.ValidateValue(p.TargetType, p.Field, p.NewValue); err != nil {
		return nil, fmt.Errorf("creating edit proposal: %w", err)
	}
	if err := collab.ValidateValue(p.TargetType, p.Field, p.OldValue); err != nil {
		return nil, fmt.Errorf("creating edit proposal: %w", err)
	}

	if _, err := dbWrapper.Exec(ctx, fmt.Sprintf(
		"INSERT INTO %s (target_type, target_id, field, old_value, new_value, rationale, author_id, status) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		editProposalsTable),
		p.TargetType, p.TargetID, p.Field, strOrNull(p.OldValue), strOrNull(p.NewValue),
		p.Rationale, p.AuthorID, string(p.Status)); err != nil {
		return nil, fmt.Errorf("creating edit proposal: %w", err)
	}

	// Newest row for this exact key. The partial unique index means at most one
	// is open, but an importer may insert a decided one, so ordering by id rather
	// than filtering on status finds what this call just wrote either way.
	return qb.get(ctx, qb.selectDataset().Where(
		goqu.C("author_id").Eq(p.AuthorID),
		goqu.C("target_type").Eq(p.TargetType),
		goqu.C("target_id").Eq(p.TargetID),
		goqu.C("field").Eq(p.Field),
	).Order(goqu.C("id").Desc()))
}

// strOrNull keeps NULL and "" distinct. A proposal that clears a field and a
// proposal that sets it to the empty string are different edits, and collapsing
// them here would make "clear this field" unexpressible.
func strOrNull(v *string) interface{} {
	if v == nil {
		return nil
	}
	return *v
}

func (qb *EditProposalStore) Find(ctx context.Context, id int) (*models.EditProposal, error) {
	ret, err := qb.get(ctx, qb.selectDataset().Where(goqu.C("id").Eq(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrNotFound
	}
	return ret, err
}

func (qb *EditProposalStore) FindOpen(ctx context.Context, targetType string, targetID int, field string) (*models.EditProposal, error) {
	ret, err := qb.get(ctx, qb.selectDataset().Where(
		goqu.C("target_type").Eq(targetType),
		goqu.C("target_id").Eq(targetID),
		goqu.C("field").Eq(field),
		goqu.C("status").Eq(string(models.ProposalOpen)),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, models.ErrNotFound
	}
	return ret, err
}

func (qb *EditProposalStore) FindByAuthor(ctx context.Context, authorID int, limit int) ([]*models.EditProposal, error) {
	if limit <= 0 {
		limit = 50
	}
	return qb.getMany(ctx, qb.selectDataset().
		Where(goqu.C("author_id").Eq(authorID)).
		Order(goqu.C("created_at").Desc(), goqu.C("id").Desc()).
		Limit(uint(limit)))
}

// FindByStatus is the moderation queue, oldest first: a proposal nobody has
// looked at is the one that has been open longest.
func (qb *EditProposalStore) FindByStatus(ctx context.Context, status models.ProposalStatus, limit, offset int) ([]*models.EditProposal, error) {
	if limit <= 0 {
		limit = 50
	}
	return qb.getMany(ctx, qb.selectDataset().
		Where(goqu.C("status").Eq(string(status))).
		Order(goqu.C("created_at").Asc(), goqu.C("id").Asc()).
		Limit(uint(limit)).Offset(uint(offset)))
}

// FindRejectedByAuthor returns this author's rejected proposals, newest first.
//
// It backs the sticky-rejection rule in collab. Derived from the proposal rows
// rather than a separate sticky table: the rule binds (target, field, author),
// and that is already recorded on the row that recorded the rejection, so a
// second table would be a second thing to fall out of sync with the first.
func (qb *EditProposalStore) FindRejectedByAuthor(ctx context.Context, authorID int) ([]*models.EditProposal, error) {
	return qb.getMany(ctx, qb.selectDataset().
		Where(goqu.C("author_id").Eq(authorID), goqu.C("status").Eq(string(models.ProposalRejected))).
		Order(goqu.C("id").Desc()))
}

// FindFiltered lists proposals with every filter optional and composed with AND.
//
// The filters are built as a slice rather than as a chain of ifs so that adding
// one cannot change the behaviour of the others — a chain is where a filter
// added later quietly becomes an OR.
func (qb *EditProposalStore) FindFiltered(ctx context.Context, targetType *string, targetID *int, targetField *string, authorID *int, status models.ProposalStatus, limit, offset int) ([]*models.EditProposal, error) {
	if limit <= 0 {
		limit = 50
	}

	conds := []exp.Expression{}
	if targetType != nil {
		conds = append(conds, goqu.C("target_type").Eq(*targetType))
	}
	if targetID != nil {
		conds = append(conds, goqu.C("target_id").Eq(*targetID))
	}
	if targetField != nil {
		conds = append(conds, goqu.C("field").Eq(*targetField))
	}
	if authorID != nil {
		conds = append(conds, goqu.C("author_id").Eq(*authorID))
	}
	if status != "" {
		conds = append(conds, goqu.C("status").Eq(string(status)))
	}

	q := qb.selectDataset()
	if len(conds) > 0 {
		q = q.Where(conds...)
	}
	return qb.getMany(ctx, q.
		Order(goqu.C("created_at").Desc(), goqu.C("id").Desc()).
		Limit(uint(limit)).Offset(uint(offset)))
}

// FindPendingFor lists open proposals this user has not yet voted on.
//
// Expressed as a NOT EXISTS rather than a join-and-filter, because a join would
// need DISTINCT to avoid returning a proposal once per vote and DISTINCT on a
// row set that includes a nullable vote column is a subtle way to lose rows.
func (qb *EditProposalStore) FindPendingFor(ctx context.Context, userID int, limit, offset int) ([]*models.EditProposal, error) {
	if limit <= 0 {
		limit = 50
	}
	return qb.getMany(ctx, qb.selectDataset().
		Where(goqu.C("status").Eq(string(models.ProposalOpen))).
		Where(goqu.C("id").NotIn(goqu.Select("proposal_id").From(goqu.T(proposalVotesTable)).
			Where(goqu.C("user_id").Eq(userID)))).
		Order(goqu.C("created_at").Asc(), goqu.C("id").Asc()).
		Limit(uint(limit)).Offset(uint(offset)))
}

// MyVote returns this user's vote on a proposal: 1, -1, or 0 when they have
// not voted.
//
// 0 rather than an error or a pointer, because a missing vote is the common case
// and the caller only ever asks "is this 1 or -1".
func (qb *ProposalVoteStore) MyVote(ctx context.Context, proposalID, userID int) (int, error) {
	// sql.ErrNoRows is translated to 0 because "this user has not voted" is a
	// state the schema represents by the absence of a row, and the caller asked
	// a yes/no question. Returning the error would make every unanswered
	// proposal on a page look like a query failure.
	var row struct {
		Value int `db:"value"`
	}
	err := dbWrapper.Get(ctx, &row,
		"SELECT value FROM proposal_votes WHERE proposal_id = ? AND user_id = ?",
		proposalID, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return row.Value, nil
}

// CountOpenByAuthor backs the NewAccountProposalHold trust signal: a brand-new
// account that immediately opens twenty proposals is doing something other than
// using the instance.
func (qb *EditProposalStore) CountOpenByAuthor(ctx context.Context, authorID int) (int, error) {
	return count(ctx, dialect.Select(goqu.COUNT("*")).From(qb.table()).
		Where(goqu.C("author_id").Eq(authorID), goqu.C("status").Eq(string(models.ProposalOpen))))
}

// SetStatus records a decision, stamping decided_at and decided_by.
//
// The WHERE clause carries `status = 'open'`, so a proposal that is already
// decided is not re-decided. A late vote or a double-clicked moderator button
// must not silently rewrite an outcome people have already been told about, and
// an audit trail is only worth having if a terminal state is terminal. The
// affected-row count distinguishes "already decided" from "not found", which a
// plain UPDATE reports identically.
func (qb *EditProposalStore) SetStatus(ctx context.Context, id int, status models.ProposalStatus, decidedBy int) error {
	if status == models.ProposalOpen {
		return errors.New("cannot reopen a proposal; supersede it and open a new one")
	}
	if !status.IsTerminal() {
		return fmt.Errorf("%q is not a terminal proposal status", status)
	}

	// A withdrawal is the author acting, not a moderator ruling, so it records
	// no decider. Everything else is a decision somebody made.
	var decider interface{}
	if status != models.ProposalWithdrawn {
		decider = decidedBy
	}

	res, err := dbWrapper.Exec(ctx, fmt.Sprintf(
		"UPDATE %s SET status = ?, decided_at = CURRENT_TIMESTAMP, decided_by = ? WHERE id = ? AND status = ?",
		editProposalsTable), string(status), decider, id, string(models.ProposalOpen))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		// Wrapped so callers can errors.Is it. The applier relies on this
		// sentinel: an accepted proposal that cannot be applied must KEEP its
		// accepted status, because that is what the voters decided.
		return fmt.Errorf("proposal %d is not open; a decided proposal cannot be re-decided: %w",
			id, collab.ErrAlreadyDecided)
	}
	return nil
}

// Score reads the computed tally from the proposal_scores view.
//
// A view read, not a SUM written here, because the view is the single definition
// of the tally and two implementations of it would eventually disagree — and the
// disagreement would decide an election.
func (qb *EditProposalStore) Score(ctx context.Context, proposalID int) (models.ProposalScore, error) {
	// A row struct rather than a dest slice: dbWrapper.Get is sqlx's Get, which
	// wants something with db tags. Handing it a []interface{} of columns fails
	// at scan time with a message about "dest type slice", which says nothing
	// useful about the actual mistake.
	var row struct {
		Net         int `db:"net"`
		Voters      int `db:"voters"`
		AuthorVoted int `db:"author_voted"`
	}

	if err := dbWrapper.Get(ctx, &row,
		"SELECT net, voters, author_voted FROM proposal_scores WHERE proposal_id = ?", proposalID); err != nil {
		return models.ProposalScore{}, err
	}
	return models.ProposalScore{
		Net:         row.Net,
		Voters:      row.Voters,
		AuthorVoted: row.AuthorVoted == 1,
	}, nil
}

func (qb *EditProposalStore) get(ctx context.Context, q *goqu.SelectDataset) (*models.EditProposal, error) {
	ret, err := qb.getMany(ctx, q)
	if err != nil {
		return nil, err
	}
	if len(ret) == 0 {
		return nil, sql.ErrNoRows
	}
	return ret[0], nil
}

func (qb *EditProposalStore) getMany(ctx context.Context, q *goqu.SelectDataset) ([]*models.EditProposal, error) {
	const single = false
	var ret []*models.EditProposal
	// NOTE the single Scan per callback invocation. queryFunc already advances
	// the cursor before calling f, so calling r.Next() here again would skip the
	// row just advanced to -- which yields zero rows and NO error, the worst
	// possible failure shape. This is why every other store in this package
	// scans once and returns.
	if err := queryFunc(ctx, q, single, func(r *sqlx.Rows) error {
		var p proposalRow
		if err := r.StructScan(&p); err != nil {
			return err
		}
		resolved := p.resolve()
		ret = append(ret, resolved)
		return nil
	}); err != nil {
		return nil, err
	}
	return ret, nil
}

// proposalRow maps edit_proposals. old_value and new_value are sql.NullString
// because NULL and "" are different facts about the field being changed, and
// that difference has to survive all the way to the caller.
type proposalRow struct {
	ID         int         `db:"id"`
	TargetType string      `db:"target_type"`
	TargetID   int         `db:"target_id"`
	Field      string      `db:"field"`
	OldValue   null.String `db:"old_value"`
	NewValue   null.String `db:"new_value"`
	Rationale  null.String `db:"rationale"`
	AuthorID   int         `db:"author_id"`
	CreatedAt  Timestamp   `db:"created_at"`
	Status     string      `db:"status"`
	DecidedAt  null.Time   `db:"decided_at"`
	DecidedBy  null.Int    `db:"decided_by"`
}

func (p *proposalRow) resolve() *models.EditProposal {
	out := &models.EditProposal{
		ID:         p.ID,
		TargetType: p.TargetType,
		TargetID:   p.TargetID,
		Field:      p.Field,
		Rationale:  p.Rationale.String,
		AuthorID:   p.AuthorID,
		CreatedAt:  p.CreatedAt.Timestamp,
		Status:     models.ProposalStatus(p.Status),
	}
	if p.OldValue.Valid {
		v := p.OldValue.String
		out.OldValue = &v
	}
	if p.NewValue.Valid {
		v := p.NewValue.String
		out.NewValue = &v
	}
	if p.DecidedAt.Valid {
		t := p.DecidedAt.Time
		out.DecidedAt = &t
	}
	if p.DecidedBy.Valid {
		v := int(p.DecidedBy.Int64)
		out.DecidedBy = &v
	}
	return out
}

// --- votes -----------------------------------------------------------------

// ProposalVoteStore persists votes.
//
// There is no tally column anywhere in this struct, and that is the design:
// non-negotiable #4, no stored counters.
type ProposalVoteStore struct {
	repository
	tableMgr *table
}

func NewProposalVoteStore() *ProposalVoteStore {
	return &ProposalVoteStore{
		repository: repository{tableName: proposalVotesTable, idColumn: "proposal_id"},
		tableMgr:   proposalVoteTableMgr,
	}
}

func (qb *ProposalVoteStore) table() exp.IdentifierExpression { return qb.tableMgr.table }

// Cast records or replaces a vote.
//
// The upsert is what makes re-voting mean "change your mind" rather than "add
// another vote". A plain INSERT would be refused by the composite primary key —
// the correct outcome, but a confusing error — while the upsert expresses the
// intent. The primary key stays the thing that stops one account contributing
// twice, which is the Sybil shape MinVoters exists to bound.
func (qb *ProposalVoteStore) Cast(ctx context.Context, proposalID, userID, value int) error {
	// Checked here as well as by the CHECK so the error names the problem. A
	// vote table accepting other weights invites weighted voting, which turns
	// reputation into an auction.
	if value != 1 && value != -1 {
		return fmt.Errorf("vote value must be 1 or -1, got %d", value)
	}

	_, err := dbWrapper.Exec(ctx, fmt.Sprintf(
		"INSERT INTO %s (proposal_id, user_id, value) VALUES (?, ?, ?) "+
			"ON CONFLICT (proposal_id, user_id) DO UPDATE SET value = excluded.value",
		proposalVotesTable), proposalID, userID, value)
	return err
}

func (qb *ProposalVoteStore) Withdraw(ctx context.Context, proposalID, userID int) error {
	_, err := dbWrapper.Exec(ctx, fmt.Sprintf(
		"DELETE FROM %s WHERE proposal_id = ? AND user_id = ?", proposalVotesTable), proposalID, userID)
	return err
}

func (qb *ProposalVoteStore) List(ctx context.Context, proposalID int) ([]models.Vote, error) {
	const single = false
	var ret []models.Vote
	if err := queryFunc(ctx, dialect.From(qb.table()).Select(qb.table().All()).
		Where(goqu.C("proposal_id").Eq(proposalID)), single, func(r *sqlx.Rows) error {
		// A row struct, not models.Vote: StructScan needs db tags, and adding
		// persistence tags to a model type would make the model's fields
		// renameable only by editing the schema.
		var v voteRow
		if err := r.StructScan(&v); err != nil {
			return err
		}
		ret = append(ret, v.resolve())
		return nil
	}); err != nil {
		return nil, err
	}
	return ret, nil
}

type voteRow struct {
	ProposalID int       `db:"proposal_id"`
	UserID     int       `db:"user_id"`
	Value      int       `db:"value"`
	CreatedAt  Timestamp `db:"created_at"`
}

func (v *voteRow) resolve() models.Vote {
	return models.Vote{
		ProposalID: v.ProposalID,
		UserID:     v.UserID,
		Value:      v.Value,
		CreatedAt:  v.CreatedAt.Timestamp,
	}
}

var (
	_ models.EditProposalStore = (*EditProposalStore)(nil)
	_ models.ProposalVoteStore = (*ProposalVoteStore)(nil)
)
