package collab

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Mode tests. M4 step 4.1.
//
// The load-bearing one is TestMode_PublicOverPlainHTTPRefusesToStart, and its
// companion TestMode_ContributeOverPlainHTTPIsFine: the check has to refuse
// public-over-HTTP AND permit the modes that are legitimately served over
// plain HTTP. A guard that refuses everything is not a guard, and an operator
// who cannot run their private instance on a LAN will route around it.

// TestMode_SharesMetadata pins §2's table. Contribute and public share; private
// does not.
func TestMode_SharesMetadata(t *testing.T) {
	for _, tc := range []struct {
		mode Mode
		want bool
	}{
		{ModePrivate, false},
		{ModeContribute, true},
		{ModePublic, true},
		{Mode("private "), false}, // trailing space is not a valid mode
		{Mode("PUBLIC"), false},   // case-sensitive: the schema CHECK is
		{Mode(""), false},
	} {
		if got := tc.mode.SharesMetadata(); got != tc.want {
			t.Errorf("%q.SharesMetadata() = %v, want %v", tc.mode, got, tc.want)
		}
	}
}

// TestMode_ServesMediaOnlyInPublic: the distinction that keeps this from being a
// public file host.
func TestMode_ServesMediaOnlyInPublic(t *testing.T) {
	if ModePrivate.ServesMedia() {
		t.Error("private mode serves media")
	}
	if ModeContribute.ServesMedia() {
		t.Error("contribute mode serves media; §6.4's default is metadata flows, media does not")
	}
	if !ModePublic.ServesMedia() {
		t.Error("public mode must be able to serve media, subject to a grant")
	}
}

// TestMode_PublicOverPlainHTTPRefusesToStart is the milestone's headline.
func TestMode_PublicOverPlainHTTPRefusesToStart(t *testing.T) {
	err := ModeErrors(ModePublic, "http")
	if !errors.Is(err, ErrInsecurePublicMode) {
		t.Fatalf("public over http returned %v, want ErrInsecurePublicMode", err)
	}

	// The message has to be actionable: an operator who is told only "public
	// mode requires HTTPS" still does not know which knob to turn.
	for _, want := range []string{"X-Forwarded-Proto", "contribute"} {
		if !contains2(err.Error(), want) {
			t.Errorf("error %q should mention %q so the operator knows what to change", err, want)
		}
	}

	// And RequireStartup wraps it, so a caller cannot accidentally start anyway.
	if err := (&ModeEnforcer{Mode: ModePublic, Scheme: "http"}).RequireStartup(); !errors.Is(err, ErrInsecurePublicMode) {
		t.Errorf("RequireStartup returned %v, want the same sentinel", err)
	}
}

// TestMode_ContributeOverPlainHTTPIsFine: the other direction. Contribute is a
// trusted-network or proxied mode, and refusing it would push operators to
// disable the check rather than fix anything.
func TestMode_ContributeOverPlainHTTPIsFine(t *testing.T) {
	for _, scheme := range []string{"http", "HTTP", "", "ws"} {
		if err := ModeErrors(ModeContribute, scheme); err != nil {
			t.Errorf("contribute over %q returned %v, want nil: this mode is legitimately served over plain HTTP", scheme, err)
		}
	}
	if err := ModeErrors(ModePrivate, "http"); err != nil {
		t.Errorf("private over http returned %v, want nil", err)
	}
}

// TestMode_PublicOverHTTPSIsFine, including case and whitespace, because a
// proxy that sends "HTTPS" must not trip the check.
//
// Note what is NOT in this list: a raw "X-Forwarded-Proto" header value like
// "https, http". RequestScheme splits that on the comma; ModeErrors receives the
// already-normalised single scheme. Feeding one layer's raw input to the other
// would be testing a format that never occurs -- and the fact that this test
// initially listed it is why the comment is here.
func TestMode_PublicOverHTTPSIsFine(t *testing.T) {
	for _, scheme := range []string{"https", "HTTPS", " https "} {
		if err := ModeErrors(ModePublic, scheme); err != nil {
			t.Errorf("public over %q returned %v, want nil", scheme, err)
		}
	}

	// The end-to-end path: a request behind a proxy whose header carries a
	// trailing hop still yields a scheme the startup check accepts.
	r := httptest.NewRequest("GET", "http://host/x", nil)
	r.Header.Set("X-Forwarded-Proto", "https, http")
	got := RequestScheme(r)
	if err := ModeErrors(ModePublic, got); err != nil {
		t.Errorf("public behind a proxy returned %v (scheme %q), want nil", err, got)
	}
}

// TestMode_InvalidIsNamedNotSilentlyTreated: an unrecognised mode must be an
// error naming the valid set, not a default. Defaulting here would mean a typo
// in a config file silently chose a posture.
func TestMode_InvalidIsNamed(t *testing.T) {
	for _, bad := range []Mode{"", "Public", "public ", "open", "contribute\n"} {
		err := ModeErrors(bad, "https")
		if !errors.Is(err, ErrModeInvalid) {
			t.Errorf("ModeErrors(%q, https) = %v, want ErrModeInvalid", bad, err)
		}
		if !contains2(err.Error(), "private") {
			t.Errorf("error %q should name the valid modes so a typo is fixable", err)
		}
		if bad.Valid() {
			t.Errorf("%q.Valid() = true", bad)
		}
	}
}

// TestModeFrom_DefaultsToTheMostRestrictive: an absent mode is a configuration
// gap, and a gap must not resolve to "public".
func TestModeFrom_DefaultsToTheMostRestrictive(t *testing.T) {
	if got := ModeFrom(context.Background()); got != ModePrivate {
		t.Errorf("ModeFrom on an empty context = %q, want %q: a missing mode must fail closed", got, ModePrivate)
	}
	// And an INVALID mode on the context also fails closed rather than being
	// taken at face value.
	ctx := WithMode(context.Background(), Mode("nonsense"))
	if got := ModeFrom(ctx); got != ModePrivate {
		t.Errorf("ModeFrom with an invalid mode = %q, want %q", got, ModePrivate)
	}
	ctx = WithMode(context.Background(), ModePublic)
	if got := ModeFrom(ctx); got != ModePublic {
		t.Errorf("ModeFrom with public = %q, want public", got)
	}
}

// TestRequestScheme: the mode check and the base-URL middleware must agree, or
// the instance refuses to start while believing it is behind TLS.
func TestRequestScheme(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  *http.Request
		want string
	}{
		{"plain request", httptest.NewRequest("GET", "http://host/x", nil), "http"},
		{"tls request", httptest.NewRequest("GET", "https://host/x", nil), "https"},
		{"forwarded https", func() *http.Request {
			r := httptest.NewRequest("GET", "http://host/x", nil)
			r.Header.Set("X-Forwarded-Proto", "https")
			return r
		}(), "https"},
		{"forwarded https with a trailing entry", func() *http.Request {
			r := httptest.NewRequest("GET", "http://host/x", nil)
			r.Header.Set("X-Forwarded-Proto", "https, http")
			return r
		}(), "https"},
		{"forwarded mixed, http first", func() *http.Request {
			r := httptest.NewRequest("GET", "http://host/x", nil)
			r.Header.Set("X-Forwarded-Proto", "http, https")
			return r
		}(), "http"},
		{"forwarded uppercase", func() *http.Request {
			r := httptest.NewRequest("GET", "http://host/x", nil)
			r.Header.Set("X-Forwarded-Proto", "HTTPS")
			return r
		}(), "https"},
		{"a bogus forwarded value does not become https", func() *http.Request {
			r := httptest.NewRequest("GET", "http://host/x", nil)
			r.Header.Set("X-Forwarded-Proto", "httpsx")
			return r
		}(), "httpsx"},
	} {
		if got := RequestScheme(tc.req); got != tc.want {
			t.Errorf("%s: RequestScheme = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestMode_ForwardedProtoCannotTalkTheInstanceIntoTLS: an attacker-supplied
// header must not be able to satisfy the public-mode check. This is the case
// that makes "trust X-Forwarded-Proto" a real decision rather than a convenience.
func TestMode_ForwardedProtoCannotTalkTheInstanceIntoTLS(t *testing.T) {
	r := httptest.NewRequest("GET", "http://host/x", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	// RequestScheme reports https -- which is correct, the edge said so.
	if got := RequestScheme(r); got != "https" {
		t.Fatalf("RequestScheme = %q, want https", got)
	}
	// But ModeErrors is what gates startup, and it must be given the scheme the
	// OPERATOR configured, not one a request supplied. Startup has no request,
	// which is exactly why this cannot be spoofed at startup time.
	if err := ModeErrors(ModePublic, "http"); err == nil {
		t.Fatal("a request header satisfied the startup check: the startup scheme must come from configuration, never from a request")
	}
}

// TestMode_MustServeMediaTo pins §6.4's two-separate-grants rule, including the
// undifferentiated 404.
func TestMode_MustServeMediaTo(t *testing.T) {
	// Private and contribute refuse everyone, grant or not.
	for _, m := range []Mode{ModePrivate, ModeContribute} {
		for _, grant := range []bool{false, true} {
			err := m.MustServeMediaTo(grant)
			if err == nil {
				t.Errorf("%q.MustServeMediaTo(grant=%v) = nil, want a refusal: §6.4 says the default instance serves no media", m, grant)
			}
			if !IsMediaNotFound(err) {
				t.Errorf("%q.MustServeMediaTo(%v) = %v, want the not-found sentinel", m, grant, err)
			}
		}
	}

	// Public serves a grantee and refuses an ungranted one.
	if err := ModePublic.MustServeMediaTo(true); err != nil {
		t.Errorf("public with a grant returned %v, want nil", err)
	}
	if err := ModePublic.MustServeMediaTo(false); !IsMediaNotFound(err) {
		t.Errorf("public without a grant returned %v, want not-found", err)
	}

	// The two refusals are the SAME error, or the second leaks the first.
	modeErr := ModeContribute.MustServeMediaTo(false)
	grantErr := ModePublic.MustServeMediaTo(false)
	if modeErr.Error() != grantErr.Error() {
		t.Errorf("refusals differ: %q vs %q -- a caller can tell 'wrong mode' from 'no grant', which discloses that the file exists", modeErr, grantErr)
	}
	if modeErr.Error() != "not found" {
		t.Errorf("media refusal message = %q, want \"not found\": it maps to a 404 and must not describe the reason", modeErr)
	}
}

// TestMode_AcceptsAnonymousProposals pins §2's "who can post" column.
func TestMode_AcceptsAnonymousProposals(t *testing.T) {
	if ModePrivate.AcceptsAnonymousProposals() {
		t.Error("private mode accepts anonymous proposals; §2 says owner only")
	}
	if ModeContribute.AcceptsAnonymousProposals() {
		t.Error("contribute mode accepts anonymous proposals; §2 says owner and publishers")
	}
	if !ModePublic.AcceptsAnonymousProposals() {
		t.Error("public mode must accept proposals from anyone, under §5 governance")
	}
}

// TestCheckStartup_WizardGatesThePublicMode is step 4.4's rule, tested at the
// layer that enforces it: a public mode is refused until the wizard has run,
// because a mode set by a migration or a restored backup is not a choice.
func TestCheckStartup_WizardGatesThePublicMode(t *testing.T) {
	// Public, over TLS, but nobody chose it.
	err := CheckStartup(ModePublic, "https", false)
	if !errors.Is(err, ErrModeNotChosen) {
		t.Fatalf("public without the wizard returned %v, want ErrModeNotChosen", err)
	}
	// The message has to say what to do.
	for _, want := range []string{"wizard", "contribute"} {
		if !contains2(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}

	// Public, over TLS, chosen: fine.
	if err := CheckStartup(ModePublic, "https", true); err != nil {
		t.Errorf("public with the wizard completed returned %v, want nil", err)
	}

	// The wizard gate must NOT apply to the other modes: a private instance that
	// has not run the wizard is simply a private instance, and refusing to start
	// it would make an unconfigured instance unbootable.
	for _, m := range []Mode{ModePrivate, ModeContribute} {
		if err := CheckStartup(m, "http", false); err != nil {
			t.Errorf("%q without the wizard returned %v, want nil: the gate is about PUBLIC, not about being configured", m, err)
		}
	}

	// And the TLS check still runs first: an unchosen public mode over plain HTTP
	// reports the insecure scheme, because that is the more urgent of the two.
	if err := CheckStartup(ModePublic, "http", false); !errors.Is(err, ErrInsecurePublicMode) {
		t.Errorf("public over http without the wizard returned %v, want the TLS error first", err)
	}
}

func contains2(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
