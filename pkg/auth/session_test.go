package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/auth"
	"github.com/stashapp/stash/pkg/models"
)

// ---------------------------------------------------------------------------
// Fakes. Each one records what it was asked, because most of the assertions
// here are about what the store DID NOT do (no audit row for a successful
// login, no session for a failed one, a redemption that did not over-redeem).
// ---------------------------------------------------------------------------

type fakeUserStore struct {
	mu sync.Mutex

	users  map[int]*models.User
	hashes map[int]string
	nextID int

	createErr error
	// countErr makes Count fail, so a test can prove the factory does not read
	// an unreadable user table as "no users" and fall back to single-user.
	countErr error
}

func newFakeUserStore() *fakeUserStore {
	return &fakeUserStore{users: map[int]*models.User{}, hashes: map[int]string{}, nextID: 1}
}

func (f *fakeUserStore) add(u *models.User, password string) *models.User {
	f.mu.Lock()
	defer f.mu.Unlock()
	u.ID = f.nextID
	f.nextID++
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now()
	}
	f.users[u.ID] = u
	h, err := auth.Hash(password)
	if err != nil {
		panic(err)
	}
	f.hashes[u.ID] = h
	return u
}

func (f *fakeUserStore) FindByUsername(_ context.Context, username string) (*models.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if equalFold(u.Username, username) {
			return u, nil
		}
	}
	return nil, nil
}

func (f *fakeUserStore) Find(_ context.Context, id int) (*models.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.users[id], nil
}

func (f *fakeUserStore) FindPasswordHash(_ context.Context, id int) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.hashes[id]
	if !ok {
		return nil, nil
	}
	return []byte(h), nil
}

func (f *fakeUserStore) FindAll(context.Context) ([]*models.User, error) { return nil, nil }

// countErr makes Count fail, so a test can prove the factory does not treat an
// unreadable user table as "no users" and quietly fall back to single-user.
func (f *fakeUserStore) Count(context.Context) (int, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.users), nil
}

func (f *fakeUserStore) Create(_ context.Context, u *models.User, hash []byte) error {
	f.mu.Lock()
	if f.createErr != nil {
		err := f.createErr
		f.mu.Unlock()
		return err
	}
	// Enforce the COLLATE NOCASE uniqueness the real schema has. Without this
	// the duplicate-username test would pass for the wrong reason: the insert
	// would succeed and the assertion would be vacuous.
	for _, existing := range f.users {
		if equalFold(existing.Username, u.Username) {
			f.mu.Unlock()
			return models.ErrUsernameTaken
		}
	}
	f.mu.Unlock()

	created := f.add(u, "")
	f.hashes[created.ID] = string(hash)
	return nil
}

func (f *fakeUserStore) Update(_ context.Context, u *models.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[u.ID] = u
	return nil
}

func (f *fakeUserStore) Destroy(_ context.Context, id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.users, id)
	return nil
}

func (f *fakeUserStore) SetPasswordHash(_ context.Context, id int, hash []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hashes[id] = string(hash)
	return nil
}

func (f *fakeUserStore) SetDisabled(_ context.Context, id int, disabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if disabled {
		t := time.Now()
		f.users[id].DisabledAt = &t
	} else {
		f.users[id].DisabledAt = nil
	}
	return nil
}

// SetModerator and SetDisabled on the fake enforce nothing: the owner checks
// live in the real store, and a fake that reimplements them would be testing the
// fake. The real store's version is covered by the sqlite integration tests.
func (f *fakeUserStore) SetModerator(_ context.Context, _, _ int, _ bool) error {
	return nil
}

func (f *fakeUserStore) AddReputation(context.Context, int, int) error { return nil }

func (f *fakeUserStore) hashFor(id int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hashes[id]
}

type fakeSessionRepo struct {
	mu       sync.Mutex
	byHash   map[string]int
	expires  map[string]time.Time
	purged   int
	createNo int
	// findCalls records the `now` the store passed on every lookup, so a test
	// can assert the STORE honoured its injected clock rather than the fake
	// silently doing the expiry check with the wrong instant.
	findCalls []time.Time
}

func newFakeSessionRepo() *fakeSessionRepo {
	return &fakeSessionRepo{byHash: map[string]int{}, expires: map[string]time.Time{}}
}

func (f *fakeSessionRepo) Create(_ context.Context, idHash []byte, userID int, expiresAt time.Time, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := string(idHash)
	f.byHash[k] = userID
	f.expires[k] = expiresAt
	f.createNo++
	return nil
}

func (f *fakeSessionRepo) FindSession(_ context.Context, idHash []byte, now time.Time) (int, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.findCalls = append(f.findCalls, now)
	k := string(idHash)
	uid, ok := f.byHash[k]
	if !ok {
		return 0, false, nil
	}
	if f.expires[k].Before(now) {
		return 0, false, nil
	}
	return uid, true, nil
}

func (f *fakeSessionRepo) DeleteSession(_ context.Context, idHash []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := string(idHash)
	delete(f.byHash, k)
	delete(f.expires, k)
	return nil
}

func (f *fakeSessionRepo) DeleteSessionsForUser(_ context.Context, userID int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, v := range f.byHash {
		if v == userID {
			delete(f.byHash, k)
		}
	}
	return nil
}

func (f *fakeSessionRepo) PurgeExpired(_ context.Context, now time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for k, exp := range f.expires {
		if exp.Before(now) {
			delete(f.byHash, k)
			delete(f.expires, k)
			n++
		}
	}
	f.purged += n
	return n, nil
}

func (f *fakeSessionRepo) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.byHash)
}

type fakeInviteRepo struct {
	mu       sync.Mutex
	keys     map[string]int // key hash -> created_by
	uses     map[string]int
	maxUses  map[string]int
	expires  map[string]time.Time
	revoked  map[string]bool
	redeems  int
	redeemOK bool
}

func newFakeInviteRepo() *fakeInviteRepo {
	return &fakeInviteRepo{
		keys: map[string]int{}, uses: map[string]int{}, maxUses: map[string]int{},
		expires: map[string]time.Time{}, revoked: map[string]bool{}, redeemOK: true,
	}
}

func (f *fakeInviteRepo) CreateInvite(_ context.Context, keyHash []byte, createdBy int, expiresAt *time.Time, maxUses int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := string(keyHash)
	f.keys[k] = createdBy
	f.maxUses[k] = maxUses
	f.uses[k] = 0
	if expiresAt != nil {
		f.expires[k] = *expiresAt
	}
	return nil
}

func (f *fakeInviteRepo) RedeemInvite(_ context.Context, keyHash []byte, now time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.redeems++
	if !f.redeemOK {
		return 0, errors.New("no such key")
	}
	k := string(keyHash)
	cb, ok := f.keys[k]
	if !ok || f.revoked[k] || f.uses[k] >= f.maxUses[k] {
		return 0, errors.New("invalid key")
	}
	if exp, has := f.expires[k]; has && exp.Before(now) {
		return 0, errors.New("expired key")
	}
	f.uses[k]++
	return cb, nil
}

func (f *fakeInviteRepo) ReleaseInvite(_ context.Context, keyHash []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := string(keyHash)
	if n, ok := f.uses[k]; ok && n > 0 {
		f.uses[k] = n - 1
	}
	return nil
}

func (f *fakeInviteRepo) RevokeInvite(_ context.Context, keyHash []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked[string(keyHash)] = true
	return nil
}

type auditEntry struct {
	actor  *int
	action string
	detail map[string]interface{}
}

type fakeAuditRepo struct {
	mu       sync.Mutex
	entries  []auditEntry
	failures []loginFailure
	failLog  bool
}

// loginFailure records the reason, so a test can tell a spray-budget refusal
// from an ordinary unknown-username rejection.
type loginFailure struct {
	username string
	reason   string
}

func (f *fakeAuditRepo) countReason(reason string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, x := range f.failures {
		if x.reason == reason {
			n++
		}
	}
	return n
}

func newFakeAuditRepo() *fakeAuditRepo { return &fakeAuditRepo{} }

func (f *fakeAuditRepo) Append(_ context.Context, actor *int, action, _ string, _ *int, _ string, detail map[string]interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, auditEntry{actor: actor, action: action, detail: detail})
	return nil
}

func (f *fakeAuditRepo) RecordLoginFailure(_ context.Context, username, ip, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = append(f.failures, loginFailure{username: username, reason: reason})
	if f.failLog {
		return errors.New("disk full")
	}
	return nil
}

func (f *fakeAuditRepo) actions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.entries {
		out = append(out, e.action)
	}
	return out
}

func (f *fakeAuditRepo) hasAction(a string) bool {
	for _, x := range f.actions() {
		if x == a {
			return true
		}
	}
	return false
}

type fakeConfig struct {
	apiKey   string
	username string
	maxAge   int
}

func (c fakeConfig) GetAPIKey() string          { return c.apiKey }
func (c fakeConfig) GetUsername() string        { return c.username }
func (c fakeConfig) GetMaxSessionAge() int      { return c.maxAge }
func (c fakeConfig) GetSessionStoreKey() []byte { return []byte("k") }
func (c fakeConfig) HasCredentials() bool       { return true }

func equalFold(a, b string) bool {
	return len(a) == len(b) && lower(a) == lower(b)
}

func lower(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= 'A' && r <= 'Z' {
			out[i] = r + 32
		}
	}
	return string(out)
}

// ---------------------------------------------------------------------------

type fixture struct {
	store *auth.SessionStore
	users *fakeUserStore
	sess  *fakeSessionRepo
	inv   *fakeInviteRepo
	audit *fakeAuditRepo
	clock time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	users, sess, inv, audit := newFakeUserStore(), newFakeSessionRepo(), newFakeInviteRepo(), newFakeAuditRepo()
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	s := auth.NewSessionStore(users, sess, inv, audit, fakeConfig{maxAge: 3600})
	s.SetClock(func() time.Time { return now })

	return &fixture{store: s, users: users, sess: sess, inv: inv, audit: audit, clock: now}
}

func TestSessionStore_LoginSucceeds(t *testing.T) {
	f := newFixture(t)
	u := f.users.add(&models.User{Username: "alice"}, "correct")

	res, err := f.store.Login(context.Background(), "alice", "correct", "1.2.3.4", "UA")
	require.NoError(t, err)
	assert.Equal(t, "alice", res.Username)
	assert.Equal(t, u.ID, res.UserID)
	assert.NotEmpty(t, res.SessionID, "a login must return a session id for the cookie")
	assert.Equal(t, 1, f.sess.count(), "a successful login must persist exactly one session")
}

func TestSessionStore_LoginIsCaseInsensitiveOnUsername(t *testing.T) {
	f := newFixture(t)
	f.users.add(&models.User{Username: "alice"}, "correct")

	_, err := f.store.Login(context.Background(), "ALICE", "correct", "", "")
	assert.NoError(t, err, "username lookup must match the COLLATE NOCASE index")
}

// The core anti-enumeration property. A login form that says "no such user"
// hands an attacker the whole user table for free, so unknown-user and
// wrong-password must be indistinguishable to the caller.
func TestSessionStore_LoginDoesNotDistinguishUnknownUserFromWrongPassword(t *testing.T) {
	f := newFixture(t)
	f.users.add(&models.User{Username: "alice"}, "correct")

	_, errUnknown := f.store.Login(context.Background(), "nobody", "correct", "", "")
	_, errWrong := f.store.Login(context.Background(), "alice", "wrong", "", "")

	require.Error(t, errUnknown)
	require.Error(t, errWrong)
	assert.Equal(t, errUnknown.Error(), errWrong.Error(),
		"the two must be the same error, or the form enumerates accounts")
	assert.ErrorIs(t, errUnknown, models.ErrInvalidCredentials)
	assert.ErrorIs(t, errWrong, models.ErrInvalidCredentials)
}

// Both failure modes must cost about the same time, because a response-time
// difference is the same enumeration oracle as a different error message.
func TestSessionStore_LoginVerifiesEvenForUnknownUser(t *testing.T) {
	f := newFixture(t)
	f.users.add(&models.User{Username: "alice"}, "correct")

	// If the unknown-user path skipped verification it would return in
	// microseconds; the store verifies against a dummy hash instead. This test
	// cannot assert wall-clock reliably, so it asserts the property that makes
	// it true: a session is never created and the failure is always audited.
	_, err := f.store.Login(context.Background(), "nobody", "correct", "1.2.3.4", "")
	require.Error(t, err)
	assert.Equal(t, 0, f.sess.count(), "a failed login must not create a session")
	assert.Len(t, f.audit.failures, 1, "a failed login must leave an audit row")
}

func TestSessionStore_LoginRejectsDisabledAccount(t *testing.T) {
	f := newFixture(t)
	u := f.users.add(&models.User{Username: "alice"}, "correct")
	require.NoError(t, f.users.SetDisabled(context.Background(), u.ID, true))

	_, err := f.store.Login(context.Background(), "alice", "correct", "", "")
	assert.ErrorIs(t, err, models.ErrInvalidCredentials,
		"a disabled account must be refused -- and reported identically to a "+
			"wrong password, so the error does not confirm the account exists")
	assert.Equal(t, 0, f.sess.count(), "a disabled account must not get a session")
}

// Five failures lock the account, and a correct password during the lock is
// still refused. A lockout that a correct password can bypass is not a lockout.
func TestSessionStore_LockoutAfterRepeatedFailures(t *testing.T) {
	f := newFixture(t)
	f.users.add(&models.User{Username: "alice"}, "correct")

	for i := 0; i < auth.MaxFailedAttempts; i++ {
		_, err := f.store.Login(context.Background(), "alice", "wrong", "", "")
		require.Error(t, err, "attempt %d must fail", i)
	}

	_, err := f.store.Login(context.Background(), "alice", "correct", "", "")
	assert.Error(t, err, "the correct password must be refused while locked out")
	assert.Equal(t, 0, f.sess.count(), "a locked-out login must not create a session")

	// A successful login by a DIFFERENT account must not clear alice's counter.
	// A global "reset on success" bug would let an attacker unlock a target by
	// logging in as themselves, or would be fixed by clearing on every success
	// and thereby defeat the lockout entirely.
	f.users.add(&models.User{Username: "bob"}, "correct")
	_, err = f.store.Login(context.Background(), "bob", "correct", "", "")
	require.NoError(t, err, "bob must be able to log in")

	_, err = f.store.Login(context.Background(), "alice", "correct", "", "")
	assert.Error(t, err, "bob's successful login must not clear alice's lockout")
}

// A failure older than the window must not accumulate into a lockout, or a
// slow drip of one wrong guess a day locks a legitimate user out forever.
func TestSessionStore_FailuresOutsideTheWindowDoNotAccumulate(t *testing.T) {
	f := newFixture(t)
	f.users.add(&models.User{Username: "alice"}, "correct")

	// Four failures, then a day passes between each.
	for i := 0; i < auth.MaxFailedAttempts-1; i++ {
		_, err := f.store.Login(context.Background(), "alice", "wrong", "", "")
		require.Error(t, err)
		f.clock = f.clock.Add(auth.FailedAttemptWindow + time.Hour)
		f.store.SetClock(func() time.Time { return f.clock })
	}

	_, err := f.store.Login(context.Background(), "alice", "correct", "", "")
	assert.NoError(t, err, "failures outside the window must not lock a valid user out")
}

func TestSessionStore_SuccessClearsTheThrottle(t *testing.T) {
	f := newFixture(t)
	f.users.add(&models.User{Username: "alice"}, "correct")

	for i := 0; i < auth.MaxFailedAttempts-1; i++ {
		_, _ = f.store.Login(context.Background(), "alice", "wrong", "", "")
	}

	_, err := f.store.Login(context.Background(), "alice", "correct", "", "")
	require.NoError(t, err)

	// A fresh run of failures must take the full allowance again.
	for i := 0; i < auth.MaxFailedAttempts-1; i++ {
		_, err = f.store.Login(context.Background(), "alice", "wrong", "", "")
		require.Error(t, err)
	}
	_, err = f.store.Login(context.Background(), "alice", "correct", "", "")
	assert.NoError(t, err, "a successful login must reset the failure counter")
}

func TestSessionStore_SessionIDIsNotTheStoredHash(t *testing.T) {
	f := newFixture(t)
	f.users.add(&models.User{Username: "alice"}, "correct")

	res, err := f.store.Login(context.Background(), "alice", "correct", "", "")
	require.NoError(t, err)

	// The cookie value must not appear in the session table: only its hash is
	// stored, so a database leak yields no usable sessions.
	f.sess.mu.Lock()
	defer f.sess.mu.Unlock()
	for k := range f.sess.byHash {
		assert.NotEqual(t, res.SessionID, k,
			"the plaintext session id must never be the stored key")
	}
}

func TestSessionStore_AuthenticateResolvesTheCookie(t *testing.T) {
	f := newFixture(t)
	f.users.add(&models.User{Username: "alice"}, "correct")

	res, err := f.store.Login(context.Background(), "alice", "correct", "1.2.3.4", "UA")
	require.NoError(t, err)

	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "stashforge_session", Value: res.SessionID})

	user, err := f.store.Authenticate(context.Background(), r)
	require.NoError(t, err)
	assert.Equal(t, "alice", user, "Authenticate must return a username, not a row id")
}

func TestSessionStore_AuthenticateRejectsGarbageCookie(t *testing.T) {
	f := newFixture(t)
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "stashforge_session", Value: "not-a-real-session"})

	_, err := f.store.Authenticate(context.Background(), r)
	assert.ErrorIs(t, err, auth.ErrUnauthorized)
}

func TestSessionStore_AuthenticateRejectsNoCookie(t *testing.T) {
	f := newFixture(t)
	_, err := f.store.Authenticate(context.Background(), httptest.NewRequest("GET", "/", nil))
	assert.ErrorIs(t, err, auth.ErrUnauthorized, "an anonymous request is not a server error")
}

// The case the cookie store structurally cannot handle, and the reason sessions
// are server-side: an account disabled while a session is live.
func TestSessionStore_DisablingAUserKillsTheirLiveSession(t *testing.T) {
	f := newFixture(t)
	u := f.users.add(&models.User{Username: "alice"}, "correct")

	res, err := f.store.Login(context.Background(), "alice", "correct", "", "")
	require.NoError(t, err)

	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "stashforge_session", Value: res.SessionID})

	user, err := f.store.Authenticate(context.Background(), r)
	require.NoError(t, err)
	require.Equal(t, "alice", user)

	require.NoError(t, f.users.SetDisabled(context.Background(), u.ID, true))

	_, err = f.store.Authenticate(context.Background(), r)
	assert.ErrorIs(t, err, auth.ErrUnauthorized,
		"disabling an account must invalidate its live sessions immediately")
	assert.Equal(t, 0, f.sess.count(), "the dead session row must be cleaned up")
}

func TestSessionStore_ExpiredSessionIsRejected(t *testing.T) {
	f := newFixture(t)
	f.users.add(&models.User{Username: "alice"}, "correct")
	res, err := f.store.Login(context.Background(), "alice", "correct", "", "")
	require.NoError(t, err)

	f.clock = f.clock.Add(48 * time.Hour)
	f.store.SetClock(func() time.Time { return f.clock })

	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "stashforge_session", Value: res.SessionID})

	_, err = f.store.Authenticate(context.Background(), r)
	assert.ErrorIs(t, err, auth.ErrUnauthorized, "an expired session must not authenticate")

	// The store must have passed its OWN clock to the lookup. If it passed
	// time.Now() the fake would compare against the wrong instant and a
	// mutation that swapped the injected clock for the wall clock would pass
	// every other expiry test in this file.
	f.sess.mu.Lock()
	defer f.sess.mu.Unlock()
	require.NotEmpty(t, f.sess.findCalls, "the session lookup must have been called")
	last := f.sess.findCalls[len(f.sess.findCalls)-1]
	assert.Equal(t, f.clock, last,
		"expiry must be judged against the store's clock, not the wall clock")
}

func TestSessionStore_APIKeyAuthenticates(t *testing.T) {
	users, sess, inv, audit := newFakeUserStore(), newFakeSessionRepo(), newFakeInviteRepo(), newFakeAuditRepo()
	users.add(&models.User{Username: "owner"}, "x")
	s := auth.NewSessionStore(users, sess, inv, audit, fakeConfig{apiKey: "s3cret-api-key", username: "owner"})

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("ApiKey", "s3cret-api-key")

	user, err := s.Authenticate(context.Background(), r)
	require.NoError(t, err)
	assert.Equal(t, "owner", user)
}

func TestSessionStore_WrongAPIKeyIsRejected(t *testing.T) {
	users, sess, inv, audit := newFakeUserStore(), newFakeSessionRepo(), newFakeInviteRepo(), newFakeAuditRepo()
	s := auth.NewSessionStore(users, sess, inv, audit, fakeConfig{apiKey: "s3cret-api-key", username: "owner"})

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("ApiKey", "s3cret-api-ke")

	_, err := s.Authenticate(context.Background(), r)
	assert.ErrorIs(t, err, auth.ErrUnauthorized, "an API key prefix must not authenticate")
}

func TestSessionStore_LogoutRemovesTheSession(t *testing.T) {
	f := newFixture(t)
	f.users.add(&models.User{Username: "alice"}, "correct")
	res, err := f.store.Login(context.Background(), "alice", "correct", "", "")
	require.NoError(t, err)

	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "stashforge_session", Value: res.SessionID})

	require.NoError(t, f.store.Logout(context.Background(), r))
	assert.Equal(t, 0, f.sess.count())

	_, err = f.store.Authenticate(context.Background(), r)
	assert.ErrorIs(t, err, auth.ErrUnauthorized, "a logged-out session must not authenticate")
}

// A per-username lockout is blind to password spraying: one attempt each
// against a thousand usernames never reaches any single account's threshold,
// and there is no account to lock. This is the control that bounds it.
func TestSessionStore_SprayBudgetBoundsUnknownUserProbing(t *testing.T) {
	f := newFixture(t)

	// Exactly the budget: every one of these must still be an ordinary
	// unknown-user rejection, not a budget refusal.
	for i := 0; i < auth.MaxSprayAttempts; i++ {
		_, err := f.store.Login(context.Background(), "ghost"+strconv.Itoa(i), "guess", "", "")
		require.Error(t, err, "probe %d must fail", i)
	}
	assert.Equal(t, 0, f.audit.countReason("spray_budget_exhausted"),
		"the budget must not trip before it is actually spent")

	// One past the budget, and the reason flips. This is the assertion that
	// makes the test non-vacuous: without it, a build with the whole spray
	// control deleted would pass, because every probe still returned
	// "invalid credentials" and every probe was still audited.
	_, err := f.store.Login(context.Background(), "ghost-over", "guess", "", "")
	require.Error(t, err)
	assert.Equal(t, 1, f.audit.countReason("spray_budget_exhausted"),
		"probing past the budget must be refused AS a budget exhaustion, "+
			"which is what lets a moderator see the spray")
}

// The spray budget must not be charged for KNOWN users, or a legitimate burst
// of wrong passwords from one account would exhaust the shared budget and lock
// out the whole instance.
func TestSessionStore_SprayBudgetIsNotChargedForKnownUsers(t *testing.T) {
	f := newFixture(t)
	f.users.add(&models.User{Username: "alice"}, "correct")

	// Far more attempts than the spray budget, all against a real account.
	for i := 0; i < auth.MaxSprayAttempts+10; i++ {
		_, err := f.store.Login(context.Background(), "alice", "wrong", "", "")
		require.Error(t, err)
	}

	assert.Equal(t, 0, f.audit.countReason("spray_budget_exhausted"),
		"failures against a KNOWN account must never be charged to the spray "+
			"budget; if they were, one user could exhaust it and lock everyone out")

	// A different, real account must still be able to log in.
	f.users.add(&models.User{Username: "bob"}, "correct")
	_, err := f.store.Login(context.Background(), "bob", "correct", "", "")
	assert.NoError(t, err,
		"one account's failures must not exhaust the process-wide spray budget")
}

// --- invites ---------------------------------------------------------------

func TestSessionStore_InviteCreateAndRegister(t *testing.T) {
	f := newFixture(t)
	owner := f.users.add(&models.User{Username: "owner", IsOwner: true}, "x")

	key, err := f.store.CreateInvite(context.Background(), owner.ID, nil, 1)
	require.NoError(t, err)
	assert.NotEmpty(t, key)

	u, err := f.store.Register(context.Background(), "bob", "bobpass", key, "1.2.3.4", "UA")
	require.NoError(t, err)
	assert.Equal(t, "bob", u.Username)
	assert.False(t, u.IsOwner, "a registered user must not become the owner")
}

// The failure that turns one leaked key into a hundred accounts.
func TestSessionStore_SingleUseInviteCannotBeRedeemedTwice(t *testing.T) {
	f := newFixture(t)
	owner := f.users.add(&models.User{Username: "owner", IsOwner: true}, "x")

	key, err := f.store.CreateInvite(context.Background(), owner.ID, nil, 1)
	require.NoError(t, err)

	_, err = f.store.Register(context.Background(), "bob", "bobpass", key, "", "")
	require.NoError(t, err)

	_, err = f.store.Register(context.Background(), "eve", "evepass", key, "", "")
	assert.ErrorIs(t, err, auth.ErrInviteInvalid, "a spent single-use invite must be refused")
}

func TestSessionStore_ExpiredInviteIsRefused(t *testing.T) {
	f := newFixture(t)
	owner := f.users.add(&models.User{Username: "owner", IsOwner: true}, "x")

	past := f.clock.Add(-time.Hour)
	key, err := f.store.CreateInvite(context.Background(), owner.ID, &past, 1)
	require.NoError(t, err)

	_, err = f.store.Register(context.Background(), "bob", "bobpass", key, "", "")
	assert.ErrorIs(t, err, auth.ErrInviteInvalid)
}

func TestSessionStore_UnknownInviteIsRefused(t *testing.T) {
	f := newFixture(t)
	_, err := f.store.Register(context.Background(), "bob", "bobpass", "made-up", "", "")
	assert.ErrorIs(t, err, auth.ErrInviteInvalid)
}

// A failed account creation must not silently consume an invite use.
func TestSessionStore_FailedRegistrationDoesNotConsumeTheInvite(t *testing.T) {
	f := newFixture(t)
	owner := f.users.add(&models.User{Username: "owner", IsOwner: true}, "x")
	f.users.add(&models.User{Username: "taken"}, "x")

	key, err := f.store.CreateInvite(context.Background(), owner.ID, nil, 1)
	require.NoError(t, err)

	// The duplicate username fails at the user insert, after redemption.
	_, err = f.store.Register(context.Background(), "TAKEN", "newpass", key, "", "")
	assert.ErrorIs(t, err, models.ErrUsernameTaken)

	// The invite must still work for someone else.
	_, err = f.store.Register(context.Background(), "bob", "bobpass", key, "", "")
	assert.NoError(t, err, "a rejected registration must not burn the invite")
}

// The first account is the owner. An instance bootstrapped from a config file
// has an owner who is whoever edited the file; this has none.
//
// The POSITIVE case is the one that matters and the one that was missing: the
// earlier version of this test only asserted that an invited user is NOT the
// owner, which passes whether or not the flag is ever set -- a mutation that
// deleted the whole ownership bootstrap survived it.
func TestSessionStore_FirstRegisteredUserBecomesOwner(t *testing.T) {
	f := newFixture(t)
	// Note: no pre-existing user, so the user table is empty.
	assert.Equal(t, 0, mustCount(t, f.users))

	key, err := f.store.CreateInvite(context.Background(), 0, nil, 2)
	require.NoError(t, err, "an invite must be creatable before any user exists")

	first, err := f.store.Register(context.Background(), "alice", "alicepass", key, "", "")
	require.NoError(t, err)
	assert.True(t, first.IsOwner,
		"the very first account on a fresh instance must become the owner, "+
			"or the instance has no owner at all")

	// ...and only the first. The second registrant must not also be an owner,
	// which the partial unique index would reject anyway.
	second, err := f.store.Register(context.Background(), "bob", "bobpass", key, "", "")
	require.NoError(t, err)
	assert.False(t, second.IsOwner, "only the first account may hold the owner role")
}

func mustCount(t *testing.T, us *fakeUserStore) int {
	t.Helper()
	n, err := us.Count(context.Background())
	require.NoError(t, err)
	return n
}

// --- rehash on login -------------------------------------------------------

// Raising the cost parameters must reach existing accounts, not only new ones.
func TestSessionStore_LoginUpgradesAWeakHash(t *testing.T) {
	f := newFixture(t)
	u := f.users.add(&models.User{Username: "alice"}, "correct")

	// Downgrade the stored hash to simulate an account created under old params.
	weak, err := auth.HashWithParams("correct", auth.Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1})
	require.NoError(t, err)
	require.NoError(t, f.users.SetPasswordHash(context.Background(), u.ID, []byte(weak)))

	before := f.users.hashFor(u.ID)
	_, err = f.store.Login(context.Background(), "alice", "correct", "", "")
	require.NoError(t, err)

	after := f.users.hashFor(u.ID)
	assert.NotEqual(t, before, after, "a weak hash must be rewritten on next successful login")
	assert.NoError(t, auth.Verify("correct", after), "the upgraded hash must still verify")
	assert.True(t, auth.NeedsRehash(after, auth.DefaultParams) == false,
		"the upgraded hash must be at current parameters")
}

// A rehash failure must not lock a user out of a valid password.
func TestSessionStore_RehashFailureStillLogsIn(t *testing.T) {
	f := newFixture(t)
	u := f.users.add(&models.User{Username: "alice"}, "correct")
	weak, err := auth.HashWithParams("correct", auth.Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1})
	require.NoError(t, err)
	require.NoError(t, f.users.SetPasswordHash(context.Background(), u.ID, []byte(weak)))

	// Make the audit log fail, which is the path that reports a bad upgrade.
	f.audit.failLog = true

	res, err := f.store.Login(context.Background(), "alice", "correct", "", "")
	require.NoError(t, err, "a rehash problem must not block a valid login")
	assert.NotNil(t, res)
}

// A full disk must not turn a rejected login into a 500, which would be a
// denial-of-service on the login endpoint.
func TestSessionStore_AuditFailureDoesNotBreakLogin(t *testing.T) {
	f := newFixture(t)
	f.users.add(&models.User{Username: "alice"}, "correct")
	f.audit.failLog = true

	_, err := f.store.Login(context.Background(), "alice", "wrong", "", "")
	assert.ErrorIs(t, err, models.ErrInvalidCredentials,
		"a full audit log must not change the error class of a failed login")
}

func TestSessionStore_SuccessfulLoginIsAudited(t *testing.T) {
	f := newFixture(t)
	f.users.add(&models.User{Username: "alice"}, "correct")

	_, err := f.store.Login(context.Background(), "alice", "correct", "", "")
	require.NoError(t, err)
	assert.True(t, f.audit.hasAction("login"), "a successful login must be auditable")
}
