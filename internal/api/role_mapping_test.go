package api

import (
	"testing"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/pkg/models"
)

// The proof that replacing isModerator(u) with roleOf(u).Can(...) in the two
// moderation gates was a refactor and not a change of who may moderate.
//
// This is the kind of test that has to exist whenever a permission check is
// re-expressed in a new vocabulary. The new spelling is better -- it is
// table-driven, it names capabilities instead of implying them from two booleans,
// and it makes a public instance expressible. None of that is worth anything if
// the people who could moderate before cannot moderate now, and the failure
// mode of getting that wrong is a moderator discovering it during an incident.

// users is the cross-product of the two booleans a user row can carry, plus the
// nil case. Six rows, which is every user the database can represent today.
var users = []struct {
	name string
	user *models.User
}{
	{"anonymous visitor", nil},
	{"ordinary user", &models.User{ID: 2}},
	{"moderator", &models.User{ID: 3, IsModerator: true}},
	{"owner", &models.User{ID: 4, IsOwner: true}},
	// Reachable by data, if not by any code path: both flags set. The owner
	// flag wins in both predicates, but a row like this should not change
	// anyone's access when the mapping is edited later.
	{"owner who is also flagged moderator", &models.User{ID: 5, IsOwner: true, IsModerator: true}},
	{"a user with neither flag but a nonzero id", &models.User{ID: 6}},
}

// TestRoleOfAgreesWithM2ModeratorPredicate is the load-bearing test in this
// file.
func TestRoleOfAgreesWithM2ModeratorPredicate(t *testing.T) {
	for _, tc := range users {
		t.Run(tc.name, func(t *testing.T) {
			want := isModerator(tc.user)
			got := collab.Can(roleOf(tc.user), collab.CapModerate)

			if got != want {
				t.Errorf("roleOf(%s).Can(CapModerate) = %v, but M2's "+
					"isModerator = %v; the moderation gate changed for this user",
					tc.name, got, want)
			}
		})
	}
}

// TestRoleOfNamesEveryRow: whatever the mapping is, it must produce a named
// role. A default branch that silently returns the zero Role would grant
// nothing, which is the safe direction, but it would also mean the mapping has
// a hole nobody notices until a legitimate user is refused.
func TestRoleOfNamesEveryRow(t *testing.T) {
	for _, tc := range users {
		t.Run(tc.name, func(t *testing.T) {
			role := roleOf(tc.user)
			if role.String() == "" || role.String() == "unknown" {
				t.Errorf("roleOf(%s) = %q, which is not a named role", tc.name, role.String())
			}
		})
	}
}

// TestRoleOfMapsTheDocumentedWay pins the specific mapping, so a change to any
// branch names itself.
func TestRoleOfMapsTheDocumentedWay(t *testing.T) {
	tests := []struct {
		name string
		user *models.User
		want collab.Role
	}{
		{"no user record is public", nil, collab.RolePublic},
		{"owner is admin", &models.User{IsOwner: true}, collab.RoleAdmin},
		{"moderator is steward", &models.User{IsModerator: true}, collab.RoleSteward},
		{"anyone else is a contributor", &models.User{}, collab.RoleContributor},
		// The load-bearing one. M2 let anyone propose; SUBSCRIBER would remove
		// that from every existing user at upgrade time with no migration and
		// no error.
		{"an ordinary user can still propose", &models.User{}, collab.RoleContributor},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := roleOf(tc.user); got != tc.want {
				t.Errorf("roleOf = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRoleOfGrantsProposalToEveryRoleThatCouldBefore: M2's proposal gate is
// "not nil user". Any user row could propose, so every mapped role must still be
// able to. This is the migration-safety property, and it is separate from
// moderation on purpose -- the two gates are checked by two different tables.
func TestRoleOfGrantsProposalToEveryRoleThatCouldBefore(t *testing.T) {
	for _, tc := range users {
		t.Run(tc.name, func(t *testing.T) {
			// The M2 rule: any authenticated user may propose.
			couldBefore := tc.user != nil
			canNow := collab.Can(roleOf(tc.user), collab.CapPropose)

			if canNow != couldBefore {
				t.Errorf("could propose before = %v, can propose now = %v; "+
					"M2 let any authenticated user propose and the upgrade must "+
					"not take that from anyone", couldBefore, canNow)
			}
		})
	}
}

// TestPublicRoleCannotPropose: the one case where the new vocabulary is
// STRICTLY more correct than the old one. M2's nil check answered the question
// for the GraphQL resolver; the role table answers it for the model, so the
// answer cannot be forgotten at a call site that does not happen to have a
// context.
func TestPublicRoleCannotPropose(t *testing.T) {
	if collab.Can(roleOf(nil), collab.CapPropose) {
		t.Error("the public role can propose; an anonymous visitor is not a voter")
	}
	if collab.Can(roleOf(nil), collab.CapVote) {
		t.Error("the public role can vote; same reason")
	}
}

// TestRoleOfIsTotal: every input produces a decision rather than a panic. A
// resolver calling this on a nil user is the ordinary case, not an edge case.
func TestRoleOfIsTotal(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("roleOf panicked: %v", r)
		}
	}()
	for _, tc := range users {
		_ = collab.Can(roleOf(tc.user), collab.CapModerate)
		_ = collab.Can(roleOf(tc.user), collab.CapPropose)
		_ = collab.Can(roleOf(tc.user), collab.CapVote)
		_ = collab.Can(roleOf(tc.user), collab.CapWithdrawOwn)
	}
}
