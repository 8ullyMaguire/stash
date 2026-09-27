package collab

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// Library access. M4 step 4.3, spec §6.4.
//
//	TWO SEPARATE GRANTS, AND THIS IS LOAD-BEARING
//
//	Metadata may be published to the commons per §6.2.
//	Media is served only to users holding a user_library_access row, and only
//	if the owner runs a mode that serves files at all.
//
// A user can therefore contribute curation to a public instance while the files
// stay on the owner's disk. The two are separate grants because conflating them
// is the failure this project exists to avoid: a user who can see a scene in
// the shared metadata has NOT thereby been given the file, and the code that
// decides has to be able to say so.
//
// The refusal is 404, not 403, and that is not a cosmetic choice. An ungranted
// user can see the scene in metadata -- its title, its performer, its runtime --
// and must not be able to learn that a file exists at all. A 403 says "there is
// something here and you may not have it", which is the disclosure §6.4 forbids.
// So there is exactly ONE refusal here, and it is indistinguishable from the
// refusal for a file that does not exist.

// ErrNoLibraryAccess is the single refusal. Undeliberately uninformative: see
// the note above. There is no separate error for "no such library", because a
// caller that could tell them apart could enumerate libraries.
var ErrNoLibraryAccess = errors.New("not found")

// IsNoLibraryAccess reports whether an error is the access refusal, so a handler
// maps it to 404 without string matching.
func IsNoLibraryAccess(err error) bool { return err == ErrNoLibraryAccess }

// LibraryStore reads and writes library access grants.
//
// Reads take the user and the library and answer a question, rather than
// exposing a "is this row present" helper that callers might forget to consult.
// The decision and the lookup are the same call on purpose.
type LibraryStore interface {
	// HasAccess reports whether the user holds a grant for the library.
	HasAccess(ctx context.Context, userID, libraryID int64) (bool, error)
	// Grant gives a user access to a library. Idempotent.
	Grant(ctx context.Context, userID, libraryID int64) error
	// Revoke removes a grant. Idempotent, and not an error when absent: the
	// desired end state is "this user has no access", and reporting failure for
	// a revoke of something already absent would make a retry loop.
	Revoke(ctx context.Context, userID, libraryID int64) error
	// UsersWithAccess lists who holds a grant, for the owner's view of their
	// own library. Sorted, so the answer is stable for a UI or an audit log.
	UsersWithAccess(ctx context.Context, libraryID int64) ([]int64, error)
}

// AccessDecision is the answer to "may this user have this file", and the three
// inputs it needs.
//
// Mode and grant are SEPARATE FIELDS rather than a single bool, so the reason for
// a refusal survives into the audit log even though it is deliberately absent
// from the HTTP response. Collapsing them to `allowed bool` is how an operator
// ends up staring at a 404 and being told "no" with no way to find out which of
// the two rules fired.
type AccessDecision struct {
	Mode Mode
	// HasGrant is the per-user grant answer, passed in rather than looked up so
	// that the mode's rule and the grant's answer cannot quietly stand in for
	// one another.
	HasGrant bool
}

// Decide applies §6.4: the mode first, then the grant, and always the same
// refusal.
//
// Order matters for the audit log, not the response: both refusals are
// indistinguishable externally, but internally "wrong mode" is an owner problem
// and "no grant" is a user's problem, and the two need different messages to the
// person who can fix them.
func (d AccessDecision) Decide() error {
	if !d.Mode.Valid() {
		return fmt.Errorf("%w: %q", ErrModeInvalid, d.Mode)
	}
	if !d.Mode.ServesMedia() {
		// Private and contribute serve nobody, grant or not. The mode is the
		// outer boundary; a grant does not punch through it.
		return ErrNoLibraryAccess
	}
	if !d.HasGrant {
		return ErrNoLibraryAccess
	}
	return nil
}

// Allowed is the boolean form, for callers that only need the yes/no.
func (d AccessDecision) Allowed() bool { return d.Decide() == nil }

// MetadataVisible reports whether the user can see the scene's METADATA.
//
// Deliberately a separate question from Decide, and it is the one that makes the
// arrangement coherent: a user can be shown a scene and still get a 404 for its
// file. §6.4's whole point is that those two answers differ, so they are not
// computed from one another here.
//
// An unconfigured instance shares no metadata at all, so the answer is false
// outside contribute and public.
func (m Mode) MetadataVisible() bool { return m.SharesMetadata() }

// AuthorizeLibraryAccess is the one call the serving path makes.
//
// It takes the mode, the grant answer, and returns the single refusal, so a
// handler cannot accidentally build a 403 by handling a different error. The
// libraryID is unused by the decision and is accepted only so a call site reads
// as the question it is -- the access decision depends on the grant, not on the
// library's existence, and a caller that already has a grant for a deleted
// library must still be refused by the store's foreign key.
func AuthorizeLibraryAccess(d AccessDecision) error {
	return d.Decide()
}

// SortUserIDs returns a sorted copy, for stable output.
func SortUserIDs(ids []int64) []int64 {
	out := append([]int64(nil), ids...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
