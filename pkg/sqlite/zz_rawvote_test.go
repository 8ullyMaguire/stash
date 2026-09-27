//go:build integration

package sqlite

import "context"

// DbgRawVote inserts a vote row directly, bypassing ProposalVoteStore.Cast and
// therefore bypassing its Go-level value check. Exists so a test can prove the
// SCHEMA refuses a bad weight on its own; without it, the CHECK is a second
// line of defence that nothing exercises.
func DbgRawVote(ctx context.Context, proposalID, userID, value int) (int64, error) {
	res, err := dbWrapper.Exec(ctx,
		"INSERT INTO proposal_votes (proposal_id, user_id, value) VALUES (?, ?, ?)",
		proposalID, userID, value)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
