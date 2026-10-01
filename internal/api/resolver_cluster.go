package api

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/pkg/sqlite"
)

// The identity-cluster resolvers.
//
// # The property this file is built around
//
// **A cluster is unnamed until a human names it, and the schema says so with a
// nullable field.** Every conversion below goes through the pointer types rather
// than through a deref-and-default. The tempting version is
// `*out.Name = "Unknown"` with a comment saying it is a placeholder, and it is
// wrong for a reason that only shows up months later: once stored, "Unknown" is
// indistinguishable from a name a scraper returned, so a user cannot tell which
// clusters they have actually reviewed. The null is load-bearing and this file
// keeps it.
//
// The second property: **naming is a recorded act.** `NamePersonCluster` calls
// the store, which writes the current name and an audit row in one transaction.
// There is no resolver that sets a name without the audit, and no path that
// deletes one. `ClearPersonClusterName` is not that path -- it un-names and
// keeps the history, which is a different act and is named differently so a
// blank form field cannot become an unnamed cluster that the log claims was
// named "".
//
// These are resolvers, not services. The rules live in the store where they hold
// for the pipeline and the job too, because a governance property enforced only
// on the GraphQL path is not a property of the system.

// Client-facing errors, phrased as answers rather than identifiers. A caller
// told only "invalid" cannot tell a missing cluster from a forbidden one, and
// those need different responses.
var (
	errClusterNotFound = errors.New("no such cluster")
	// Deliberately not "invalid name": the caller sent an empty string, and
	// telling them so is more useful than telling them the value was rejected.
	// The distinction that matters to them is "use clearPersonClusterName
	// instead", which is in the message.
	errClusterNameEmpty = errors.New("a cluster name cannot be empty; " +
		"to remove a name, use clearPersonClusterName")
	errClusterLimitRequired = errors.New("a limit is required: a library with " +
		"no scraper can hold hundreds of thousands of unnamed clusters")
)

func clusterStore() *sqlite.ClusterStore {
	return manager.GetInstance().PersonClusters
}

func clusterIDFromString(s string) (int64, error) {
	// Parsed as int64, not int. Cluster ids are INTEGER PRIMARY KEY in SQLite,
	// which is int64 on every platform, and a narrower parse would refuse an id
	// the database happily holds -- producing "no such cluster" for a cluster
	// that exists, which is the worst possible error for a lookup.
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q is not a cluster id", errClusterNotFound, s)
	}
	return id, nil
}

// --- GraphQL model conversion --------------------------------------------

// personClusterModel builds the GraphQL type from the row.
//
// Every optional field goes through a pointer for the reason in the file header,
// and the members are NOT loaded here: `PersonCluster.members` is a field
// resolver (below) so a list of 20 clusters does not read every member of every
// cluster to render 20 rows.
func personClusterModel(c sqlite.Cluster) *PersonCluster {
	return &PersonCluster{
		ID:         strconv.FormatInt(c.ID, 10),
		Name:       c.Name,
		Handle:     c.Handle,
		Avatar:     c.Avatar,
		Confidence: c.Confidence,
		State:      c.State,
		Engine:     c.Engine,
		CreatedAt:  c.CreatedAt,
		UpdatedAt:  c.UpdatedAt,
	}
}

// clusterModels converts a page of rows. The store returns POINTERS because a
// Get of a missing cluster has to be able to say "none" without a zero value
// that looks like a real row; taking []*Cluster keeps that decision in one place
// instead of making every caller remember it.
func clusterModels(rows []*sqlite.Cluster) []*PersonCluster {
	out := make([]*PersonCluster, 0, len(rows))
	for _, c := range rows {
		out = append(out, personClusterModel(*c))
	}
	return out
}

func personClusterMemberModel(m sqlite.Member) *PersonClusterMember {
	return &PersonClusterMember{
		ClusterID:   strconv.FormatInt(m.ClusterID, 10),
		TargetType:  m.TargetType,
		TargetID:    strconv.FormatInt(m.TargetID, 10),
		FrameIndex:  int(m.FrameIndex),
		Left:        m.FaceLeft,
		Top:         m.FaceTop,
		Width:       m.FaceWidth,
		Height:      m.FaceHeight,
		DetectScore: m.DetectScore,
		Distance:    m.Distance,
	}
}

func nameRecordModel(n sqlite.NameRecord) *PersonClusterNameRecord {
	return &PersonClusterNameRecord{
		ClusterID: strconv.FormatInt(n.ClusterID, 10),
		Name:      n.Name,
		Actor:     n.Actor,
		CreatedAt: n.CreatedAt,
	}
}

// --- Queries --------------------------------------------------------------

func (r *queryResolver) PersonCluster(ctx context.Context, id string) (*PersonCluster, error) {
	cid, err := clusterIDFromString(id)
	if err != nil {
		return nil, err
	}

	c, err := clusterStore().GetCluster(ctx, cid)
	if err != nil {
		return nil, err
	}
	// A missing id is null, not an error, matching every other find* in this
	// schema: a client that raced a delete should get null rather than a failure
	// it has to special-case.
	if c == nil {
		return nil, nil
	}
	return personClusterModel(*c), nil
}

func (r *queryResolver) PersonClusters(ctx context.Context, limit int, offset *int) ([]*PersonCluster, error) {
	// The schema makes limit non-null so a client cannot omit it, but a
	// hand-written request can still send null, and 0 is what a JavaScript
	// client sending `limit: null` coerces to. Both are the same mistake --
	// asking for everything -- so both land here.
	if limit <= 0 {
		return nil, errClusterLimitRequired
	}

	off := 0
	if offset != nil {
		off = *offset
		if off < 0 {
			off = 0
		}
	}

	rows, err := clusterStore().ListClusters(ctx, limit, off)
	if err != nil {
		return nil, err
	}
	return clusterModels(rows), nil
}

func (r *queryResolver) UnnamedPersonClusters(ctx context.Context, limit int) (*PersonClusterReviewPage, error) {
	if limit <= 0 {
		return nil, errClusterLimitRequired
	}

	clusters, total, err := clusterStore().ListUnnamed(ctx, limit)
	if err != nil {
		return nil, err
	}
	// Checked rather than cast blind: a library with more unnamed clusters than
	// an int can hold would silently report a negative count, and a UI rendering
	// "showing 20 of -2147483628" is worse than an error.
	if total > int64(maxInt()) {
		return nil, fmt.Errorf("too many unnamed clusters to count: %d", total)
	}

	return &PersonClusterReviewPage{
		Clusters: clusterModels(clusters),
		Total:    int(total),
	}, nil
}

func (r *queryResolver) PersonClusterNames(ctx context.Context, clusterId string) ([]*PersonClusterNameRecord, error) {
	cid, err := clusterIDFromString(clusterId)
	if err != nil {
		return nil, err
	}

	// A cluster that does not exist yields an empty history, not an error. The
	// history is a property of a cluster, and a client asking about a cluster it
	// just deleted should see nothing rather than a failure.
	hist, err := clusterStore().NameHistory(ctx, cid)
	if err != nil {
		return nil, err
	}

	out := make([]*PersonClusterNameRecord, 0, len(hist))
	for _, n := range hist {
		out = append(out, nameRecordModel(n))
	}
	return out, nil
}

// --- Mutations ------------------------------------------------------------

func (r *mutationResolver) NamePersonCluster(ctx context.Context, clusterId string, name string, actor string) (*PersonCluster, error) {
	cid, err := clusterIDFromString(clusterId)
	if err != nil {
		return nil, err
	}

	// Checked here as well as in the store. Redundant, and it is the redundancy
	// that matters: the store's error names the parameter for a programmer, this
	// one tells the person looking at the screen what to do instead. The store
	// check is the guarantee; this is the courtesy.
	if name == "" {
		return nil, errClusterNameEmpty
	}

	// The actor comes from the authenticated session when there is one, and the
	// supplied value is only a fallback. NOT the other way round: a browser
	// mutation that can pass its own actor string is a mutation that can lie
	// about who named a face, and that string is the only record of who decided
	// two appearances are the same person.
	actor = resolveClusterActor(ctx, actor)

	if err := clusterStore().NameCluster(ctx, cid, name, actor); err != nil {
		return nil, translateClusterError(err)
	}

	return r.reloadCluster(ctx, cid)
}

func (r *mutationResolver) ClearPersonClusterName(ctx context.Context, clusterId string) (*PersonCluster, error) {
	cid, err := clusterIDFromString(clusterId)
	if err != nil {
		return nil, err
	}

	if err := clusterStore().ClearName(ctx, cid); err != nil {
		return nil, translateClusterError(err)
	}
	return r.reloadCluster(ctx, cid)
}

func (r *mutationResolver) SetPersonClusterHandle(ctx context.Context, clusterId string, handle string) (*PersonCluster, error) {
	cid, err := clusterIDFromString(clusterId)
	if err != nil {
		return nil, err
	}

	if err := clusterStore().SetHandle(ctx, cid, handle); err != nil {
		return nil, translateClusterError(err)
	}
	return r.reloadCluster(ctx, cid)
}

// reloadCluster re-reads after a write rather than patching the returned
// struct. The store's mutation touches more than the field the client asked for
// -- updated_at certainly -- and a client that sees a stale timestamp caches it
// and later compares two clusters that were never different.
func (r *mutationResolver) reloadCluster(ctx context.Context, id int64) (*PersonCluster, error) {
	c, err := clusterStore().GetCluster(ctx, id)
	if err != nil {
		return nil, err
	}
	if c == nil {
		// The write succeeded and the row is gone. Said plainly rather than as a
		// generic not-found: this is a real outcome (something deleted the
		// cluster concurrently) and a bare "no such cluster" after a successful
		// mutation reads as a server bug.
		return nil, fmt.Errorf("%w: the cluster was deleted while it was being named", errClusterNotFound)
	}
	return personClusterModel(*c), nil
}

// resolveClusterActor prefers the authenticated user over the supplied string.
//
// A browser mutation carrying `actor: "alice"` in a session belonging to bob
// would write an audit row naming alice. The session wins whenever there is one;
// the argument exists for the pipeline's unauthenticated passes.
func resolveClusterActor(ctx context.Context, supplied string) string {
	if u, err := currentUser(ctx); err == nil && u != nil && u.Username != "" {
		return u.Username
	}
	if supplied == "" {
		// "system" rather than a silent empty string, because the store requires
		// a non-empty actor and "" would be a row claiming nothing at all. It is
		// still a claim, and the schema documents it as one.
		return "system"
	}
	return supplied
}

// translateClusterError turns a store error into something a client can act on.
//
// The store's errors are written for a programmer reading a log; this is for the
// person who typed the name. Anything unrecognised passes through unchanged
// rather than being flattened -- an error that loses its cause is an error nobody
// can debug.
func translateClusterError(err error) error {
	if err == nil {
		return nil
	}
	// The store's signal for a cluster that does not exist. Surfaced as the
	// not-found error so a client racing a delete gets the same answer from the
	// mutation as from the query.
	if errors.Is(err, sqlite.ErrClusterNotFound) {
		return errClusterNotFound
	}
	return err
}

// maxInt is the largest value an int can hold on this platform, as an int64.
//
// Written out rather than pulled from math.MaxInt because the callers want the
// value to compare a COUNT against, and the conversion direction is the part
// that is easy to get wrong.
func maxInt() int64 { return int64(^uint(0) >> 1) }

// --- Field resolvers ------------------------------------------------------

// MemberCount is a COUNT rather than len(members), so a list of clusters does
// not read every member of every cluster to render a count column. The members
// remain available and a client asking for both pays for both -- which is the
// honest arrangement: a field that quietly reuses another field's data is a
// field whose cost nobody can predict.
func (r *personClusterResolver) MemberCount(ctx context.Context, obj *PersonCluster) (int, error) {
	id, err := clusterIDFromString(obj.ID)
	if err != nil {
		return 0, err
	}
	// The store returns int, having already refused to hand back a value that
	// does not fit -- so there is no overflow check to do here. Repeating it
	// would imply the store might return one, and a check that cannot fire is a
	// check that will be copied into the next resolver and left to rot.
	return clusterStore().MemberCount(ctx, id)
}

func (r *personClusterResolver) Members(ctx context.Context, obj *PersonCluster) ([]*PersonClusterMember, error) {
	id, err := clusterIDFromString(obj.ID)
	if err != nil {
		return nil, err
	}

	// Nearest first, which is the store's ordering and the one that answers
	// "why are these linked". Re-sorting here would be a second ordering to keep
	// in step with the first.
	members, err := clusterStore().Members(ctx, id)
	if err != nil {
		return nil, err
	}

	out := make([]*PersonClusterMember, 0, len(members))
	for _, m := range members {
		out = append(out, personClusterMemberModel(m))
	}
	return out, nil
}
