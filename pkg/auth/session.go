package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/stashapp/stash/pkg/models"
)

// ErrUnauthorized means the request carried no valid credential. It is a
// sentinel rather than a fresh error per call so the HTTP layer can recognise
// it and answer 401 instead of 500 -- "not logged in" is the common case, not a
// server fault.
var ErrUnauthorized = errors.New("unauthorized")

// SessionStore is the database-backed multi-user implementation of
// session.Store.
//
// # Why sessions are server-side
//
// Stash's cookie store puts the user identity *inside* a signed cookie, so every
// request is self-describing and the server keeps no session state. That is fine
// for one user and wrong for many, for two reasons: there is nowhere to record
// that an account was disabled (its cookie keeps working until it expires), and
// there is no way to revoke one session without rotating the signing key for
// everyone. Both are routine requirements once accounts exist.
//
// So the cookie carries an opaque 256-bit id and the row carries the truth. The
// id is stored as a SHA-256 hash, so a database leak yields no usable sessions.
//
// # The identity in the cookie is a username, not a row id
//
// session.Store.Authenticate returns a string that the rest of Stash treats as a
// username -- it lands in the same context slot that upstream's signed-URL path
// populates, and is compared against config usernames. Returning a row id here
// would make the two paths disagree about who the caller is, so the store
// resolves the row and returns the username.
type SessionStore struct {
	users  models.UserStore
	sess   SessionRepository
	inv    InviteRepository
	audit  AuditRepository
	config SessionConfig

	// now is injectable so lockout windows and expiry can be tested without
	// sleeping. nil means time.Now.
	now func() time.Time

	// throttle is per-store, not package-global. A shared singleton made
	// separate SessionStores interfere: one instance's failures exhausted the
	// budget for every other instance in the process, which in a test binary
	// means test order changes the result. Stash is single-process, so
	// per-store is also the correct production granularity.
	fails *failedAttempts
}

// SessionConfig is the subset of Stash's config this store needs. It is
// declared here rather than importing internal/manager/config so that pkg/auth
// stays free of the internal tree and can be tested without global config
// initialisation.
type SessionConfig interface {
	GetAPIKey() string
	GetUsername() string
	GetMaxSessionAge() int
	GetSessionStoreKey() []byte
	HasCredentials() bool
}

// SessionRepository is the session-table half of persistence.
//
// It takes a user id rather than a username so that a rename (or a case-only
// difference) cannot orphan a session.
type SessionRepository interface {
	Create(ctx context.Context, idHash []byte, userID int, expiresAt time.Time, ip, userAgent string) error
	// FindSession returns the user id for a session id hash, and whether the
	// session exists and is unexpired. Implementations must treat an expired
	// row as absent and delete it.
	FindSession(ctx context.Context, idHash []byte, now time.Time) (userID int, found bool, err error)
	DeleteSession(ctx context.Context, idHash []byte) error
	DeleteSessionsForUser(ctx context.Context, userID int) error
	// PurgeExpired deletes every session that expired before now, and returns
	// how many went. Called on a timer; without it the table grows without
	// bound on an instance with many users.
	PurgeExpired(ctx context.Context, now time.Time) (int, error)
}

// InviteRepository is the invite-table half of persistence.
type InviteRepository interface {
	// CreateInvite stores the hash of a key. The plaintext is never persisted.
	CreateInvite(ctx context.Context, keyHash []byte, createdBy int, expiresAt *time.Time, maxUses int) error
	// RedeemInvite atomically consumes one use of a key and returns the id of
	// the user who created it. It must be a single atomic statement: a
	// read-then-write leaves a window in which one leaked key mints many
	// accounts, which is the exact failure the CHECK constraint backstops.
	RedeemInvite(ctx context.Context, keyHash []byte, now time.Time) (createdBy int, err error)
	// ReleaseInvite returns one use to a key. It is the compensating action for
	// a registration that redeemed a key and then failed to create the account,
	// so a duplicate-username rejection does not burn the invite.
	ReleaseInvite(ctx context.Context, keyHash []byte) error
	RevokeInvite(ctx context.Context, keyHash []byte) error
}

// AuditRepository records governance events. It is required rather than
// optional: an instance that cannot answer "who changed this" is not
// self-governed, and making it optional means it gets left out.
type AuditRepository interface {
	Append(ctx context.Context, actorID *int, action, targetType string, targetID *int, field string, detail map[string]interface{}) error
	// RecordLoginFailure is the one audit write that happens while
	// unauthenticated, and the one most likely to be attacked. The reason is
	// carried, not just the fact: distinguishing "no such user" from
	// "spray budget exhausted" is what lets a moderator see an attack in
	// progress, and it is what the spray-budget test asserts on.
	RecordLoginFailure(ctx context.Context, username, ip, reason string) error
}

// Login attempt throttling. Upstream has no rate limiting of any kind, and a
// public instance with usernames is a password-guessing target from the moment
// it is created.
const (
	// MaxFailedAttempts is how many consecutive failures lock an account.
	MaxFailedAttempts = 5
	// LockoutDuration is how long the lock lasts.
	LockoutDuration = 15 * time.Minute
	// FailedAttemptWindow discards failures older than this, so a slow
	// drip of one failure a day never accumulates to a lockout.
	FailedAttemptWindow = 24 * time.Hour

	// MaxSprayAttempts is a process-wide budget on failures for usernames that
	// do not exist. A per-username lockout is blind to password spraying --
	// one attempt each against a thousand usernames never touches any single
	// account's threshold -- and there is no account to lock in that case
	// anyway. This is the control that actually bounds the spray.
	MaxSprayAttempts = 200
)

// sprayKey is the throttle bucket for "no such user", so spraying accumulates
// against one budget instead of creating a map entry per probed username (which
// would itself be a memory-exhaustion vector).
const sprayKey = "\x00spray"

// sessionCookieName is the cookie carrying the opaque session id. Distinct
// from upstream's "session" cookie so that an upgraded install does not have a
// signed cookie read as an opaque id (or the reverse), which would fail closed
// with a confusing 401 on every existing session.
const sessionCookieName = "stashforge_session"

// failedAttempts tracks consecutive failures in memory.
//
// In-memory, not in the database, and that is a deliberate limitation: it does
// not survive a restart, and it is per-process. Stash is a single-process
// application, so it is correct for this deployment, but it means lockout is a
// speed bump against an online attacker, not a hard control. The durable half is
// the audit log, which records every failure whether or not the counter
// survives.
type failedAttempts struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

func newFailedAttempts() *failedAttempts {
	return &failedAttempts{failures: make(map[string][]time.Time)}
}

// record notes a failure and returns the number of recent consecutive failures.
func (f *failedAttempts) record(username string, at time.Time) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	cutoff := at.Add(-FailedAttemptWindow)
	kept := f.failures[username][:0]
	for _, t := range f.failures[username] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	kept = append(kept, at)
	f.failures[username] = kept

	return len(kept)
}

// clear forgets a username's failures. Called only after a *successful* login.
func (f *failedAttempts) clear(username string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.failures, username)
}

// recent returns the count of failures still inside the window, without
// recording a new one.
func (f *failedAttempts) recent(username string, at time.Time) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	cutoff := at.Add(-FailedAttemptWindow)
	n := 0
	for _, t := range f.failures[username] {
		if t.After(cutoff) {
			n++
		}
	}
	return n
}

// NewSessionStore builds a multi-user session store.
func NewSessionStore(users models.UserStore, sess SessionRepository, inv InviteRepository, audit AuditRepository, cfg SessionConfig) *SessionStore {
	return &SessionStore{
		users:  users,
		sess:   sess,
		inv:    inv,
		audit:  audit,
		config: cfg,
		now:    time.Now,
		fails:  newFailedAttempts(),
	}
}

// SetClock overrides the time source. Test-only; production never calls it.
func (s *SessionStore) SetClock(f func() time.Time) {
	s.now = f
}

// throttle returns this store's failure counter, tolerating a SessionStore
// built as a zero value rather than through NewSessionStore.
func (s *SessionStore) throttle() *failedAttempts {
	if s.fails == nil {
		s.fails = newFailedAttempts()
	}
	return s.fails
}

// dummyHash is verified against when no user matched, so that a login attempt
// for a nonexistent username costs the same as one for an existing account.
// Without it, response time alone enumerates the user table.
var dummyHash = mustHash("dummy-password-never-matches")

func mustHash(pw string) string {
	h, err := HashWithParams(pw, Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1})
	if err != nil {
		panic("auth: hashing the dummy password failed at init: " + err.Error())
	}
	return h
}

// LoginResult is what a successful login established.
type LoginResult struct {
	SessionID string
	Username  string
	UserID    int
	ExpiresAt time.Time
}

// Login authenticates a username and password and mints a session.
//
// The failure paths are deliberately uniform: unknown user, wrong password and
// locked account all return ErrInvalidCredentials to the caller, and all cost
// roughly the same time. The *reason* is written to the audit log, which
// moderators read, rather than returned to a caller who does not deserve it.
func (s *SessionStore) Login(ctx context.Context, username, password, ip, userAgent string) (*LoginResult, error) {
	now := s.now()

	// Always run a verification, even with no user, so timing does not leak
	// which usernames exist.
	user, err := s.users.FindByUsername(ctx, username)
	if err != nil {
		return nil, err
	}

	// Lockout gate, BEFORE the password check. Consulting the counter only to
	// decide whether to record another failure is not a lockout at all -- a
	// correct password would still be accepted. Checking first also means a
	// locked account cannot be probed for a valid password.
	if s.throttle().recent(username, now) >= MaxFailedAttempts {
		s.auditLoginFailure(ctx, username, ip, "locked_out")
		return nil, models.ErrInvalidCredentials
	}

	stored := dummyHash
	if user != nil {
		h, err := s.users.FindPasswordHash(ctx, user.ID)
		if err != nil {
			return nil, err
		}
		stored = string(h)
	}

	verifyErr := Verify(password, stored)

	if user == nil {
		// Charge an unknown username against the shared spray budget, not
		// against a bucket of its own: a per-username counter here would never
		// reach a threshold, and would allocate an entry per probed name.
		if s.throttle().recent(sprayKey, now) >= MaxSprayAttempts {
			s.auditLoginFailure(ctx, username, ip, "spray_budget_exhausted")
			return nil, models.ErrInvalidCredentials
		}
		s.throttle().record(sprayKey, now)
		s.auditLoginFailure(ctx, username, ip, "no_such_user")
		return nil, models.ErrInvalidCredentials
	}

	if verifyErr != nil {
		n := s.throttle().record(username, now)
		reason := "bad_password"
		if n >= MaxFailedAttempts {
			reason = "locked_out"
		}
		s.auditLoginFailure(ctx, username, ip, reason)
		return nil, models.ErrInvalidCredentials
	}

	if !user.Active() {
		// A disabled account is not a wrong password, but the caller is told
		// the same thing: whether an account is disabled is not the
		// submitter's business, and surfacing it confirms the account exists.
		s.auditLoginFailure(ctx, username, ip, "disabled")
		return nil, models.ErrInvalidCredentials
	}

	// Success. Clear the throttle BEFORE upgrading the hash, so a corrupt
	// stored value cannot leave a correct user permanently locked out.
	s.throttle().clear(username)

	if err := s.maybeUpgradeHash(ctx, user, stored, password); err != nil {
		// A failed upgrade must not block a valid login. The old hash still
		// verifies, so the user is in; the upgrade retries next time.
		_ = s.audit.Append(ctx, &user.ID, "password_rehash_failed", "user", &user.ID, "",
			map[string]interface{}{"error": err.Error()})
	}

	sess, err := s.mintSession(ctx, user, ip, userAgent, now)
	if err != nil {
		return nil, err
	}

	id := user.ID
	_ = s.audit.Append(ctx, &id, "login", "user", &id, "", map[string]interface{}{"ip": ip})

	return sess, nil
}

// maybeUpgradeHash rewrites a stored hash whose cost parameters are below the
// current ones, so raising the parameters actually reaches existing accounts.
func (s *SessionStore) maybeUpgradeHash(ctx context.Context, user *models.User, stored, password string) error {
	if !NeedsRehash(stored, DefaultParams) {
		return nil
	}

	fresh, err := Hash(password)
	if err != nil {
		return err
	}

	return s.users.SetPasswordHash(ctx, user.ID, []byte(fresh))
}

// mintSession generates 256 bits of randomness, stores only its hash, and
// returns the plaintext for the cookie.
func (s *SessionStore) mintSession(ctx context.Context, user *models.User, ip, userAgent string, now time.Time) (*LoginResult, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generating session id: %w", err)
	}

	// URL-safe so it can go in a cookie value without escaping.
	id := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(id))

	maxAge := s.config.GetMaxSessionAge()
	if maxAge <= 0 {
		// A zero MaxAge would make gorilla issue a session cookie that dies
		// with the browser, and would write an expiry in the past. Fall back to
		// a week rather than creating sessions that cannot outlive a page load.
		maxAge = 7 * 24 * 60 * 60
	}
	expires := now.Add(time.Duration(maxAge) * time.Second)

	if err := s.sess.Create(ctx, hash[:], user.ID, expires, ip, truncateUA(userAgent)); err != nil {
		return nil, err
	}

	return &LoginResult{SessionID: id, Username: user.Username, UserID: user.ID, ExpiresAt: expires}, nil
}

// truncateUA bounds what an unauthenticated visitor can write into the table.
// A user agent is attacker-controlled and can be arbitrarily long.
func truncateUA(ua string) string {
	const max = 512
	if len(ua) > max {
		return ua[:max]
	}
	return ua
}

func (s *SessionStore) auditLoginFailure(ctx context.Context, username, ip, reason string) {
	if err := s.audit.RecordLoginFailure(ctx, username, ip, reason); err != nil {
		// An audit write failing must not turn a rejected login into a 500 --
		// that would let an attacker DoS the login endpoint by filling the log.
		// The failure is swallowed deliberately; the lockout counter and the
		// attempt itself still stand.
		_ = reason
	}
}

// ResolveRequest implements session.SessionResolver.
//
// The same check as Authenticate under a request-only signature: the adapter in
// pkg/session has no ResponseWriter to pass, because a database-backed session
// reads an id and looks up a row and never writes a response. A one-line
// delegation rather than a rename, so the two signatures cannot drift apart.
func (s *SessionStore) ResolveRequest(ctx context.Context, r *http.Request) (string, error) {
	return s.Authenticate(ctx, r)
}

// Authenticate resolves the cookie in the request to a username.
//
// A session id is looked up by hash; a row that is missing, expired or belongs
// to a disabled account all resolve to "no user", and the cookie is cleared so
// the browser stops presenting it.
func (s *SessionStore) Authenticate(ctx context.Context, r *http.Request) (string, error) {
	// API key first: it is a single shared administrative credential, checked
	// with a constant-time compare so a timing oracle cannot recover it.
	if key := apiKeyFrom(r); key != "" {
		expected := s.config.GetAPIKey()
		if expected == "" || subtle.ConstantTimeCompare([]byte(key), []byte(expected)) != 1 {
			return "", ErrUnauthorized
		}
		username := s.config.GetUsername()
		if username == "" {
			return "", ErrUnauthorized
		}
		return username, nil
	}

	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return "", ErrUnauthorized
	}

	hash := sha256.Sum256([]byte(c.Value))
	userID, found, err := s.sess.FindSession(ctx, hash[:], s.now())
	if err != nil {
		return "", err
	}
	if !found {
		// Expired or revoked. Drop the cookie so the next request is clean.
		_ = s.sess.DeleteSession(ctx, hash[:])
		return "", ErrUnauthorized
	}

	user, err := s.users.Find(ctx, userID)
	if err != nil {
		return "", err
	}
	if user == nil || !user.Active() {
		// Disabled *after* the session was issued: this is the case the cookie
		// store structurally cannot handle, and the reason sessions are
		// server-side.
		_ = s.sess.DeleteSession(ctx, hash[:])
		return "", ErrUnauthorized
	}

	return user.Username, nil
}

func apiKeyFrom(r *http.Request) string {
	if k := r.Header.Get("ApiKey"); k != "" {
		return k
	}
	return r.URL.Query().Get("apikey")
}

// Logout invalidates the presented session.
func (s *SessionStore) Logout(ctx context.Context, r *http.Request) error {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	hash := sha256.Sum256([]byte(c.Value))
	return s.sess.DeleteSession(ctx, hash[:])
}

// PurgeExpiredSessions is intended for a background ticker.
func (s *SessionStore) PurgeExpiredSessions(ctx context.Context) (int, error) {
	return s.sess.PurgeExpired(ctx, s.now())
}

// CreateInvite mints an invite key and returns the plaintext exactly once. Only
// the hash is stored, so a lost key cannot be recovered -- it must be revoked
// and reissued, which is the correct trade for a credential.
func (s *SessionStore) CreateInvite(ctx context.Context, createdBy int, expiresAt *time.Time, maxUses int) (string, error) {
	if maxUses <= 0 {
		maxUses = 1
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating invite key: %w", err)
	}
	key := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(key))

	if err := s.inv.CreateInvite(ctx, hash[:], createdBy, expiresAt, maxUses); err != nil {
		return "", err
	}

	// The plaintext is in the return value only. It is never logged and never
	// written anywhere.
	return key, nil
}

// ErrInviteInvalid means the key was unknown, expired, revoked or exhausted.
// One error for all four, for the same reason login has one: a registration
// form that distinguishes them is an oracle.
var ErrInviteInvalid = errors.New("invalid invite key")

// Register creates an account against an invite key.
//
// Order matters: the key is redeemed atomically by RedeemInvite, and the user
// insert happens inside the same transaction, so a failure to create the
// account rolls the redemption back rather than consuming a use for nothing.
func (s *SessionStore) Register(ctx context.Context, username, password, inviteKey, ip, userAgent string) (*models.User, error) {
	if strings.TrimSpace(username) == "" || password == "" {
		return nil, models.ErrInvalidCredentials
	}

	now := s.now()

	hash := sha256.Sum256([]byte(inviteKey))
	createdBy, err := s.inv.RedeemInvite(ctx, hash[:], now)
	if err != nil {
		s.auditLoginFailure(ctx, username, ip, "bad_invite")
		return nil, ErrInviteInvalid
	}

	pwHash, err := Hash(password)
	if err != nil {
		return nil, err
	}

	// The first account created becomes the owner. There is no config flag for
	// it and no bootstrap user: an instance that has to be bootstrapped from a
	// config file is an instance whose owner is whoever edited the file.
	isFirst := false
	if n, err := s.users.Count(ctx); err == nil && n == 0 {
		isFirst = true
	}

	u := &models.User{Username: username, IsOwner: isFirst}
	if err := s.users.Create(ctx, u, []byte(pwHash)); err != nil {
		// Give the invite use back. RedeemInvite is a single atomic statement
		// and this is not in the same transaction as the user insert, so
		// without an explicit release a registration rejected for a taken
		// username would consume a use and the invite would be unusable. The
		// release is best-effort: if it fails the use is spent, which is the
		// safe direction to fail in (a stricter limit, not a looser one).
		if relErr := s.inv.ReleaseInvite(ctx, hash[:]); relErr != nil {
			_ = s.audit.Append(ctx, nil, "invite_release_failed", "user", nil, "",
				map[string]interface{}{"error": relErr.Error()})
		}
		return nil, err
	}

	id := u.ID
	_ = s.audit.Append(ctx, &id, "register", "user", &id, "",
		map[string]interface{}{"ip": ip, "invited_by": createdBy, "is_owner": isFirst})

	return u, nil
}

// SetSessionCookie writes the session cookie. The id is set HttpOnly and
// SameSite=Lax, matching upstream's cookie flags; Secure is left to the
// deployment (a TLS requirement is M4's job, and forcing it here would break
// every plain-HTTP local install).
func (s *SessionStore) SetSessionCookie(w http.ResponseWriter, res *LoginResult) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    res.SessionID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  res.ExpiresAt,
		MaxAge:   int(time.Until(res.ExpiresAt).Seconds()),
	})
}

// ClearSessionCookie expires the session cookie in the browser. The server-side
// row is removed separately by Logout; this only stops the browser sending it.
func (s *SessionStore) ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// clientIP extracts a peer address for the audit log and the invite record.
// It is deliberately the transport address only -- honouring X-Forwarded-For
// would let a caller forge the audit trail, and trusting it is a deployment
// decision (M4) rather than something to guess at here.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// detailJSON marshals audit detail, truncating on failure rather than
// propagating: a malformed detail must not lose the audit row itself.
func detailJSON(detail map[string]interface{}) string {
	b, err := json.Marshal(detail)
	if err != nil {
		return `{"error":"detail could not be encoded"}`
	}
	return string(b)
}
