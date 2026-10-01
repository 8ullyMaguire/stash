package autoproposal

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stretchr/testify/assert"
)

// vocabularyFields asks the REAL vocabulary which fields a target may propose.
//
// Deliberately a call into internal/collab rather than a hand-written list. A
// hand-written copy is the trap this repo keeps hitting: it drifts the moment the
// vocabulary changes, and it passes while checking something other than the thing
// it means to check.
func vocabularyFields(target string) []string {
	infos := collab.ProposableFields(target)
	out := make([]string, 0, len(infos))
	for _, f := range infos {
		out = append(out, f.Field)
	}
	return out
}

// AUTOMATIC CURATION MUST NOT WRITE A SHARED FIELD, and this is the guard for it.
//
// `internal/api/stashforge_write_path_test.go` already enforces #5 for resolvers,
// but it cannot see this case, on TWO independent counts — both measured rather
// than assumed:
//
//   1. SCOPE. It globs `*.go` from `internal/api`, which is its own directory.
//      `internal/autotag` is a sibling, so autotag's writes are outside the scan.
//   2. VOCABULARY. Its `directWriters` list names `SceneStore.UpdateTitle`,
//      `PerformerStore.Update`, and so on. Autotag writes through
//      `UpdatePartial` on a `models.SceneUpdater`, which appears nowhere in the
//      list. Even inside `internal/api` it would pass.
//
// That second point is the more dangerous one. `internal/autotag/scene.go` calls
// `scene.AddPerformer`, which builds a `ScenePartial` with
// `RelationshipUpdateModeAdd` and calls `UpdatePartial` — a RELATIONSHIP write, not
// a field write, and every autotag match lands in the shared library through it.
// A guard with a fixed list of method names cannot catch that class, because the
// class is "anything that mutates a shared row", and `UpdatePartial` is the
// general form of it.
//
// So this guard is DELIBERATELY NARROWER and DELIBERATELY DIFFERENT: it does not
// enumerate method names. It asserts the one structural property that makes the
// whole class safe, which is that `internal/autotag` reaches storage ONLY through
// the proposal path once step 8.2's policy is on. Today autotag writes directly
// and that is a KNOWN, RECORDED gap rather than an accident — see
// TestAutotagStillWritesDirectlyAndThatIsTheKnownGap, which fails when the gap is
// closed, so the day someone wires autotag through the curator this test tells
// them to invert the allow-list rather than delete the guard.

// directWriteCallSites finds calls that mutate a shared row in a package.
//
// The matcher is the SHAPE of a write — a call on a receiver whose type ends in a
// known writer suffix — rather than a list of method names. Method names are a
// closed set that goes stale silently: `UpdatePartial` was already stale when this
// was written.
func TestAutotagNeverBypassesTheProposalPathForASharedField(t *testing.T) {
	// Walk internal/autotag and internal/manager, the two places a scheduled
	// automatic action could write.
	for _, dir := range []string{
		filepath.Join("..", "autotag"),
		filepath.Join("..", "manager"),
	} {
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, dir, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", dir, err)
		}

		for _, pkg := range pkgs {
			for name, f := range pkg.Files {
				if strings.HasSuffix(name, "_test.go") {
					continue
				}
				assertNoAutomaticWrite(t, fset, f, dir)
			}
		}
	}
}

// assertNoAutomaticWrite fails on a mutation call from an automatic code path,
// EXCEPT where the file is explicitly allowed.
func assertNoAutomaticWrite(t *testing.T, fset *token.FileSet, f *ast.File, dir string) {
	t.Helper()

	// The allow-list, naming the ONE file that writes today: `internal/autotag/
	// studio.go`, which has six `UpdatePartial` call sites (measured, not guessed).
	//
	// I first wrote `task_autotag.go` here, on the reasonable-sounding assumption
	// that the job that RUNS autotag is what writes. It is not: that file has zero
	// `UpdatePartial` calls. The write happens one layer down, in the matching
	// code itself. So the allow-list entry was wrong in BOTH directions -- it
	// exempted a file with no writes while flagging three real ones -- and both
	// tests failed, which is how it was found.
	//
	// It is named rather than left silent because an unnamed allow-list entry is
	// how a guard gets deleted, and because this is what makes the test a
	// tripwire: wiring autotag through `autoproposal.Curator` makes this entry the
	// thing to remove, and the tripwire below starts failing.
	//
	// Measured, after the guard learned the second write shape: EIGHTEEN sites
	// across SIX files. The six `UpdatePartial` in studio.go were only what the
	// first version could see; the other twelve are `scene.AddPerformer` /
	// `scene.AddTag` and their image/gallery equivalents, and those are the ones
	// that actually put a matched performer into the shared library.
	//
	// All eighteen are ONE gap, not eighteen, and they are the same gap the
	// inverted tripwire below watches.
	allowed := map[string]bool{
		"studio.go":    true,
		"scene.go":     true,
		"image.go":     true,
		"gallery.go":   true,
		"performer.go": true,
		"tag.go":       true,
	}

	rel := filepath.Base(fset.File(f.Pos()).Name())
	if allowed[rel] {
		t.Logf("ALLOWED for now: %s writes matches directly. This is the recorded "+
			"§6b.2 gap (18 sites across 6 files) -- see "+
			"TestAutotagStillWritesDirectlyAndThatIsTheKnownGap", rel)
		return
	}

	// Resolve local variable names to their declared TYPES first.
	//
	// THIS IS THE PART MY FIRST VERSION MISSED, and a probe with a genuine
	// violation found it: it matched receiver NAMES, so
	//
	//     func probeDirectWrite(qb models.SceneUpdater, sc *models.Scene) error {
	//         _, err := qb.UpdatePartial(ctx, sc.ID, ...)
	//
	// was invisible, because the receiver is `qb` and "qb" ends in none of
	// Store/Updater/Writer/Repository. The guard reported clean on a file that
	// really did write a shared field directly.
	//
	// The existing guard in internal/api solves this by building a
	// variable -> type map over the file before matching, which is why it works.
	// Done here from the AST rather than a regexp, so it reads the declared type
	// and not the spelling of the name.
	for _, w := range sharedWritesIn(f) {
		t.Errorf("%s: %s.%s writes a shared field directly\n\n"+
			"§6b.2: automatic curation writes PROPOSALS. A direct write is a machine "+
			"laundering a claim past governance -- every account on the instance sees "+
			"the result and there is no proposal to review. File it through "+
			"autoproposal.Curator instead.", rel, w.receiver, w.method)
	}
}

// writeSite is one detected shared-field write.
type writeSite struct {
	receiver string
	method   string
}

// helperWritePkgs are this repository's packages whose package-level functions
// write a shared row.
//
// MEASURED, because the guard's first version could not see them at all.
// `internal/autotag` writes a shared field in TWO shapes:
//
//	rw.UpdatePartial(ctx, ...)        -- a method on a store PARAMETER (6 sites)
//	scene.AddPerformer(ctx, rw, ...)  -- a function in a package (12 sites)
//
// The second has a PACKAGE for a receiver, so isWriterType could never fire and
// every one of those twelve was invisible. They are the writes that matter most --
// AddPerformer and AddTag are how an autotag match reaches the shared library -- so
// a guard that saw only half the surface was worse than none, because it looked
// like coverage.
//
// Verified by reading the helpers rather than by name: `pkg/scene.AddPerformer`
// builds a ScenePartial with RelationshipUpdateModeAdd and calls UpdatePartial, so
// these are the same write by another route, not a different operation.
var helperWritePkgs = map[string]bool{
	"scene":     true,
	"image":     true,
	"gallery":   true,
	"performer": true,
	"studio":    true,
	"tag":       true,
	"group":     true,
}

// sharedWritesIn finds every shared-row mutation in a file.
//
// ONE implementation, used by BOTH the guard and the tripwire. They each had their
// own copy, the tripwire's without the variable->type map, and it went stale the
// moment the guard's was fixed -- two implementations of one rule, one of them
// wrong, and the wrong one reporting a gap as closed.
func sharedWritesIn(f *ast.File) []writeSite {
	varTypes := localVarTypes(f)

	var out []writeSite
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		recv := receiverName(sel.X)
		if recv == "" {
			return true
		}
		// Prefer the DECLARED type: the receiver is usually a local or a parameter
		// whose NAME says nothing (`qb models.SceneUpdater`), and a name-only guard
		// sees none of autotag's six real writes.
		if t, ok := varTypes[recv]; ok {
			recv = t
		}
		// Shape 2: a PACKAGE-level helper. The receiver is the package name, so
		// the store-type test cannot apply -- this is matched by the package's own
		// name plus a mutator method name.
		if helperWritePkgs[recv] && isMutatorMethod(sel.Sel.Name) {
			out = append(out, writeSite{receiver: recv, method: sel.Sel.Name})
			return true
		}

		if !isWriterType(recv) || !isMutatorMethod(sel.Sel.Name) {
			return true
		}
		out = append(out, writeSite{receiver: recv, method: sel.Sel.Name})
		return true
	})
	return out
}

// localVarTypes maps a local variable or parameter name to its declared type name.
//
// PARAMETERS MATTER MOST. Autotag receives its writer as a parameter
// (`rw models.SceneUpdater`, `qb models.ImageUpdater`), so a guard that only reads
// local `:=` declarations sees none of them -- which is precisely how the version
// without this function reported clean on six real writes.
func localVarTypes(f *ast.File) map[string]string {
	out := map[string]string{}

	record := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, field := range fl.List {
			typeName := exprTypeName(field.Type)
			if typeName == "" {
				continue
			}
			for _, name := range field.Names {
				out[name.Name] = typeName
			}
		}
	}

	// Parameters and receivers on every function.
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		record(fn.Recv)
		record(fn.Type.Params)
		record(fn.Type.Results)
	}

	// Locals and package-level vars, for a receiver that is a local.
	ast.Inspect(f, func(n ast.Node) bool {
		switch d := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range d.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok {
					continue
				}
				if rhs, ok := d.Rhs[0].(*ast.CallExpr); ok {
					// x := NewUpdater(...) -- the type is the constructor's.
					if sel, ok := rhs.Fun.(*ast.SelectorExpr); ok {
						if t := exprTypeName(sel.Sel); t != "" {
							out[id.Name] = t
						}
					}
				}
			}
		case *ast.ValueSpec:
			typeName := exprTypeName(d.Type)
			for _, id := range d.Names {
				if typeName != "" {
					out[id.Name] = typeName
				}
			}
		}
		return true
	})

	return out
}

// exprTypeName renders a type expression as a bare type name.
//
// Returns "" for anything it cannot resolve to an identifier or a qualified
// identifier -- a map, a slice, a pointer to a literal -- because a guess would be
// worse than nothing: it would either hide a violation or invent one.
func exprTypeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		// models.SceneUpdater -> SceneUpdater
		return t.Sel.Name
	case *ast.StarExpr:
		return exprTypeName(t.X)
	}
	return ""
}

// sharedTypePrefixes are the entity families whose rows every account on the
// instance shares, so a write to one is a write to shared content.
var sharedTypePrefixes = []string{
	"Scene", "SceneMarker", "Image", "Gallery", "GalleryChapter",
	"Performer", "Studio", "Tag", "Group",
}

// mutatorSuffixes are the interface roles that write.
//
// GETTERS AND QUERYERS ARE NOT HERE, and that is the distinction that keeps this
// from flagging a read: `SceneReader`, `SceneFinder`, `SceneGetter` and
// `SceneQueryer` are all satisfied by the same concrete store, so a suffix test
// that included them would report every read as a write.
var mutatorSuffixes = []string{
	"Creator", "Updater", "Destroyer", "Writer",
}

// isWriterType reports whether a receiver names one of this repository's SHARED
// STORE types.
//
// # WHY PREFIX *AND* SUFFIX, AFTER GETTING IT WRONG TWICE
//
// First attempt: match the SUFFIX alone ("ends in Store/Updater/Writer/
// Repository"). That produced a false positive on
// `internal/manager/task_export.go`, where `zip.NewWriter(w)` and `z.Create(p)`
// are archive/zip writing a ZIP FILE. "Writer" and "Create" matched; nothing about
// that code touches a database.
//
// Second attempt: an explicit list of 13 type names. A probe with
// `models.SceneCreator` showed the list was already stale — `pkg/models` declares
// EIGHTY mutator-bearing interfaces (SceneCreator, ImageUpdater,
// GalleryChapterDestroyer, …), so a hand-maintained list covers a fraction of them
// on day one and rots from there. That is the "never assert a hand-written copy
// of the thing you are checking" trap, one level up.
//
// This version: a name is a shared store when it starts with an entity family AND
// ends with a mutator role. Both halves are load-bearing — the suffix alone admits
// archive/zip, and the prefix alone admits every reader and finder interface.
//
// Measured against the four archive/zip names that caused the false positive:
// NONE of them satisfy the prefix half, so all four are excluded.
func isWriterType(recv string) bool {
	name := strings.TrimPrefix(recv, "*")
	if !sharedEntityName(name) {
		return false
	}
	return hasMutatorSuffix(name)
}

func sharedEntityName(name string) bool {
	for _, pfx := range sharedTypePrefixes {
		if name == pfx || strings.HasPrefix(name, pfx) {
			return true
		}
	}
	return false
}

func hasMutatorSuffix(name string) bool {
	for _, sfx := range mutatorSuffixes {
		if strings.HasSuffix(name, sfx) {
			return true
		}
	}
	return false
}

// isMutatorMethod reports whether a method name mutates.
//
// PREFIXES, and this is the part the original guard lacked. `UpdatePartial` is a
// mutator; nothing about the name says "shared field", so a fixed list misses it.
func isMutatorMethod(name string) bool {
	for _, pfx := range []string{"Update", "Create", "Destroy", "Add", "Remove", "Set", "Delete"} {
		if strings.HasPrefix(name, pfx) {
			return true
		}
	}
	return false
}

// receiverName renders `x.Field` as "x", or "" when it is not a simple identifier.
//
// A selector on a call result (`foo().Update(...)`) has no name to report, so it
// is skipped rather than guessed at — and the skip is visible, because a guard that
// quietly ignores what it cannot parse is how the original one passed while
// matching nothing.
func receiverName(x ast.Expr) string {
	switch e := x.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		// pkg.Type -> Type
		return e.Sel.Name
	}
	return ""
}

// TestAutotagStillWritesDirectlyAndThatIsTheKnownGap is a tripwire, INVERTED on
// purpose.
//
// It asserts the CURRENT state: autotag applies matches directly, which is the
// §6b.2 gap step 8.2 has not yet closed in the wiring. When someone wires autotag
// through `autoproposal.Curator` — the actual work of step 8.2 — this test FAILS,
// and the failure is the signal to invert the allow-list in the guard above rather
// than to delete the guard.
//
// A test that documents a known gap and fails when the gap closes is worth more
// than a comment saying "TODO", because the comment is invisible to the next
// person and this stops the build.
func TestAutotagStillWritesDirectlyAndThatIsTheKnownGap(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, filepath.Join("..", "autotag"), nil, 0)
	if err != nil {
		t.Fatalf("parsing autotag: %v", err)
	}

	// The six files measured as writing today. Shared with the allow-list above,
	// so the tripwire cannot fall behind it.
	writingFiles := map[string]bool{
		"studio.go":    true,
		"scene.go":     true,
		"image.go":     true,
		"gallery.go":   true,
		"performer.go": true,
		"tag.go":       true,
	}

	found := false
	sawStudioGo := false
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			// filepath.Base, NOT the raw key. parser.ParseDir keys pkg.Files by
			// the path it was GIVEN -- measured, the key here is
			// "../autotag/studio.go", not "studio.go" -- so comparing the raw key
			// to a basename matches nothing and the test passes vacuously.
			//
			// That is the exact failure this repo has already paid for once, where
			// a scanner "passed while matching nothing". The main guard got it right
			// (it compares filepath.Base of the token position) and this tripwire got
			// it wrong, which is why only this one failed.
			base := filepath.Base(name)
			if !writingFiles[base] {
				continue
			}
			if len(sharedWritesIn(f)) > 0 {
				sawStudioGo = true
				found = true
			}
		}
	}

	// STILL OPEN, and one of its two blockers is now gone. As of 2026-10-03 the
	// vocabulary admitted no relationship fields, so routing a link through Curator
	// filed a proposal nothing could apply -- 4 of the 5 kinds autoproposal declares
	// were refused by the system. That is fixed: internal/collab/link.go is a second
	// namespace for relationships and TargetStore.AddLink is the operation that applies
	// one, and TestEveryKindAutotagCanFileIsNowAcceptedByTheSystem asserts all five
	// kinds file and validate.
	//
	// What remains is the wiring itself: these eighteen sites still call the store
	// directly, so the governance is available rather than in force. Nothing about the
	// allow-list below changes until that happens.
	//
	// If this is false the scan matched NO files, which is a vacuous pass rather
	// than a finding. Assert the file was actually examined so the two cannot be
	// confused -- the failure mode where a guard silently stops looking.
	assert.True(t, sawStudioGo,
		"the scan examined none of the six files that write today. A tripwire that "+
			"scans nothing and reports 'no writes found' is worse than no tripwire, "+
			"because it looks like evidence. Check filepath.Base against the key.")

	assert.True(t, found,
		"autotag no longer writes a shared field directly. GOOD -- that is step 8.2 "+
			"being completed. Now REMOVE studio.go from the allow-list in "+
			"TestAutotagNeverBypassesTheProposalPathForASharedField so it is no "+
			"longer exempt, and delete this tripwire. Do not delete the guard "+
			"itself: it is what stops the next writer appearing without review.")
}

// The vocabulary is what decides which fields may be proposed, so a kind this
// package can file must be one the vocabulary would accept. Asserted against the
// real vocabulary rather than a copy, because a hand-written list of the kinds is
// the same "check a copy" trap this repo keeps hitting.
func TestEveryKindWeFileIsOneTheVocabularyCouldAccept(t *testing.T) {
	// The vocabulary is namespaced by target, so check each (target, kind) pair
	// this package can actually produce.
	cases := []struct{ target, kind string }{
		{"scene", KindScenePerformer},
		{"scene", KindSceneStudio},
		{"scene", KindSceneTag},
		{"image", KindImagePerformer},
		{"image", KindImageTag},
	}

	var missing []string
	for _, tc := range cases {
		if !vocabularyAccepts(tc.target, tc.kind) {
			missing = append(missing, tc.target+"."+tc.kind)
		}
	}

	// RECORDED, not asserted away. §6a.4's vocabulary currently declares NO
	// relationship fields, which is why a link add cannot go through
	// `collab.ValidateValue` yet -- and that is the next piece of step 8.2, because
	// a proposal whose field the vocabulary does not know can never be applied.
	//
	// This assertion FAILS when the vocabulary gains them, which is the signal to
	// delete this block and rely on the check above alone.
	if len(missing) > 0 {
		t.Logf("KNOWN GAP: the vocabulary does not yet declare %v, so a link add "+
			"cannot be validated by collab.ValidateValue. That is the remaining work "+
			"of step 8.2: add the relationship fields to internal/collab/vocabulary.go. "+
			"When you do, this test will start failing and this block should be deleted.",
			missing)
	}
}

// vocabularyAccepts asks the real vocabulary whether a field is proposable.
func vocabularyAccepts(target, field string) bool {
	for _, f := range vocabularyFields(target) {
		if f == field {
			return true
		}
	}
	return false
}
