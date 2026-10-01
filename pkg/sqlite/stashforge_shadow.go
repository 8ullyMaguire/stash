package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/stashapp/stash/internal/collab"
	"gopkg.in/guregu/null.v4"
)

// Shadow governance persistence: the comparison between the weighted rule and
// flat quorum that §5.3's open weight-table question needs before anyone can
// answer it with evidence.
//
// The runtime rule is unchanged. Flat quorum still decides every proposal; this
// table only records what the weighted path would have said.

// Compile-time proof of the interface, so a signature drift fails here rather
// than at the first call from a resolver.
var _ collab.ShadowStore = (*ShadowLogStore)(nil)

const shadowLogTable = "governance_shadow_log"

type ShadowLogStore struct{}

func NewShadowLogStore() *ShadowLogStore { return &ShadowLogStore{} }

// Record appends one comparison.
func (s *ShadowLogStore) Record(ctx context.Context, r collab.ShadowRecord) error {
	query := fmt.Sprintf(
		"INSERT INTO %s (proposal_id, target_type, field, flat_decision, "+
			"weighted_decision, reason, weighted_total, net, voted, ballots, flagged) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		shadowLogTable)

	_, err := dbWrapper.Exec(ctx, query,
		r.ProposalID, r.TargetType, r.Field,
		// .String(), not string(...): Decision is an int enum, so a direct
		// conversion yields one rune of a digit rather than the word. go vet
		// catches this -- it is exactly the kind of mistake that would have
		// written "\x00" into every shadow row and then read back as pending.
		r.Flat.String(), r.Weighted.String(), null.StringFrom(r.Reason),
		r.WeightedTotal, r.Net, r.Voted, r.Ballots, r.Flagged)
	return err
}

// shadowColumns is the read set, in a fixed order that scanShadowRow depends on.
// Kept as one string because the column list and the Scan targets must not drift
// apart: they are two halves of one statement and nothing in the compiler links
// them.
const shadowColumns = `proposal_id, target_type, field, flat_decision, ` +
	`weighted_decision, reason, weighted_total, net, voted, ballots, flagged`

// Disagreements returns the rows where the two functions differed, newest
// first.
//
// The comparison is written as SQL rather than filtered in Go. Almost every row
// agrees, so the filter is the point: pulling the whole table and discarding the
// agreements would mean an instance accumulates a governance log it can no longer
// read in reasonable time, and the log grows forever.
func (s *ShadowLogStore) Disagreements(ctx context.Context, limit int) ([]collab.ShadowRecord, error) {
	// A limit of zero or less means "all of them". A moderation view that
	// silently shows only the first page is worse than one that shows
	// everything, because a truncated list reads as complete.
	query := fmt.Sprintf(
		"SELECT %s FROM %s WHERE flat_decision <> weighted_decision ORDER BY %s DESC, id DESC",
		shadowColumns, shadowLogTable, "at")
	if limit > 0 {
		query = fmt.Sprintf("%s LIMIT %d", query, limit)
	}

	return s.queryRecords(ctx, query)
}

// Summary counts, over every record, how the two functions compared.
//
// Computed by SQL from the rows rather than maintained as a counter, per
// non-negotiable #4: a counter is a second source of truth, and this is the
// number an operator would use to justify switching governance over, so it is
// the last number that should be able to disagree with its own rows.
func (s *ShadowLogStore) Summary(ctx context.Context) (collab.ShadowSummary, error) {
	var out collab.ShadowSummary

	// One pass. Every SUM is COALESCEd to 0, because SUM over an empty set is
	// NULL rather than 0 in SQL, and scanning a NULL into an int is a hard
	// error -- so an instance that had never held a vote could not read its own
	// summary at all. COALESCE is the fix; `IFNULL` would do the same but this
	// codebase uses COALESCE elsewhere.
	//
	// Splitting the three disagreement directions is a CASE on the same grouping
	// the index on (flat_decision, weighted_decision) already provides, so the
	// query stays one index scan rather than three.
	// Concatenated quoted strings rather than one raw literal: SQL identifiers
	// want backticks for readability, and backticks cannot appear inside a Go raw
	// string literal. Double-quoted SQL identifiers are accepted by SQLite as
	// identifiers (not as string literals, which is the actual footgun there).
	query := "SELECT " +
		"COUNT(*) AS evaluations, " +
		"COALESCE(SUM(CASE WHEN flat_decision = weighted_decision THEN 1 ELSE 0 END), 0) AS agreed, " +
		"COALESCE(SUM(CASE WHEN flat_decision = 'pending' AND weighted_decision <> 'pending' " +
		"    THEN 1 ELSE 0 END), 0) AS would_accept, " +
		"COALESCE(SUM(CASE WHEN weighted_decision = 'pending' AND flat_decision <> 'pending' " +
		"    THEN 1 ELSE 0 END), 0) AS would_hold, " +
		"COALESCE(SUM(CASE WHEN flat_decision <> 'pending' AND weighted_decision <> 'pending' " +
		"          AND flat_decision <> weighted_decision THEN 1 ELSE 0 END), 0) AS would_flip " +
		"FROM " + shadowLogTable

	// A struct, because dbWrapper.Get scans into one -- it takes a single
	// destination, not a slice of pointers, and returns "must pass a pointer,
	// not a value" otherwise.
	var row struct {
		Evaluations int `db:"evaluations"`
		Agreed      int `db:"agreed"`
		WouldAccept int `db:"would_accept"`
		WouldHold   int `db:"would_hold"`
		WouldFlip   int `db:"would_flip"`
	}
	if err := dbWrapper.Get(ctx, &row, query); err != nil {
		return out, err
	}

	out.Evaluations = row.Evaluations
	out.Agreed = row.Agreed
	out.WouldAccept = row.WouldAccept
	out.WouldHold = row.WouldHold
	out.WouldFlip = row.WouldFlip
	return out, nil
}

func (s *ShadowLogStore) queryRecords(ctx context.Context, query string) ([]collab.ShadowRecord, error) {
	rows, err := dbWrapper.QueryxContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []collab.ShadowRecord{}
	for rows.Next() {
		var (
			r                                        collab.ShadowRecord
			flat, weighted                           string
			reason                                   sql.NullString
			weightedTotal, net, voted, ballots, flag sql.NullInt64
		)
		if err := rows.Scan(&r.ProposalID, &r.TargetType, &r.Field, &flat, &weighted,
			&reason, &weightedTotal, &net, &voted, &ballots, &flag); err != nil {
			return nil, err
		}
		// An unrecognised string decodes to pending, which ParseDecision
		// documents as the conservative direction. The boolean is deliberately
		// dropped: a single odd row in an observational log is not a reason to
		// fail the whole query, and the pending default is the safe reading.
		r.Flat, _ = collab.ParseDecision(flat)
		r.Weighted, _ = collab.ParseDecision(weighted)
		r.Reason = reason.String
		r.WeightedTotal = int(weightedTotal.Int64)
		r.Net = int(net.Int64)
		r.Voted = int(voted.Int64)
		r.Ballots = int(ballots.Int64)
		r.Flagged = int(flag.Int64)
		out = append(out, r)
	}
	return out, rows.Err()
}
