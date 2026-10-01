package api

import (
	"strconv"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/pkg/models"
)

// The GraphQL-side value objects for M4's hosting surface.
//
// They are DISTINCT from the collab domain types on purpose, and the reason is
// worth stating because it looks like duplication:
//
//   - collab types carry behaviour and no GraphQL concerns. `collab.Library`
//     resolves is_private's null case, owns its ordering, and knows nothing about
//     queries.
//   - these are the shapes the SCHEMA promises, and the schema promises things
//     the domain does not: a `Boolean` where the domain has a three-state
//     `*bool`, and a non-null `String!` where the domain has a pointer that may
//     be nil.
//
// The conversion is the place where a null is turned into "not specified", and
// it is one function per type so there is exactly one place to look for what a
// nil became. gqlgen maps a `*bool` onto a non-null Boolean by returning false
// for nil, which is right for `granteeCount` and catastrophic for `isPrivate`:
// a deferring library would be published. The conversion below refuses instead.

// TOTPStatusModel is the caller's own 2FA state.
//
// Enrolled and Required are separate fields, and they answer different
// questions. "Required" is the POLICY; "enrolled" is the STATE. An owner with no
// secret is required-but-not-enrolled, which is a LOCKOUT and not a free pass, so
// a client that reads one field for both will either lock the user out or let
// them through.
type TOTPStatusModel struct {
	Enrolled    bool
	Required    bool
	Fingerprint *string
}

// TOTPEnrollmentModel is a freshly generated, not-yet-active secret.
//
// The only time the plaintext exists outside the user's authenticator. A field
// on `User` would instead be readable by every read-only session on the instance,
// and one compromised read key would harvest every unscanned secret and mint
// codes for all of them, forever.
type TOTPEnrollmentModel struct {
	Uri         string
	Secret      string
	Fingerprint string
}

// LibraryModel is one library, as the schema sees it.
//
// IsPrivate is a *string-ish pointer, NOT a bool, because the schema's
// `isPrivate: Boolean` is nullable and the domain's three states must survive
// the trip. A library that defers to its owner's consent is NOT the same as a
// library marked not-private, and flattening the two here would publish a library
// whose owner never decided.
type LibraryModel struct {
	ID           string
	Name         string
	IsPrivate    *bool
	IsDefault    bool
	GranteeCount int
}

// libraryModelToAPI converts one library.
//
// The conversion of the null is the whole function. `IsPrivate` stays a nil
// pointer so the schema emits `null`; the client distinguishes "explicitly
// public", "explicitly private" and "defer to my consent" without a second query.
func libraryModelToAPI(lib *collab.Library) *LibraryModel {
	if lib == nil {
		return nil
	}
	return &LibraryModel{
		ID:           strconv.FormatInt(lib.ID, 10),
		Name:         lib.Name,
		IsPrivate:    lib.IsPrivate, // nil survives as null
		IsDefault:    lib.IsDefault,
		GranteeCount: lib.GranteeCount,
	}
}

// libraryModelsToAPI converts a list, and returns an empty slice for an empty
// list rather than nil.
//
// A nil slice is what a GraphQL list field marshals to `[]` either way, so this
// is not about the wire format — it is about a test asserting len() == 0 against
// a value that is nil, which reads as "no libraries" and means "never queried".
func libraryModelsToAPI(libs collab.LibraryList) []*LibraryModel {
	out := make([]*LibraryModel, 0, len(libs))
	for _, lib := range libs {
		if m := libraryModelToAPI(lib); m != nil {
			out = append(out, m)
		}
	}
	return out
}

// userModelsToAPI is a pass-through, present so the grantee list has one obvious
// place to change if the user shape ever needs mapping.
func userModelsToAPI(users []*models.User) []*models.User {
	if users == nil {
		return []*models.User{}
	}
	return users
}
