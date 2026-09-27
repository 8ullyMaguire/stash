package collab

import (
	"testing"
)

// Table-driven over the full cross-product, as the plan requires: every role
// against every capability. A handful of hand-picked cases would let an invalid
// combination survive, and the whole point of a table over flags is that the
// invalid combinations are unrepresentable.

var allRoles = []Role{RolePublic, RoleSubscriber, RoleContributor, RoleSteward, RoleAdmin}

var allCapabilities = []Capability{
	CapBrowse, CapVote, CapPropose, CapWithdrawOwn,
	CapSeeModerationQueue, CapModerate,
	CapManageRoles, CapManageSettings, CapManagePeers,
}

// wantCapability is the expectation, written once so the table reads as a
// matrix rather than as scattered assertions.
//
// Each row is a role; each column is a capability. Read down a column and you
// have the threshold for that capability, which is the question a reader
// actually has.
var wantCapability = map[Role]map[Capability]bool{
	RolePublic: {
		CapBrowse: true,
	},
	RoleSubscriber: {
		CapBrowse: true, CapVote: true, CapWithdrawOwn: true,
	},
	RoleContributor: {
		CapBrowse: true, CapVote: true, CapPropose: true, CapWithdrawOwn: true,
	},
	RoleSteward: {
		CapBrowse: true, CapVote: true, CapPropose: true, CapWithdrawOwn: true,
		CapSeeModerationQueue: true, CapModerate: true,
	},
	RoleAdmin: {
		CapBrowse: true, CapVote: true, CapPropose: true, CapWithdrawOwn: true,
		CapSeeModerationQueue: true, CapModerate: true,
		CapManageRoles: true, CapManageSettings: true, CapManagePeers: true,
	},
}

func TestRoleCapabilitiesFullCrossProduct(t *testing.T) {
	for _, r := range allRoles {
		for _, c := range allCapabilities {
			r, c := r, c
			t.Run(r.String()+"/"+c.String(), func(t *testing.T) {
				want := wantCapability[r][c]
				if got := Can(r, c); got != want {
					t.Errorf("Can(%s, %s) = %v, want %v", r, c, got, want)
				}
			})
		}
	}
}

// TestEveryRoleHasAnEntry guards the table against a role added to the enum and
// forgotten here. The failure mode without this is silent: a new role with no
// map entry gets the zero Capabilities and denies everything, which looks like a
// working permission system that happens to deny a role nobody has tested.
func TestEveryRoleHasAnEntry(t *testing.T) {
	for _, r := range allRoles {
		if _, ok := roleCapabilities[r]; !ok {
			t.Errorf("role %s has no entry in roleCapabilities; it will deny every "+
				"capability by default, which is indistinguishable from a working "+
				"permission table", r)
		}
	}
	// And no entry may exist for a role that is not declared: a stray entry is
	// dead configuration that will be read as intent.
	for r := range roleCapabilities {
		if !r.Valid() {
			t.Errorf("roleCapabilities has an entry for invalid role %d", int(r))
		}
	}
}

// TestCapabilityTableCoversEveryCapability is the mirror of the above for the
// capability side. A Capability constant with no entry in capabilityFields
// answers false everywhere, so it is a permission that cannot be granted to
// anyone — found only when a user reports they cannot do something.
func TestCapabilityTableCoversEveryCapability(t *testing.T) {
	for _, c := range allCapabilities {
		if _, ok := capabilityFields[c]; !ok {
			t.Errorf("capability %s has no entry in capabilityFields; it can never "+
				"be granted to any role", c)
		}
	}
	for c := range capabilityFields {
		if c < CapBrowse || c > CapManagePeers {
			t.Errorf("capabilityFields has an entry for undeclared capability %d", int(c))
		}
	}
}

// TestOnlyPublicIsAnonymous pins the predicate the rest of the system leans on.
func TestOnlyPublicIsAnonymous(t *testing.T) {
	// Public is the only role without a user behind it, so it is the only role
	// that returns false. (The first version of this test asserted the
	// negation, which failed on all five roles and was wrong: RequiresLogin
	// asks "is there an account", not "is it the public role".)
	for _, r := range allRoles {
		want := r != RolePublic
		if got := r.RequiresLogin(); got != want {
			t.Errorf("%s.RequiresLogin() = %v, want %v", r, got, want)
		}
	}
}

// TestPublicRoleGrantsNothingThatWrites is the security property, stated
// directly rather than left to the cross-product table.
//
// Anonymous write capability is the bug this role exists to prevent, and it is
// worth an explicit test with an explicit name: a reader scanning the table for
// permission changes should trip over this immediately.
func TestPublicRoleGrantsNothingThatWrites(t *testing.T) {
	writes := []Capability{
		CapVote, CapPropose, CapWithdrawOwn,
		CapModerate, CapManageRoles, CapManageSettings, CapManagePeers,
	}
	for _, c := range writes {
		if Can(RolePublic, c) {
			t.Errorf("public role holds %s; an anonymous request must never be "+
				"able to write", c)
		}
	}
	// Browsing is the one thing public has, and it is tier-filtered rather than
	// absent — which is why the check is about writes and not about
	// BrowseTiered.
	if !Can(RolePublic, CapBrowse) {
		t.Error("public role cannot browse; a public site that cannot be read is " +
			"not a public site")
	}
}

// TestUnknownRoleAndCapabilityFailClosed covers both edges of the table.
func TestUnknownRoleAndCapabilityFailClosed(t *testing.T) {
	bad := []Role{Role(-1), Role(5), Role(99)}
	for _, r := range bad {
		if r.Valid() {
			t.Errorf("role %d reports Valid", int(r))
		}
		if Can(r, CapBrowse) {
			t.Errorf("invalid role %d can browse; must fail closed", int(r))
		}
		if Can(r, CapManageRoles) {
			t.Errorf("invalid role %d can manage roles; must fail closed", int(r))
		}
		if RoleSatisfies(r, RolePublic) {
			t.Errorf("invalid role %d satisfies RolePublic; must fail closed", int(r))
		}
	}

	for _, c := range []Capability{Capability(-1), Capability(99)} {
		if Can(RoleAdmin, c) {
			t.Errorf("admin granted undeclared capability %d; an unrecognised "+
				"capability must not be treated as granted", int(c))
		}
	}
}

// TestRoleSatisfiesIsMonotonic: if a role satisfies a requirement, every more
// privileged role does too. Checked over the full product because monotonicity
// is exactly the property a per-case test would miss.
func TestRoleSatisfiesIsMonotonic(t *testing.T) {
	for _, held := range allRoles {
		for _, req := range allRoles {
			if !RoleSatisfies(held, req) {
				continue
			}
			for _, higher := range allRoles {
				if higher <= held {
					continue
				}
				if !RoleSatisfies(higher, req) {
					t.Errorf("%s satisfies %s but the more privileged %s does not; "+
						"privilege ordering is not monotonic", held, req, higher)
				}
			}
		}
	}
}

func TestRoleSatisfies(t *testing.T) {
	tests := []struct {
		held, required Role
		want           bool
	}{
		{RolePublic, RolePublic, true},
		{RolePublic, RoleSubscriber, false},
		{RoleSubscriber, RoleSubscriber, true},
		{RoleSubscriber, RoleContributor, false},
		{RoleContributor, RoleSubscriber, true},
		{RoleSteward, RoleContributor, true},
		{RoleSteward, RoleSteward, true},
		{RoleAdmin, RoleSteward, true},
		{RoleAdmin, RoleAdmin, true},
		{RoleContributor, RoleSteward, false},
		{RolePublic, RoleAdmin, false},
	}
	for _, tc := range tests {
		if got := RoleSatisfies(tc.held, tc.required); got != tc.want {
			t.Errorf("RoleSatisfies(%s, %s) = %v, want %v",
				tc.held, tc.required, got, tc.want)
		}
	}
}

// TestMinRoleForAgreesWithTheTable: the two must not be able to disagree. If
// MinRoleFor returned a role the table does not grant the capability to, the UI
// would offer a promotion that then fails.
func TestMinRoleForAgreesWithTheTable(t *testing.T) {
	for _, c := range allCapabilities {
		min, ok := MinRoleFor(c)
		if !ok {
			t.Errorf("no role holds %s", c)
			continue
		}
		if !Can(min, c) {
			t.Errorf("MinRoleFor(%s) = %s but that role does not hold it", c, min)
		}
		// And nothing below it may hold it, or "minimum" is a lie.
		for _, r := range allRoles {
			if r >= min {
				break
			}
			if Can(r, c) {
				t.Errorf("MinRoleFor(%s) = %s but %s, which is below it, holds it",
					c, min, r)
			}
		}
	}
}

func TestCanAny(t *testing.T) {
	// Empty list is false, not a panic and not true.
	if CanAny(RoleAdmin) {
		t.Error("CanAny with no capabilities returned true")
	}
	if CanAny(RolePublic, CapVote, CapModerate) {
		t.Error("public holds none of vote/moderate but CanAny said true")
	}
	if !CanAny(RolePublic, CapVote, CapBrowse) {
		t.Error("public holds browse but CanAny said false")
	}
	if !CanAny(RoleSubscriber, CapVote) {
		t.Error("subscriber holds vote but CanAny said false")
	}
}

// TestResolveRolePreservesM2Behaviour is the migration test.
//
// M2 had two booleans and no contributor tier: every ordinary account could
// propose. If ResolveRole maps an ordinary account to subscriber, M2b silently
// removes the ability to propose from every existing user on the upgrade path —
// with no migration and no error. This test is what makes that visible.
func TestResolveRolePreservesM2Behaviour(t *testing.T) {
	tests := []struct {
		name        string
		isOwner     bool
		isModerator bool
		want        Role
	}{
		{"owner maps to admin", true, false, RoleAdmin},
		{"owner who is also moderator is still admin", true, true, RoleAdmin},
		{"moderator maps to steward", false, true, RoleSteward},
		// The load-bearing one: M2 let anyone propose, so an ordinary account
		// must still be able to.
		{"ordinary account keeps the ability to propose", false, false, RoleContributor},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveRole(tc.isOwner, tc.isModerator)
			if got != tc.want {
				t.Errorf("ResolveRole(owner=%v, moderator=%v) = %s, want %s",
					tc.isOwner, tc.isModerator, got, tc.want)
			}
		})
	}

	// Spelled out separately, because it is the property rather than a case.
	ordinary := ResolveRole(false, false)
	if !Can(ordinary, CapPropose) {
		t.Error("an ordinary M2 account cannot propose after the upgrade; M2 " +
			"allowed it and ResolveRole must preserve that")
	}
	if !Can(ResolveRole(false, true), CapModerate) {
		t.Error("an M2 moderator cannot moderate after the upgrade")
	}
	if !Can(ResolveRole(true, false), CapManageSettings) {
		t.Error("the M2 owner cannot manage settings after the upgrade")
	}
}

func TestRoleStringRoundTrips(t *testing.T) {
	want := map[string]Role{
		"public": RolePublic, "subscriber": RoleSubscriber,
		"contributor": RoleContributor, "steward": RoleSteward, "admin": RoleAdmin,
	}
	for s, r := range want {
		if got := r.String(); got != s {
			t.Errorf("Role(%d).String() = %q, want %q", int(r), got, s)
		}
	}
	if got := Role(-1).String(); got != "unknown" {
		t.Errorf("invalid role String() = %q, want \"unknown\"", got)
	}
}
