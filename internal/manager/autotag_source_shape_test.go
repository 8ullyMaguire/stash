package manager

import (
	"os"
	"path/filepath"
	"strings"
)

// Helpers for the source-shape checks in autotag_performer_aliases_test.go.
//
// WHY SOURCE SHAPE AND NOT BEHAVIOUR: see that file's header. The bug being guarded is
// a placement error -- a call that exists in the wrong branch -- and the only property
// that distinguishes "the call is in both paths" from "the call is in one path" is
// where it sits relative to a branch.
//
// # WHY NOT USE go/ast
//
// Because these tests assert INTENT ("this branch must load aliases"), and a substring
// search on well-known source is readable by anyone who opens the file, while an AST
// walk is not. The tree is small and the anchors are exact strings, so a refactor that
// breaks a check produces a message naming what moved.
//
// The tradeoff is real and worth stating: a rename makes these fail without a behaviour
// change. That is the correct direction to fail -- a renamed branch in the auto-tag job
// is worth a human looking at #2507 -- and it is why each failure message says what it
// expected and where.

// readTaskAutotag returns the source of the auto-tag job.
func readTaskAutotag(t interface{ Fatal(...interface{}) }) string {
	path := filepath.Join("task_autotag.go")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("cannot read ", path, ": ", err)
	}
	return string(b)
}

// section returns the source of one top-level func, from its `func` line to the line
// before the next `func ` at column zero.
//
// The boundary is COLUMN ZERO because these helpers are called from tests, not from
// production code, so there is no nesting to confuse: every function in this file is
// declared at the left margin.
func section(src, header string) string {
	i := strings.Index(src, header)
	if i < 0 {
		return ""
	}
	rest := src[i+len(header):]
	j := strings.Index(rest, "\nfunc ")
	if j < 0 {
		return src[i:]
	}
	return src[i : i+len(header)+j]
}

// performerBranch returns the source of one `if` arm inside autoTagPerformers.
//
// Used rather than `section` because the two arms are INSIDE one function, and the
// branch text is bounded by the matching `} else {` / closing brace. The boundary is
// the next top-level `if` on the same indentation, which is what the brace-matching
// below recovers: this walks braces from the arm's opening line.
//
// The brace walk is not a parser and will be wrong if a branch contains a brace inside
// a string literal. It does not here, and the alternative -- pulling in go/ast for a
// two-line intent check -- costs more than it buys. If a future edit does put braces in
// a string, the check FAILS and the message points at this function, which is the
// acceptable failure direction: a false failure over a silent pass.
func performerBranch(src, opener string) string {
	fn := section(src, "func (j *autoTagJob) autoTagPerformers")
	if fn == "" {
		return ""
	}
	i := strings.Index(fn, opener)
	if i < 0 {
		return ""
	}
	depth := 0
	started := false
	for k := i; k < len(fn); k++ {
		switch fn[k] {
		case '{':
			depth++
			started = true
		case '}':
			depth--
			if started && depth == 0 {
				return fn[i : k+1]
			}
		}
	}
	return fn[i:]
}

// sharedPerformerLoop returns the `for _, performer := range performers` body -- the
// one loop both the wildcard and single-id branches feed into.
//
// Added when the wildcard test was found to be too narrow: the fix loads aliases HERE
// rather than in the wildcard branch, because that is where autoTagStudios loads them.
// A helper for the shared loop is what lets the test assert the property ("every
// performer gets its aliases loaded") without pinning the location.
func sharedPerformerLoop(src string) string {
	fn := section(src, "func (j *autoTagJob) autoTagPerformers")
	if fn == "" {
		return ""
	}
	i := strings.Index(fn, "for _, performer := range performers")
	if i < 0 {
		return ""
	}
	depth := 0
	started := false
	for k := i; k < len(fn); k++ {
		switch fn[k] {
		case '{':
			depth++
			started = true
		case '}':
			depth--
			if started && depth == 0 {
				return fn[i : k+1]
			}
		}
	}
	return fn[i:]
}
