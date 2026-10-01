package api

import (
	"context"

	"github.com/stashapp/stash/pkg/models"
)

// userResolver is the per-field resolver set for the User type, and it holds
// exactly one method.
//
// gqlgen generates the interface, so it only asks for fields it cannot bind on
// its own. `isOwner` and `isModerator` are not here because they autobind
// straight off the struct — the model field and the schema field share a name,
// so a resolver would only be a second place to get the same value wrong.

// Disabled reports whether the account may not log in.
//
// A null disabled_at means active, and the model exposes Active() for exactly
// that. Re-deriving "is it disabled" from the timestamp here would be a second
// definition of the same fact, and the two would eventually disagree.
func (r *userResolver) Disabled(ctx context.Context, user *models.User) (bool, error) {
	return !user.Active(), nil
}
