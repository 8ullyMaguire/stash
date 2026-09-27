// The PersonCluster store.
//
// # The property this file is built around
//
// **A cluster is unnamed by default, and naming one is a separate, recorded
// act.** Not a fallback, not an empty string standing in for an absent name --
// an unnamed cluster HAS NO NAME.
//
// The reason is in the spec (§7.1): a person is a row a scraper found a name
// for, so for a corpus with no scraper the person is fragmented across every
// appearance with no thread. Requiring a name before linking is requiring the
// thing that is missing. The item links to a CLUSTER, and the cluster is not
// yet a performer.
//
// That decision is easy to state and easy to lose. A CRUD helper that takes a
// name, a struct with `Name string`, a GraphQL type where `name: String!` --
// each of those reintroduces "unnamed" as an empty string, and an empty string
// is a VALUE a user interface will happily display, filter on, and sort by. So:
//
//   - Name is a POINTER, and nil is the normal state
//   - the only way to set one is NameCluster, which writes the audit row
//   - there is no Update that accepts a name
//
// # Why the audit table is separate
//
// A name attached to a cluster is a claim about a person's identity that the
// system inferred and a human accepted. Six months later, when it turns out to
// be two people, the only question that matters is who decided and when. A name
// written in place, overwriting the previous one, answers nothing -- so the
// history lives in its own APPEND-ONLY table and a rename is a new row rather
// than an update.
//
// `person_clusters.name` is a CACHE of the latest name, not the record. Every
// write goes through the audit table, and a test asserts the two agree.

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ClusterStore persists person clusters and their members.
//
// It goes through the package's `dbWrapper` rather than holding its own
// *sql.DB, for a reason that is not stylistic: dbWrapper is transaction-aware.
// The tests run every statement inside a rolled-back transaction so each gets
// private rows and leaves nothing behind, and a store holding its own
// connection would write to the real database while the test believed it was
// writing to a transaction. That is a class of bug where the suite passes and
// the fixture data is destroyed.
type ClusterStore struct {
	tableName string
}

func NewClusterStore() *ClusterStore {
	return &ClusterStore{tableName: "person_clusters"}
}

// Cluster is a persisted person cluster.
//
// Name is a POINTER. See the file comment: nil is the normal state, and a
// non-pointer string would make "unnamed" indistinguishable from "named with
// the empty string", which is a value every consumer downstream will display.
// The sqlx tag does the work -- a NULL column scans into a *string as nil, so
// the distinction survives from the row to GraphQL without a conversion helper
// that could get it wrong.
type Cluster struct {
	ID     int64   `db:"id"`
	State  string  `db:"state"`
	Engine string  `db:"engine"`
	Handle *string `db:"handle"`
	Name   *string `db:"name"`
	Avatar *string `db:"avatar_path"`

	// Confidence is a POINTER because the column is nullable and NULL is a
	// distinct statement from 0.0: a singleton has no merge decision behind it,
	// so a confidence for it would be an invented number. A UI that has to say
	// "nothing to show" apart from "shown, and it is zero" needs both.
	Confidence *float64 `db:"confidence"`

	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

// The four states, mirroring the CHECK on `person_clusters.state`.
//
// Named as constants rather than string literals at call sites, because the
// state machine is the part of this feature most likely to grow a fifth value
// and a literal is a value that has to be found before it can be changed.
const (
	StateSingleton = "singleton"
	StateSettled   = "settled"
	StateAmbiguous = "ambiguous"
	StateMerged    = "merged"
)

// ValidState reports whether a state is one the schema allows.
//
// The CHECK constraint already refuses anything else, so this is belt and
// braces -- and it earns its place by producing an error that says which value
// was wrong, where the constraint produces "CHECK constraint failed".
func ValidState(state string) bool {
	switch state {
	case StateSingleton, StateSettled, StateAmbiguous, StateMerged:
		return true
	}
	return false
}

// NameRecord is one entry in a cluster's name history.
//
// It lives in the store rather than in `internal/cluster` because the GraphQL
// layer describes a naming event, not a naming algorithm, and a consumer that
// has to import the database package to say "this was named by alice on
// Tuesday" has the layering backwards.
type NameRecord struct {
	ClusterID int64     `db:"cluster_id"`
	Name      string    `db:"name"`
	Actor     string    `db:"actor"`
	CreatedAt time.Time `db:"created_at"`
}

// Member is one face belonging to a cluster.
type Member struct {
	ClusterID   int64     `db:"cluster_id"`
	TargetType  string    `db:"target_type"`
	TargetID    int64     `db:"target_id"`
	FrameIndex  int       `db:"frame_index"`
	FaceLeft    int       `db:"face_left"`
	FaceTop     int       `db:"face_top"`
	FaceWidth   int       `db:"face_width"`
	FaceHeight  int       `db:"face_height"`
	DetectScore float64 `db:"detector_score"`
	Distance    float64 `db:"distance"`
	Embedding   []byte  `db:"embedding"`
}

const clusterColumns = `id, state, engine, handle, name, avatar_path,
	confidence, created_at, updated_at`

// No created_at: migration 97 gave person_cluster_members no timestamp, and the
// column was being selected anyway. The review order is by DISTANCE, which is
// the one that answers "why are these linked", so a created_at here would be a
// second ordering nobody reads. Adding it is a migration for a column with no
// reader, which is the wrong order to do things in.
const memberColumns = `cluster_id, target_type, target_id, frame_index,
	face_left, face_top, face_width, face_height,
	detector_score, distance, embedding`

// CreateCluster inserts a cluster with NO name.
//
// There is deliberately no `name` parameter. A caller that wants to name a
// cluster calls CreateCluster and then NameCluster, so the audit row always
// exists. Making naming part of creation is exactly how a system ends up with
// named clusters and no record of who named them.
func (s *ClusterStore) CreateCluster(ctx context.Context, confidence *float64) (int64, error) {
	if confidence != nil && (*confidence < 0 || *confidence > 1) {
		return 0, fmt.Errorf("confidence %v is outside 0..1", *confidence)
	}

	// The pointer is passed straight through, so a caller with no confidence
	// claim writes NULL rather than 0.0. The column's nullability exists to
	// express exactly that, and this is the only write that has to honour it.
	res, err := dbWrapper.Exec(ctx,
		`INSERT INTO person_clusters (confidence) VALUES (?)`, confidence)
	if err != nil {
		return 0, fmt.Errorf("insert person cluster: %w", err)
	}
	if err := expectOneRow(res, "insert person cluster"); err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("insert person cluster: last insert id: %w", err)
	}
	return id, nil
}

// NameCluster sets a cluster's name and records who set it.
//
// Both statements run in the CALLER's transaction -- the store opens none of its
// own. That is a deliberate departure from the obvious design and it is what
// makes the pair atomic: a name without its audit row is the one state this
// design exists to prevent, and a store that opened its own transaction could
// not enlist the caller's, so a concurrent ClearName could interleave between
// the two writes and leave a name with no record of who set it.
func (s *ClusterStore) NameCluster(ctx context.Context, clusterID int64, name, actor string) error {
	if name == "" {
		return fmt.Errorf("a name is required to name a cluster; to UNNAME one " +
			"call ClearName, so the two acts are never confused")
	}
	if actor == "" {
		// Not defaulted to "system". An audit row saying "system" for a name a
		// human typed is worse than no row: it looks like an attribution and is
		// not one. So the caller must pass the value, even when it is "system".
		return fmt.Errorf("an actor is required to name a cluster; 'system' is a " +
			"valid value but must be passed explicitly")
	}

	now := time.Now().UTC()
	res, err := dbWrapper.Exec(ctx,
		`UPDATE person_clusters SET name = ?, updated_at = ? WHERE id = ?`,
		name, now, clusterID)
	if err != nil {
		return fmt.Errorf("set cluster name: %w", err)
	}
	if err := expectOneRow(res, "name cluster %d", clusterID); err != nil {
		return err
	}

	res, err = dbWrapper.Exec(ctx,
		`INSERT INTO person_cluster_names (cluster_id, name, actor, created_at)
		 VALUES (?, ?, ?, ?)`,
		clusterID, name, actor, now)
	if err != nil {
		return fmt.Errorf("record cluster name audit: %w", err)
	}
	return expectOneRow(res, "record the name audit for cluster %d", clusterID)
}

// ClearName removes a cluster's current name, keeping the history.
//
// Renaming is not deletion. Six months later the question "was this always this
// person?" is answerable only if the previous name is still on file.
func (s *ClusterStore) ClearName(ctx context.Context, clusterID int64) error {
	res, err := dbWrapper.Exec(ctx,
		`UPDATE person_clusters SET name = NULL, updated_at = ? WHERE id = ?`,
		time.Now().UTC(), clusterID)
	if err != nil {
		return fmt.Errorf("clear cluster name: %w", err)
	}
	return expectOneRow(res, "clear the name on cluster %d", clusterID)
}

// SetHandle sets the cluster's stable external label.
func (s *ClusterStore) SetHandle(ctx context.Context, clusterID int64, handle string) error {
	if handle == "" {
		return fmt.Errorf("a handle is required; to remove one call ClearHandle")
	}
	res, err := dbWrapper.Exec(ctx,
		`UPDATE person_clusters SET handle = ?, updated_at = ? WHERE id = ?`,
		handle, time.Now().UTC(), clusterID)
	if err != nil {
		return fmt.Errorf("set cluster handle: %w", err)
	}
	return expectOneRow(res, "set the handle on cluster %d", clusterID)
}

// SetState transitions a cluster's state.
//
// ValidState is checked here as well as by the CHECK, for the reason above: an
// error naming the bad value is worth more than a constraint message.
func (s *ClusterStore) SetState(ctx context.Context, clusterID int64, state string) error {
	if !ValidState(state) {
		return fmt.Errorf("state %q is not one of %s/%s/%s/%s",
			state, StateSingleton, StateSettled, StateAmbiguous, StateMerged)
	}
	res, err := dbWrapper.Exec(ctx,
		`UPDATE person_clusters SET state = ?, updated_at = ? WHERE id = ?`,
		state, time.Now().UTC(), clusterID)
	if err != nil {
		return fmt.Errorf("set cluster state: %w", err)
	}
	return expectOneRow(res, "set the state on cluster %d", clusterID)
}

// GetCluster loads a cluster by id.
//
// No result is an error rather than a nil cluster with a nil error, because a
// caller that forgets to check nil will otherwise dereference a struct that
// looks real.
func (s *ClusterStore) GetCluster(ctx context.Context, id int64) (*Cluster, error) {
	var c Cluster
	err := dbWrapper.Get(ctx, &c,
		`SELECT `+clusterColumns+` FROM person_clusters WHERE id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("get cluster %d: %w", id, err)
	}
	return &c, nil
}

// ListClusters returns clusters, newest first, with a bounded page.
//
// Paging rather than a bare SELECT because a library with no scraper can hold
// hundreds of thousands of unnamed clusters, and the UI lists them. The limit is
// REQUIRED, so "give me everything" has to be said out loud and paid for.
func (s *ClusterStore) ListClusters(ctx context.Context, limit, offset int) ([]*Cluster, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("limit %d must be positive; a caller that wants "+
			"everything must say so and accept the cost", limit)
	}

	var out []*Cluster
	err := dbWrapper.Select(ctx, &out,
		`SELECT `+clusterColumns+` FROM person_clusters
		 ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list clusters: %w", err)
	}
	return out, nil
}

// ListUnnamed returns clusters with no name, oldest first, with the total count
// still unnamed.
//
// Oldest first because that is the REVIEW ORDER: the cluster that has been
// unnamed longest is the one a user is most likely to have an opinion about.
// Newest-first -- which is what ListClusters uses -- would put the clusters the
// pipeline just made at the top, and those are the ones nobody has had time to
// look at yet.
//
// The count is returned alongside the page so a UI that renders "showing 50 of
// 4,200" has the number without a second query. It is taken AFTER the page, so
// the two cannot straddle a concurrent write and report a total that disagrees
// with the rows on screen.
func (s *ClusterStore) ListUnnamed(ctx context.Context, limit int) (clusters []*Cluster, reviewable int64, err error) {
	if limit <= 0 {
		return nil, 0, fmt.Errorf("limit %d must be positive", limit)
	}

	clusters = make([]*Cluster, 0, limit)
	if err := dbWrapper.Select(ctx, &clusters,
		`SELECT `+clusterColumns+` FROM person_clusters
		 WHERE name IS NULL ORDER BY created_at ASC, id ASC LIMIT ?`, limit); err != nil {
		return nil, 0, fmt.Errorf("list unnamed clusters: %w", err)
	}

	reviewable, err = s.UnnamedCount(ctx)
	if err != nil {
		return nil, 0, err
	}
	return clusters, reviewable, nil
}

// UnnamedCount reports how many clusters have no name.
//
// It exists because the unnamed case is the NORMAL case, so a UI needs to say
// "4,200 clusters, none named" without loading 4,200 rows to find out. Without
// it the natural implementation is to list everything and count in Go, which is
// a table scan on the largest table in the feature. The partial index on
// (created_at, id) WHERE name IS NULL makes this a range count.
func (s *ClusterStore) UnnamedCount(ctx context.Context) (int64, error) {
	var n int64
	if err := dbWrapper.Get(ctx, &n,
		`SELECT COUNT(*) FROM person_clusters WHERE name IS NULL`); err != nil {
		return 0, fmt.Errorf("count unnamed clusters: %w", err)
	}
	return n, nil
}

// AddMember attaches an appearance to a cluster.
//
// The embedding is a required argument rather than read from a detector global:
// the membership row and the vector that justified it are written together, so
// a member can never exist whose embedding was lost, and the over-merge guard
// can load a cluster's distances in one query without a second round trip.
func (s *ClusterStore) AddMember(ctx context.Context, m Member) error {
	if m.TargetType == "" {
		return fmt.Errorf("target_type is required; a face belongs to a target and " +
			"a member with no target cannot be shown to anyone")
	}
	if m.Distance < 0 {
		return fmt.Errorf("distance %v is negative", m.Distance)
	}
	if len(m.Embedding) == 0 {
		return fmt.Errorf("embedding is required; a member whose embedding was " +
			"not stored cannot be compared, and the cluster it joined cannot be " +
			"re-checked without re-running detection on the whole library")
	}

	res, err := dbWrapper.Exec(ctx,
		`INSERT INTO person_cluster_members
		   (cluster_id, target_type, target_id, frame_index,
		    face_left, face_top, face_width, face_height,
		    detector_score, distance, embedding)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ClusterID, m.TargetType, m.TargetID, m.FrameIndex,
		m.FaceLeft, m.FaceTop, m.FaceWidth, m.FaceHeight,
		m.DetectScore, m.Distance, m.Embedding)
	if err != nil {
		return fmt.Errorf("add cluster member: %w", err)
	}
	return expectOneRow(res, "add a member to cluster %d", m.ClusterID)
}

// RemoveMember detaches an appearance from a cluster.
//
// Identified by target rather than by row id, because the caller knows which
// face it is looking at, and a row id would have to be looked up first.
func (s *ClusterStore) RemoveMember(ctx context.Context, clusterID int64, targetType string, targetID int64, frameIndex int) error {
	res, err := dbWrapper.Exec(ctx,
		`DELETE FROM person_cluster_members
		 WHERE cluster_id = ? AND target_type = ? AND target_id = ? AND frame_index = ?`,
		clusterID, targetType, targetID, frameIndex)
	if err != nil {
		return fmt.Errorf("remove cluster member: %w", err)
	}
	return expectOneRow(res, "remove a member from cluster %d", clusterID)
}

// Members returns a cluster's members, nearest first.
//
// Nearest rather than oldest, and the reason is that this list is what a
// reviewer reads when asking "why are these two linked". The nearest face is the
// strongest evidence and belongs at the top; a face that joined at 0.9 because
// nothing nearer existed is the one worth questioning, and putting it first
// buries the answer.
func (s *ClusterStore) Members(ctx context.Context, clusterID int64) ([]Member, error) {
	var out []*Member
	err := dbWrapper.Select(ctx, &out,
		`SELECT `+memberColumns+` FROM person_cluster_members
		 WHERE cluster_id = ?
		 ORDER BY distance ASC, target_type ASC, target_id ASC, frame_index ASC`,
		clusterID)
	if err != nil {
		return nil, fmt.Errorf("list cluster members: %w", err)
	}

	members := make([]Member, 0, len(out))
	for _, m := range out {
		members = append(members, *m)
	}
	return members, nil
}

// MemberCount reports how many appearances a cluster holds.
//
// The spec says a cluster is "meaningless to a user until it has three or more
// appearances", so the UI needs this constantly and computing it by loading
// every member row is the obvious way to get it wrong.
func (s *ClusterStore) MemberCount(ctx context.Context, clusterID int64) (int, error) {
	var n int
	if err := dbWrapper.Get(ctx, &n,
		`SELECT COUNT(*) FROM person_cluster_members WHERE cluster_id = ?`,
		clusterID); err != nil {
		return 0, fmt.Errorf("count cluster members: %w", err)
	}
	return n, nil
}

// ClustersForTarget reports which clusters claim an appearance.
//
// This is the query the ambiguous state exists to serve. A face claimed by two
// clusters is not a duplicate to be silently resolved -- it is conflicting
// evidence the UI must show both candidates for, and a result set longer than
// one is the signal.
func (s *ClusterStore) ClustersForTarget(ctx context.Context, targetType string, targetID int64, frameIndex int) ([]int64, error) {
	var ids []int64
	err := dbWrapper.Select(ctx, &ids,
		`SELECT DISTINCT cluster_id FROM person_cluster_members
		 WHERE target_type = ? AND target_id = ? AND frame_index = ?`,
		targetType, targetID, frameIndex)
	if err != nil {
		return nil, fmt.Errorf("clusters for target: %w", err)
	}
	// sqlx leaves a nil slice for zero rows, which is the right value -- an
	// unclaimed appearance is a legitimate answer, not an error.
	return ids, nil
}

// NameHistory returns every name a cluster has ever had, oldest first.
//
// Append-only by construction: this file has no UPDATE or DELETE path against
// person_cluster_names, so the history cannot be edited through the store.
func (s *ClusterStore) NameHistory(ctx context.Context, clusterID int64) ([]NameRecord, error) {
	var out []*NameRecord
	err := dbWrapper.Select(ctx, &out,
		`SELECT cluster_id, name, actor, created_at
		 FROM person_cluster_names WHERE cluster_id = ?
		 ORDER BY created_at ASC, id ASC`, clusterID)
	if err != nil {
		return nil, fmt.Errorf("list cluster name history: %w", err)
	}

	records := make([]NameRecord, 0, len(out))
	for _, r := range out {
		records = append(records, *r)
	}
	return records, nil
}

// expectOneRow refuses a silent no-op.
//
// Every write in this file goes through it, because a statement that matched
// zero rows and returned no error is indistinguishable from one that worked --
// until a caller adds a face to a cluster that does not exist and finds out
// later, from a listing that does not show it.
func expectOneRow(res sql.Result, what string, args ...interface{}) error {
	prefix := what
	if len(args) > 0 {
		prefix = fmt.Sprintf(what, args...)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: the database did not report a row count: %w", prefix, err)
	}
	if affected == 0 {
		return fmt.Errorf("%s: the statement matched no rows", prefix)
	}
	return nil
}
