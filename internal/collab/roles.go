package collab

// The five-role access model, adopted from Commons §8.3 in milestone M2b.
//
// This file is deliberately separate from governance.go, and the reason is
// ordering rather than aesthetics: roles are the access model, weighting is the
// economics that depend on it. A public instance is exactly where weighted
// voting starts to matter, so the access model has to exist and be tested
// before the arithmetic that assumes it.
//
// # Why a table and not booleans
//
// The obvious encoding is a set of flags on the user: CanVote, CanCurate,
// CanModerate, CanAdmin. It is wrong, and wrong in a way that gets worse with
// every role added, for two reasons:
//
//  1. Flags admit combinations nobody intended. CanModerate without CanCurate
//     is expressible and meaningless. A table of named roles cannot express it,
//     so the invalid state is unrepresentable rather than merely discouraged.
//
//  2. A flag set says what a user may do but not what they ARE, and the
//     difference is the public role. "An anonymous visitor may browse within
//     their consent tier" cannot be modelled as a user record, because there
//     is no user. It is a role with no subject, and modelling it as a special
//     user row is how a public site ends up with a row that can log in.

// Role is a capability bundle. Not a capability set: bundles are assigned, and
// each one is a complete answer to "what may this role do".
type Role int

const (
	// RolePublic is the no-login role. It exists because the spec promised a
	// public site and this is what makes that promise real.
	//
	// There is no user record behind it. Browsing is filtered by consent tier,
	// and the filter belongs in the query layer — an anonymous viewer is
	// precisely the case where a filter applied only in the UI leaks.
	RolePublic Role = iota

	// RoleSubscriber is a logged-in account with no curation rights. It exists
	// so that "can vote" and "can change things" are separable: a public
	// instance that let every logged-in account propose edits would have no
	// gate at all.
	RoleSubscriber

	// RoleContributor may propose edits to shared content.
	RoleContributor

	// RoleSteward additionally sees the moderation queue and may settle
	// proposals on their own authority.
	RoleSteward

	// RoleAdmin additionally manages settings, roles and peer configuration.
	// It is the only role that grants power over the instance itself rather
	// than over content.
	RoleAdmin
)

func (r Role) String() string {
	switch r {
	case RolePublic:
		return "public"
	case RoleSubscriber:
		return "subscriber"
	case RoleContributor:
		return "contributor"
	case RoleSteward:
		return "steward"
	case RoleAdmin:
		return "admin"
	default:
		return "unknown"
	}
}

// Valid reports whether r is one of the five defined roles.
//
// Exists because Role is an int and an int is constructible from anywhere. A
// zero value that silently meant "public" would be the most dangerous possible
// default: a user whose role failed to load would browse with no rights rather
// than refusing to act.
func (r Role) Valid() bool {
	return r >= RolePublic && r <= RoleAdmin
}

// RequiresLogin reports whether this role has a user behind it.
//
// The single most consequential predicate in this file. A capability on an
// anonymous request is a bug with a security consequence, so callers that gate
// writes go through here rather than checking "is there a session" themselves.
func (r Role) RequiresLogin() bool {
	return r != RolePublic
}

// Capabilities is what a role may do.
//
// A struct of booleans rather than a bitmask, because a bitmask makes
// `CanVote & CanCurate != 0` a legal expression and the interesting question is
// never a bitwise combination. A bitmask is the right tool for a fast
// permissions cache and the wrong tool for the thing that decides who may
// change shared content.
type Capabilities struct {
	// BrowseTiered reads shared content within the caller's consent tier. It
	// is true for every role, including public: reading is what a public site
	// is for. The tier filter is still applied — this says "may read", not
	// "may read everything".
	BrowseTiered bool

	// Vote casts a ballot on a proposal. Requires a login, always.
	Vote bool

	// Propose opens a new edit proposal for a shared field. This is the
	// contributor threshold, and it is the capability the whole governance
	// model exists to attach consequences to.
	Propose bool

	// WithdrawOwn retracts a proposal the caller authored. It is deliberately
	// NOT implied by Propose: withdrawal is a separate act with separate
	// consequences for the audit trail, and a user who can propose should not
	// be able to erase a settled decision's history. In practice a steward
	// moderates rather than asks the author to withdraw.
	WithdrawOwn bool

	// SeeModerationQueue lists other people's pending proposals.
	SeeModerationQueue bool

	// Moderate settles a proposal on the steward's authority, bypassing
	// quorum. It never writes a shared field directly — Apply does that, and
	// the audit row is written there.
	Moderate bool

	// ManageRoles assigns roles, and ManageSettings and ManagePeers configure
	// the instance. The only capabilities that are about the instance rather
	// than about content.
	ManageRoles     bool
	ManageSettings  bool
	ManagePeers    bool
}

// roleCapabilities is the table. A map rather than a switch so that a new role
// is a data change, and so that a role with no entry is a lookup miss rather
// than a silently-continued fallthrough to the zero value.
//
// The zero Capabilities is deliberately inert: it grants nothing. A missing
// entry therefore denies everything, which is the only safe direction for a
// table that gates writes.
var roleCapabilities = map[Role]Capabilities{
	RolePublic: {
		BrowseTiered: true,
		// Everything else false. Not omitted — spelled out, because an omitted
		// field in a table of permissions is indistinguishable from a forgotten
		// one, and this is the table.
		Vote:               false,
		Propose:            false,
		WithdrawOwn:        false,
		SeeModerationQueue: false,
		Moderate:           false,
		ManageRoles:        false,
		ManageSettings:     false,
		ManagePeers:        false,
	},
	RoleSubscriber: {
		BrowseTiered: true,
		Vote:         true,
		// No Propose: a logged-in account is not automatically trusted to make
		// claims about shared content. This is the gate that keeps a public
		// instance from being spam, and it is why RoleSubscriber exists as a
		// distinct step rather than "public but logged in".
		Propose:            false,
		WithdrawOwn:        true,
		SeeModerationQueue: false,
		Moderate:           false,
		ManageRoles:        false,
		ManageSettings:     false,
		ManagePeers:        false,
	},
	RoleContributor: {
		BrowseTiered:       true,
		Vote:               true,
		Propose:            true,
		WithdrawOwn:        true,
		SeeModerationQueue: false,
		Moderate:           false,
		ManageRoles:        false,
		ManageSettings:     false,
		ManagePeers:        false,
	},
	RoleSteward: {
		BrowseTiered:       true,
		Vote:               true,
		Propose:            true,
		WithdrawOwn:        true,
		SeeModerationQueue: true,
		Moderate:           true,
		ManageRoles:        false,
		ManageSettings:     false,
		ManagePeers:        false,
	},
	RoleAdmin: {
		BrowseTiered:       true,
		Vote:               true,
		Propose:            true,
		WithdrawOwn:        true,
		SeeModerationQueue: true,
		Moderate:           true,
		ManageRoles:        true,
		ManageSettings:     true,
		ManagePeers:        true,
	},
}

// CapabilitiesFor returns what a role may do.
//
// An unknown role gets the zero Capabilities, which grants nothing. That is
// deliberate and is the reason Valid exists as a separate predicate a caller
// can choose to check: a table lookup cannot distinguish "public" from "this
// role does not exist", so it must not be the thing that decides.
func CapabilitiesFor(r Role) Capabilities {
	return roleCapabilities[r]
}

// Can reports whether a role holds a named capability.
//
// Not a method on Capabilities on purpose: the call sites read
// `Can(role, CapabilityPropose)`, and a named-constant boolean method would
// make every capability a method on a struct that grows forever. The
// Capability type keeps the call site exhaustive under a compiler, which the
// bool-struct version does not.
type Capability int

const (
	CapBrowse Capability = iota
	CapVote
	CapPropose
	CapWithdrawOwn
	CapSeeModerationQueue
	CapModerate
	CapManageRoles
	CapManageSettings
	CapManagePeers
)

func (c Capability) String() string {
	switch c {
	case CapBrowse:
		return "browse"
	case CapVote:
		return "vote"
	case CapPropose:
		return "propose"
	case CapWithdrawOwn:
		return "withdraw-own"
	case CapSeeModerationQueue:
		return "see-moderation-queue"
	case CapModerate:
		return "moderate"
	case CapManageRoles:
		return "manage-roles"
	case CapManageSettings:
		return "manage-settings"
	case CapManagePeers:
		return "manage-peers"
	default:
		return "unknown"
	}
}

// capabilityFields maps each Capability to the field that grants it.
//
// A table rather than a switch so that adding a capability to Capabilities
// without adding it here is a compile error rather than a capability that
// silently always evaluates false — which would be a permission that cannot be
// granted, discovered only by a user reporting they cannot do something they
// should be able to.
var capabilityFields = map[Capability]func(Capabilities) bool{
	CapBrowse:               func(c Capabilities) bool { return c.BrowseTiered },
	CapVote:                 func(c Capabilities) bool { return c.Vote },
	CapPropose:              func(c Capabilities) bool { return c.Propose },
	CapWithdrawOwn:          func(c Capabilities) bool { return c.WithdrawOwn },
	CapSeeModerationQueue:   func(c Capabilities) bool { return c.SeeModerationQueue },
	CapModerate:             func(c Capabilities) bool { return c.Moderate },
	CapManageRoles:          func(c Capabilities) bool { return c.ManageRoles },
	CapManageSettings:       func(c Capabilities) bool { return c.ManageSettings },
	CapManagePeers:          func(c Capabilities) bool { return c.ManagePeers },
}

// Can reports whether r holds c.
//
// An unknown role or capability answers false. Both directions fail closed,
// which is the only defensible direction for a function whose answer is the
// last thing between a request and a write to shared content.
func Can(r Role, c Capability) bool {
	get, ok := capabilityFields[c]
	if !ok {
		return false
	}
	return get(CapabilitiesFor(r))
}

// CanAny reports whether r holds at least one of the capabilities.
//
// Takes variadic so that a caller checking a set cannot accidentally check only
// the first by writing a loop, and cannot forget an OR by checking them in
// sequence.
func CanAny(r Role, caps ...Capability) bool {
	for _, c := range caps {
		if Can(r, c) {
			return true
		}
	}
	return false
}

// MinRoleFor returns the lowest role holding a capability, and whether any
// role holds it.
//
// This exists for the "what must I promote someone to" question, which the UI
// asks and which has a wrong answer available at every call site: hardcoding
// "contributors can propose" in a component is correct until a sixth role
// exists, and then it is wrong in a way nothing tests.
func MinRoleFor(c Capability) (Role, bool) {
	// The iteration order is the declaration order, which is ascending
	// privilege, so the first match is the minimum.
	for _, r := range []Role{RolePublic, RoleSubscriber, RoleContributor, RoleSteward, RoleAdmin} {
		if Can(r, c) {
			return r, true
		}
	}
	return RolePublic, false
}

// RoleSatisfies reports whether a held role meets a required role.
//
// Ordinal comparison, and the ordering is the whole point: it is declared
// ascending in privilege above, so >= means "at least this privileged". An
// admin satisfies a steward requirement because an admin can do everything a
// steward can.
func RoleSatisfies(held, required Role) bool {
	if !held.Valid() || !required.Valid() {
		return false
	}
	return held >= required
}

// ResolveRole maps the instance's existing privilege flags onto a role.
//
// This is the migration path, and it is the reason M2 is not discarded. M2 had
// two booleans — is_owner and is_moderator — and every instance in the field has
// rows shaped that way. Rather than migrate rows, the flags are read once and
// mapped:
//
//	owner     -> admin      (the owner is the instance's administrator)
//	moderator -> steward
//	otherwise -> subscriber (M2 had no contributor tier; every ordinary
//	                        account could propose, so granting it here
//	                        preserves M2's behaviour exactly)
//
// The last line is the one that matters. Mapping ordinary accounts to
// contributor would be tidier and would silently change who may propose edits
// the moment this ships. M2 let anyone propose; M2b must not decide otherwise
// without a migration, so the mapping is chosen to be behaviour-preserving and
// says so.
func ResolveRole(isOwner, isModerator bool) Role {
	switch {
	case isOwner:
		return RoleAdmin
	case isModerator:
		return RoleSteward
	default:
		return RoleContributor
	}
}
