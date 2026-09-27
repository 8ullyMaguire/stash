package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/doug-martin/goqu/v9"
	"github.com/doug-martin/goqu/v9/exp"
	"github.com/jmoiron/sqlx"
	"github.com/mattn/go-sqlite3"
	"gopkg.in/guregu/null.v4"

	"github.com/stashapp/stash/pkg/models"
)

const (
	userTable = "users"

	userUsernameColumn     = "username"
	userPasswordHashColumn = "password_hash"
	userEmailColumn        = "email"
	userCreatedAtColumn    = "created_at"
	userDisabledAtColumn   = "disabled_at"
	userIsOwnerColumn      = "is_owner"
	userReputationColumn   = "reputation"
	userIsModeratorColumn  = "is_moderator"
)

type userRow struct {
	ID int `db:"id" goqu:"skipinsert"`
	// Username, PasswordHash, IsOwner and Reputation are the only columns
	// written on insert. Email, CreatedAt, DisabledAt are `goqu:"skipinsert"`
	// so the database supplies them: the schema defaults created_at to
	// CURRENT_TIMESTAMP and the other two to NULL. Writing a zero time.Time
	// here would be sent as "0001-01-01 00:00:00", silently overriding the
	// column default, and every account would be born in year one.
	Username     string      `db:"username"`
	PasswordHash []byte      `db:"password_hash"`
	Email        null.String `db:"email" goqu:"skipinsert"`
	CreatedAt    Timestamp   `db:"created_at" goqu:"skipinsert"`
	DisabledAt   null.Time   `db:"disabled_at" goqu:"skipinsert"`
	IsOwner      bool        `db:"is_owner"`
	IsModerator  bool        `db:"is_moderator"`
	Reputation   int         `db:"reputation"`
}

func (r *userRow) fromUser(u models.User, passwordHash []byte) {
	r.ID = u.ID
	r.Username = u.Username
	r.PasswordHash = passwordHash
	r.Email = null.StringFromPtr(u.Email)
	r.CreatedAt = Timestamp{Timestamp: u.CreatedAt}
	r.DisabledAt = null.TimeFromPtr(u.DisabledAt)
	r.IsOwner = u.IsOwner
	r.IsModerator = u.IsModerator
	r.Reputation = u.Reputation
}

func (r *userRow) resolve() *models.User {
	return &models.User{
		ID:          r.ID,
		Username:    r.Username,
		Email:       r.Email.Ptr(),
		CreatedAt:   r.CreatedAt.Timestamp,
		DisabledAt:  r.DisabledAt.Ptr(),
		IsOwner:     r.IsOwner,
		IsModerator: r.IsModerator,
		Reputation:  r.Reputation,
	}
}

// UserStore persists accounts. See models.User for the role semantics; the
// short version is that IsOwner grants instance administration and NOT
// authority over shared content, so nothing in the content path may consult it.
type UserStore struct {
	repository
	tableMgr *table
}

func NewUserStore() *UserStore {
	return &UserStore{
		repository: repository{
			tableName: userTable,
			idColumn:  idColumn,
		},
		tableMgr: userTableMgr,
	}
}

func (qb *UserStore) table() exp.IdentifierExpression {
	return qb.tableMgr.table
}

func (qb *UserStore) selectDataset() *goqu.SelectDataset {
	return dialect.From(qb.table()).Select(qb.table().All())
}

func (qb *UserStore) get(ctx context.Context, q *goqu.SelectDataset) (*models.User, error) {
	ret, err := qb.getMany(ctx, q)
	if err != nil {
		return nil, err
	}
	if len(ret) == 0 {
		return nil, sql.ErrNoRows
	}
	return ret[0], nil
}

func (qb *UserStore) getMany(ctx context.Context, q *goqu.SelectDataset) ([]*models.User, error) {
	const single = false
	var ret []*models.User
	// StructScan takes a pointer to a single struct, not a slice, so rows are
	// scanned one at a time and appended. Passing a []userRow panics inside
	// sqlx's reflect mapper rather than returning an error.
	if err := queryFunc(ctx, q, single, func(r *sqlx.Rows) error {
		var row userRow
		if err := r.StructScan(&row); err != nil {
			return err
		}
		ret = append(ret, row.resolve())
		return nil
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

// isConstraintError reports whether err is a SQLite constraint violation.
func isConstraintError(err error) bool {
	var sqliteError sqlite3.Error
	if errors.As(err, &sqliteError) {
		return sqliteError.Code == sqlite3.ErrConstraint
	}
	return false
}

// Create inserts a new account. passwordHash must already be a PHC argon2id
// string from pkg/auth -- this store deliberately never sees a plaintext
// password, so there is no code path here that could log one.
func (qb *UserStore) Create(ctx context.Context, newUser *models.User, passwordHash []byte) error {
	var r userRow
	r.fromUser(*newUser, passwordHash)

	id, err := qb.tableMgr.insertID(ctx, r)
	if err != nil {
		// The username index is COLLATE NOCASE, so a case-variant collision
		// surfaces here as a constraint error. Translate it to a sentinel the
		// caller can act on without inspecting driver error strings -- and
		// without echoing the submitted username back to an unauthenticated
		// endpoint, which is how registration becomes a user enumeration oracle.
		if isConstraintError(err) {
			return models.ErrUsernameTaken
		}
		return err
	}

	created, err := qb.Find(ctx, id)
	if err != nil {
		return fmt.Errorf("finding user after create: %w", err)
	}

	*newUser = *created
	return nil
}

// Update writes the mutable profile fields. It deliberately cannot change the
// password hash or the owner flag: password changes go through the auth service,
// which re-hashes and can require the current password, and the owner flag is
// the one role the spec says must never be ambiguous. Letting a general Update
// reach either column would bypass those rules from an ordinary update call.
func (qb *UserStore) Update(ctx context.Context, updatedUser *models.User) error {
	_, err := dbWrapper.Exec(ctx, fmt.Sprintf(
		"UPDATE %s SET %s = ?, %s = ?, %s = ? WHERE %s = ?",
		userTable, userEmailColumn, userDisabledAtColumn, userReputationColumn, idColumn),
		null.StringFromPtr(updatedUser.Email),
		null.TimeFromPtr(updatedUser.DisabledAt),
		updatedUser.Reputation,
		updatedUser.ID,
	)
	return err
}

// SetPasswordHash replaces the stored credential. Only pkg/auth's login rehash
// path and an explicit password change may call it.
func (qb *UserStore) SetPasswordHash(ctx context.Context, id int, hash []byte) error {
	_, err := dbWrapper.Exec(ctx, fmt.Sprintf(
		"UPDATE %s SET %s = ? WHERE %s = ?", userTable, userPasswordHashColumn, idColumn),
		hash, id)
	return err
}

// FindPasswordHash returns the stored PHC string for a user id. It is separate
// from Find because the rest of the application has no reason to hold a
// password hash, and returning it from the general finder would guarantee that
// some future endpoint logs or serialises it.
func (qb *UserStore) FindPasswordHash(ctx context.Context, id int) ([]byte, error) {
	var hash []byte
	err := dbWrapper.Get(ctx, &hash, fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s = ?", userPasswordHashColumn, userTable, idColumn), id)
	if err != nil {
		return nil, err
	}
	return hash, nil
}

func (qb *UserStore) find(ctx context.Context, id int) (*models.User, error) {
	q := qb.selectDataset().Where(qb.tableMgr.byID(id))
	return qb.get(ctx, q)
}

// Find returns the user with this id, or nil if there is none.
func (qb *UserStore) Find(ctx context.Context, id int) (*models.User, error) {
	ret, err := qb.find(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return ret, err
}

// FindByUsername looks up an account case-insensitively, matching the
// `users.username` COLLATE NOCASE index. A miss returns (nil, nil): absence is
// the normal case on a login form, not a fault.
func (qb *UserStore) FindByUsername(ctx context.Context, username string) (*models.User, error) {
	q := qb.selectDataset().Where(goqu.C(userUsernameColumn).Eq(username))
	ret, err := qb.get(ctx, q)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return ret, err
}

func (qb *UserStore) FindAll(ctx context.Context) ([]*models.User, error) {
	q := dialect.From(qb.table()).Select(qb.table().All()).Order(goqu.C(userUsernameColumn).Asc())
	return qb.getMany(ctx, q)
}

func (qb *UserStore) Count(ctx context.Context) (int, error) {
	q := dialect.Select(goqu.COUNT("*")).From(qb.table())
	return count(ctx, q)
}

func (qb *UserStore) Destroy(ctx context.Context, id int) error {
	return qb.destroyExisting(ctx, []int{id})
}

// SetDisabled withdraws or restores the ability to log in while keeping the
// row, so authored proposals and audit history survive. A hard delete would
// silently rewrite history.
func (qb *UserStore) SetDisabled(ctx context.Context, id int, disabled bool) error {
	actor, err := qb.Find(ctx, id)
	if err != nil {
		return err
	}

	// Disabling the last owner would leave an instance nobody can administer:
	// no moderator appointments, no user management, no way back short of
	// editing the database by hand. Refused, and the reason is the only way an
	// operator learns why their button did nothing.
	if disabled && actor.IsOwner {
		return models.ErrCannotDisableOwner
	}

	var ts null.Time
	if disabled {
		ts = null.TimeFrom(time.Now())
	}
	_, err = dbWrapper.Exec(ctx, fmt.Sprintf(
		"UPDATE %s SET %s = ? WHERE %s = ?", userTable, userDisabledAtColumn, idColumn),
		ts, id)
	return err
}

// SetModerator appoints or removes a moderator.
//
// The owner check is here rather than in the resolver on purpose. A governance
// invariant that lives in a call site is one forgotten check away from being
// violated, and the resolver is exactly the layer a future refactor rewrites.
// It takes the acting user so the store can authorise, not just mutate.
func (qb *UserStore) SetModerator(ctx context.Context, actorID, id int, moderator bool) error {
	actor, err := qb.Find(ctx, actorID)
	if err != nil {
		return err
	}
	if !actor.IsOwner {
		return models.ErrNotOwner
	}

	// The owner is not a moderator, and cannot make themselves one. Removing
	// the flag is allowed -- but the schema's single-owner index means an
	// instance always keeps exactly one administrator, so this is a role
	// change and not a way to resign.
	target, err := qb.Find(ctx, id)
	if err != nil {
		return err
	}
	if target.IsOwner && moderator {
		return models.ErrOwnerIsNotModerator
	}

	_, err = dbWrapper.Exec(ctx, fmt.Sprintf(
		"UPDATE %s SET %s = ? WHERE %s = ?", userTable, userIsModeratorColumn, idColumn),
		moderator, id)
	return err
}

// AddReputation applies a delta. Reputation is delta-only by design: it moves
// when a quorum accepts a proposal, so there is no code path by which a user
// can set their own score.
func (qb *UserStore) AddReputation(ctx context.Context, id int, delta int) error {
	_, err := dbWrapper.Exec(ctx, fmt.Sprintf(
		"UPDATE %s SET %s = %s + ? WHERE %s = ?",
		userTable, userReputationColumn, userReputationColumn, idColumn),
		delta, id)
	return err
}

// CountOwners reports how many rows hold is_owner = 1. The schema's partial
// unique index caps this at one, so a result above 1 means the index was
// dropped and the governance invariant is gone -- which is why this is
// exported rather than assumed.
func (qb *UserStore) CountOwners(ctx context.Context) (int, error) {
	q := dialect.Select(goqu.COUNT("*")).From(qb.table()).Where(goqu.C(userIsOwnerColumn).IsTrue())
	return count(ctx, q)
}

var _ models.UserStore = (*UserStore)(nil)
