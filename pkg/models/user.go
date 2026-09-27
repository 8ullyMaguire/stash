package models

import (
	"context"
	"errors"
	"time"
)

// ErrUsernameTaken means the username collides with an existing account.
// The `users.username` index is COLLATE NOCASE, so "Alice" and "alice" are
// one account and the store reports this rather than a raw constraint error --
// the message must not echo the password that was submitted alongside it.
var ErrUsernameTaken = errors.New("username already taken")

// ErrInvalidCredentials is deliberately opaque. A login failure must not
// reveal whether the username exists: distinguishing "no such user" from
// "wrong password" turns the login form into a user enumeration oracle.
var ErrInvalidCredentials = errors.New("invalid credentials")

// ErrAccountLocked means too many consecutive failures. See
// UserStore.RecordLoginFailure.
var ErrAccountLocked = errors.New("account temporarily locked")

// ErrNotOwner is returned when a non-owner attempts an owner-only operation.
var ErrNotOwner = errors.New("owner privileges required")

// User is a StashForge account. Upstream Stash has no user model at all --
// auth is a single shared username/password in config -- so this is new
// ground rather than a refactor of an existing type.
type User struct {
	ID           int
	Username     string
	Email        *string
	CreatedAt    time.Time
	DisabledAt   *time.Time
	IsOwner      bool
	Reputation   int
	SessionCount int
}

// Active reports whether the account may authenticate. A disabled user keeps
// their row so authored proposals and audit history survive; only the ability
// to log in is withdrawn.
func (u *User) Active() bool {
	return u != nil && u.DisabledAt == nil
}

// Owns reports whether the user holds the owner role.
//
// Owner is NOT a superuser. Per the spec the owner gets user management,
// library paths, instance settings and moderator appointment -- and no
// authority over shared content. A moderator quorum that the owner could
// unilaterally overrule would not be moderation, so no method on this type
// confers that power, and nothing in the content path consults IsOwner.
func (u *User) Owns() bool {
	return u != nil && u.IsOwner
}

// UserReader is the read half of user persistence.
type UserReader interface {
	// FindByUsername returns the user with this exact (case-insensitive)
	// username, or nil if there is none. It never returns an error for a
	// missing user: absence is the normal case on a login form, not a fault.
	FindByUsername(ctx context.Context, username string) (*User, error)
	Find(ctx context.Context, id int) (*User, error)

	// FindPasswordHash returns the stored PHC argon2id string. It is on the
	// reader rather than folded into Find because the rest of the application
	// has no reason to hold a credential, and a general getter would guarantee
	// some future endpoint logs or serialises one.
	FindPasswordHash(ctx context.Context, id int) ([]byte, error)
	FindAll(ctx context.Context) ([]*User, error)
	Count(ctx context.Context) (int, error)
}

// UserWriter is the write half of user persistence.
//
// Create takes the PHC password hash as an explicit argument rather than
// reading it off the struct. That is deliberate: a User value must never be
// able to carry a credential, so there is no representation in which a user
// object exists with a password attached that some endpoint could log, cache or
// serialise. Hashing happens in pkg/auth before the store is called.
type UserWriter interface {
	Create(ctx context.Context, newUser *User, passwordHash []byte) error
	Update(ctx context.Context, updatedUser *User) error
	Destroy(ctx context.Context, id int) error

	// SetPasswordHash replaces a credential. Only pkg/auth's login rehash path
	// and an explicit password change may call it.
	SetPasswordHash(ctx context.Context, id int, hash []byte) error

	// SetDisabled withdraws or restores the ability to log in, keeping the
	// row. Returns ErrNotOwner if a non-owner attempts it.
	SetDisabled(ctx context.Context, id int, disabled bool) error

	// AddReputation adjusts the stored reputation. Deltas are applied by the
	// caller after a quorum accepts a proposal; this is the only sanctioned
	// route, so reputation cannot be set arbitrarily by a user.
	AddReputation(ctx context.Context, id int, delta int) error
}

// UserStore is the full interface, as used by the API and session layers.
type UserStore interface {
	UserReader
	UserWriter
}
