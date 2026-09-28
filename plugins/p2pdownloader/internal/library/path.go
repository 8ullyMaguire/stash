package library

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// ExactPathPattern builds a regex that matches one path and nothing else.
//
// # WHY THIS IS NOT `regexp.QuoteMeta` AND NOT THE PATH ITSELF
//
// The host's `findScenesByPathRegex` takes a REGEX and matches it against
// stored paths. So the path goes into a regex, and a path that arrives unescaped
// is a pattern:
//
//	/library/Scene (2019).mp4        -> a group, so it matches "Scene 2019.mp4"
//	/library/a|b.mp4                 -> alternation, matches "a.mp4" too
//	/library/[a-z].mp4                -> a character class
//	/library/.*.mp4                   -> matches EVERY .mp4 in the library
//
// The last one is the dangerous case, and it is reachable by accident: a
// download whose file is named `.*.mp4`, or `+` from a scene title, turns "did
// my file get scanned" into "did ANY file get scanned". The plugin would then
// report a link to a scene belonging to a different file, and the operator
// would believe their download was linked when it was not.
//
// So the path is quoted with QuoteMeta and then anchored. QuoteMeta escapes
// every metacharacter, and the anchors are what make the match exact --
// without them a prefix of a longer path matches.
//
// # WHY THE ANCHORS ARE `^...$` AND NOT `\A...\z`
//
// The host compiles the pattern with Go's regexp, so `\A` and `\z` would work.
// But the pattern is also visible to a human debugging a query in the host's
// logs, and a form that also works in every other regex dialect is a form that
// can be reproduced there. This is a query sent to another system: prefer the
// spelling that is portable and readable over the one that is marginally more
// precise, when they are the same thing.
//
// # WHY THE PATH IS CLEANED FIRST
//
// `filepath.Clean` is what makes `a/../b` and `b` the same string, and the host
// stores cleaned paths. Querying for the cleaned form of what was written is
// what makes the match work; querying for the raw form matches nothing and the
// plugin reports a file that was in fact scanned as never linked.
func ExactPathPattern(path string) string {
	return "^" + regexp.QuoteMeta(filepath.Clean(path)) + "$"
}

// # THE CONTAINMENT CHECK, AND WHY IT CANNOT BE DONE HERE ALONE
//
// Step 5.2 asks whether a FILE NAME is safe relative to a download root. This
// asks whether the download ROOT is inside a library the host knows about -- and
// the plugin cannot answer that on its own, because only the host knows the
// configured library paths. Two things follow from that, and both matter:
//
//  1. The plugin CAN check the mechanical half: that the path is absolute, that
//     it is not empty, and that it is not a path with a traversal component
//     still unresolved. Those are properties of the string, not of a
//     configuration the plugin cannot see.
//
//  2. The remaining half is the HOST's, and the plugin's job is to notice when
//     the host has no library covering the path rather than to guess. The host
//     already answers that: `GetStashFromDirPath` returns nil for a path under
//     no configured library, and `getScanPaths` logs
//     "is not in the configured stash paths" and continues.
//
// That second one is why `Library_Call` cannot be satisfied by a successful
// `metadataScan`: the host accepts the mutation, starts a job that scans
// nothing, and returns a job id. Success is what a path outside the library
// looks like from here.

// CheckPath is the mechanical half of the containment check, and it runs BEFORE
// anything is sent to the host.
//
// The order is the design: a path that fails here never reaches the host, so a
// malformed or relative path cannot become a scan request at all. On the other
// hand a scan of a well-formed path outside the library is harmless to *send* --
// the host refuses it -- and refusing it here would mean the plugin carrying its
// own copy of the library configuration, which is the coupling step 5.0 exists to
// remove.
func CheckPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%w: the path is empty, so there is nothing to scan",
			ErrNotInLibrary)
	}
	if !filepath.IsAbs(path) {
		// Not `filepath.Clean`ed into an absolute path either. A relative path
		// would mean the host's working directory, which is the host's
		// directory and not something the plugin should be guessing at.
		return fmt.Errorf("%w: %q is relative, and a relative path would be "+
			"resolved against the host's working directory rather than against "+
			"the configured library", ErrNotInLibrary, path)
	}
	// A cleaned absolute path has no `..` left. Uncleaned, `..` is a request to
	// walk upwards, and the walk would be relative to whichever directory the
	// host happened to start in.
	if unclean := filepath.Clean(path); unclean != path {
		return fmt.Errorf("%w: %q is not in its cleaned form (it is %q), and "+
			"an uncleaned path can contain a traversal component",
			ErrNotInLibrary, path, unclean)
	}
	return nil
}
