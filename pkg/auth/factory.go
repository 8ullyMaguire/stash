package auth

import (
	"context"

	"github.com/stashapp/stash/internal/collab"
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

	// TOTP is the 2FA repository. nil in single-user mode, and the session store
	// then does not ask about a second factor at all.
	//
	// Optional even in multi-user mode: an instance that has not been upgraded
	// yet has no TOTP table, and refusing to start over that would make the
	// upgrade a breaking change. A nil TOTP means "no 2FA", which is a weaker but
	// running state -- and it is logged, because silently running without 2FA on
	// an owner account is the failure this exists to prevent.
	TOTP TOTPStore
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
// Build assembles the session store for the resolved auth mode.
//
// ctx MUST carry a database reader when the mode has to be inferred (f.MultiUser is nil and
// f.Users is set): resolveMode counts the rows in the users table, and every sqlite read goes
// through getDBReader, which fails with a bare "not in transaction" when the context has neither a
// transaction nor a dbKey.
//
// This was not theoretical. Startup called Build(context.Background()) indirectly -- resolveMode
// did its own `context.Background()` -- and every fresh sqlite instance died at boot with:
//
//	StashForge auth: querying `SELECT COUNT(*) FROM users` [[]]: not in transaction
//
// after the migrations had already run, so the failure looked like a database problem rather than
// a missing context value. The fix is here rather than at the call site because the requirement is
// intrinsic to the query: any caller that infers the mode owes the count a reader.
//
// A caller that already knows the mode can set f.MultiUser and pass any context, including nil --
// resolveMode returns before touching the database in that case.
func (f *Factory) Build(ctx context.Context, cookieStore session.Store) (store session.Store, mode Mode, err error) {
	// Named results, initialised to a true nil interface. Every return below
	// either sets store to a real implementation or leaves it nil alongside a
	// non-nil error.
	var nilStore session.Store

	resolvedMode, err := f.resolveMode(ctx)
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
	// 2FA is attached here, where the multi-user store is built, rather than at
	// each call site. A setter that some callers remembered and others did not
	// would produce an instance where the owner account has a secret nobody
	// checks -- which reads as "2FA is on" and protects nothing.
	dbStore := NewSessionStore(f.Users, f.Sessions, f.Invites, f.Audit, f.Config)
	if f.TOTP == nil {
		// Not fatal, but said out loud: an owner account with no 2FA store is
		// exactly the state this project exists to prevent, and it should be
		// visible in the log rather than discovered in an incident.
		logger.Warnf("StashForge: no 2FA store configured; logins will not require a second factor")
	} else {
		// The store is both halves of the hook: it holds the secret AND performs
		// the atomic spend, so splitting them would mean the arithmetic and the
		// replay record could end up in different places.
		dbStore.SetTOTPVerifier(f.TOTP, f.TOTP, collab.DefaultTOTPRequired)
	}

	store = session.NewHTTPAdapter(
		dbStore,
		session.DefaultSessionCookieName,
		f.Config.GetMaxSessionAge(),
	)
	return store, mode, nil
}

func (f *Factory) resolveMode(ctx context.Context) (Mode, error) {
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
	n, err := f.Users.Count(ctx)
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
