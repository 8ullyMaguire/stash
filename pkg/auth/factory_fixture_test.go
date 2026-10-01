package auth_test

import (
	"context"
	"net/http"

	"github.com/stashapp/stash/pkg/auth"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/session"
)

// factoryFixture bundles everything Factory.Build needs, so each test states
// only the dependency it is actually about.
type factoryFixture struct {
	users     *fakeUserStore
	sess      *fakeSessionRepo
	inv       *fakeInviteRepo
	audit     *fakeAuditRepo
	authStore *auth.SessionStore
}

func newFactoryFixture(t interface{ Helper() }) *factoryFixture {
	users, sess, inv, audit := newFakeUserStore(), newFakeSessionRepo(), newFakeInviteRepo(), newFakeAuditRepo()
	return &factoryFixture{
		users:     users,
		sess:      sess,
		inv:       inv,
		audit:     audit,
		authStore: auth.NewSessionStore(users, sess, inv, audit, fakeConfig{maxAge: 3600}),
	}
}

// factory returns a Factory with every dependency wired. A test removes the one
// it is checking, so an untested dependency can never be what breaks the test.
func (f *factoryFixture) factory() *auth.Factory {
	return &auth.Factory{
		Users:    f.users,
		Sessions: f.sess,
		Invites:  f.inv,
		Audit:    f.audit,
		Config:   fakeConfig{maxAge: 3600},
	}
}

// cookieStoreDouble is a stand-in for upstream's cookie store. It exists so the
// tests can assert on IDENTITY -- "the cookie store was returned unchanged" --
// which is the property that makes a legacy install safe to adopt.
// The embedded session.Store is deliberately absent: a struct literal that
// embeds an interface satisfies it even with every method overridden, so a
// missing method would compile and then panic at runtime. Listing the six
// methods makes the compiler do the work.
type cookieStoreDouble struct {
	tag string
}

func newCookieStoreDouble() *cookieStoreDouble {
	return &cookieStoreDouble{tag: "upstream-cookie-store"}
}

func (c *cookieStoreDouble) Authenticate(w http.ResponseWriter, r *http.Request) (string, error) {
	return "configuser", nil
}

func (c *cookieStoreDouble) Login(w http.ResponseWriter, r *http.Request) error { return nil }
func (c *cookieStoreDouble) Logout(w http.ResponseWriter, r *http.Request) error {
	return nil
}
func (c *cookieStoreDouble) GetSessionUserID(w http.ResponseWriter, r *http.Request) (string, error) {
	return "configuser", nil
}
func (c *cookieStoreDouble) VisitedPluginHandler() func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler { return h }
}
func (c *cookieStoreDouble) MakePluginCookie(ctx context.Context) *http.Cookie { return nil }

var _ session.Store = (*cookieStoreDouble)(nil)
var _ models.UserStore = (*fakeUserStore)(nil)
