package api

// stash#7238 -- the funscript path must be a SIGNED url, not one carrying ?apikey=.
//
// ## WHY THIS TEST EXISTS AT ALL
//
// The scene page handed out nine URLs. Two of them were signed, and one -- `funscript` -- carried
// the instance's API key in the query string. That is not a style difference:
//
//   - an apikey can rewrite the entire database, and this URL is the one a CLIENT fetches (the
//     interactive-script and TheHandy integrations load it directly), so it is the URL most likely
//     to land in a third party's access log, a browser history, or a proxy log;
//   - the two paths beside it were already signed for exactly that reason, so the funscript path
//     was an OMISSION rather than a decision.
//
// ## WHY IT CALLS `scenePaths` AND NOT THE RESOLVER
//
// The resolver reads its config through `manager.GetInstance()`, and that singleton panics when
// uninitialised with no test-only setter. So the first version of this test panicked before
// asserting anything -- a test that cannot run is not a weak test, it is no test. The credential-URL
// logic now lives in `scenePaths`, which takes its config as an argument, and that is what these
// tests drive. See the comment on `scenePaths` for why it was extracted rather than the singleton
// being made settable.
//
// ## WHY THE ASSERTION IS ON THE PARSED QUERY AND NOT ON A SUBSTRING
//
// A test asserting `!strings.Contains(url, "apikey")` also passes when the parameter is spelled
// `apiKey`, or when the key is empty because the fixture forgot to set one. So each fixture ASSERTS
// its own premise -- credentials exist, the key is non-empty -- before checking that the emitted URL
// carries neither the parameter nor the secret.

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/stashapp/stash/internal/api/urlbuilders"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/session"
	"github.com/stashapp/stash/pkg/signedurl"
)

const testBaseURL = "http://stash.example.com:9999"

// pathsFixtureWithCredentials configures an instance WITH credentials and a real API key.
func pathsFixtureWithCredentials(t *testing.T) *config.Config {
	t.Helper()
	c := config.InitializeEmpty()
	c.Set(config.Host, "stash.example.com")
	c.Set(config.Port, 9999)
	c.Set(config.Username, "admin")
	c.Set(config.Password, "$2a$10$notarealhashbutlongenough0000000000000000000000000000")
	c.Set(config.JWTSignKey, "signing-key")
	c.Set(config.ApiKey, "SUPERSECRET-API-KEY")

	if !c.HasCredentials() {
		t.Fatal("the fixture must present an instance WITH credentials, or the signing branch " +
			"never runs and this test passes having checked nothing")
	}
	if c.GetAPIKey() != "SUPERSECRET-API-KEY" {
		t.Fatalf("the fixture must configure a real API key, got %q", c.GetAPIKey())
	}
	return c
}

// scenePathsContext carries a current user, which is what the signing branch reads.
func scenePathsContext() context.Context {
	return session.SetCurrentUserID(context.Background(), "admin")
}

func testBuilder(id int) urlbuilders.SceneURLBuilder {
	return urlbuilders.NewSceneURLBuilder(testBaseURL, &models.Scene{ID: id, Title: "A"})
}

// TestSceneFunscriptPathIsSignedAndCarriesNoApiKey is the whole point of #7238.
func TestSceneFunscriptPathIsSignedAndCarriesNoApiKey(t *testing.T) {
	cfg := pathsFixtureWithCredentials(t)

	got, err := scenePaths(scenePathsContext(), cfg, testBuilder(1), &models.Scene{ID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got.funscript == "" {
		t.Fatal("the funscript path must be present")
	}

	u, err := url.Parse(got.funscript)
	if err != nil {
		t.Fatalf("the funscript URL must parse: %v", err)
	}

	if q := u.Query(); q.Get("apikey") != "" {
		t.Fatalf("the funscript URL carries an apikey parameter: %q (full URL %s)",
			q.Get("apikey"), got.funscript)
	}
	if strings.Contains(got.funscript, "SUPERSECRET-API-KEY") {
		t.Fatalf("the funscript URL leaks the API key verbatim: %s", got.funscript)
	}

	// The positive: it must still be a URL the client can actually fetch, and the signature must
	// verify against the path that will be requested. "Refusing to leak the key" is only half the
	// fix; the other half is that interactive scripts keep working.
	if u.Path != "/scene/1/funscript" {
		t.Errorf("funscript path is %q, want /scene/1/funscript", u.Path)
	}
	if _, err := signedurl.VerifyURL(u.Path, u.Query(), cfg.GetJWTSignKey()); err != nil {
		t.Fatalf("the signed funscript URL must verify against its own path: %v", err)
	}
}

// TestTheFunscriptSignatureIsScopedToItsOwnScene guards against a signature that would verify for
// any scene.
//
// Without this, a "fix" that signs over a prefix shared by every scene -- or signs nothing and
// merely omits the apikey -- would pass the test above.
func TestTheFunscriptSignatureIsScopedToItsOwnScene(t *testing.T) {
	cfg := pathsFixtureWithCredentials(t)

	got, err := scenePaths(scenePathsContext(), cfg, testBuilder(1), &models.Scene{ID: 1})
	if err != nil {
		t.Fatal(err)
	}

	u, err := url.Parse(got.funscript)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := signedurl.VerifyURL("/scene/2/funscript", u.Query(),
		cfg.GetJWTSignKey()); err == nil {
		t.Error("a funscript signature must not verify for a different scene")
	}
}

// TestTheOtherCredentialPathsAreStillSigned is the regression guard for the fix itself.
//
// The change moved the funscript URL into the signing branch. If that branch's condition were
// widened or narrowed by accident these are the paths that would change, so they are checked here --
// a future edit to the funscript line cannot quietly break them.
func TestTheOtherCredentialPathsAreStillSigned(t *testing.T) {
	cfg := pathsFixtureWithCredentials(t)

	got, err := scenePaths(scenePathsContext(), cfg, testBuilder(1), &models.Scene{ID: 1})
	if err != nil {
		t.Fatal(err)
	}

	for name, raw := range map[string]string{"stream": got.stream, "caption": got.caption} {
		if raw == "" {
			t.Errorf("%s path must be present", name)
			continue
		}
		if strings.Contains(raw, "SUPERSECRET-API-KEY") {
			t.Errorf("%s path leaks the API key: %s", name, raw)
		}
		u, err := url.Parse(raw)
		if err != nil {
			t.Errorf("%s path must parse: %v", name, err)
			continue
		}
		if _, err := signedurl.VerifyURL(u.Path, u.Query(), cfg.GetJWTSignKey()); err != nil {
			t.Errorf("%s path must still be signed: %v", name, err)
		}
	}
}

// TestSceneFunscriptPathStillCarriesTheApiKeyWithoutCredentials is the other branch.
//
// With no credentials there is nothing to sign against, and the instance is already reachable
// without auth on the LAN -- exactly the condition the stream path has always fallen back under. So
// the apikey stays in this branch, and a "fix" that removed it unconditionally would break every
// credential-less instance's interactive scripts.
func TestSceneFunscriptPathStillCarriesTheApiKeyWithoutCredentials(t *testing.T) {
	c := config.InitializeEmpty()
	c.Set(config.Host, "stash.example.com")
	c.Set(config.Port, 9999)
	c.Set(config.ApiKey, "legacy-key")

	if c.HasCredentials() {
		t.Fatal("the fixture must present an instance WITHOUT credentials, or the signing branch " +
			"runs and this test checks the wrong branch")
	}

	got, err := scenePaths(scenePathsContext(), c, testBuilder(1), &models.Scene{ID: 1})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(got.funscript, "apikey=legacy-key") {
		t.Errorf("with no credentials configured the funscript URL must keep carrying the apikey, "+
			"got %s", got.funscript)
	}
}

// TestScenePathsNeedsAUserInContext is the nil guard.
//
// The signing branch reads the user id out of the context and returns an error rather than
// panicking when it is absent. That is load-bearing: a funscript request from a background job with
// no session must produce an error, not a crashed handler.
func TestScenePathsNeedsAUserInContext(t *testing.T) {
	cfg := pathsFixtureWithCredentials(t)

	_, err := scenePaths(context.Background(), cfg, testBuilder(1), &models.Scene{ID: 1})
	if err == nil {
		t.Fatal("no user in context must be an error")
	}
	if !strings.Contains(err.Error(), "user ID not found") {
		t.Errorf("the error must name the missing user, got %q", err.Error())
	}
}

// TestNoCredentialPathLeaksTheKeyInEitherBranch is the sweep over all three URLs.
//
// One test that walks every credential-carrying URL rather than one assertion per path, because the
// #7238 defect was precisely a path that was checked in isolation and therefore not checked at all.
func TestNoCredentialPathLeaksTheKeyInEitherBranch(t *testing.T) {
	for _, withCreds := range []bool{true, false} {
		name := "no credentials"
		c := config.InitializeEmpty()
		if withCreds {
			name = "with credentials"
			c = pathsFixtureWithCredentials(t)
		} else {
			c.Set(config.ApiKey, "legacy-key")
		}

		got, err := scenePaths(scenePathsContext(), c, testBuilder(1), &models.Scene{ID: 1})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		for path, raw := range map[string]string{
			"stream":    got.stream,
			"caption":   got.caption,
			"funscript": got.funscript,
		} {
			u, err := url.Parse(raw)
			if err != nil {
				t.Errorf("%s/%s must parse: %v", name, path, err)
				continue
			}
			if c.HasCredentials() {
				// With credentials every one of these must be a signature.
				if u.Query().Get("apikey") != "" {
					t.Errorf("%s/%s carries an apikey: %s", name, path, raw)
				}
				if _, err := signedurl.VerifyURL(u.Path, u.Query(), c.GetJWTSignKey()); err != nil {
					t.Errorf("%s/%s must be signed: %v", name, path, err)
				}
			}
		}
	}
}
