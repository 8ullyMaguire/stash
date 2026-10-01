package cluster

import "time"

// NameRecord is one entry in a cluster's name history.
//
// It lives here rather than in the sqlite package because it is the DOMAIN
// shape of "a name was attached to a cluster by someone at a time", and the
// GraphQL layer needs it as much as the store does. A store that owns the type
// makes every consumer import the database package to describe a naming event.
//
// The history is APPEND-ONLY. A rename is a new record, and ClearName leaves the
// records in place, so "was this always this person?" is answerable months
// later. Six months after a merge turns out to have been wrong, the only
// question that matters is who decided and when, and that is only answerable if
// the previous name is still on file.
type NameRecord struct {
	ClusterID int64
	Name      string
	Actor     string
	CreatedAt time.Time
}

// AppearancesThreshold is the number of members a cluster needs before the UI
// offers to name it.
//
// Three is the spec's number (§7.1: "meaningless to a user until it has three
// or more appearances"). It is a constant here rather than a literal at each
// call site, because the threshold is a product decision with one value and
// several readers, and a literal repeated at four call sites is a value that
// will be changed in three of them.
//
// It is NOT a minimum for the cluster to EXIST. A two-member cluster is real
// and browsable; the threshold governs whether naming it is worth offering, not
// whether it is permitted.
const AppearancesThreshold = 3

// IsNameable reports whether a cluster has enough appearances for the UI to
// offer naming it.
//
// A NAMED cluster is not nameable again, which is why the caller has to check
// the name separately: "should we offer the rename box" and "is there anything
// to rename" are different questions and conflating them produces a UI that
// offers to name a cluster that already has a name.
func IsNameable(memberCount int, named bool) bool {
	return !named && memberCount >= AppearancesThreshold
}
