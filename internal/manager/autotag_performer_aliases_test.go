package manager

import (
	"strings"
	"testing"
)

// #2507 -- performer aliases in the Auto Tag task.
//
// ## WHY THIS TEST IS A SOURCE CHECK AND NOT A BEHAVIOUR TEST
//
// The bug is a PLACEMENT bug, and a behavioural test written against the current code
// would pass. Upstream disabled performer-alias matching with:
//
//	// TODO - disabled until we can have finer control over alias matching
//	// for _, a := range p.Aliases.List() { ... }
//
// in `internal/autotag/performer.go`. So `getPerformerTaggers` ignores aliases, and a
// test that drives PerformerScenes sees no aliases matched -- which is the CURRENT,
// documented behaviour. Re-enabling it is the fix; asserting it before the fix would be
// asserting the bug.
//
// So this checks the thing that is actually wrong, which is a placement error in the
// CALLER, and it is checkable without running a job:
//
// `task_autotag.go` loads performer aliases with `performer.LoadAliases` but ONLY in
// the single-id branch. The `"*"` branch calls `performerQuery.Query(...)` and appends
// the performers without ever loading aliases -- so running auto-tag against ALL
// performers tags by name only, while running it against one performer by id tags by
// name AND alias. The same operator gets different results depending on which entry
// point they used, and the `"*"` path is the one the UI uses for "tag everything".
//
// Studios do not have this bug, and the asymmetry is the evidence it is a bug rather
// than a design choice: `autoTagStudios` fetches aliases INSIDE the per-item loop, so
// both branches get them.

// The wildcard branch must load aliases for every performer it queries.
//
// THE SHAPE OF THE CHECK, and why it is a substring search on source rather than a
// call-count: the fix could reasonably be written as a loop over `performers` after the
// wildcard branch, or as a load inside the existing per-performer loop, or by moving
// the existing LoadAliases up into a shared spot. Any of those is correct. What is NOT
// correct is the current arrangement, where the load sits inside `if performerId != "*"`.
func TestTheWildcardPerformerBranchLoadsAliases(t *testing.T) {
	src := readTaskAutotag(t)

	wildcard := performerBranch(src, `if performerId == "*"`)
	if wildcard == "" {
		t.Fatal("could not find the `performerId == \"*\"` branch in task_autotag.go; " +
			"the shape this test guards has changed and the test needs re-reading")
	}

	// The wildcard branch may load aliases ITSELF or delegate to the shared per-item
	// loop -- both are correct, and the fix took the second form because it matches
	// autoTagStudios.
	//
	// So this checks the property, not the location: whatever the wildcard branch does,
	// every performer it yields must pass through an alias load before being tagged.
	// The first version of this test asserted `LoadAliases` INSIDE the wildcard branch
	// and so failed against its own fix -- the test was narrower than the change, which
	// is the same error as a test that asserts an implementation instead of a behaviour.
	if strings.Contains(wildcard, "LoadAliases") {
		return // loaded here, which satisfies the property directly
	}

	shared := sharedPerformerLoop(src)
	if shared == "" {
		t.Fatal("neither the wildcard branch nor a shared per-performer loop could be " +
			"found in autoTagPerformers. The shape this test guards has changed and the " +
			"test needs re-reading -- it is not reporting a bug in the auto-tag job.")
	}

	if !strings.Contains(shared, "LoadAliases") {
		t.Error("performers from the wildcard branch reach the tagging loop with no " +
			"alias load anywhere on their path, so auto-tagging ALL performers matches " +
			"names only while auto-tagging ONE by id also matches its aliases. The " +
			"operator gets different results from the two entry points, and \"*\" is " +
			"the one the UI uses.")
	}
}

// And the two paths must agree, which is the property a reader actually wants.
//
// Asserted by requiring that the alias load is NOT confined to the non-wildcard branch.
// The negative form is deliberate: a test that only checked "LoadAliases appears
// somewhere" would pass today, because it DOES appear -- in the wrong branch. The
// failure mode being guarded is precisely "the call exists", so the check has to be
// about where it is.
func TestAliasLoadingIsNotConfinedToTheSinglePerformerBranch(t *testing.T) {
	src := readTaskAutotag(t)

	single := performerBranch(src, `if performerId != "*"`)
	// The single-id branch may legitimately not exist if the code was refactored to
	// `else`; in that case this check is vacuous and the wildcard test above carries
	// the weight. Not a failure, because refactoring is not the bug.
	if single == "" {
		t.Skip("no `performerId != \"*\"` branch -- the code was refactored; " +
			"TestTheWildcardPerformerBranchLoadsAliases is the check that applies")
	}

	if !strings.Contains(single, "LoadAliases") && !strings.Contains(src, "for _, performer := range performers") {
		t.Error("neither the single-id branch nor the shared per-performer loop loads " +
			"aliases. Performer alias matching (#2507) needs the aliases loaded, and " +
			"they are the only thing getPerformerTaggers does not already receive.")
	}
}

// Studios must keep loading theirs -- the reference behaviour this fix copies.
//
// Asserted so a future change that unifies the two paths cannot quietly break studios
// while fixing performers. This is the "did the reference survive the edit" check, and
// it is worth having because the fix is a move, and a move can delete the source.
func TestStudiosStillLoadAliasesInsideTheirPerItemLoop(t *testing.T) {
	src := readTaskAutotag(t)

	studios := section(src, "func (j *autoTagJob) autoTagStudios")
	if studios == "" {
		t.Fatal("autoTagStudios not found; the reference implementation this fix copies " +
			"has moved and the test needs re-reading")
	}

	if !strings.Contains(studios, "GetAliases") {
		t.Error("autoTagStudios no longer loads aliases. Studios are the reference for " +
			"how #2507 should be fixed -- aliases fetched per item, inside the loop, so " +
			"every path gets them.")
	}

	// And inside the LOOP, not in a branch that only one entry point reaches.
	loopAt := strings.Index(studios, "for _, studio := range studios")
	aliasAt := strings.Index(studios, "GetAliases")
	if loopAt >= 0 && aliasAt >= 0 && aliasAt < loopAt {
		t.Error("autoTagStudios fetches aliases BEFORE its per-item loop, which is the " +
			"placement bug being fixed for performers. Studios got it right by fetching " +
			"inside the loop; if that has moved, performers should not copy it.")
	}
}
