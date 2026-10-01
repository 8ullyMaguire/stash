package api

import (
	"context"
	"net/http"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/txn"
)

// The media access gate. M4 step 4.3, spec §6.4.
//
// # WHAT THIS CLOSES
//
// Until this file, `LibraryAccessStore` granted, revoked and authorised, the
// 404-vs-403 distinction was tested, and `collab.AccessDecision` applied the
// rule correctly -- and NONE of it was reachable, because a serving path had no
// library to ask about (that is migration 105) and no user to ask on behalf of
// (that is withRequestUserID). A gate that exists and is never called is the
// "referenced != used" shape this project has now hit three times, so the
// WIRING is the substance of this step and the rule was the easy part.
//
// # WHERE IT GOES, AND WHY THERE
//
// Into the per-target `*Ctx` middlewares -- imageRoutes.ImageCtx,
// sceneRoutes.SceneCtx, and the gallery, performer, studio and tag equivalents
// -- rather than into each serving handler.
//
// The reason is coverage arithmetic. There are about twenty media routes: the
// scene stream plus its HLS manifest and segments, its DASH manifest and
// segments, the screenshot, the preview, the webp, the vtt thumbs and sprite,
// the funscript, the interactive csv and heatmap, the captions, the scene-marker
// stream, the images, the thumbnails, the previews. A handler-level gate is
// twenty edits, and the twenty-first is the one somebody forgets -- and a
// missing gate call is a silent leak, not a crash.
//
// The `*Ctx` middlewares are already on every one of those routes and already
// hold the loaded row. So one call per target type covers all of them, and a
// route added later without a gate is a route with no `*Ctx` middleware, which
// is visible in review as a missing line in a route block.
//
// # WHY IT RUNS BEFORE ANY FILE IS OPENED
//
// It runs inside the `*Ctx` middleware, before the handler, so no path is
// opened, no ffmpeg is spawned and no bytes are read for a request that is
// going to be refused. That is not tidier than checking in the handler, it is a
// security property: an ffmpeg transcode started for a request that is then
// refused is a process an ungranted user can start at will, and a 404 does not
// close a process that is already running.
//
// # THE 404, AND WHY IT IS NOT A 403
//
// Every refusal is `http.NotFound`, and a store failure is a 500. That split is
// the design: an ungranted user can already see the scene in the shared
// metadata, so a 403 would confirm that a file exists, which is the disclosure
// §6.4 forbids. A 500 is deliberately distinguishable -- it means the instance
// is broken rather than the user refused, and an operator has to be able to
// tell those apart at three in the morning.

// mediaGate is the narrow interface the gate needs. An interface rather than the
// concrete sqlite store so the refusals can be tested against a store that
// fails on demand -- the case that decides whether this fails open, which a
// nil-store and a real-store pair cannot express.
type mediaGate interface {
	Mode(ctx context.Context) (collab.Mode, error)
	LibraryOfTarget(ctx context.Context, targetType string, targetID int64) (int64, error)
	LibraryOwner(ctx context.Context, libraryID int64) (int64, error)
	DefaultLibraryID(ctx context.Context) (int64, error)
	HasAccess(ctx context.Context, userID, libraryID int64) (bool, error)
}

// allowMedia reports whether this request may have the media behind
// (targetType, targetID), and writes the refusal itself when it may not.
//
// It returns TRUE when the caller should continue and the response is
// untouched; FALSE when the response has already been written and the caller
// must return immediately. That contract is why it takes the writer: a version
// that returned only a bool left every call site to write the 404, and twenty
// call sites is twenty chances to write a 403.
//
// The two dependencies come from the Manager and are checked against their
// CONCRETE types, for the reason in HANDOFF.md #9: a nil
// *sqlite.MediaScopeStore assigned to an interface is a NON-nil interface, so a
// `gate == nil` test further down would be false and the gate would run against
// a nil receiver.
func allowMedia(w http.ResponseWriter, r *http.Request, targetType string, targetID int64) bool {
	// MaybeGetInstance, NOT GetInstance. GetInstance PANICS when there is no
	// instance, and a panic is not a refusal -- it is recovered into a 500, and
	// in a goroutine it takes the process down. The nil check below is
	// therefore the ONLY thing standing between an uninitialised process and a
	// crash, which is exactly the kind of guard that gets deleted as
	// "unreachable". See manager.MaybeGetInstance.
	mgr := manager.MaybeGetInstance()
	if mgr == nil || mgr.MediaScopeStore == nil {
		// No gate on this build: REFUSE.
		//
		// The tempting alternative is to let the request through when there
		// is no gate, on the grounds that a plain Stash install always served
		// files. That is a fail-open on a security control whose availability
		// would then be a configuration question. No gate means no media.
		logger.Errorf("stashforge: no media access store is configured; refusing %s %d",
			targetType, targetID)
		http.NotFound(w, r)
		return false
	}
	gate := mediaGate(mgr.MediaScopeStore)

	if mgr.Repository.TxnManager == nil {
		logger.Errorf("stashforge: no transaction manager for the media gate; refusing %s %d",
			targetType, targetID)
		http.NotFound(w, r)
		return false
	}
	txns := mgr.Repository.TxnManager

	userID := requestUserID(r.Context())

	var allowed bool
	var refusalLibrary int64
	var hasGrant bool
	var isOwner bool

	txnErr := txn.WithReadTxn(r.Context(), txns, func(ctx context.Context) error {
		mode, err := gate.Mode(ctx)
		if err != nil {
			return err
		}

		// A mode that serves no media refuses WITHOUT any further lookup.
		// Not only an optimisation: it means an instance in `contribute`
		// never depends on the grant tables being correct, so a broken grant
		// table cannot leak media out of an instance that was never meant to
		// serve any in the first place.
		if !mode.ServesMedia() {
			return nil
		}

		d, err := collab.ResolveScope(ctx, gate, mode, userID, targetType, targetID)
		if err != nil {
			return err
		}
		allowed = d.Allowed()
		refusalLibrary, hasGrant, isOwner = d.LibraryID, d.HasGrant, d.IsOwner
		return nil
	})

	if txnErr != nil {
		// The store could not answer, so the answer is unknown, and unknown
		// is a refusal -- but a LOUD one. This is the one path that
		// deliberately does not answer 404, because a database outage
		// reported as "not found" sends an operator hunting a phantom
		// attack instead of a broken disk.
		logger.Errorf("stashforge: media access check failed for %s %d: %v",
			targetType, targetID, txnErr)
		http.Error(w, http.StatusText(http.StatusInternalServerError),
			http.StatusInternalServerError)
		return false
	}

	if !allowed {
		// Logged with the library and the reason, so an operator staring at
		// a 404 can find out WHICH rule fired. The response still says
		// nothing -- that asymmetry is the design, not an oversight.
		logger.Debugf("stashforge: refused media for %s %d to user %d "+
			"(library %d, owner %v, grant %v)",
			targetType, targetID, userID, refusalLibrary, isOwner, hasGrant)
		writeMediaRefusal(w, r)
		return false
	}

	return true
}

// writeMediaRefusal is §6.4's single refusal response.
//
// It is a named function with one caller, and the reason is TESTABILITY rather
// than tidiness: the 404-not-403 property is one of the two things §6.4 exists
// for, and inline in allowMedia it could not be tested at all -- reaching that
// line needs a configured Manager, a session and a real database, so a
// behaviour test would have needed the whole application. Mutation "answer 403
// instead of 404" SURVIVED the first version of the suite for exactly that
// reason: the only test that touched this code was the unconfigured one, which
// returns before reaching the line.
//
// A function that exists only to be testable is a normal thing; a security
// property nobody can reach is not.
func writeMediaRefusal(w http.ResponseWriter, r *http.Request) {
	// NOT http.Error(w, ..., 403). An ungranted user can already see the
	// scene in the shared metadata, so a 403 confirms that a file exists --
	// which is the disclosure §6.4 forbids. The body is http.NotFound's,
	// which names nothing: no library, no user, no grant, no mode.
	http.NotFound(w, r)
}
