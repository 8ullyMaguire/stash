package collab

import "context"

// Media scope resolution. M4 step 4.3, the part migration 105 made reachable.
//
// WHY A SEPARATE FILE
//
// `Decide` in access.go takes the grant answer as a PARAMETER, so the mode's
// rule and the grant's answer are separate inputs. This file is the layer that
// actually goes and gets them: it resolves a row's library, resolves the
// caller's standing in that library, and hands the result to `Decide`. Keeping
// it separate is what makes the difference between "the gate exists" and "the
// gate has something to look up" checkable -- for the whole of M4 the first
// sentence was true and the second was not.
//
// # THE THREE ANSWERS A SCOPE HAS
//
//	owner    the caller owns the library
//	granted  the caller holds a user_library_access row for it
//	neither  the refusal
//
// Ownership is checked BEFORE the grant and is not a special case of it. An
// owner who is not in their own access table is still the owner, and a design
// that made them grant themselves access to their own library would be one
// extra step between an operator and their own files on every install that
// upgraded.
//
// # WHY NULL IS NOT "PUBLIC"
//
// A row with no library is the case this file exists to refuse, and the
// temptation is to read it as "unrestricted, so allow it". That is a fail-open
// on the exact cases nobody is watching: a row the migration failed to backfill,
// a row written by a path that forgot the column, a row whose library was
// deleted.
//
// But the outright refusal has a cost the migration notes, and it is a real
// one: the scanner writes rows through a code path that knows nothing about
// libraries, so every newly-scanned row arrives with no library. Refusing
// outright means the owner cannot watch what they just scanned, and the fix
// that gets shipped under pressure is "make NULL mean allow" -- which is the
// fail-open, reached by a different road.
//
// So NULL resolves to the DEFAULT library, and the default library is owned by
// the owner. That is not a loophole: a row in the default library still needs
// an explicit grant from anybody who is not the owner. It is the difference
// between "nobody has organised this yet" and "this is public", which are two
// different sentences and only one of them was ever written down.

// MediaScopeStore is what a media request needs: a row's library, who owns it,
// and whether a grant exists.
//
// Declared here rather than over the sqlite store so this logic is testable
// without a database -- and, per the project's own hard-won lesson, so the
// adapter is checked against it at compile time in a NON-TEST file.
type MediaScopeStore interface {
	// LibraryOfTarget reports the library a target row belongs to. A
	// libraryID of 0 means the row is in no library -- which the resolver
	// maps to the default library, NOT to "public". See the file comment.
	LibraryOfTarget(ctx context.Context, targetType string, targetID int64) (libraryID int64, err error)
	// LibraryOwner reports who owns a library.
	LibraryOwner(ctx context.Context, libraryID int64) (userID int64, err error)
	// DefaultLibraryID reports the instance's default library, or 0 when
	// there is none (an instance with no users has no owner to own it).
	DefaultLibraryID(ctx context.Context) (int64, error)
	// HasAccess reports the grant. The same question LibraryStore asks, kept
	// separate because this interface is about a media REQUEST and the
	// other is about administering grants.
	HasAccess(ctx context.Context, userID, libraryID int64) (bool, error)
}

// Target type names, as they appear in library_of_target calls. A closed set
// rather than a parameter anyone can pass: the value reaches a query, and a
// value that is not in the set must be a refusal rather than a lookup.
const (
	TargetScene     = "scene"
	TargetImage     = "image"
	TargetGallery   = "gallery"
	TargetPerformer = "performer"
	TargetTag       = "tag"
	TargetStudio    = "studio"
	TargetGroup     = "group"
)

// TargetTypes is every target type a row can be scoped by. The resolver
// validates against this, so a typo in a call site is a refusal and not a query
// against a table name that came from a caller.
var TargetTypes = []string{
	TargetScene, TargetImage, TargetGallery, TargetPerformer,
	TargetTag, TargetStudio, TargetGroup,
}

// ValidTargetType reports whether a target type is one this project scopes.
func ValidTargetType(t string) bool {
	for _, known := range TargetTypes {
		if known == t {
			return true
		}
	}
	return false
}

// ScopeDecision is the resolved answer, and the two refusals are SEPARATE FIELDS
// for the same reason Mode and HasGrant are: internally "the row is in a library
// this caller does not own and was not granted" and "this instance serves no
// media" are different problems for different people, and they collapse to the
// same 404 on the wire.
type ScopeDecision struct {
	Mode Mode
	// LibraryID is the resolved library, after the default substitution.
	LibraryID int64
	// IsOwner is whether the caller owns that library.
	IsOwner bool
	// HasGrant is the grant answer, passed in rather than looked up so the
	// owner's ownership answer cannot quietly stand in for it.
	HasGrant bool
}

// ScopeStore is the whole of what a media request needs to ask. An interface so
// a test can supply a store that FAILS on demand, which is the case that
// decides whether this fails open or closed.
type ScopeStore interface {
	MediaScopeStore
	// Mode is the instance's posture.
	Mode(ctx context.Context) (Mode, error)
}

// ResolveScope answers "may this caller have the media behind this row".
//
// A REFUSAL IS NOT AN ERROR, and the signature says so: the returned error is
// only ever a store failure -- "the answer is unknown" -- and the refusal lives
// in ScopeDecision.Decide(). The first version conflated them and made every
// call site write `if err != nil || !d.Allowed()`, which is a distinction the
// compiler cannot help with and the reader cannot check.
//
// The split matters because the two need different handling. A refusal is the
// system working and gets a 404; a store error is the system not working, gets
// a 500, and is the operator's problem. Reporting a database outage as a 404
// would look exactly like a user being refused, and a caller cannot log its way
// out of that.
func ResolveScope(ctx context.Context, store ScopeStore, mode Mode, userID int64, targetType string, targetID int64) (ScopeDecision, error) {
	if store == nil {
		// Fail closed, with NO error: an absent store is a refusal, not an
		// outage. Note the typed-nil trap from docs/HANDOFF.md -- a nil
		// *sqlite pointer inside this interface is a NON-nil interface, so
		// this check only helps for a genuinely absent store, and the caller
		// still has to test its own concrete pointer.
		return ScopeDecision{Mode: mode}, nil
	}

	if !ValidTargetType(targetType) {
		// Refused before the lookup, because the lookup is where an
		// untrusted value becomes a table name.
		return ScopeDecision{Mode: mode}, nil
	}

	libraryID, err := store.LibraryOfTarget(ctx, targetType, targetID)
	if err != nil {
		return ScopeDecision{Mode: mode}, err
	}

	// The default substitution, and the single most consequential line in
	// the file. A row in no library belongs to the default library, which is
	// owned by the owner -- it does NOT become unrestricted.
	if libraryID == 0 {
		libraryID, err = store.DefaultLibraryID(ctx)
		if err != nil {
			return ScopeDecision{Mode: mode}, err
		}
		if libraryID == 0 {
			// No default library means no owner to own one. There is
			// nothing to grant access to and nothing to check, so the
			// refusal is the only honest answer -- and it is a refusal,
			// not an error: the database answered perfectly well.
			return ScopeDecision{Mode: mode}, nil
		}
	}

	ownerID, err := store.LibraryOwner(ctx, libraryID)
	if err != nil {
		return ScopeDecision{Mode: mode}, err
	}

	d := ScopeDecision{
		Mode:      mode,
		LibraryID: libraryID,
		IsOwner:   userID != 0 && ownerID == userID,
	}

	if d.IsOwner {
		// The owner does not need a grant row. Deliberately NOT
		// `HasGrant = true`: the field is the grant's answer, and writing
		// the owner's result into it would make a later audit unable to
		// say which of the two rules allowed the request.
		return d, nil
	}

	d.HasGrant, err = store.HasAccess(ctx, userID, libraryID)
	if err != nil {
		return ScopeDecision{Mode: mode}, err
	}
	return d, nil
}

// Decide turns a resolved scope into §6.4's single refusal.
//
// It is the same rule as AccessDecision.Decide, and it is expressed in terms of
// that type rather than reimplemented, so the mode's answer and the grant's
// answer cannot come to disagree between two copies of the same rule.
func (d ScopeDecision) Decide() error {
	return AuthorizeLibraryAccess(AccessDecision{
		Mode:     d.Mode,
		HasGrant: d.IsOwner || d.HasGrant,
	})
}

// Allowed is the boolean form.
func (d ScopeDecision) Allowed() bool { return d.Decide() == nil }
