package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/stashapp/stash/internal/collab"
)

// The first-run wizard. M4 step 4.4.
//
// # Why this file exists at all
//
// `collab.RequireWizard` refuses every request until
// `instance_settings.wizard_completed` is set. That refusal is deliberate --
// an instance that has never been told whether it is private or public must not
// start serving, because the default that is safe on a laptop is the one that
// publishes a library to the internet.
//
// But a gate with no key is a brick, and this was one: the store existed, the
// gate existed, and NOTHING could set the flag, so a fresh instance refused
// everything with no way forward. That is the same "referenced != used" shape as
// the stores that were implemented and never constructed -- a control that reads
// as a feature and cannot be operated.
//
// # Why a struct and not three Server methods
//
// The store lives on the Manager, so methods on Server reach through
// s.manager.InstanceModeStore and the key through s.manager.Config. That means
// every test must construct an entire Manager to check a JSON response, and a
// test that is awkward to write is a test that does not get written.
//
// So the wizard holds its two dependencies directly and Server constructs it once
// at route-registration time. A dependency a handler can only reach through a
// four-hop chain is a dependency nobody will test.
//
// # The three routes, and why two are unauthenticated
//
//   - GET  /stashforge/wizard   is the state. Unauthenticated, because the
//     operator has not logged in yet -- that is the situation the wizard exists
//     to resolve.
//   - POST /stashforge/wizard   decides. This is the ONLY unauthenticated POST in
//     the application, and it is authenticated by possession of the instance key
//     rather than by a session.
//   - GET  /stashforge/mode     reads the mode once decided. Also
//     unauthenticated, because it exposes nothing: a mode is a policy flag, not
//     a secret, and the login page needs to know whether to ask for a second
//     factor.
//
// The POST is guarded by the instance key because an unauthenticated "make this
// instance public" is a serious capability to hand to anyone who can reach the
// port. The key is the same one that encrypts 2FA secrets, so an operator manages
// exactly one secret, and the wizard is the first thing asked for it by.

const (
	wizardEndpoint = "/stashforge/wizard"
	modeEndpoint   = "/stashforge/mode"

	// wizardBodyLimit caps the whole request body. This request has two short
	// fields; 4 KiB is generous, and an unbounded decoder on an UNAUTHENTICATED
	// endpoint is a way to make it allocate whatever the client sends.
	wizardBodyLimit = 4 << 10
)

// InstanceKeyFunc returns the instance key, read at use time rather than
// captured. A []byte captured at construction would be replaced by a later config
// reload, and the wizard would then authenticate against a key the operator no
// longer has.
type InstanceKeyFunc func() []byte

// WizardStore is what the wizard needs from the instance-mode store.
//
// The smallest interface that covers the wizard's needs, declared HERE rather
// than in collab, because the wizard is the only consumer and a wider one would
// be an invitation to put media-serving policy in the same place as a
// first-run form.
//
// Deliberately NOT the concrete sqlite store: its methods require a transaction
// on the context, and the only harness providing one lives in another package's
// tests. More usefully, an interface lets a test supply a store that FAILS on
// demand, which is how the refusal paths get covered at all.
type WizardStore interface {
	// WizardCompleted reports whether the first-run decision has been made.
	WizardCompleted(ctx context.Context) (bool, error)
	// Mode returns the recorded mode.
	Mode(ctx context.Context) (collab.Mode, error)
	// CompleteWizard records the decision and marks the wizard done, together.
	CompleteWizard(ctx context.Context, mode collab.Mode, decidedBy *int64) error
}

// wizardHandler serves the first-run wizard.
type wizardHandler struct {
	store WizardStore
	key   InstanceKeyFunc
}

func newWizardHandler(store WizardStore, key InstanceKeyFunc) *wizardHandler {
	return &wizardHandler{store: store, key: key}
}

// wizardRequest is the POST body.
//
// Mode is a string rather than collab.Mode so a malformed value produces
// ErrModeInvalid from the domain rather than a JSON parse error: the operator gets
// "not a valid instance mode" instead of a syntax message, which is the one that
// tells them what to fix.
type wizardRequest struct {
	Mode        string `json:"mode"`
	InstanceKey string `json:"instanceKey"`
}

// wizardResponse reports the decision and any refusal in operator terms.
type wizardResponse struct {
	// Completed is true once the wizard has been decided. A client polling this
	// uses it to know whether to show the wizard or the app.
	Completed bool `json:"completed"`

	// Mode is the instance mode. Empty before the wizard is decided.
	Mode collab.Mode `json:"mode,omitempty"`

	// RequiresTLS says the mode demands HTTPS, so the UI can tell the operator
	// BEFORE they commit rather than after the server refuses to start.
	RequiresTLS bool `json:"requiresTLS"`

	// Error is the refusal in operator terms; Code is the same thing for a
	// program. A client switches on Code, never by matching an English string.
	Error string `json:"error,omitempty"`
	Code  string `json:"code,omitempty"`
}

// Refusal codes. Named constants because a client switches on these, and a bare
// string literal at each use site is how two spellings of one code end up in a
// codebase with half the clients handling neither.
const (
	codeAlreadyCompleted = "wizard_already_completed"
	codeBadInstanceKey   = "bad_instance_key"
	codeInvalidMode      = "invalid_mode"
	codeInsecureScheme   = "insecure_scheme"
	codeBadRequest       = "bad_request"
	codeInternal         = "internal_error"
)

// State answers "is the wizard done, and what mode are we in".
func (h *wizardHandler) State(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.fail(w, http.StatusInternalServerError, codeInternal,
			"this build has no instance-mode store")
		return
	}

	ctx := r.Context()
	completed, err := h.store.WizardCompleted(ctx)
	if err != nil {
		// An unreadable gate is a refusal, not a pass. Reading it as "not
		// completed" would send every request to the wizard; reading it as
		// "completed" would skip the gate entirely.
		slog.Error("stashforge: reading the wizard state", "error", err)
		h.fail(w, http.StatusInternalServerError, codeInternal, "could not read the instance settings")
		return
	}

	resp := wizardResponse{Completed: completed}
	if completed {
		mode, mErr := h.store.Mode(ctx)
		if mErr != nil {
			slog.Error("stashforge: reading the instance mode", "error", mErr)
			h.fail(w, http.StatusInternalServerError, codeInternal, "could not read the instance mode")
			return
		}
		resp.Mode = mode
		resp.RequiresTLS = mode.RequiresTLS()
	}
	h.write(w, resp)
}

// Decide records the mode and marks the wizard complete.
func (h *wizardHandler) Decide(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.fail(w, http.StatusInternalServerError, codeInternal,
			"this build has no instance-mode store")
		return
	}

	var req wizardRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, wizardBodyLimit)).Decode(&req); err != nil {
		h.fail(w, http.StatusBadRequest, codeBadRequest, "malformed request body")
		return
	}

	ctx := r.Context()

	// Already decided? Refuse. A second POST would let anyone who reaches the port
	// flip a live public instance back to private, or a private one to public, with
	// no session. The wizard is a ONE-TIME decision; changing it afterwards is an
	// authenticated operation, not this endpoint.
	//
	// Checked BEFORE the key so the refusal cannot be used to probe whether a
	// wizard has run, and so the message names the situation rather than the
	// secret.
	completed, err := h.store.WizardCompleted(ctx)
	if err != nil {
		slog.Error("stashforge: reading the wizard state", "error", err)
		h.fail(w, http.StatusInternalServerError, codeInternal, "could not read the instance settings")
		return
	}
	if completed {
		h.fail(w, http.StatusConflict, codeAlreadyCompleted,
			"the first-run wizard has already been completed; change the mode from settings instead")
		return
	}

	if !h.keyMatches(req.InstanceKey) {
		h.fail(w, http.StatusForbidden, codeBadInstanceKey, "the instance key is incorrect")
		return
	}

	mode := collab.Mode(req.Mode)
	if !mode.Valid() {
		h.fail(w, http.StatusBadRequest, codeInvalidMode,
			fmt.Sprintf("%q is not a valid instance mode; use private, contribute or public", req.Mode))
		return
	}

	// The TLS check happens BEFORE the decision is recorded. Refusing afterwards
	// would leave an instance holding a public mode that cannot start, with the
	// flag already set -- so the wizard could not be re-run, and the operator would
	// have to edit the database by hand.
	scheme := collab.RequestScheme(r)
	if err := collab.ModeErrors(mode, scheme); err != nil {
		h.fail(w, http.StatusBadRequest, codeInsecureScheme, err.Error())
		return
	}

	if err := h.store.CompleteWizard(ctx, mode, nil); err != nil {
		slog.Error("stashforge: recording the wizard decision", "error", err)
		h.fail(w, http.StatusInternalServerError, codeInternal, "could not record the instance mode")
		return
	}

	slog.Info("stashforge: first-run wizard completed", "mode", mode.String(), "scheme", scheme)
	h.write(w, wizardResponse{Completed: true, Mode: mode, RequiresTLS: mode.RequiresTLS()})
}

// Mode answers "what mode is this instance in", for the login page.
//
// Deliberately unauthenticated and deliberately boring: a policy flag and nothing
// else. It does NOT report whether the wizard is done, because a caller that needs
// that has /wizard -- and reporting both here would make this a second, less
// careful version of the first.
func (h *wizardHandler) Mode(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		h.write(w, wizardResponse{})
		return
	}

	ctx := r.Context()
	completed, err := h.store.WizardCompleted(ctx)
	if err != nil {
		slog.Error("stashforge: reading the wizard state", "error", err)
		h.fail(w, http.StatusInternalServerError, codeInternal, "could not read the instance settings")
		return
	}
	if !completed {
		// An undecided instance has no mode. Reporting the schema default here
		// would tell the login page to skip a second-factor prompt on an instance
		// that may be about to require one.
		h.write(w, wizardResponse{Completed: false})
		return
	}

	mode, err := h.store.Mode(ctx)
	if err != nil {
		slog.Error("stashforge: reading the instance mode", "error", err)
		h.fail(w, http.StatusInternalServerError, codeInternal, "could not read the instance mode")
		return
	}
	h.write(w, wizardResponse{Completed: true, Mode: mode, RequiresTLS: mode.RequiresTLS()})
}

// keyMatches compares the supplied key in constant time.
//
// crypto/subtle rather than ==: the key is compared against attacker-supplied
// input on an unauthenticated endpoint, and a byte-wise compare leaks the matching
// prefix length through timing.
//
// The LENGTH is compared first and separately, which is not a secret -- it is
// fixed by the config -- and it keeps ConstantTimeCompare from being handed two
// slices of different sizes.
//
// A nil or empty configured key matches NOTHING. Failing closed is the whole
// point: "there is no key, so there is nothing to check" is exactly how an
// instance with no key ends up accepting an unauthenticated request to make
// itself public.
func (h *wizardHandler) keyMatches(given string) bool {
	if h.key == nil {
		return false
	}
	want := h.key()
	if len(want) == 0 {
		return false
	}
	if len(given) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(given), want) == 1
}

func (h *wizardHandler) write(w http.ResponseWriter, v interface{}) {
	// The content type is set BEFORE the write: a 200 with no content type is read
	// by some clients as a download.
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Nothing useful to do -- the status line is already sent. Logging beats
		// silently returning a truncated body, which a client reads as valid JSON
		// with fields missing.
		slog.Error("stashforge: writing a wizard response", "error", err)
	}
}

func (h *wizardHandler) fail(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// A failed write on an error response is not worth a second error: the client
	// is already gone or already broken.
	_ = json.NewEncoder(w).Encode(wizardResponse{Error: message, Code: code})
}
