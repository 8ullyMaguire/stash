package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The first-run wizard over HTTP. M4 step 4.4.
//
// # The property under test
//
// `collab.RequireWizard` refuses every request until the wizard is decided, so
// BEFORE this the gate was a brick: the store existed, the gate existed, and
// nothing could set the flag. A fresh instance refused everything with no way
// forward -- the same "referenced != used" shape as the stores that were
// implemented and never constructed.
//
// So these tests have two jobs. First, that the decision can be made. Second, and
// this is the part worth reading, that making the decision is NOT something
// anyone who can reach the port can do.
//
// # Why a fake store, given the lesson in the handoff about fakes
//
// The lesson there was that a fake cannot prove the SQL. This is the other half:
// the SQL is covered in pkg/sqlite against a real database, and what needs
// testing HERE is the handler's DECISION LOGIC -- which refusals fire, in what
// order, and what each one reports.
//
// A fake is also the only way to reach the refusal paths at all. Nothing makes a
// real database fail on demand, and "the store is unreadable" is a state an
// operator will meet at 3am. The `err` field below exists for exactly that.

// fakeWizardStore implements WizardStore, and can fail on demand.
type fakeWizardStore struct {
	completed bool
	mode      collab.Mode

	// err fails every read. The "unreadable gate" state.
	err error

	// completeErr fails the write, separately from the read, because a store that
	// reads fine and cannot write is a different failure with a different
	// recovery.
	completeErr error

	completeCalls int
}

var errStoreUnreadable = errors.New("database is locked")

func (f *fakeWizardStore) WizardCompleted(context.Context) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.completed, nil
}

func (f *fakeWizardStore) Mode(context.Context) (collab.Mode, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.mode, nil
}

func (f *fakeWizardStore) CompleteWizard(_ context.Context, mode collab.Mode, _ *int64) error {
	f.completeCalls++
	if f.completeErr != nil {
		return f.completeErr
	}
	f.completed = true
	f.mode = mode
	return nil
}

const wizardTestKey = "0123456789abcdef0123456789abcdef"

func newTestWizard(store WizardStore, key string) *wizardHandler {
	var kf InstanceKeyFunc
	if key != "" {
		kf = func() []byte { return []byte(key) }
	}
	return newWizardHandler(store, kf)
}

type wizardBody struct {
	Completed   bool        `json:"completed"`
	Mode        collab.Mode `json:"mode"`
	RequiresTLS bool        `json:"requiresTLS"`
	Error       string      `json:"error"`
	Code        string      `json:"code"`
}

func decodeWizard(t *testing.T, rec *httptest.ResponseRecorder) wizardBody {
	t.Helper()
	var b wizardBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &b), "body: %s", rec.Body.String())
	return b
}

func postWizard(t *testing.T, h *wizardHandler, mode, key, scheme string) *httptest.ResponseRecorder {
	t.Helper()

	payload, err := json.Marshal(map[string]string{"mode": mode, "instanceKey": key})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, wizardEndpoint, bytes.NewReader(payload))
	if scheme == "https" {
		req.Header.Set("X-Forwarded-Proto", "https")
	}
	rec := httptest.NewRecorder()
	h.Decide(rec, req)
	return rec
}

// TestWizard_TheDecisionIsRecordedAndTheGateOpens: the whole point of the file.
func TestWizard_TheDecisionIsRecordedAndTheGateOpens(t *testing.T) {
	store := &fakeWizardStore{}
	h := newTestWizard(store, wizardTestKey)

	rec := postWizard(t, h, "private", wizardTestKey, "http")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	b := decodeWizard(t, rec)
	assert.True(t, b.Completed)
	assert.Equal(t, collab.ModePrivate, b.Mode)
	assert.Equal(t, 1, store.completeCalls, "the decision must be written exactly once")
}

// TestWizard_AWrongInstanceKeyIsRefused: the capability this endpoint holds.
func TestWizard_AWrongInstanceKeyIsRefused(t *testing.T) {
	store := &fakeWizardStore{}
	h := newTestWizard(store, wizardTestKey)

	rec := postWizard(t, h, "public", "not-the-key", "https")
	require.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, codeBadInstanceKey, decodeWizard(t, rec).Code,
		"a machine-readable code, so a client switches on a value and not an English string")
	assert.Zero(t, store.completeCalls, "a refused decision must not be written")
}

// TestWizard_NoConfiguredKeyCannotMakeAnInstancePublic: fail CLOSED.
//
// "There is no key, so there is nothing to check" is how an instance with no key
// ends up accepting an unauthenticated request to make itself public.
//
// # Three misconfigurations, because they fail in three different places
//
// "no key function" is caught by h.key == nil. "a function returning nil" and
// "a function returning an empty slice" are caught by len(want) == 0. The
// mutation harness reported "an unconfigured key matches everything" as SURVIVED
// until all three were here -- and the reason is worth recording, because the
// version before this had a map with two keys whose values were BOTH the empty
// string, so both subtests took the nil-function branch and the len(want) == 0
// guard was never executed by anything. A test table whose entries are all the
// same value tests one case and reports three.
func TestWizard_NoConfiguredKeyCannotMakeAnInstancePublic(t *testing.T) {
	cases := []struct {
		name string
		key  InstanceKeyFunc
	}{
		{"no key function at all", nil},
		{"a key function returning nil", func() []byte { return nil }},
		{"a key function returning an empty slice", func() []byte { return []byte{} }},
		{"a key function returning a short key", func() []byte { return []byte("short") }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeWizardStore{}
			h := newWizardHandler(store, tc.key)

			rec := postWizard(t, h, "public", wizardTestKey, "https")
			require.Equal(t, http.StatusForbidden, rec.Code,
				"no usable key must refuse every attempt, not admit them")
			assert.Zero(t, store.completeCalls)

			// And a request offering no key at all is refused too, rather than
			// being read as "no key needed".
			rec = postWizard(t, h, "public", "", "https")
			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.Zero(t, store.completeCalls)
		})
	}
}

// TestWizard_AKeyThatChangesIsReadAtUseTime: the reason InstanceKeyFunc is a
// function.
//
// A []byte captured at construction is replaced by a config reload, and the
// wizard would then authenticate against a key the operator no longer has --
// with no error anywhere, just a wizard that stopped working.
func TestWizard_AKeyThatChangesIsReadAtUseTime(t *testing.T) {
	store := &fakeWizardStore{}
	current := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	h := newWizardHandler(store, func() []byte { return current })

	rec := postWizard(t, h, "private", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "http")
	require.Equal(t, http.StatusOK, rec.Code)

	// Reload: the operator regenerates the key.
	store2 := &fakeWizardStore{}
	current = []byte(wizardTestKey)
	h2 := newWizardHandler(store2, func() []byte { return current })

	rec = postWizard(t, h2, "private", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "http")
	require.Equal(t, http.StatusForbidden, rec.Code,
		"the old key must stop working as soon as the config changes")
}

// TestWizard_AnInvalidModeIsRefusedWithTheOffendingValue: an operator pasting
// "Public" needs to be told what is wrong, not just that something is.
func TestWizard_AnInvalidModeIsRefusedWithTheOffendingValue(t *testing.T) {
	store := &fakeWizardStore{}
	h := newTestWizard(store, wizardTestKey)

	rec := postWizard(t, h, "Public", wizardTestKey, "https")
	require.Equal(t, http.StatusBadRequest, rec.Code)

	b := decodeWizard(t, rec)
	assert.Equal(t, codeInvalidMode, b.Code)
	assert.Contains(t, b.Error, "Public", "the refusal must name what was sent")
	assert.Contains(t, b.Error, "private", "and what would be accepted")
	assert.Zero(t, store.completeCalls)
}

// TestWizard_PublicOverPlainHTTPIsRefusedAndNotRecorded: the one that matters
// most for step 4.1.
//
// The refusal happens BEFORE the write. If it were recorded first, the instance
// would hold a public mode that cannot start, and the wizard could not be re-run
// because the flag is already set -- leaving the operator to edit the database by
// hand.
func TestWizard_PublicOverPlainHTTPIsRefusedAndNotRecorded(t *testing.T) {
	store := &fakeWizardStore{}
	h := newTestWizard(store, wizardTestKey)

	rec := postWizard(t, h, "public", wizardTestKey, "http")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, codeInsecureScheme, decodeWizard(t, rec).Code)
	assert.Zero(t, store.completeCalls,
		"a refused decision must not be written, or the operator is locked out of "+
			"the only endpoint that can set the mode")

	// And the same instance can still be decided properly.
	require.Equal(t, http.StatusOK, postWizard(t, h, "private", wizardTestKey, "http").Code)
}

// TestWizard_PublicOverHTTPSIsAccepted: the positive case, so the refusal above
// is a real gate and not a blanket denial of public mode.
func TestWizard_PublicOverHTTPSIsAccepted(t *testing.T) {
	store := &fakeWizardStore{}
	h := newTestWizard(store, wizardTestKey)

	rec := postWizard(t, h, "public", wizardTestKey, "https")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	b := decodeWizard(t, rec)
	assert.Equal(t, collab.ModePublic, b.Mode)
	assert.True(t, b.RequiresTLS, "the response tells the UI what the mode now demands")
}

// TestWizard_ContributeOverPlainHTTPIsAccepted: contribute is not a public
// instance, so TLS is not its problem. Getting this wrong would force every
// contribute instance behind a certificate it does not need.
func TestWizard_ContributeOverPlainHTTPIsAccepted(t *testing.T) {
	store := &fakeWizardStore{}
	h := newTestWizard(store, wizardTestKey)

	rec := postWizard(t, h, "contribute", wizardTestKey, "http")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.False(t, decodeWizard(t, rec).RequiresTLS)
}

// TestWizard_TheWizardIsOneShot: a second POST cannot re-decide a live instance.
//
// Otherwise anyone who reaches the port could flip a public instance to private,
// or a private one to public, with no session. Changing the mode afterwards is an
// authenticated operation, not this endpoint.
func TestWizard_TheWizardIsOneShot(t *testing.T) {
	store := &fakeWizardStore{}
	h := newTestWizard(store, wizardTestKey)
	require.Equal(t, http.StatusOK, postWizard(t, h, "private", wizardTestKey, "http").Code)

	rec := postWizard(t, h, "public", wizardTestKey, "https")
	require.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, codeAlreadyCompleted, decodeWizard(t, rec).Code)
	assert.Equal(t, 1, store.completeCalls, "the second attempt must not write")
}

// TestWizard_TheAlreadyCompletedCheckRunsBeforeTheKeyCheck: ordering.
//
// If the key were checked first, a caller with no key could learn whether the
// wizard has run from the STATUS CODE. The order is deliberate and this pins it.
func TestWizard_TheAlreadyCompletedCheckRunsBeforeTheKeyCheck(t *testing.T) {
	store := &fakeWizardStore{completed: true, mode: collab.ModePublic}
	h := newTestWizard(store, wizardTestKey)

	rec := postWizard(t, h, "private", "wrong-key-entirely", "https")
	require.Equal(t, http.StatusConflict, rec.Code,
		"the completed state is reported before the key is examined, so a bad key "+
			"cannot be used to probe whether a wizard has run")
}

// TestWizard_AnUnreadableGateIsRefusedNotPassed: the state an operator meets at
// 3am.
//
// Reading an error as "not completed" sends every request to the wizard; reading
// it as "completed" skips the gate. Both are wrong, and the second is the
// dangerous one.
func TestWizard_AnUnreadableGateIsRefusedNotPassed(t *testing.T) {
	store := &fakeWizardStore{err: errStoreUnreadable}
	h := newTestWizard(store, wizardTestKey)

	rec := httptest.NewRecorder()
	h.State(rec, httptest.NewRequest(http.MethodGet, wizardEndpoint, nil))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, codeInternal, decodeWizard(t, rec).Code)

	rec = httptest.NewRecorder()
	h.Mode(rec, httptest.NewRequest(http.MethodGet, modeEndpoint, nil))
	assert.Equal(t, http.StatusInternalServerError, rec.Code,
		"the login page must be told the mode is unknown, not that there is no 2FA")
}

// TestWizard_AGateThatCannotBeReadIsNotTreatedAsCompleted: the dangerous half of
// the same rule, tested with a store that distinguishes the two reads.
//
// # Why this needed its own store
//
// The mutation harness reported "an unreadable gate reads as completed" as
// SURVIVED even though the refusal above is tested. The mutation sets
// completed = true and falls through -- and on a store that fails EVERY read, the
// handler then calls Mode, which fails too, so it returns the same 500. The test
// passed with the guard removed.
//
// So the distinguishing store is one that fails the completed-read but ANSWERS
// Mode. That is a real state: a partially written row, or a store where one query
// is denied. Against it, "failed the read" and "decided, and the mode is private"
// produce different responses, and only one of them is correct.
func TestWizard_AGateThatCannotBeReadIsNotTreatedAsCompleted(t *testing.T) {
	store := &partialReadStore{mode: collab.ModePrivate}
	h := newTestWizard(store, wizardTestKey)

	rec := httptest.NewRecorder()
	h.State(rec, httptest.NewRequest(http.MethodGet, wizardEndpoint, nil))
	require.Equal(t, http.StatusInternalServerError, rec.Code,
		"a completed-read that fails must be an error, not a silent 'yes, decided'")
	assert.Equal(t, codeInternal, decodeWizard(t, rec).Code)

	// The mode endpoint has the same rule.
	rec = httptest.NewRecorder()
	h.Mode(rec, httptest.NewRequest(http.MethodGet, modeEndpoint, nil))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// partialReadStore fails WizardCompleted and answers Mode. The only combination
// that separates "the gate is unreadable" from "the instance is decided".
type partialReadStore struct {
	mode      collab.Mode
	readCalls int
	modeCalls int
}

func (p *partialReadStore) WizardCompleted(context.Context) (bool, error) {
	p.readCalls++
	return false, errStoreUnreadable
}

func (p *partialReadStore) Mode(context.Context) (collab.Mode, error) {
	p.modeCalls++
	return p.mode, nil
}

func (p *partialReadStore) CompleteWizard(context.Context, collab.Mode, *int64) error {
	return nil
}

// TestWizard_ADecisionThatCannotBeWrittenIsReported: a store that reads fine and
// cannot write. A 500 that says so beats a 200 that lies about completion.
func TestWizard_ADecisionThatCannotBeWrittenIsReported(t *testing.T) {
	store := &fakeWizardStore{completeErr: errStoreUnreadable}
	h := newTestWizard(store, wizardTestKey)

	rec := postWizard(t, h, "private", wizardTestKey, "http")
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, codeInternal, decodeWizard(t, rec).Code)
	assert.False(t, decodeWizard(t, rec).Completed, "a decision that failed to persist is not completed")
}

// TestWizard_ANilStoreIsRefusedNotPanicked: a build without the store. A panic
// here is a remote crash on an unauthenticated endpoint.
func TestWizard_ANilStoreIsRefusedNotPanicked(t *testing.T) {
	h := newWizardHandler(nil, func() []byte { return []byte(wizardTestKey) })

	rec := httptest.NewRecorder()
	h.State(rec, httptest.NewRequest(http.MethodGet, wizardEndpoint, nil))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	rec = postWizard(t, h, "private", wizardTestKey, "http")
	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	// Mode is the exception: the login page must still get an answer, because a
	// hard failure there would block a login on a build that has no 2FA anyway.
	rec = httptest.NewRecorder()
	h.Mode(rec, httptest.NewRequest(http.MethodGet, modeEndpoint, nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.False(t, decodeWizard(t, rec).Completed)
}

// TestWizard_TheStateEndpointReportsTheDecision: what the client polls.
func TestWizard_TheStateEndpointReportsTheDecision(t *testing.T) {
	store := &fakeWizardStore{}
	h := newTestWizard(store, wizardTestKey)

	rec := httptest.NewRecorder()
	h.State(rec, httptest.NewRequest(http.MethodGet, wizardEndpoint, nil))
	require.Equal(t, http.StatusOK, rec.Code)

	b := decodeWizard(t, rec)
	assert.False(t, b.Completed, "an undecided instance has not completed the wizard")
	assert.Empty(t, b.Mode, "and has no mode: reporting the schema default here would "+
		"tell a client to skip a second-factor prompt on an instance that may be "+
		"about to require one")

	require.Equal(t, http.StatusOK, postWizard(t, h, "public", wizardTestKey, "https").Code)

	rec = httptest.NewRecorder()
	h.State(rec, httptest.NewRequest(http.MethodGet, wizardEndpoint, nil))
	b = decodeWizard(t, rec)
	assert.True(t, b.Completed)
	assert.Equal(t, collab.ModePublic, b.Mode)
	assert.True(t, b.RequiresTLS, "the UI can warn BEFORE the decision, rather than "+
		"after the server refuses to start")
}

// TestWizard_ABodyOverTheLimitIsRefusedForItsSize: the limit, tested as a limit.
//
// The mutation harness caught that my first version of this test did not test the
// cap. It sent 8 KiB of garbage in the `mode` field and asserted "not 200" -- and
// the handler refused it for being an INVALID MODE, so the assertion passed with
// or without MaxBytesReader. A test that passes with the guard removed is not a
// test of the guard.
//
// So this asserts the refusal reason, and uses a body that is over the limit and
// otherwise VALID: a huge but well-formed instanceKey. Nothing about it is
// invalid, so the only thing that can refuse it is the size.
func TestWizard_ABodyOverTheLimitIsRefusedForItsSize(t *testing.T) {
	store := &fakeWizardStore{}
	h := newTestWizard(store, wizardTestKey)

	// A body that is valid JSON, has a valid mode, and has a correct-length key --
	// padded past the limit with whitespace, which the decoder skips and the
	// limit still counts. This isolates SIZE as the only reason to refuse.
	padded := make([]byte, wizardBodyLimit*4)
	for i := range padded {
		padded[i] = ' '
	}
	payload, err := json.Marshal(map[string]string{"mode": "private", "instanceKey": wizardTestKey})
	require.NoError(t, err)
	body := append(append([]byte{}, payload...), padded...)

	req := httptest.NewRequest(http.MethodPost, wizardEndpoint, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.Decide(rec, req)

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code,
		"an oversized body must be refused for its SIZE, not for anything else")
	assert.Equal(t, codeBodyTooLarge, decodeWizard(t, rec).Code)
	assert.Zero(t, store.completeCalls, "and nothing may be written from a body that was never fully read")
}

// TestWizard_ABodyJustUnderTheLimitIsAccepted: so the test above is measuring the
// boundary and not "big bodies are always rejected".
func TestWizard_ABodyJustUnderTheLimitIsAccepted(t *testing.T) {
	store := &fakeWizardStore{}
	h := newTestWizard(store, wizardTestKey)

	payload, err := json.Marshal(map[string]string{"mode": "private", "instanceKey": wizardTestKey})
	require.NoError(t, err)
	// Sized FROM the payload, not as a fraction of the limit: half the limit plus
	// the JSON is over the limit, which is how the first version of this test
	// asserted 200 for a body the handler correctly refused.
	// Comfortably under rather than one byte under: MaxBytesReader reads one byte
	// past its limit to detect overflow, so a body of exactly limit-1 leaves the
	// probe reading that sentinel. A boundary test has to know where the
	// boundary actually is, and it is not where it looks.
	padded := make([]byte, wizardBodyLimit-len(payload)-64)
	for i := range padded {
		padded[i] = ' '
	}
	body := append(append([]byte{}, payload...), padded...)
	require.Less(t, len(body), wizardBodyLimit, "this body is meant to be UNDER the limit")

	req := httptest.NewRequest(http.MethodPost, wizardEndpoint, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.Decide(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
}

// TestWizard_AMalformedBodyIsRefused: and does not panic or half-apply.
func TestWizard_AMalformedBodyIsRefused(t *testing.T) {
	store := &fakeWizardStore{}
	h := newTestWizard(store, wizardTestKey)

	req := httptest.NewRequest(http.MethodPost, wizardEndpoint, bytes.NewReader([]byte("{not json")))
	rec := httptest.NewRecorder()
	h.Decide(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, codeBadRequest, decodeWizard(t, rec).Code)
	assert.Zero(t, store.completeCalls, "a body that could not be parsed must not be half-applied")
}

// TestWizard_TheModeEndpointDisclosesNothing: it exists for the login page, and
// the login page runs before anyone is authenticated.
func TestWizard_TheModeEndpointDisclosesNothing(t *testing.T) {
	store := &fakeWizardStore{}
	h := newTestWizard(store, wizardTestKey)

	rec := httptest.NewRecorder()
	h.Mode(rec, httptest.NewRequest(http.MethodGet, modeEndpoint, nil))
	require.Equal(t, http.StatusOK, rec.Code)

	b := decodeWizard(t, rec)
	assert.False(t, b.Completed, "an undecided instance reports no mode")
	assert.Empty(t, b.Error, "no error, because there is nothing wrong yet")

	require.Equal(t, http.StatusOK, postWizard(t, h, "contribute", wizardTestKey, "http").Code)

	rec = httptest.NewRecorder()
	h.Mode(rec, httptest.NewRequest(http.MethodGet, modeEndpoint, nil))
	b = decodeWizard(t, rec)
	assert.True(t, b.Completed)
	assert.Equal(t, collab.ModeContribute, b.Mode)
	assert.False(t, b.RequiresTLS)
}

// TestWizard_EveryRefusalCarriesACode: a client switches on Code. A refusal with
// a message and no code is a refusal half the clients cannot handle.
func TestWizard_EveryRefusalCarriesACode(t *testing.T) {
	cases := []struct {
		name    string
		store   *fakeWizardStore
		mode    string
		key     string
		scheme  string
		rawBody string
	}{
		{"bad key", &fakeWizardStore{}, "private", "wrong", "http", ""},
		{"invalid mode", &fakeWizardStore{}, "nonsense", wizardTestKey, "https", ""},
		{"insecure", &fakeWizardStore{}, "public", wizardTestKey, "http", ""},
		{"already done", &fakeWizardStore{completed: true}, "private", wizardTestKey, "http", ""},
		{"unreadable store", &fakeWizardStore{err: errStoreUnreadable}, "private", wizardTestKey, "http", ""},
		{"unwritable store", &fakeWizardStore{completeErr: errStoreUnreadable}, "private", wizardTestKey, "http", ""},
		{"malformed body", &fakeWizardStore{}, "", "", "", "{not json"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestWizard(tc.store, wizardTestKey)

			var rec *httptest.ResponseRecorder
			if tc.rawBody != "" {
				req := httptest.NewRequest(http.MethodPost, wizardEndpoint, bytes.NewReader([]byte(tc.rawBody)))
				rec = httptest.NewRecorder()
				h.Decide(rec, req)
			} else {
				rec = postWizard(t, h, tc.mode, tc.key, tc.scheme)
			}

			require.NotEqual(t, http.StatusOK, rec.Code, "this case is a refusal")
			b := decodeWizard(t, rec)
			assert.NotEmpty(t, b.Code, "a refusal must carry a machine-readable code")
			assert.NotEmpty(t, b.Error, "and a message, because a code alone does not tell an operator what to do")
		})
	}
}
