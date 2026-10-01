package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The mesh's serve-side budget, from M8 step 0 probe 3.
//
// # WHY THE REFUSAL LIVES HERE AND NOT IN A SETTING
//
// Probe 3 asked whether a node can host replicas without becoming an unbounded
// liability, and the answer this file implements is "yes, if the cap is enforced
// at the point the bytes leave". A cap stored as configuration is a promise by
// the operator; a cap checked after the transfer has already happened is not a
// cap. So:
//
//   - the running total is a SUM over an append-only log (never a column, so it
//     cannot be edited into agreement with a lie);
//   - the check and the log write happen in the SAME transaction, so two
//     concurrent fetches cannot both pass a check only one of them fits in;
//   - and ServeReplica is the only way to record bytes, so a caller cannot
//     serve without being counted.
//
// # WHY A NODE WITH NO BUDGET SERVES NOTHING
//
// The default had to be chosen, and "no budget means unlimited" is the reading
// that turns a fresh install into an open relay. ErrNoBudget is therefore
// distinct from ErrBudgetExhausted: the first is "this node never opted in",
// the second is "this node opted in and is full", and an operator needs to tell
// them apart to fix them.

const (
	meshServeLogTable    = "mesh_replication_serve_log"
	meshServeBudgetTable = "mesh_replication_budget"
)

var (
	// ErrNoBudget means the node has no budget row for the current period. It is
	// not exhaustion -- it is the safe default for a node that never opted in.
	ErrNoBudget = errors.New("no serve budget for this node and period")

	// ErrBudgetExhausted means the budget exists and this fetch does not fit in
	// what is left of it.
	ErrBudgetExhausted = errors.New("serve budget exhausted")
)

// MeshServeStore records what this node has served to the mesh, and refuses a
// fetch that would exceed the budget for the current period.
type MeshServeStore struct {
	repository
	db *Database
}

func NewMeshServeStore(db *Database) *MeshServeStore {
	return &MeshServeStore{
		repository: repository{tableName: meshServeLogTable, idColumn: "id"},
		db:         db,
	}
}

// CurrentPeriod is the budget period a serve belongs to: 'YYYY-MM' in UTC.
//
// UTC deliberately, not local: a budget that resets at a different wall clock in
// every timezone is a budget whose fleet-wide peak is unbounded, because every
// node's peak lands on a different hour.
func CurrentPeriod(now time.Time) string {
	return now.UTC().Format("2006-01")
}

// nextPeriod is the exclusive upper bound of the current period, so the SUM is
// one indexed range rather than a strftime on every row.
func nextPeriod(period string) string {
	t, err := time.Parse("2006-01", period)
	if err != nil {
		return period
	}
	return t.AddDate(0, 1, 0).Format("2006-01")
}

func periodBounds(period string) (string, string) {
	return period + "-01 00:00:00", nextPeriod(period) + "-01 00:00:00"
}

// SetBudget records the byte budget for a node and period, replacing any
// previous value.
//
// Replacing rather than rejecting a second write is deliberate: lowering a
// budget must be possible while the mesh is running, or an operator who notices
// they are being used as a relay has no way to stop it except by deleting the
// node from the mesh entirely.
func (s *MeshServeStore) SetBudget(ctx context.Context, nodeID, period string, budgetBytes int64) error {
	if budgetBytes < 0 {
		return fmt.Errorf("setting serve budget: budget must not be negative, got %d", budgetBytes)
	}
	if nodeID == "" {
		return errors.New("setting serve budget: node id is required")
	}
	if _, err := dbWrapper.Exec(ctx, fmt.Sprintf(
		"INSERT INTO %s (node_id, period, budget_bytes) VALUES (?, ?, ?) "+
			"ON CONFLICT(node_id, period) DO UPDATE SET budget_bytes = excluded.budget_bytes",
		meshServeBudgetTable),
		nodeID, period, budgetBytes); err != nil {
		return fmt.Errorf("setting serve budget: %w", err)
	}
	return nil
}

// ServedBytes is the running total for a node and period: the computed value,
// never a stored one. This is the SUM the cap is checked against.
func (s *MeshServeStore) ServedBytes(ctx context.Context, nodeID, period string) (int64, error) {
	from, to := periodBounds(period)
	var served sql.NullInt64
	if err := dbWrapper.Get(ctx, &served, fmt.Sprintf(
		"SELECT SUM(bytes) FROM %s WHERE served_at >= ? AND served_at < ?",
		meshServeLogTable), from, to); err != nil {
		return 0, fmt.Errorf("summing served bytes: %w", err)
	}
	if !served.Valid {
		return 0, nil
	}
	return served.Int64, nil
}

// BudgetFor returns the configured budget and whether one exists.
func (s *MeshServeStore) BudgetFor(ctx context.Context, nodeID, period string) (int64, bool, error) {
	var budget sql.NullInt64
	err := dbWrapper.Get(ctx, &budget, fmt.Sprintf(
		"SELECT budget_bytes FROM %s WHERE node_id = ? AND period = ?",
		meshServeBudgetTable), nodeID, period)
	// Absent is (0, false, nil), not an error: asking whether a budget exists is
	// the question, so "no" is a legitimate answer. See the ErrNoRows handling in
	// ServeReplica for why this is not interchangeable with models.ErrNotFound.
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("reading serve budget: %w", err)
	}
	return budget.Int64, budget.Valid, nil
}

// ServeReplica records that `bytes` were served for `contentHash`, or refuses.
//
// THE TRANSACTION IS THE CAP. Checking the budget and writing the log row in one
// transaction is what makes the cap real under concurrency: two simultaneous
// fetches each read the same running total, and without the transaction both
// would pass a check only one of them fits in. SQLite serialises writers, so the
// second one re-reads a total that now includes the first.
//
// The budget row is read inside the same transaction for the same reason: a
// budget lowered between check and write must not be honoured by an in-flight
// fetch.
func (s *MeshServeStore) ServeReplica(ctx context.Context, nodeID, contentHash string, bytes int64, now time.Time) error {
	if bytes < 0 {
		return fmt.Errorf("recording serve: bytes must not be negative, got %d", bytes)
	}
	// The content hash IS the capability -- probe 1 established that the
	// transports disclose peer addresses and no design here hides them, so
	// authorisation is possession of the hash and there is no identity to check.
	// An empty hash would be authorisation by anyone saying nothing.
	if contentHash == "" {
		return errors.New("recording serve: content hash is the capability, so it may not be empty")
	}
	if nodeID == "" {
		return errors.New("recording serve: node id is required")
	}

	period := CurrentPeriod(now)
	from, to := periodBounds(period)

	// getTx reports (nil, error) when there is NO transaction and (tx, nil) when
	// there is one -- the opposite of the usual shape. Checking the error rather
	// than the tx is the only correct reading of it.
	tx := ctx
	ownTransaction := false
	if _, err := getTx(ctx); err != nil {
		var err error
		tx, err = s.db.Begin(ctx, true)
		if err != nil {
			return fmt.Errorf("beginning the serve transaction: %w", err)
		}
		ownTransaction = true
	}
	// Only when this call opened the transaction: rolling back one the caller
	// owns would discard the caller's earlier work on the way past.
	if ownTransaction {
		defer func() { _ = s.db.Rollback(tx) }()
	}

	var budget sql.NullInt64
	err := dbWrapper.Get(tx, &budget, fmt.Sprintf(
		"SELECT budget_bytes FROM %s WHERE node_id = ? AND period = ?",
		meshServeBudgetTable), nodeID, period)
	// `sql.ErrNoRows` is the ABSENCE of a budget, which is the safe default and
	// not a failure -- so it is translated here into ErrNoBudget and nowhere
	// else. This package has two not-found conventions and they are not
	// interchangeable: dbWrapper.Get (raw sqlx) returns sql.ErrNoRows, while the
	// goqu stores return models.ErrNotFound. Checking the wrong one means this
	// refusal never fires and the caller gets an error naming a column instead
	// of an instruction -- which is exactly what the first run of this test
	// reported.
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: node %s period %s", ErrNoBudget, nodeID, period)
	}
	if err != nil {
		return fmt.Errorf("reading serve budget: %w", err)
	}
	if !budget.Valid {
		// A row that exists with a NULL budget. Same answer: refuse, do not
		// default to unlimited. See ErrNoBudget.
		return fmt.Errorf("%w: node %s period %s (budget is NULL)", ErrNoBudget, nodeID, period)
	}

	var served sql.NullInt64
	if err := dbWrapper.Get(tx, &served, fmt.Sprintf(
		"SELECT SUM(bytes) FROM %s WHERE served_at >= ? AND served_at < ?",
		meshServeLogTable), from, to); err != nil {
		return fmt.Errorf("summing served bytes: %w", err)
	}
	var already int64
	if served.Valid {
		already = served.Int64
	}

	if already+bytes > budget.Int64 {
		return fmt.Errorf("%w: %d + %d > %d for node %s period %s",
			ErrBudgetExhausted, already, bytes, budget.Int64, nodeID, period)
	}

	if _, err := dbWrapper.Exec(tx, fmt.Sprintf(
		"INSERT INTO %s (content_hash, bytes, served_at) VALUES (?, ?, ?)",
		meshServeLogTable), contentHash, bytes, now.UTC().Format("2006-01-02 15:04:05")); err != nil {
		return fmt.Errorf("recording serve: %w", err)
	}

	if ownTransaction {
		if err := s.db.Commit(tx); err != nil {
			return fmt.Errorf("committing the serve transaction: %w", err)
		}
	}
	return nil
}
