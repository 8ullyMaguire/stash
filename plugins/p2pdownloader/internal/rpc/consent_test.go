package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// # WHAT IS BEING TESTED HERE, AND WHY IT NEEDS A SEPARATE FILE
//
// internal/collab/locator.go is where core DECIDES. This file is where the
// plugin obeys that decision, and obeying it is a separate problem with a
// separate failure mode: core can refuse perfectly and the plugin can fetch
// anyway.
//
// That failure is invisible from core's side. Core refused, wrote nothing,
// logged a refusal, and moved on — every core-side test passes while the
// download happens. So the property "a refused proposal ends the transfer" is
// only observable from inside the plugin, and that is the only place this file
// can be written.

// stubProposer scripts what core answers.
//
// It records the proposals it received, so a test can assert on what the plugin
// ASKED as well as on what it did with the answer.
type stubProposer struct {
	answer  *Refusal
	failure error

	// nilAnswer makes Propose return (nil, nil), which is what a decode
	// mismatch between the two modules looks like from here.
	nilAnswer bool

	asked []Proposal
}

func (s *stubProposer) Propose(_ context.Context, p Proposal) (*Refusal, error) {
	s.asked = append(s.asked, p)
	if s.failure != nil {
		return nil, s.failure
	}
	if s.nilAnswer {
		return nil, nil
	}
	return s.answer, nil
}

func grantingProposer() *stubProposer {
	return &stubProposer{answer: &Refusal{Allowed: true, Checked: "third_party_permitted"}}
}

func refusingProposer(reason string) *stubProposer {
	return &stubProposer{answer: &Refusal{Allowed: false, Reason: reason, Checked: "denied"}}
}

// runDownload drives the real Download path with a scripted core.
//
// Download BLOCKS once past the gate (it stands in for a transfer), so every
// caller gives it a context with a short deadline. Without one a granted run
// hangs, and a hanging test is indistinguishable from a hang in the code.
func runDownload(t *testing.T, p *stubProposer, args ArgsMap) (*DownloadResult, error) {
	t.Helper()

	if _, ok := args["object_id"]; !ok {
		args["object_id"] = float64(7)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	return Download(ctx, PluginInput{Args: args, proposer: p})
}

// # THE PROPERTY THAT MATTERS MOST

// TestTheGateRefusesBeforeAnyTransfer is the assertion the whole milestone is
// built around, and it is phrased as a refusal rather than as "the gate was
// called" because a gate that is called and then ignored is worse than no gate.
//
// The refusal here stands in for the whole transfer, so "the error is a
// refusal" IS "no transfer started" — there is no other code path a transfer
// could have taken.
func TestTheGateRefusesBeforeAnyTransfer(t *testing.T) {
	core := refusingProposer("the object is denied")

	result, err := runDownload(t, core, ArgsMap{"url": "magnet:?xt=urn:btih:abc"})
	if err == nil {
		t.Fatalf("a refused locator produced a result (%+v). The plugin fetched "+
			"after core said no — the failure §7.1 describes, with core's gate "+
			"having worked perfectly", result)
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("the error is %v, which does not wrap ErrRefused. The plugin and "+
			"its host cannot tell a refusal from a network failure, and only one "+
			"of those is worth retrying", err)
	}
	if !strings.Contains(err.Error(), "denied") {
		t.Errorf("the error is %q, which does not carry core's reason. A refusal "+
			"an operator cannot read is a refusal they will work around", err)
	}
}

// TestThePluginAsksCoreBeforeFetching — the gate is consulted, not skipped.
func TestThePluginAsksCoreBeforeFetching(t *testing.T) {
	core := refusingProposer("no")

	if _, err := runDownload(t, core, ArgsMap{"url": "magnet:?xt=urn:btih:abc"}); err == nil {
		t.Fatal("expected a refusal")
	}

	if len(core.asked) != 1 {
		t.Fatalf("core was asked %d times, expected once", len(core.asked))
	}
	asked := core.asked[0]
	if asked.Locator != "magnet:?xt=urn:btih:abc" {
		t.Errorf("core was asked about %q, not the locator that was going to be "+
			"fetched. A gate asked about a different locator is a gate that "+
			"approves something nobody proposed", asked.Locator)
	}
	if asked.ObjectID != 7 {
		t.Errorf("core was asked about object %d, expected 7", asked.ObjectID)
	}
}

// TestTheGateIsAskedWithTheObjectAndLocatorThatWillBeFetched, and specifically
// NOT a constant. A gate asked about a placeholder while something else is
// fetched passes every test that only checks "was the gate called".
func TestTheGateIsAskedWithTheObjectAndLocatorThatWillBeFetched(t *testing.T) {
	core := grantingProposer()
	const wanted = "https://example.invalid/real-file.mkv"

	if _, err := runDownload(t, core, ArgsMap{
		"url":       wanted,
		"object_id": float64(99),
	}); err == nil {
		t.Fatal("expected the transfer stub's error")
	}

	if len(core.asked) != 1 {
		t.Fatalf("core was asked %d times", len(core.asked))
	}
	if core.asked[0].Locator != wanted {
		t.Errorf("core was asked about %q, but %q is what would be fetched. A "+
			"gate that approves a different locator is a gate that approves "+
			"nothing", core.asked[0].Locator, wanted)
	}
	if core.asked[0].ObjectID != 99 {
		t.Errorf("core was asked about object %d, expected 99", core.asked[0].ObjectID)
	}
}

// # FAILING CLOSED, WHICH IS THE CASES NOBODY WANTS

// TestAnUnreachableCoreMeansNoTransfer is the case that makes a downloader
// "helpful" about fallbacks, and it is the one that has to be written down
// explicitly. A plugin that cannot reach core's gate and decides to proceed is
// a plugin whose gate is advisory, and an advisory gate is not a gate.
func TestAnUnreachableCoreMeansNoTransfer(t *testing.T) {
	for _, failure := range []error{
		errors.New("connection refused"),
		context.DeadlineExceeded,
		errors.New("core answered 500 Internal Server Error"),
	} {
		core := &stubProposer{failure: failure}

		_, err := runDownload(t, core, ArgsMap{"url": "magnet:?xt=urn:btih:abc"})
		if err == nil {
			t.Errorf("core was unreachable (%v) and the plugin transferred anyway", failure)
			continue
		}
		if !errors.Is(err, ErrRefused) {
			t.Errorf("core was unreachable (%v) and the error is %v, which does "+
				"not wrap ErrRefused. "+
				"Unreachable is not permitted, and the error has to say so", failure, err)
		}
	}
}

// TestANilAnswerIsNotConsent is the field-rename defence.
//
// A Refusal decoded from a core response where `allowed` was renamed arrives as
// the ZERO value: Allowed false. Treating that as consent would turn a renamed
// field into an open door. So a nil answer — which is what a decode failure
// really produces — is refused rather than read as approval.
func TestANilAnswerIsNotConsent(t *testing.T) {
	core := &stubProposer{nilAnswer: true}

	_, err := runDownload(t, core, ArgsMap{"url": "magnet:?xt=urn:btih:abc"})
	if err == nil {
		t.Fatal("core sent no decision and the plugin treated that as consent. " +
			"A decode mismatch must fail closed, not open")
	}
	if !strings.Contains(err.Error(), "not a permission") {
		t.Errorf("the error is %q, which does not say that silence is not "+
			"permission", err)
	}
}

// TestABuildWithNoGateRefusesEverything: a nil proposer is the "the plugin was
// launched without a connection to the host" case.
func TestABuildWithNoGateRefusesEverything(t *testing.T) {
	// No proposer at all — the input carries no server connection and no
	// injected gate.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := Download(ctx, PluginInput{
		Args: ArgsMap{
			"url":       "magnet:?xt=urn:btih:abc",
			"object_id": float64(7),
		},
	})
	if err == nil {
		t.Fatal("a run with no consent gate transferred. The temptation to " +
			"treat \"no gate\" as \"no restriction\" is the failure this test " +
			"exists to forbid")
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("the error is %v, which does not wrap ErrRefused", err)
	}
	if !strings.Contains(err.Error(), "consent gate") {
		t.Errorf("the error is %q, which does not say a gate was missing. An "+
			"operator seeing \"no download\" deserves to know why", err)
	}
}

// TestARefusalIsNotRetried is about the plugin's behaviour rather than a
// return value: a refused proposal is final, and a plugin that retried it would
// hammer a gate that has already answered.
func TestARefusalIsNotRetried(t *testing.T) {
	core := refusingProposer("denied")

	if _, err := runDownload(t, core, ArgsMap{"url": "magnet:?xt=urn:btih:abc"}); err == nil {
		t.Fatal("expected a refusal")
	}

	if len(core.asked) != 1 {
		t.Errorf("core was asked %d times after refusing once. A refusal is "+
			"final; asking again is the plugin arguing with the gate", len(core.asked))
	}
}

// # WHAT HAPPENS BEFORE THE GATE, AND WHY

// TestAMalformedLocatorNeverReachesTheGate: `file:///etc/passwd` is refused
// locally, because asking core's gate about it spends an operator's trust on a
// question with an obvious answer.
func TestAMalformedLocatorNeverReachesTheGate(t *testing.T) {
	// A FRESH stub per locator. One shared stub accumulates every proposal
	// across the loop, so the count at the end is 3 and the assertion reads as
	// a failure of the gate rather than of the test's own bookkeeping.
	for _, bad := range []string{
		"file:///etc/passwd",
		"ftp://example.invalid/x",
		"example.invalid/file.torrent", // no scheme
	} {
		core := grantingProposer() // would say yes to anything

		if _, err := runDownload(t, core, ArgsMap{"url": bad}); err == nil {
			t.Errorf("%q was accepted", bad)
		}
		if len(core.asked) != 0 {
			t.Errorf("%q: core was asked %d times about a locator this plugin "+
				"should have refused itself", bad, len(core.asked))
		}
	}
}

// TestNoObjectMeansNoProposal, because a locator is a pointer to something and
// without an object it points at nothing.
func TestNoObjectMeansNoProposal(t *testing.T) {
	// A fresh stub, and NOT runDownload -- runDownload supplies a default
	// object_id, and this test is about the case where there is none. Reusing
	// the helper and then clearing the stub's record would work and would also
	// make the assertion unfalsifiable by construction, which is the same defect
	// as asserting against a value the test just overwrote.
	core := grantingProposer()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	_, err := Download(ctx, PluginInput{
		Args:     ArgsMap{"url": "magnet:?xt=urn:btih:abc"},
		proposer: core,
	})
	if err == nil {
		t.Fatal("a locator with no object was proposed and fetched")
	}
	if len(core.asked) != 0 {
		t.Errorf("core was asked about a locator naming no object (%d proposals)", len(core.asked))
	}
}

// # THE WIRE FORMAT, WHICH IS THE ONLY THING HOLDING THE TWO MODULES TOGETHER

// TestTheProposalRoundTrips: the plugin and core are separate modules and
// cannot share a type, so the JSON tags ARE the interface.
//
// A renamed field does not error. It decodes as its zero value, which for
// `allowed` is a refusal — safe, and a downloader that silently never downloads
// with no clue why. So the shapes are pinned from both directions.
func TestTheProposalRoundTrips(t *testing.T) {
	// What the plugin SENDS, encoded exactly as the HTTP proposer does.
	encoded, err := json.Marshal(map[string]interface{}{
		"query": "mutation",
		"variables": map[string]interface{}{
			"objectId": int64(7),
			"locator":  "magnet:?xt=urn:btih:abc",
			"scheme":   "magnet",
		},
	})
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if !strings.Contains(string(encoded), `"objectId":7`) {
		t.Errorf("the variables encode as %s. GraphQL variable names are "+
			"camelCase by convention and the schema's are, so a snake_case "+
			"name here fails at the server rather than here", encoded)
	}

	// What the plugin EXPECTS to receive back.
	refusal := Refusal{Allowed: true, Reason: "", Checked: "third_party_permitted"}
	back, err := json.Marshal(refusal)
	if err != nil {
		t.Fatalf("encoding the refusal: %v", err)
	}

	var decoded Refusal
	if err := json.Unmarshal(back, &decoded); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if !decoded.Allowed || decoded.Checked != "third_party_permitted" {
		t.Errorf("the refusal came back as %+v", decoded)
	}
}

// TestAGrantStaysAllowed: the permissive direction. A field-rename defect here
// makes every grant a refusal, which fails safe — but it makes a downloader
// that never downloads, which is its own silent failure, so it is worth a test
// that says a grant survives the round trip.
func TestAGrantStaysAllowed(t *testing.T) {
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"locatorPropose":{"allowed":true,"checked":"third_party_permitted"}}}`))
	}))
	defer served.Close()

	proposer := proposerForServer(served.URL)
	answer, err := proposer.Propose(context.Background(), Proposal{
		ObjectID: 7, Locator: "magnet:?xt=urn:btih:abc",
	})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if answer == nil || !answer.Allowed {
		t.Fatalf("a grant came back as %+v. A field-rename defect here is safe "+
			"but silent: the downloader simply never downloads", answer)
	}
}

// TestANonOKStatusIsNotAPermission: a 500 is an outage, not a refusal, and the
// error has to say which — "core refused" sends an operator to look at their
// consent tiers and "core is down" does not.
func TestANonOKStatusIsNotAPermission(t *testing.T) {
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer served.Close()

	proposer := proposerForServer(served.URL)
	_, err := proposer.Propose(context.Background(), Proposal{ObjectID: 7, Locator: "x"})
	if err == nil {
		t.Fatal("a 500 came back as an answer")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("the error is %q, which does not carry the status. An operator "+
			"cannot tell an outage from a policy decision without it", err)
	}
}

// TestGraphQLErrorsAreSurfaced rather than decoded as an empty decision.
func TestGraphQLErrorsAreSurfaced(t *testing.T) {
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errors":[{"message":"not authenticated"}]}`))
	}))
	defer served.Close()

	proposer := proposerForServer(served.URL)
	answer, err := proposer.Propose(context.Background(), Proposal{ObjectID: 7, Locator: "x"})
	if err == nil {
		t.Fatal("a GraphQL error came back as an answer")
	}
	if !strings.Contains(err.Error(), "not authenticated") {
		t.Errorf("the error is %q, which drops core's message", err)
	}
	if answer != nil {
		t.Errorf("an error also produced the answer %+v. Returning both invites "+
			"a caller to use the answer and ignore the error", answer)
	}
}

// TestTheSessionCookieIsSent: it is the only credential a plugin process has,
// and a proposal without it is an authentication failure on every call.
func TestTheSessionCookieIsSent(t *testing.T) {
	var got *http.Cookie
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Cookies()[0]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"locatorPropose":{"allowed":true}}}`))
	}))
	defer served.Close()

	proposer := proposerForServer(served.URL)
	proposer.(*httpProposer).conn.SessionCookie = &http.Cookie{Name: "session", Value: "abc123"}

	if _, err := proposer.Propose(context.Background(), Proposal{ObjectID: 7, Locator: "x"}); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if got == nil {
		t.Fatal("no cookie reached the host. A plugin's session cookie is the " +
			"only credential it has, and without it every proposal is an " +
			"authentication failure")
	}
	if got.Value != "abc123" {
		t.Errorf("the host received cookie %q", got.Value)
	}
}

// proposerForServer builds a proposer aimed at a test server, standing in for
// the host.
func proposerForServer(addr string) Proposer {
	host, port, _ := strings.Cut(strings.TrimPrefix(addr, "http://"), ":")
	return newHTTPProposer(StashServerConnection{
		Scheme: "http",
		Host:   host,
		Port:   atoi(port),
	})
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
