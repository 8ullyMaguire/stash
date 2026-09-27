package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The governance invariant: no resolver in this package may write a shared
// content field directly.
//
// StashForge's entire premise is that shared content changes by vote. If a
// resolver can write a scene title, then the vote is a formality — the value is
// already there and the proposal is asking people to ratify a fait accompli.
// That is not a bug that shows up in a test's assertions; it is a bug that
// makes the tests pass while the product lies.
//
// So this test reads the SOURCE rather than the behaviour. A behavioural test
// would have to call every resolver and assert nothing changed, which is both
// slow and incomplete: it only covers the paths someone thought to call.
//
// The two "allow" cases below are the whole reason this test is not a blunt
// grep. Both are legitimate, and both are bounded.

// writers are the store calls that change a shared field.
//
// Deliberately listed rather than pattern-matched, because "which methods mutate
// a target row" is a judgement about intent. A regexp would either be so broad
// it flagged the apply path it exists to protect, or so narrow it missed a
// writer nobody thought of.
//
// The receiver type is included because a bare method name cannot tell
// SceneMarkerStore.UpdateTags — the tags a user puts on their own bookmark — from
// a shared tag write. Those are different acts: one is a personal note, the other
// is an assertion about a shared object that other people are right to vote on.
// A name-only grep conflates them, and a test that fails on a legitimate call
// gets deleted, which loses the real check too.
var directWriters = []struct {
	receiver string
	method   string
}{
	{"SceneStore", "UpdateTitle"},
	{"SceneStore", "UpdateDetails"},
	{"SceneStore", "UpdateDate"},
	{"SceneStore", "UpdateRating"},
	{"SceneStore", "UpdateOrganized"},
	{"SceneStore", "UpdateCoverImage"},
	{"PerformerStore", "Update"},
	{"StudioStore", "Update"},
	{"TagStore", "Update"},
	{"ImageStore", "Update"},
	{"GalleryStore", "Update"},
	{"GroupStore", "Update"},
	{"SceneStore", "Destroy"},
	{"PerformerStore", "Destroy"},
	{"StudioStore", "Destroy"},
	{"TagStore", "Destroy"},
	{"ImageStore", "Destroy"},
	{"GalleryStore", "Destroy"},
	{"GroupStore", "Destroy"},
}

// sharedFieldStores are the stores whose rows are shared content: every
// account on the instance sees them and every account may propose changes to
// them. A store absent from this list is either per-user state (scene markers,
// their own tags) or a join table, neither of which is governed.
var sharedFieldStores = []string{
	"SceneStore", "PerformerStore", "StudioStore", "TagStore",
	"ImageStore", "GalleryStore", "GroupStore",
}

// isSharedWrite reports whether a call mutates a shared field.
//
// Both halves matter. A call on a shared store is only a violation if the method
// is one of the mutators, and a mutator name is only a violation on a shared
// store — SceneMarkerStore.UpdateTags is a user tagging their own bookmark.
func isSharedWrite(body string, call callSite) bool {
	if !isSharedStore(call.receiver) {
		return false
	}
	for _, w := range directWriters {
		if w.receiver == call.receiver && w.method == call.method {
			return true
		}
	}
	return false
}

func isSharedStore(receiver string) bool {
	for _, s := range sharedFieldStores {
		if s == receiver {
			return true
		}
	}
	return false
}

// allowedCallers are the files permitted to call the writers above.
//
// collabTargetStore is the one writer path in StashForge: it is the apply path,
// and it writes ONLY from collab.Applier, which is only called once a proposal
// has settled. resolver_model_* is untouched by this milestone and is listed
// because those resolvers are the ones that will be migrated when governance
// covers their types.
var allowedCallers = map[string]bool{
	"stashforge_collab_target.go": true,
}

func TestNoResolverWritesASharedFieldDirectly(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("globbing api package: %v", err)
	}

	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		if allowedCallers[f] {
			continue
		}

		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		// Strip the import block so a writer's name in an import list is not
		// mistaken for a call. The import is not a mutation; only a selector
		// expression is.
		body := stripImports(string(src))
		checked++

		for _, call := range findCalls(body) {
			if isSharedWrite(body, call) {
				t.Errorf("%s:%d calls %s.%s directly.\n"+
					"Shared content may only change through collab.Applier, which runs "+
					"after a proposal settles. A resolver that writes the row makes the vote "+
					"a formality: the value is already there and the proposal is asking "+
					"people to ratify it.\n"+
					"Use collab.NewProposer(...).Create to propose, and let the vote or a "+
					"moderator settle it.",
					f, call.line, call.receiver, call.method)
			}
		}
	}

	if checked == 0 {
		t.Fatal("no source files were checked — the glob matched nothing, so this " +
			"test would pass having verified nothing")
	}
}

// callSite is one method call: what it calls, and which store it calls it on.
type callSite struct {
	receiver string
	method   string
	line     int
}

// three call shapes appear in this package, and all three have to be handled or
// the scan silently misses a third of the calls:
//
//	qb.UpdateTitle(...)          receiver is a local from a factory
//	Store.UpdateTitle(...)       receiver is the type name itself
//	iface.UpdateTitle(...)       receiver is a parameter or field
//
// The receiver is resolved to a store type by the declaration that introduced
// it, because `qb` could be any store in the package and a test that treats
// every `qb` as a SceneStore would flag every call in the file.
var (
	importBlockRe = regexp.MustCompile(`(?s)^import \(.*?^\)`)
	methodCallRe  = regexp.MustCompile(`(\w+)\.(\w+)\(`)
	// declRe matches `name := sqlite.NewThingStore()` and `name := &ThingStore{}`.
	declRe = regexp.MustCompile(`(\w+)\s*:?=\s*(?:sqlite\.)?(?:New(\w+)Store|&(\w+)Store|\*?(\w+)Store)`)
)

// stripImports removes the import block from a Go source file.
//
// The early return matters and is not defensive padding. The pattern is anchored
// at ^ with (?s) DOTALL, so on a file with no `import (` block — a snippet, or a
// file with a single-line import — it matches from the start of the file to the
// last line that begins with `)`, which is most of the file. The first version of
// this test did exactly that, and consequently found nothing to flag and passed.
// The meta-test below is what caught it.
func stripImports(src string) string {
	if !importBlockRe.MatchString(src) {
		return src
	}
	return importBlockRe.ReplaceAllString(src, "")
}

// findCalls returns every method call in src with its receiver resolved to a
// store type where the resolution is unambiguous.
//
// An unresolvable receiver is reported as the variable name itself, so it can
// never accidentally match a store type and silently pass. Failing to resolve
// must not mean "not a violation".
func findCalls(src string) []callSite {
	// Build the local-name -> store-type map first, over the whole file: a
	// variable declared at the top is called at the bottom.
	types := map[string]string{}
	for _, m := range declRe.FindAllStringSubmatch(src, -1) {
		name := m[1]
		// declRe's alternation consumes the trailing "Store", so the capture is
		// the bare type name ("Scene") and the suffix has to go back on. The
		// first version compared the bare name against "SceneStore" and matched
		// nothing, which is how the meta-test below earned its place.
		for _, g := range m[2:] {
			if g != "" {
				types[name] = g + "Store"
				break
			}
		}
	}

	var out []callSite
	for _, m := range methodCallRe.FindAllStringSubmatchIndex(src, -1) {
		recv, method := src[m[2]:m[3]], src[m[4]:m[5]]
		if t, ok := types[recv]; ok {
			recv = t
		}
		out = append(out, callSite{
			receiver: recv,
			method:   method,
			line:     1 + strings.Count(src[:m[0]], "\n"),
		})
	}
	return out
}

// TestProposalsAreTheOnlyWritePath is the second half of the invariant, and the
// half that a source grep cannot check.
//
// It asserts that the mutation surface exists at all: propose, vote, withdraw,
// moderate. If someone deletes the proposal mutations believing the moderators
// can just write the field, this fails — the grep above would pass, because with
// no proposals the resolvers would not be writing anything.
func TestProposalsAreTheOnlyWritePath(t *testing.T) {
	for _, want := range []string{
		"func (r *mutationResolver) Propose(",
		"func (r *mutationResolver) Vote(",
		"func (r *mutationResolver) Withdraw(",
		"func (r *mutationResolver) Moderate(",
	} {
		if !resolverSourceContains(t, want) {
			t.Errorf("missing %q.\n"+
				"Shared content has no write path. The governance invariant says every "+
				"change settles through a proposal, so removing the proposal mutations "+
				"leaves the library read-only with no way to change it.", want)
		}
	}
}

func resolverSourceContains(t *testing.T, needle string) bool {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("globbing: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		if strings.Contains(string(src), needle) {
			return true
		}
	}
	return false
}

// TestWritePathDetectorCatchesAViolation is the meta-test.
//
// Every detector that has never been observed to fail is a detector whose
// failure mode is "silently matches nothing". The first version of this file
// passed on its first run partly because it was wrong, which is exactly the case
// worth guarding: a scanner whose receiver resolution is too strict reports
// nothing forever and looks healthy.
//
// So this asserts the scanner's behaviour on snippets with known answers, in
// both directions. A bad snippet must be caught; the legitimate calls that the
// first version flagged by mistake must NOT be.
func TestWritePathDetectorCatchesAViolation(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		wantHit bool
	}{
		{
			name: "shared store mutator is caught",
			src: `package api
func f(ctx context.Context) error {
	qb := sqlite.NewSceneStore()
	return qb.UpdateTitle(ctx, 1, "x")
}`,
			wantHit: true,
		},
		{
			name: "performer store mutator is caught",
			src: `package api
func f(ctx context.Context) error {
	qb := sqlite.NewPerformerStore()
	return qb.Update(ctx, 1, models.PerformerUpdateInput{})
}`,
			wantHit: true,
		},
		{
			name: "tag store destroy is caught",
			src: `package api
func f(ctx context.Context) error {
	qb := sqlite.NewTagStore()
	return qb.Destroy(ctx, []int{1})
}`,
			wantHit: true,
		},
		{
			// The false positive that the first version produced. A user tagging
			// their own scene marker is not asserting anything about shared
			// content, so it must pass.
			name: "scene marker tags are not a shared write",
			src: `package api
func f(ctx context.Context) error {
	qb := sqlite.NewSceneMarkerStore()
	return qb.UpdateTags(ctx, 1, []int{2})
}`,
			wantHit: false,
		},
		{
			name: "unrelated receiver is not a shared write",
			src: `package api
func f(ctx context.Context) error {
	qb := sqlite.NewImageFileStore()
	return qb.UpdateTitle(ctx, 1, "x")
}`,
			wantHit: false,
		},
		{
			name: "a read on a shared store is not a write",
			src: `package api
func f(ctx context.Context) error {
	qb := sqlite.NewSceneStore()
	_, err := qb.Find(ctx, 1)
	return err
}`,
			wantHit: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := stripImports(tc.src)
			hit := false
			for _, call := range findCalls(body) {
				if isSharedWrite(body, call) {
					hit = true
					break
				}
			}
			if hit != tc.wantHit {
				t.Errorf("scanner says hit=%v, want %v", hit, tc.wantHit)
			}
		})
	}
}
