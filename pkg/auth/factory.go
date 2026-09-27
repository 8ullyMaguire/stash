package auth

import (
	"context"

	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/session"
)

// Mode is how this instance authenticates.
type Mode int

const (
	// ModeSingleUser is upstream Stash: one shared username and password in
	// config, identity carried inside a signed cookie. A legacy install keeps
	// working with no migration and no user table.
	ModeSingleUser Mode = iota

	// ModeMultiUser is StashForge: accounts in the database, server-side
	// sessions, invites.
	ModeMultiUser
)

func (m Mode) String() string {
	if m == ModeMultiUser {
		return "multi-user"
	}
	return "single-user"
}

// Factory builds the session store appropriate to the instance.
//
// This is the piece that makes the fork safe to adopt: an existing install
// keeps its exact authentication behaviour, and only an instance that opts in
// gets accounts. Selecting multi-user by default would silently lock every
// existing user out of their own library, which is the fastest way to make a
// fork uninstallable.
//
// The mode is inferred from whether the user table has ever been populated
// rather than from a config flag, so there is no state in which the two
// disagree: an install with users in the table always authenticates them,
// whatever the config says. MultiUser overrides that when set.
type Factory struct {
	// Users is the account store. May be nil, in which case the mode is
	// single-user unless MultiUser forces otherwise.
	Users models.UserStore

	// Sessions, Invites and Audit are the multi-user repositories. All three
	// are required in multi-user mode: without any of them the instance would
	// silently run without an audit trail, which is the one thing this project
	// exists to guarantee. So a missing one is an error, never a no-op default.
	Sessions SessionRepository
	Invites  InviteRepository
	Audit    AuditRepository

	// Config supplies the API key, the legacy username and the max session age.
	Config SessionConfig

	// MultiUser forces the mode. nil means "infer from the user table".
	MultiUser *bool
}

// Build returns the session store for this instance and the mode it chose.
//
// cookieStore is constructed by the caller because it needs a
// session.SessionConfig, which lives with the config type; passing it in keeps
// this function free of the internal config tree and therefore testable.
//
// On error it returns a nil *interface* rather than a typed nil pointer. A
// typed nil returned as an interface is non-nil, so a caller doing
// `if store == nil` would pass and then panic on first use; this keeps the
// error path honest at the cost of one named variable.
func (f *Factory) Build(cookieStore session.Store) (store session.Store, mode Mode, err error) {
	// Named results, initialised to a true nil interface. Every return below
	// either sets store to a real implementation or leaves it nil alongside a
	// non-nil error.
	var nilStore session.Store

	resolvedMode, err := f.resolveMode()
	if err != nil {
		return nilStore, ModeSingleUser, err
	}
	mode = resolvedMode

	if mode == ModeSingleUser {
		logger.Infof("StashForge: single-user auth (upstream cookie sessions)")
		return cookieStore, mode, nil
	}

	switch {
	case f.Users == nil:
		return nilStore, mode, missingDependency("Users")
	case f.Sessions == nil:
		return nilStore, mode, missingDependency("Sessions")
	case f.Invites == nil:
		return nilStore, mode, missingDependency("Invites")
	case f.Audit == nil:
		return nilStore, mode, missingDependency("Audit")
	case f.Config == nil:
		return nilStore, mode, missingDependency("Config")
	}

	logger.Infof("StashForge: multi-user auth enabled (database accounts, server-side sessions)")

	// The database store is request-shaped (no ResponseWriter: it reads an id
	// and looks up a row, it never writes a response). The HTTPAdapter is what
	// presents it as a session.Store, and it owns the session cookie. The cookie
	// name and lifetime come from the session config, so the adapter is built
	// here rather than inside SessionStore, which must stay HTTP-free.
	store = session.NewHTTPAdapter(
		NewSessionStore(f.Users, f.Sessions, f.Invites, f.Audit, f.Config),
		session.DefaultSessionCookieName,
		f.Config.GetMaxSessionAge(),
	)
	return store, mode, nil
}

func (f *Factory) resolveMode() (Mode, error) {
	if f.MultiUser != nil {
		if *f.MultiUser {
			return ModeMultiUser, nil
		}
		return ModeSingleUser, nil
	}

	if f.Users == nil {
		return ModeSingleUser, nil
	}

	// Infer from the user table. An empty table means a fresh or legacy
	// install, which stays single-user: promoting an instance to accounts
	// without an explicit decision is how you lock someone out of their own
	// library.
	n, err := f.Users.Count(context.Background())
	if err != nil {
		// A database read failure must NOT silently fall back to single-user.
		// That would authenticate every visitor as the single legacy config
		// user, which is the opposite of the safe direction. Fail loudly.
		return ModeSingleUser, err
	}

	if n == 0 {
		return ModeSingleUser, nil
	}
	return ModeMultiUser, nil
}

type missingDependency string

func (e missingDependency) Error() string {
	return "StashForge multi-user auth requires " + string(e) + ", but it was not provided"
}
