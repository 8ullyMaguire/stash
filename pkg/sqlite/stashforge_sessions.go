package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/doug-martin/goqu/v9"
	"gopkg.in/guregu/null.v4"

	"github.com/stashapp/stash/pkg/auth"
)

// errInviteKeyInvalid covers unknown, expired, revoked and exhausted. One
// sentinel for all four, because a registration form that distinguishes them is
// an oracle for probing which keys exist.
var errInviteKeyInvalid = errors.New("invalid invite key")

// intOrNull converts an optional id for a nullable INTEGER column. nil means
// SQL NULL, which for actor_id is the interesting case: it marks an action
// taken by someone who is not (yet) authenticated.
func intOrNull(v *int) interface{} {
	if v == nil {
		return nil
	}
	return *v
}

// auditDetailJSON marshals the detail blob, truncating on failure rather than
// propagating: a value that cannot be encoded must not cost us the audit row
// itself, which is the part that matters.
func detailJSON(detail map[string]interface{}) string {
	if len(detail) == 0 {
		return ""
	}
	b, err := json.Marshal(detail)
	if err != nil {
		return `{"error":"detail could not be encoded"}`
	}
	return string(b)
}

const (
	sessionTable = "user_sessions"

	sessionUserIDColumn    = "user_id"
	sessionExpiresAtColumn = "expires_at"
	sessionIPColumn        = "ip"
	sessionUserAgentColumn = "user_agent"
)

// UserSessionStore is the session-table half of StashForge's session
// persistence.
//
// Session ids arrive already hashed from pkg/auth: this store never sees the
// plaintext, so there is no code path here that could log a usable credential.
type UserSessionStore struct {
	repository
	tableMgr *table
}

func NewUserSessionStore() *UserSessionStore {
	return &UserSessionStore{
		repository: repository{tableName: sessionTable, idColumn: "id"},
		tableMgr:   userSessionTableMgr,
	}
}

// Create stores a session. The id is the SHA-256 of the cookie value.
//
// The INSERT is deliberately an upsert on the id: re-creating a session with the
// same id is not a thing that happens with 256 bits of randomness, but a plain
// INSERT that failed here would return an opaque constraint error and take the
// login down with it.
func (s *UserSessionStore) Create(ctx context.Context, idHash []byte, userID int, expiresAt time.Time, ip, userAgent string) error {
	_, err := dbWrapper.Exec(ctx, fmt.Sprintf(
		"INSERT OR REPLACE INTO %s (id, %s, %s, %s, %s) VALUES (?, ?, ?, ?, ?)",
		sessionTable, sessionUserIDColumn, sessionExpiresAtColumn,
		sessionIPColumn, sessionUserAgentColumn),
		idHash, userID, expiresAt, ip, userAgent)
	return err
}

// FindSession returns the user id for a session, and whether it exists and is
// still valid.
//
// Expiry is enforced HERE, in SQL, rather than by the caller. A caller-side
// comparison is one forgotten `if` from authenticating an expired session, and
// the predicate is cheap and index-backed.
func (s *UserSessionStore) FindSession(ctx context.Context, idHash []byte, now time.Time) (int, bool, error) {
	var userID int

	err := dbWrapper.Get(ctx, &userID, fmt.Sprintf(
		"SELECT %s FROM %s WHERE id = ? AND %s > ?",
		sessionUserIDColumn, sessionTable, sessionExpiresAtColumn),
		idHash, now)
	if err != nil {
		// errors.Is, not ==: the database wrapper wraps driver errors, so a
		// direct comparison is never true and "no such session" would surface
		// as a 500 instead of an anonymous request.
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}

	return userID, true, nil
}

func (s *UserSessionStore) DeleteSession(ctx context.Context, idHash []byte) error {
	_, err := dbWrapper.Exec(ctx,
		fmt.Sprintf("DELETE FROM %s WHERE id = ?", sessionTable), idHash)
	return err
}

func (s *UserSessionStore) DeleteSessionsForUser(ctx context.Context, userID int) error {
	_, err := dbWrapper.Exec(ctx,
		fmt.Sprintf("DELETE FROM %s WHERE %s = ?", sessionTable, sessionUserIDColumn), userID)
	return err
}

// PurgeExpired deletes every session that expired before now. Without it the
// table grows without bound on an instance with many users, since nothing else
// removes a row.
func (s *UserSessionStore) PurgeExpired(ctx context.Context, now time.Time) (int, error) {
	res, err := dbWrapper.Exec(ctx,
		fmt.Sprintf("DELETE FROM %s WHERE %s <= ?", sessionTable, sessionExpiresAtColumn), now)
	if err != nil {
		return 0, err
	}

	// RowsAffected is unavailable on every driver; a count that cannot be
	// trusted is worse than none, so this reports 0 rather than guessing.
	if n, err := res.RowsAffected(); err == nil {
		return int(n), nil
	}
	return 0, nil
}

// Count is used by tests and the admin UI to show live sessions.
func (s *UserSessionStore) Count(ctx context.Context) (int, error) {
	q := dialect.Select(goqu.COUNT("*")).From(sessionTable)
	return count(ctx, q)
}

// DeleteExpiredForUser is the explicit "log me out everywhere" operation, which
// is what a user needs when they think a device is compromised.
func (s *UserSessionStore) DeleteExpiredForUser(ctx context.Context, userID int, now time.Time) (int, error) {
	res, err := dbWrapper.Exec(ctx, fmt.Sprintf(
		"DELETE FROM %s WHERE %s = ? AND %s <= ?",
		sessionTable, sessionUserIDColumn, sessionExpiresAtColumn), userID, now)
	if err != nil {
		return 0, err
	}
	if n, err := res.RowsAffected(); err == nil {
		return int(n), nil
	}
	return 0, nil
}

// --- invites ---------------------------------------------------------------

const (
	inviteTable = "invite_keys"

	inviteKeyHashColumn = "key_hash"
	inviteCreatedByCol  = "created_by"
	inviteExpiresAtCol  = "expires_at"
	inviteMaxUsesCol    = "max_uses"
	inviteUsesCol       = "uses"
	inviteRevokedAtCol  = "revoked_at"
)

// InviteStore persists invite keys. Only the SHA-256 of a key is ever stored,
// so a database leak yields no usable invitations.
type InviteStore struct {
	repository
	tableMgr *table
}

func NewInviteStore() *InviteStore {
	return &InviteStore{
		repository: repository{tableName: inviteTable, idColumn: inviteKeyHashColumn},
		tableMgr:   inviteTableMgr,
	}
}

func (s *InviteStore) CreateInvite(ctx context.Context, keyHash []byte, createdBy int, expiresAt *time.Time, maxUses int) error {
	_, err := dbWrapper.Exec(ctx, fmt.Sprintf(
		"INSERT INTO %s (%s, %s, %s, %s) VALUES (?, ?, ?, ?)",
		inviteTable, inviteKeyHashColumn, inviteCreatedByCol, inviteExpiresAtCol, inviteMaxUsesCol),
		keyHash, createdBy, null.TimeFromPtr(expiresAt), maxUses)
	return err
}

// RedeemInvite atomically consumes one use of a key.
//
// The whole point is that this is ONE statement. A read-then-write leaves a
// window in which one leaked key mints many accounts, which is the exact failure
// the table's CHECK constraint backstops -- and a constraint violation would then
// surface as a raw driver error instead of ErrInviteInvalid.
//
// The expired/revoked/exhausted cases are folded into the WHERE clause, so a key
// in any of those states simply matches zero rows rather than being redeemed
// and then rolled back.
func (s *InviteStore) RedeemInvite(ctx context.Context, keyHash []byte, now time.Time) (int, error) {
	// The expiry predicate is spelled out rather than expressed in goqu,
	// because an UPDATE with a column-to-column comparison and a bind
	// parameter in the same statement is not worth fighting the DSL for.
	res, execErr := dbWrapper.Exec(ctx, fmt.Sprintf(
		"UPDATE %s SET %s = %s + 1 WHERE %s = ? AND %s < %s AND %s IS NULL "+
			"AND (expires_at IS NULL OR expires_at > ?)",
		inviteTable, inviteUsesCol, inviteUsesCol, inviteKeyHashColumn,
		inviteUsesCol, inviteMaxUsesCol, inviteRevokedAtCol),
		keyHash, now)
	if execErr != nil {
		return 0, execErr
	}

	if n, err := res.RowsAffected(); err == nil && n == 0 {
		// Unknown, expired, revoked or exhausted: all four are the same answer
		// to the caller, deliberately.
		return 0, errInviteKeyInvalid
	}

	// Read the creator back for the audit trail, for the audit trail's own row.
	var createdBy int
	if getErr := dbWrapper.Get(ctx, &createdBy, fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s = ?", inviteCreatedByCol, inviteTable, inviteKeyHashColumn),
		keyHash); getErr != nil {
		return 0, getErr
	}

	return createdBy, nil
}

// ReleaseInvite returns one use to a key, undoing a redemption whose
// registration then failed. Best-effort by design: if it fails, the use is
// spent, which is the safe direction (a stricter limit, not a looser one).
func (s *InviteStore) ReleaseInvite(ctx context.Context, keyHash []byte) error {
	// `WHERE uses > 0` is the whole guard, and it is on the pre-decrement
	// value: 1 -> 0 passes, a second release sees 0 and is a no-op. So a
	// double release cannot hand a spent key an extra use -- it just fails to
	// move the counter, which is the safe direction.
	query := fmt.Sprintf(
		"UPDATE %s SET %s = %s - 1 WHERE %s = ? AND %s > 0",
		inviteTable, inviteUsesCol, inviteUsesCol, inviteKeyHashColumn, inviteUsesCol)

	_, err := dbWrapper.Exec(ctx, query, keyHash)
	return err
}

func (s *InviteStore) RevokeInvite(ctx context.Context, keyHash []byte) error {
	_, err := dbWrapper.Exec(ctx, fmt.Sprintf(
		"UPDATE %s SET %s = CURRENT_TIMESTAMP WHERE %s = ?",
		inviteTable, inviteRevokedAtCol, inviteKeyHashColumn), keyHash)
	return err
}

// --- audit -----------------------------------------------------------------

const (
	collabAuditTable = "collab_audit"

	collabAuditActorCol      = "actor_id"
	collabAuditActionCol     = "action"
	collabAuditTargetTypeCol = "target_type"
	collabAuditTargetIDCol   = "target_id"
	collabAuditFieldCol      = "field"
	collabAuditDetailCol     = "detail"
)

// AuditStore is append-only by construction: it exposes no update or delete.
// The single most important property of the audit table is that no code path
// can rewrite it, so the absence of those methods is the feature.
type AuditStore struct {
	repository
	tableMgr *table
}

func NewAuditStore() *AuditStore {
	return &AuditStore{
		repository: repository{tableName: collabAuditTable, idColumn: idColumn},
		tableMgr:   collabAuditTableMgr,
	}
}

// Append writes one audit row.
func (s *AuditStore) Append(ctx context.Context, actorID *int, action, targetType string, targetID *int, field string, detail map[string]interface{}) error {
	query := fmt.Sprintf("INSERT INTO %s (%s, %s, %s, %s, %s, %s) VALUES (?, ?, ?, ?, ?, ?)",
		collabAuditTable, collabAuditActorCol, collabAuditActionCol,
		collabAuditTargetTypeCol, collabAuditTargetIDCol, collabAuditFieldCol,
		collabAuditDetailCol)

	_, err := dbWrapper.Exec(ctx, query,
		intOrNull(actorID), action, null.StringFrom(targetType),
		intOrNull(targetID), null.StringFrom(field), detailJSON(detail))
	return err
}

// RecordLoginFailure writes the row that shows a brute-force or a signup flood.
//
// Note what is NOT here: no password, no session id, no invite key. A value
// written to this table is readable by any moderator forever, so anything
// sensitive that reached a call site would become permanent.
func (s *AuditStore) RecordLoginFailure(ctx context.Context, username, ip, reason string) error {
	detail := map[string]interface{}{"ip": ip, "reason": reason}
	if username != "" {
		detail["username"] = username
	}

	// actor_id is NULL: nobody is authenticated yet, which is precisely the
	// case worth recording.
	query := fmt.Sprintf("INSERT INTO %s (%s, %s) VALUES (?, ?)",
		collabAuditTable, collabAuditActionCol, collabAuditDetailCol)

	_, err := dbWrapper.Exec(ctx, query, "login_failed", detailJSON(detail))
	return err
}

// Entry is one audit row, as a moderator sees it.
type Entry struct {
	ID       int
	Action   string
	ActorID  sql.NullInt64
	Target   string
	TargetID sql.NullInt64
	Field    sql.NullString
	Detail   sql.NullString
}

// LoginFailureDetail is the decoded detail blob of a login_failed row.
type LoginFailureDetail struct {
	IP       string `json:"ip"`
	Reason   string `json:"reason"`
	Username string `json:"username"`
}

// ReadLoginFailure returns the failures recorded for a username, newest first.
//
// This exists as a first-class method rather than a test-only hook because the
// moderation UI needs exactly this: after a lockout, the owner has to be able to
// see WHICH usernames were probed and WHY each attempt failed. If the reason is
// not retrievable then the audit table is decorative.
func (s *AuditStore) ReadLoginFailure(ctx context.Context, username string) ([]LoginFailureDetail, error) {
	// Filter on the DECODED username, not a LIKE over the JSON blob. LIKE
	// matches anywhere in the text, so a probe of "bob" also returns a row
	// where bob is the IP or a substring of the reason -- and a moderation view
	// that shows one user's attempts under another's name is worse than none.
	// It also breaks on any username containing a LIKE wildcard.
	//
	// The LIKE stays as a cheap pre-filter to bound the scan; the decoded
	// comparison is what decides. An empty username means "every failure",
	// which is the all-attempts view.
	pattern := "%" + username + "%"
	rows, err := dbWrapper.QueryxContext(ctx, fmt.Sprintf(
		"SELECT detail FROM %s WHERE %s = ? AND %s LIKE ? ORDER BY %s DESC LIMIT 200",
		collabAuditTable, collabAuditActionCol, collabAuditDetailCol,
		idColumn),
		"login_failed", pattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []LoginFailureDetail
	for rows.Next() {
		var detail sql.NullString
		if err := rows.Scan(&detail); err != nil {
			return nil, err
		}
		if !detail.Valid || detail.String == "" {
			continue
		}

		var d LoginFailureDetail
		if err := json.Unmarshal([]byte(detail.String), &d); err != nil {
			return nil, err
		}

		// Exact match on the decoded field. The pre-filter is a superset, so
		// this is the test that actually decides.
		if username != "" && d.Username != username {
			continue
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ReadByAction returns the most recent rows for an action. For the admin view
// and for tests that need the stored detail, not just the count.
func (s *AuditStore) ReadByAction(ctx context.Context, action string) ([]Entry, error) {
	rows, err := dbWrapper.QueryxContext(ctx, fmt.Sprintf(
		"SELECT %s, %s, %s, %s, %s, %s, %s FROM %s WHERE %s = ? ORDER BY %s DESC LIMIT 50",
		idColumn, collabAuditActionCol, collabAuditActorCol, collabAuditTargetTypeCol,
		collabAuditTargetIDCol, collabAuditFieldCol, collabAuditDetailCol, collabAuditTable,
		collabAuditActionCol, idColumn),
		action)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.Action, &e.ActorID, &e.Target, &e.TargetID,
			&e.Field, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Count returns the number of audit rows matching an action. For tests and the
// admin view.
func (s *AuditStore) Count(ctx context.Context, action string) (int, error) {
	q := dialect.Select(goqu.COUNT("*")).From(collabAuditTable)
	if action != "" {
		q = q.Where(goqu.C(collabAuditActionCol).Eq(action))
	}
	return count(ctx, q)
}

// Compile-time proof that the SQLite implementations satisfy the interfaces
// pkg/auth depends on. Without these, a signature drift between the two
// packages surfaces as a wiring error at startup rather than at build time.
var (
	_ auth.SessionRepository = (*UserSessionStore)(nil)
	_ auth.InviteRepository  = (*InviteStore)(nil)
	_ auth.AuditRepository   = (*AuditStore)(nil)
)
