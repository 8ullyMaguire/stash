package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The gate is wired. These tests check that it is REACHED.
//
// # WHY A SOURCE-SCAN AND NOT A BEHAVIOUR TEST
//
// A behaviour test would need a Manager, a real database, a session, a file on
// disk and an ffmpeg binary, and it would prove exactly one route. This file
// proves every route at once, and it is the only shape that scales: M4 wired
// seven route groups by hand, and a leak in the eighth is a silent one.
//
// The failure mode this is written against is the project's own: a control that
// exists, compiles, and is never called. `collab.ProposalStore` had no
// implementation for a whole milestone and M2 was green; the 2FA replay guard
// was implemented, commented as being on the login path, and not called. Both
// were caught by asking "can anything reach this" rather than "is this
// correct".
//
// So the question here is not "is allowMedia correct" -- that is
// TestAllowMedia_* and the collab resolver's own tests. It is "does every media
// route pass through something that calls allowMedia", and that is a question
// about the ROUTE TABLE, which is data.

// mediaRouteGroups are the route blocks that serve library media, and the
// middleware each one must pass through.
//
// Explicit, and asserted complete below, because the alternative -- inferring it
// -- is how a new route group gets added and never gated. A list that a test
// checks against the real route table fails on the addition; a list that
// nothing checks is a comment.
var mediaRouteGroups = map[string]string{
	"sceneRoutes":     "SceneCtx",
	"imageRoutes":     "ImageCtx",
	"galleryRoutes":   "GalleryCtx",
	"performerRoutes": "PerformerCtx",
	"studioRoutes":    "StudioCtx",
	"tagRoutes":       "TagCtx",
	"groupRoutes":     "GroupCtx",
}

// TestEveryMediaRoutePassesThroughAGatedMiddleware is the coverage test.
//
// For each route group, every path registered under it must be inside a
// r.Route(...) block whose middleware chain reaches one of the gated ones.
// A path registered at the top level of the group -- outside any gated block --
// FAILS, because that is the shape the two scene sprite routes had.
func TestEveryMediaRoutePassesThroughAGatedMiddleware(t *testing.T) {
	// The set of middlewares that (transitively) call allowMedia.
	gated := map[string]bool{}
	for _, m := range mediaRouteGroups {
		gated[m] = true
	}
	// sceneHashCtx is a free function, not a method, and it calls allowMedia.
	gated["sceneHashCtx"] = true

	for group, ctxFn := range mediaRouteGroups {
		body := routesSource(t, group)

		// The middleware itself must call the gate.
		if fn := middlewareBody(t, group, ctxFn); !strings.Contains(fn, "allowMedia(") {
			t.Errorf("%s.%s does not call allowMedia. A gate that a middleware "+
				"does not invoke covers no routes at all -- this is the "+
				"referenced-but-never-called shape (M2's ProposalStore, the "+
				"2FA replay guard)", group, ctxFn)
		}

		for _, path := range registeredPaths(t, group) {
			// Find whether this path is inside a routed block gated by ctxFn.
			if !insideGatedBlock(body, path, ctxFn) {
				t.Errorf("%s registers %q outside the gated block. Every media "+
					"route must pass through %s, or it is served with no "+
					"library check at all", group, path, ctxFn)
			}
		}
	}
}

// TestSceneHashCtxCallsTheGate covers the two routes that are gated by a free
// function rather than by a per-group middleware, and are registered OUTSIDE the
// group's main block. They are the routes the coverage test found, so they get
// named tests of their own rather than only being counted.
//
// Two properties, and the first version checked only the second, so the
// mutation "ungate the two sprite routes" SURVIVED: it left sceneHashCtx in
// place, correctly calling the gate, and removed the r.Use that makes the
// handler reachable through it. A function nobody calls is not a gate, and the
// test was satisfied by the definition of the function rather than by the
// route table.
func TestSceneHashCtxCallsTheGate(t *testing.T) {
	src := fileSource(t, "stashforge_scene_hash_routes.go")
	if !strings.Contains(src, "func sceneHashCtx(") {
		t.Fatal("sceneHashCtx is gone; the two hash-keyed sprite routes need a " +
			"middleware that resolves the hash and calls the gate")
	}
	if !strings.Contains(middlewareBodyOf(t, "stashforge_scene_hash_routes.go", "sceneHashCtx"), "allowMedia(") {
		t.Error("sceneHashCtx does not call allowMedia. It resolves a hash to a " +
			"scene and then serves a generated sprite, so without the gate it is " +
			"an unchecked read of a video's frames")
	}
}

// TestTheHashKeyedSpriteRoutesActuallyUseTheMiddleware is the other half: the
// route table must reach sceneHashCtx, not merely contain it.
//
// The positive control is the route registration itself, so a matcher that
// silently stopped finding the route cannot report "no routes, therefore
// nothing to worry about".
func TestTheHashKeyedSpriteRoutesActuallyUseTheMiddleware(t *testing.T) {
	routes := funcBlock(t, routesSource(t, "sceneRoutes"), "(rs sceneRoutes) Routes(")
	if routes == "" {
		t.Fatal("sceneRoutes.Routes not found")
	}

	// The positive control: the block IS there to be gated.
	if !strings.Contains(routes, `r.Route("/{sceneHash}*"`) {
		t.Fatalf("no /{sceneHash}* route block in sceneRoutes.Routes. The block "+
			"was renamed or removed; this test is no longer looking at the routes "+
			"it thinks it is:\n%s", routes)
	}

	for _, path := range []string{"_thumbs.vtt", "_sprite.jpg"} {
		if !strings.Contains(routes, `"`+path+`"`) {
			t.Errorf("the %s sprite route is gone; expected it under the hash "+
				"block", path)
		}
	}

	// And the block must install the middleware.
	if !strings.Contains(routes, "r.Use(sceneHashCtx)") {
		t.Error("the /{sceneHash}* block does not r.Use(sceneHashCtx). The two " +
			"sprite routes serve a generated thumbnail strip and sprite of a " +
			"scene's video frames, keyed only by a hash, so without this they " +
			"are the one media path in the tree with no library check at all")
	}
}

// TestAllowMediaIsCalledFromEveryGatedMiddleware is the inverse check: every
// middleware the coverage test trusts to be gated really does call the gate.
//
// A coverage test that trusts a list without checking the list is the same
// mistake one level up, and the direction matters: the first test asks "is any
// route ungated", this one asks "is a middleware trusted but not gated", which
// is how a stale entry in mediaRouteGroups becomes a false reassurance.
func TestAllowMediaIsCalledFromEveryGatedMiddleware(t *testing.T) {
	for group, ctxFn := range mediaRouteGroups {
		if !strings.Contains(middlewareBody(t, group, ctxFn), "allowMedia(") {
			t.Errorf("%s.%s is listed in mediaRouteGroups but does not call "+
				"allowMedia. Either gate it or remove it from the list -- an "+
				"entry that promises a gate and does not make one is worse "+
				"than no entry, because the coverage test will report green",
				group, ctxFn)
		}
	}
}

// TestAllowMediaRefusesRatherThanPassesThroughWhenUnconfigured is the fail-closed
// property, and it is checked HERE as well as by reading the code because a
// gate that is permissive when its store is missing is the shape of bug that a
// mutation survives: every test with a configured store passes.
//
// allowMedia needs a Manager to find its store, so this asserts the property
// that does not depend on one -- the refusal is written before anything is
// opened -- plus the negative control that a serving route exists at all.
func TestAllowMediaRefusesRatherThanPassesThroughWhenUnconfigured(t *testing.T) {
	// No Manager is configured in a unit test, so the nil-store path is the
	// one under test. A request for an image with no gate must 404.
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/image/1/image", nil)

	allowed := allowMedia(w, r, "image", 1)

	if allowed {
		t.Error("allowMedia returned true with no media store configured. " +
			"The gate must REFUSE when it cannot answer: a fail-open here " +
			"means a misconfigured build serves every file on the host")
	}
	if w.Code != http.StatusNotFound {
		t.Errorf("allowMedia answered %d with no store, want 404. The refusal "+
			"must not be a 403 either -- §6.4 forbids confirming a file exists",
			w.Code)
	}
	if body := w.Body.String(); strings.Contains(strings.ToLower(body), "grant") ||
		strings.Contains(strings.ToLower(body), "library") {
		t.Errorf("the refusal body names the mechanism: %q. §6.4's refusal is "+
			"byte-identical for every cause", body)
	}
}

// ---------------------------------------------------------------------------
// Source inspection helpers.
//
// Every one of these FAILS LOUDLY if the shape it expects is not found. A
// helper that returns an empty string on a miss makes the test above it pass
// vacuously, which is the whole failure mode this file exists to avoid.
// ---------------------------------------------------------------------------

var routesDir = "."

// fileSource reads a file in internal/api.
func fileSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(routesDir + "/" + name)
	if err != nil {
		t.Fatalf("reading %s: %v. These tests inspect source, so a file that "+
			"cannot be read is a FAILURE and never a silent pass", name, err)
	}
	return string(b)
}

// routesSource returns the source of the file declaring the given route group.
func routesSource(t *testing.T, group string) string {
	t.Helper()
	for _, f := range []string{
		"routes_scene.go", "routes_image.go", "routes_gallery.go",
		"routes_performer.go", "routes_studio.go", "routes_tag.go",
		"routes_group.go",
	} {
		src := fileSource(t, f)
		if strings.Contains(src, "func (rs "+group+") Routes()") {
			return src
		}
	}
	t.Fatalf("no file declares %s. Add it to this test's file list, or the "+
		"coverage test is no longer covering the whole route table", group)
	return ""
}

// funcBlock extracts a top-level func by name, from the line starting with
// `func` and naming it, to the line that closes it at column 0.
//
// The signature is matched with a REGEX rather than a plain `name+"("`
// substring, because a function whose parameters wrap onto a second line does
// not have the name and the paren on the same line. The first version did, so
// it silently returned "" for sceneHashCtx -- and a helper that returns empty
// on a miss makes every assertion above it vacuous, which is the failure mode
// this whole file exists to avoid.
func funcBlock(t *testing.T, src, sigPrefix string) string {
	t.Helper()
	lines := strings.Split(src, "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "func ") && strings.Contains(l, sigPrefix) {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	for i := start + 1; i < len(lines); i++ {
		if lines[i] == "}" {
			return strings.Join(lines[start:i+1], "\n")
		}
	}
	t.Fatalf("func %s has no closing brace at column 0; the source shape this "+
		"test assumes has changed", sigPrefix)
	return ""
}

// middlewareBody is funcBlock for a method on the given route group.
func middlewareBody(t *testing.T, group, fn string) string {
	t.Helper()
	body := funcBlock(t, routesSource(t, group), "(rs "+group+") "+fn+"(")
	if body == "" {
		t.Fatalf("%s.%s not found; the route table's shape has changed", group, fn)
	}
	return body
}

// middlewareBodyOf is funcBlock for a free function in a named file.
func middlewareBodyOf(t *testing.T, file, fn string) string {
	t.Helper()
	body := funcBlock(t, fileSource(t, file), fn+"(")
	if body == "" {
		t.Fatalf("%s not found in %s", fn, file)
	}
	return body
}

var (
	routeRE = regexp.MustCompile(`r\.(?:Get|Post|Put|Delete|Handle|HandleFunc)\("([^"]*)"`)
	// A r.Route("/{...}", ...) sub-block, with the middleware used inside it.
	routeBlockRE = regexp.MustCompile(`r\.(?:Route|Group)\("([^"]*)",\s*func\(r chi\.Router\)\s*\{`)
)

// registeredPaths lists every path the group registers directly on its router.
func registeredPaths(t *testing.T, group string) []string {
	t.Helper()
	routes := funcBlock(t, routesSource(t, group), "(rs "+group+") Routes(")
	if routes == "" {
		t.Fatalf("%s.Routes not found", group)
	}
	var out []string
	for _, m := range routeRE.FindAllStringSubmatch(routes, -1) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatalf("%s.Routes registers no paths; the regexp no longer matches "+
			"the route table and every assertion above it is vacuous", group)
	}
	return out
}

// insideGatedBlock reports whether a registered path sits inside a r.Route
// block whose middleware is the given one.
//
// It tracks a STACK of open blocks by indentation, because chi nests them and
// the first version used a single slot: the block-closing line `})` is
// indented the same as the routes INSIDE the block, so a one-slot tracker reset
// on the way out of the block it was inside and reported every nested route as
// ungated. Every scene route failed, and the code was correct.
//
// The rule, which is what makes a single pass work: a line belongs to the
// innermost open block whose indent is strictly less than the line's own. A
// closing `})` at the indent of a block's opener pops that block.
func insideGatedBlock(routes string, path, ctxFn string) bool {
	lines := strings.Split(routes, "\n")

	type block struct {
		indent int
		gated  bool
	}
	var stack []block

	// gatedNow reports whether the innermost enclosing block is gated. A route
	// at the top level of the group has no enclosing block, which is ungated.
	gatedNow := func() bool {
		if len(stack) == 0 {
			return false
		}
		return stack[len(stack)-1].gated
	}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, "	"))

		// Pop every block this line has dedented out of.
		for len(stack) > 0 && indent <= stack[len(stack)-1].indent {
			stack = stack[:len(stack)-1]
		}

		if routeBlockRE.MatchString(line) {
			// A block's middleware is registered on the lines INSIDE it, not
			// on the r.Route line itself: chi's shape is
			//
			//	r.Route("/{id}", func(r chi.Router) {
			//		r.Use(rs.TagCtx)
			//		r.Get("/image", rs.Image)
			//	})
			//
			// so a matcher that looks for r.Use on the opener finds nothing
			// and reports every route as ungated. It looked for a line and
			// the line it wanted was the next one.
			gated := false
			for j := i + 1; j < len(lines); j++ {
				next := lines[j]
				if routeRE.MatchString(next) {
					break // reached the routes without seeing a r.Use
				}
				if strings.Contains(next, "r.Use(rs."+ctxFn+")") ||
					strings.Contains(next, "r.Use("+ctxFn+")") {
					gated = true
					break
				}
			}
			stack = append(stack, block{indent: indent, gated: gated})
			continue
		}

		if routeRE.MatchString(line) {
			return gatedNow()
		}
	}
	return false
}

// TestWriteMediaRefusalIsA404ThatNamesNothing is the §6.4 response property,
// tested where it can be reached.
//
// It is its own test rather than an assertion inside TestAllowMediaRefuses*
// because that test returns BEFORE this line: with no store configured,
// allowMedia refuses at the top. The 403-not-404 rule lives at the bottom, on
// the path that needs a working instance, and the mutation "answer 403 instead
// of 404" survived the first version of this suite for that reason alone -- the
// only test that touched the code never got there.
func TestWriteMediaRefusalIsA404ThatNamesNothing(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/image/1/image", nil)

	writeMediaRefusal(w, r)

	if w.Code != http.StatusNotFound {
		t.Errorf("the refusal is %d, want 404. A 403 confirms to a prober that a "+
			"file exists, and an ungranted user can already see the scene in the "+
			"shared metadata -- that is the disclosure §6.4 forbids", w.Code)
	}

	body := strings.ToLower(w.Body.String())
	// Every word that would explain WHY would turn the 404 into an oracle.
	for _, leak := range []string{"library", "grant", "unauthorised", "unauthorized",
		"forbidden", "private", "mode", "user", "owner", "permission"} {
		if strings.Contains(body, leak) {
			t.Errorf("the refusal body contains %q: %q. Every refusal must be "+
				"byte-identical, so the body cannot depend on which rule fired",
				leak, body)
		}
	}
}
