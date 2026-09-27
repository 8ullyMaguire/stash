package collab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Instance mode. M4 step 4.1, spec §2.
//
// THREE MODES, AND THE ONE THAT MATTERS IS WHAT THEY REFUSE.
//
//	private     nothing leaves the host
//	contribute  metadata shared, media not served
//	public      metadata shared, media per access grant
//
// In NO mode does media leave the host unprompted. "Public" means the metadata
// is public; the files are still served only to users holding a grant (§6.4).
// That distinction is the whole reason the project can serve a community without
// becoming a public file host, and it is the direct answer to the security
// objections in upstream #2792.
//
// So this file is not a feature switch. Every method here is a REFUSAL: "may I",
// "must I refuse", "what does this mode forbid". A mode that is implemented as a
// capability list is a mode where forgetting an entry silently allows something.

// Mode is the instance's posture.
type Mode string

const (
	ModePrivate    Mode = "private"
	ModeContribute Mode = "contribute"
	ModePublic     Mode = "public"
)

// Valid reports whether the mode is one the schema permits.
func (m Mode) Valid() bool {
	switch m {
	case ModePrivate, ModeContribute, ModePublic:
		return true
	}
	return false
}

// String satisfies fmt.Stringer so a mode in an error message reads as the
// operator typed it.
func (m Mode) String() string { return string(m) }

// SharesMetadata reports whether this mode publishes metadata to the commons.
//
// Note that this is about the MODE, not about any individual library or user:
// `contribute` shares by default with a per-library opt-out, and a user's own
// consent still governs whether their library is actually exported. The mode
// says what is permitted; consent says what was agreed.
func (m Mode) SharesMetadata() bool {
	return m == ModeContribute || m == ModePublic
}

// ServesMedia reports whether this mode serves media to users at all.
//
// Private: never. Contribute: never -- §6.4's default, "the instance is
// contribute, metadata flows, media does not". Public: only to a user holding an
// access grant, which this method cannot check and MustServeMediaTo must.
func (m Mode) ServesMedia() bool { return m == ModePublic }

// AcceptsAnonymousProposals reports whether a user who is not the owner may post
// to the commons. §2's table: private is owner-only, contribute is owner-plus-
// publishes, public is anyone under §5 governance.
func (m Mode) AcceptsAnonymousProposals() bool { return m == ModePublic }

// RequiresTLS reports whether the mode may be served over plain HTTP.
//
// PUBLIC ONLY, and that is the point of step 4.1. A public instance hands
// session cookies to anyone on the internet; over plain HTTP those cookies cross
// the network in the clear, and a public instance is precisely the one whose
// users cannot be told to use a VPN. So a public mode over HTTP REFUSES TO
// START, rather than starting and logging a warning nobody reads.
//
// Contribute and private are single-user or trusted-network instances by
// definition, so they are not required to terminate TLS themselves -- typically
// they are behind a reverse proxy that does it, or on a LAN.
func (m Mode) RequiresTLS() bool { return m == ModePublic }

// ErrInsecurePublicMode is returned when a public instance would be served over
// plain HTTP. A named sentinel because the fix is a configuration change and the
// operator needs to be told WHICH one.
var ErrInsecurePublicMode = errors.New("public mode requires HTTPS")

// ErrModeInvalid is returned for a mode string the schema does not permit.
var ErrModeInvalid = errors.New("not a valid instance mode")

// ModeErrors explains, in operator terms, what to change. Written as a function
// rather than a constant because the message names the actual observed scheme,
// and a message that says "configure TLS" without saying what is currently
// configured is half an answer.
func ModeErrors(m Mode, scheme string) error {
	if !m.Valid() {
		return fmt.Errorf("%w: %q (want %q, %q or %q)", ErrModeInvalid, m, ModePrivate, ModeContribute, ModePublic)
	}
	if !m.RequiresTLS() {
		return nil
	}
	if isSecureScheme(scheme) {
		return nil
	}
	return fmt.Errorf("%w: the instance is %q but requests arrive as %q. "+
		"Either terminate TLS in front of stash (set X-Forwarded-Proto: https, "+
		"which this instance already honours), or set the mode to %q if this "+
		"instance is not meant to be public",
		ErrInsecurePublicMode, m, scheme, ModeContribute)
}

// isSecureScheme is deliberately narrow: only https counts.
//
// An operator behind a proxy that sets X-Forwarded-Proto to something else is
// running plain HTTP, and accepting a wider set here would let a typo'd or
// attacker-controlled header talk the instance into believing it is secure.
func isSecureScheme(scheme string) bool {
	return strings.EqualFold(strings.TrimSpace(scheme), "https")
}

// RequestScheme reports the scheme a request arrived over, reusing the
// precedence the server already applies in BaseURLMiddleware.
//
// Kept in one function because the mode check and the base-URL construction must
// agree: if they disagree, the instance refuses to start while believing it is
// behind TLS, and the operator's only symptom is that it will not boot.
func RequestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		// Only the first entry: a client can send "https, http" and the
		// left-most is the one the edge set. Taking the whole header would let
		// a request claim https by appending it.
		if i := strings.IndexByte(proto, ','); i >= 0 {
			proto = proto[:i]
		}
		return strings.ToLower(strings.TrimSpace(proto))
	}
	if r.URL != nil && r.URL.Scheme != "" {
		return strings.ToLower(r.URL.Scheme)
	}
	return "http"
}

// MustServeMediaTo decides whether a media request may proceed.
//
// The signature takes the grant answer as a PARAMETER rather than looking it up,
// so the mode's rule and the grant's answer are separate inputs and neither can
// quietly stand in for the other. Returns a 404-shaped error in every refusal
// case, because §6.4 requires it: an ungranted user must not learn that the
// file exists.
func (m Mode) MustServeMediaTo(hasGrant bool) error {
	if !m.Valid() {
		return fmt.Errorf("%w: %q", ErrModeInvalid, m)
	}
	if !m.ServesMedia() {
		// Private and contribute serve no media to anyone. A grant does not
		// help: the mode is the outer boundary and the grant is the inner one.
		return ErrMediaNotServed
	}
	if !hasGrant {
		return ErrMediaNotServed
	}
	return nil
}

// ErrMediaNotServed is returned for every media refusal, deliberately
// undifferentiated: "this mode serves no media" and "you have no grant" must
// look identical from outside, or the second leaks the existence of the file
// through the first.
var ErrMediaNotServed = errors.New("not found")

// IsMediaNotFound reports whether an error is a media refusal, so a handler can
// map it to 404 without string matching.
func IsMediaNotFound(err error) bool { return err == ErrMediaNotServed }

// ModeEnforcer applies the mode's startup rules.
//
// It is a type rather than a function taking config so that the mode, the
// resolver that reports problems, and the failure to start all come from one
// place -- three functions each re-deriving "is this mode safe" is how a startup
// check and a request check come to disagree.
type ModeEnforcer struct {
	Mode Mode
	// Scheme is how the instance believes it is being reached. "https" when
	// behind a TLS-terminating proxy.
	Scheme string
}

// Check returns the startup error, if any.
func (e *ModeEnforcer) Check() error { return ModeErrors(e.Mode, e.Scheme) }

// RequireStartup returns an error the caller should refuse to start on.
//
// Separate from Check because "check" is what a test or a dry-run calls, and
// "refuse to start" is what main() calls. Collapsing them means a caller that
// wants to know can accidentally proceed anyway.
func (e *ModeEnforcer) RequireStartup() error {
	if err := e.Check(); err != nil {
		return fmt.Errorf("refusing to start: %w", err)
	}
	return nil
}

// ModeStore reads the instance's posture.
//
// Declared here rather than left implicit so the store adapter has something to
// satisfy, and so the read surface is visible: a resolver that needs the mode
// takes this, and cannot accidentally also reach a writer.
type ModeStore interface {
	// Mode returns the posture, or ModePrivate for an unconfigured instance.
	Mode(ctx context.Context) (Mode, error)
	// WizardCompleted reports whether the blocking first-run wizard has been
	// answered (step 4.4). Separate from Mode because "private because somebody
	// chose private" and "private because nothing has been configured yet" are
	// different states, and only the first means the wizard ran.
	WizardCompleted(ctx context.Context) (bool, error)
}

// CheckStartup is the step 4.1 rule, and the wizard's veto over it.
//
// The wizard gate is the part worth stating: a public mode is refused until the
// wizard has been completed, even if the row already says public. A mode set by
// a migration, a restored backup, or a hand-edited row is not a choice, and
// "somebody chose to be public" is the only thing that should permit it. This is
// what makes step 4.4 load-bearing rather than a nice screen.
func CheckStartup(mode Mode, scheme string, wizardCompleted bool) error {
	if err := ModeErrors(mode, scheme); err != nil {
		return err
	}
	if mode == ModePublic && !wizardCompleted {
		return fmt.Errorf("%w: the instance is configured as %q but the first-run wizard has not been completed. "+
			"A public posture must be chosen deliberately -- complete the wizard, or set the mode to %q",
			ErrModeNotChosen, mode, ModeContribute)
	}
	return nil
}

// ErrModeNotChosen is returned when a public posture is configured but nobody
// chose it.
var ErrModeNotChosen = errors.New("public mode was not chosen by the operator")

// ModeFromContext reads the mode an instance is running in.
type ModeKey struct{}

// WithMode attaches a mode to a context. For request-scoped use where a
// resolver must know the posture without reaching for a global.
func WithMode(ctx context.Context, m Mode) context.Context {
	return context.WithValue(ctx, ModeKey{}, m)
}

// ModeFrom returns the mode on the context, or ModePrivate if absent.
//
// DEFAULTS TO THE MOST RESTRICTIVE MODE, not to the most permissive. An absent
// mode is a configuration gap, and a gap must not resolve to "public" -- the
// whole reason this project can be exposed safely is that it fails closed.
func ModeFrom(ctx context.Context) Mode {
	if m, ok := ctx.Value(ModeKey{}).(Mode); ok && m.Valid() {
		return m
	}
	return ModePrivate
}
