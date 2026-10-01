package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"time"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/pkg/auth"
)

// The 2FA store. M4 step 4.2.
//
// It implements auth.TOTPStore, so it is the durable half of the replay guard:
// collab.VerifyTOTP proves a code is arithmetically correct, and SpendTOTPStep is
// what makes it single-use. The record lives here rather than in memory because
// a restart must not reopen the replay window -- otherwise a code observed once
// becomes reusable every time the process bounces.
//
// The secret is stored SEALED. The instance key comes from the caller rather than
// from global config, because pkg/sqlite's stores are constructed explicitly and
// reaching for a global here would make the store untestable without one.

// TOTPInstanceKey supplies the key the secret is sealed with.
//
// A function rather than a []byte so the key is read at use time: a store built
// during startup would otherwise capture a key that a later config reload
// replaces, and every stored secret would become undecryptable.
type TOTPInstanceKey func() ([]byte, error)

type TOTPStore struct {
	repository
	instanceKey TOTPInstanceKey
	// required is the policy hook: owners must use 2FA, others may.
	required func(isOwner bool) bool
}

var (
	_ auth.TOTPStore    = (*TOTPStore)(nil)
	_ auth.TOTPVerifier = (*TOTPStore)(nil)
)

func NewTOTPStore(key TOTPInstanceKey, required func(isOwner bool) bool) *TOTPStore {
	if required == nil {
		required = collab.DefaultTOTPRequired
	}
	return &TOTPStore{repository{tableName: "user_totp"}, key, required}
}

// totpRow is the raw column state, before decryption.
//
// The db tags are not optional. sqlx maps columns to fields by tag, and a column
// with no matching destination is an error -- "missing destination name
// used_steps" -- not a skipped field. So the tags are the contract with the
// schema, and a column rename breaks here rather than silently reading zero.
type totpRow struct {
	Secret    sql.NullString `db:"secret"`
	UsedSteps sql.NullString `db:"used_steps"`
}

// Secret returns the user's PLAINTEXT secret, decrypting on the way out.
//
// Empty string means "not enrolled". An empty string in the COLUMN is corrupt and
// comes back as an error, because a corrupt enrolment read as "not enrolled"
// would let a user whose secret was wiped log in with one factor while believing
// they had two.
func (s *TOTPStore) Secret(ctx context.Context, userID int) (string, error) {
	var row totpRow
	err := dbWrapper.Get(ctx, &row, "SELECT secret, used_steps FROM user_totp WHERE user_id = ?", userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil // not enrolled
		}
		return "", fmt.Errorf("reading the 2FA secret for user %d: %w", userID, err)
	}
	if !row.Secret.Valid {
		return "", nil // the row exists but no secret: not enrolled
	}
	if strings.TrimSpace(row.Secret.String) == "" {
		return "", fmt.Errorf("%w: user %d has an empty stored 2FA secret", collab.ErrTOTPSecretInvalid, userID)
	}

	key, err := s.instanceKey()
	if err != nil {
		return "", fmt.Errorf("reading the instance key: %w", err)
	}
	secret, err := collab.DecryptTOTPSecret(row.Secret.String, key)
	if err != nil {
		return "", err
	}
	return secret.Reveal(), nil
}

// Fingerprint returns the display-safe fingerprint of a user's stored secret.
//
// It exists so the GraphQL layer can show "is this the right code" without ever
// holding the secret. The plaintext leaves THIS function and goes to a
// Fingerprint(), which is the last few groups and cannot be reversed; a resolver
// that called Secret and did the same arithmetic would be a resolver holding a
// plaintext TOTP secret, and that is the exact thing the sealed column prevents.
//
// The decrypt happens here, inside the component that holds the instance key,
// and nothing wider than the fingerprint crosses the boundary.
func (s *TOTPStore) Fingerprint(ctx context.Context, userID int) (string, error) {
	secret, err := s.Secret(ctx, userID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(secret) == "" {
		// Not enrolled is not an error here. The caller decides what an
		// absent fingerprint means -- the GraphQL layer renders the field as
		// null -- so returning an error would make a correct query fail for a
		// user who simply has not enrolled.
		return "", nil
	}
	return collab.TOTPSecret(secret).Fingerprint(), nil
}

// SetSecret seals and stores a new secret, and CLEARS the spent-step record.
//
// Clearing is the point. A re-enrolment gives the user a new secret with new
// codes; keeping the old record would mean the first login after re-scanning a
// QR code could be refused because a step number from the previous enrolment
// happens to match. That is a support ticket that looks like a security feature.
func (s *TOTPStore) SetSecret(ctx context.Context, userID int, secret string) error {
	if strings.TrimSpace(secret) == "" {
		return errors.New("refusing to store an empty 2FA secret")
	}
	key, err := s.instanceKey()
	if err != nil {
		return fmt.Errorf("reading the instance key: %w", err)
	}
	sealed, err := collab.EncryptTOTPSecret(collab.TOTPSecret(secret), key)
	if err != nil {
		return err
	}

	_, err = dbWrapper.Exec(ctx,
		`INSERT INTO user_totp (user_id, secret, used_steps, enrolled_at, updated_at)
		      VALUES (?, ?, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		 ON CONFLICT (user_id) DO UPDATE
		    SET secret = excluded.secret, used_steps = '', updated_at = CURRENT_TIMESTAMP`,
		userID, sealed)
	return err
}

// RemoveSecret unenrols the user, deleting the row so "not enrolled" and
// "enrolled with an empty secret" cannot be confused.
func (s *TOTPStore) RemoveSecret(ctx context.Context, userID int) error {
	_, err := dbWrapper.Exec(ctx, "DELETE FROM user_totp WHERE user_id = ?", userID)
	return err
}

// SpendTOTPStep records a step and reports whether it was already spent.
//
// THE CHECK AND THE RECORD ARE ONE STATEMENT. Two statements would be a race: two
// concurrent logins with the same code would both read "not spent" and both
// succeed, which is precisely what the plan's single-use requirement forbids. A
// single INSERT ... ON CONFLICT ... WHERE NOT EXISTS either inserts a new row,
// updates an existing one adding the step, or does nothing when the step is
// already present -- and the change count says which happened.
//
// The whole thing is also inside the caller's transaction where there is one, so
// a rollback takes the spend with it.
func (s *TOTPStore) SpendTOTPStep(ctx context.Context, userID int, step int64) (bool, error) {
	// ONE statement, and this is the load-bearing part of step 4.2's single-use
	// requirement. Read-modify-write split in two is a race: two concurrent
	// logins with the same code both read "not spent" and both succeed. "Purge,
	// then record" is worse, because the purge erases the step being recorded --
	// my first version did that, and the test caught it on the first run.
	//
	// The list keeps EVERY step inside the window, not just the newest. A list
	// collapsed to the latest step would let a code from two steps ago be
	// replayed while a newer one is spent, which is the same weakness as having
	// no record at all.
	//
	// The INSERT arm seeds a NULL secret: a step is only ever spent by a user who
	// had a secret to verify, and a NULL secret still reads as "not enrolled"
	// rather than as a corrupt enrolment.
	res, err := dbWrapper.Exec(ctx,
		`INSERT INTO user_totp (user_id, secret, used_steps, updated_at)
		      VALUES (?, NULL, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT (user_id) DO UPDATE
		    SET used_steps = trim(
		          (CASE WHEN user_totp.used_steps = '' THEN ''
		                ELSE user_totp.used_steps || ',' END) || excluded.used_steps, ','),
		        updated_at = CURRENT_TIMESTAMP
		 WHERE (',' || user_totp.used_steps || ',') NOT LIKE ('%,' || excluded.used_steps || ',%')`,
		userID, strconv.FormatInt(step, 10))
	if err != nil {
		return false, fmt.Errorf("recording the 2FA step for user %d: %w", userID, err)
	}

	// RowsAffected is the whole mechanism: the WHERE clause makes an
	// already-spent step a no-op, so a changed row means THIS caller is the one
	// that spent it, and an unchanged row means it was already spent. There is no
	// separate read to race against.
	affected, err := res.RowsAffected()
	if err != nil {
		// Cannot prove the step was fresh, so do not admit the code: a caller
		// that cannot tell is not entitled to assume the safe answer.
		return false, fmt.Errorf("confirming the 2FA step for user %d: %w", userID, err)
	}
	return affected > 0, nil
}

// UsedSteps returns the recorded steps, for diagnostics and tests.
//
// Not on the login path: the login asks SpendTOTPStep, which is the only
// operation that needs to be atomic with the record.
func (s *TOTPStore) UsedSteps(ctx context.Context, userID int) ([]int64, error) {
	// No db tag: this scans into a bare local, not a struct field, and a tag on a
	// local is a syntax error. sqlx matches a single destination by position when
	// the query returns one column.
	var raw sql.NullString
	err := dbWrapper.Get(ctx, &raw, "SELECT used_steps FROM user_totp WHERE user_id = ?", userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading 2FA steps for user %d: %w", userID, err)
	}
	return parseSteps(raw.String), nil
}

func parseSteps(raw string) []int64 {
	raw = strings.Trim(strings.TrimSpace(raw), ",")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		if err != nil {
			// A malformed entry is dropped rather than failing the read: the
			// list is a cache of which steps are spent, and refusing to log
			// anyone in because one entry is corrupt is the wrong trade.
			continue
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Verify checks a code and, if it is good, spends its time step.
//
// THE ORDER IS DELIBERATE: find the matching step, verify arithmetically, and
// only then spend. Spending first would burn a legitimate user's step on an
// attacker's wrong guess, which is a denial of service against a single account
// and is why the record lives behind the arithmetic rather than in front of it.
//
// A code that matches nothing at all is the SAME error as a wrong code. Saying
// "that code is from a different time" tells an attacker how far their clock
// is off, and with that they can narrow the search.
func (s *TOTPStore) Verify(ctx context.Context, userID int, code string) error {
	secret, err := s.Secret(ctx, userID)
	if err != nil {
		return err
	}
	if secret == "" {
		// Not enrolled. The caller has already asked Required, so reaching here
		// means the state changed between the two calls; refusing is the safe
		// reading of an unanswerable question.
		return collab.ErrTOTPInvalid
	}

	// VerifyTOTPDetailed returns the step it matched, which is what the spend
	// needs. The steps this store has already recorded are passed in as the
	// in-memory exclusion set so a replayed code is refused HERE, before it can
	// reach the database at all -- the durable record below is the backstop, not
	// the first line.
	used, err := s.UsedSteps(ctx, userID)
	if err != nil {
		return err
	}
	usedSet := make(map[int64]bool, len(used))
	for _, st := range used {
		usedSet[st] = true
	}

	step, err := collab.VerifyTOTPDetailed(collab.TOTPSecret(secret), code, time.Now(), usedSet)
	if err != nil {
		return err
	}

	// Atomic: refuses a step another login already spent.
	fresh, err := s.SpendTOTPStep(ctx, userID, step)
	if err != nil {
		return err
	}
	if !fresh {
		return collab.ErrTOTPInvalid
	}
	return nil
}

// Required implements the policy hook: owners must use 2FA.
func (s *TOTPStore) Required(ctx context.Context, userID int) (bool, error) {
	var isOwner bool
	if err := dbWrapper.Get(ctx, &isOwner, "SELECT is_owner FROM users WHERE id = ?", userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("no such user %d", userID)
		}
		return false, fmt.Errorf("reading whether user %d is the owner: %w", userID, err)
	}
	return s.required(isOwner), nil
}

// Enrolled reports whether the user has a secret, and when they enrolled.
func (s *TOTPStore) Enrolled(ctx context.Context, userID int) (bool, error) {
	var n int64
	if err := dbWrapper.Get(ctx, &n,
		"SELECT count(*) FROM user_totp WHERE user_id = ? AND secret IS NOT NULL", userID); err != nil {
		return false, fmt.Errorf("checking 2FA enrolment for user %d: %w", userID, err)
	}
	return n > 0, nil
}
