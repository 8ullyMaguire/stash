package file

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/stashapp/stash/pkg/logger"
)

// PruneEmptyDirs controls whether a move removes source directories it leaves empty.
//
// stash#5631. The request was for moveFiles to clean up empty directories afterwards; the reporter was
// explicit that the goal was "just to clean up stash" rather than to remove directories outright.
//
// IT IS OPT-IN AND DEFAULTS TO FALSE, which is a decision and not a dodge. This is destructive and
// irreversible: a directory that becomes empty because its last file moved is not distinguishable, at
// the filesystem level, from a directory a user emptied by hand and expected to keep. Removing the
// second silently is the kind of surprise that erodes trust in an organiser, and a library manager
// deleting user directories without being asked is worse than leaving some empty ones behind. So it is
// a flag, off unless asked for.
//
// It also only ever removes a directory that is BOTH empty AND inside the library -- see
// pruneEmptyDirsFrom for why the containment check is load-bearing rather than defensive.

// SetPruneEmptyDirs enables or disables pruning of directories emptied by a move.
//
// False by default. Call it BEFORE the move, not during: the source directories are recorded as files
// are moved, so toggling it mid-move would leave the recorded set describing two different policies.
func (m *Mover) SetPruneEmptyDirs(prune bool) {
	m.pruneEmptyDirs = prune
}

// noteEmptiedDir records that a file has left oldPath, so oldPath's parent may now be empty.
//
// Recorded rather than acted on immediately. Two reasons:
//   - a directory is only prunable once the LAST file has left it, so the decision cannot be made at
//     the first move out of it;
//   - pruning during the move means pruning before the transaction commits. If the transaction then
//     rolls back, rollback() renames the files back and the directories are legitimately populated
//     again -- but they have already been removed, and renaming back into a missing directory fails.
//     So the work is deferred to the post-commit hook.
func (m *Mover) noteEmptiedDir(oldPath string) {
	parent := filepath.Dir(oldPath)
	if parent == "" || parent == "." || parent == string(filepath.Separator) {
		return
	}
	if m.emptiedDirs == nil {
		m.emptiedDirs = make(map[string]struct{})
	}
	m.emptiedDirs[parent] = struct{}{}
}

// pruneEmptiedDirs removes the recorded directories that are now genuinely empty.
//
// Called from the post-commit hook only, so the filesystem state it inspects is the state the
// transaction committed.
func (m *Mover) pruneEmptiedDirs() {
	// Check the flag HERE as well as at the recording site in moveFile.
	//
	// moveFile only records when m.pruneEmptyDirs is set, so the flag is already true whenever
	// emptiedDirs is non-empty and this check is redundant today. It is here anyway because the
	// invariant that matters is "pruning never happens unless it was asked for", and that should be
	// enforced by the code that does the pruning rather than inferred from a different code path
	// happening to cooperate. SetPruneEmptyDirs is public: a caller that flips the flag off after a
	// move, or a future caller that populates emptiedDirs directly, should not be able to trigger a
	// deletion by accident.
	if !m.pruneEmptyDirs {
		return
	}

	if len(m.emptiedDirs) == 0 {
		return
	}

	// Deepest first. A directory emptied by moving out its last file may have a parent that just
	// became empty too -- if every file in a/b/c moved out, both c and then b are candidates. Sorting
	// by depth descending and retrying the parent handles the chain in one pass.
	dirs := make([]string, 0, len(m.emptiedDirs))
	for dir := range m.emptiedDirs {
		dirs = append(dirs, dir)
	}
	sortByDepthDesc(dirs)

	for _, dir := range dirs {
		m.pruneEmptyDirsFrom(dir)
	}

	m.emptiedDirs = nil
}

// pruneEmptyDirsFrom removes dir if it is empty, then keeps walking up while each ancestor is also
// empty.
//
// The walk-up is what makes a single call do the right thing for a nested tree, and it stops at the
// first non-empty ancestor: if a parent still has files in it, it is not prunable and neither is
// anything above it.
func (m *Mover) pruneEmptyDirsFrom(dir string) {
	for {
		if !m.canPrune(dir) {
			return
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			// Raced with something else, or not ours to read. Either way, stop: an unreadable
			// directory is not an empty one.
			logger.Debugf("#5631: not pruning %s: %v", dir, err)
			return
		}

		if len(entries) != 0 {
			return
		}

		if err := os.Remove(dir); err != nil {
			logger.Debugf("#5631: not pruning %s: %v", dir, err)
			return
		}
		logger.Infof("#5631: removed directory %s, left empty by a move", dir)

		dir = filepath.Dir(dir)
	}
}

// canPrune is the containment check, and it is the reason this method is safe to run at all.
//
// Three conditions, all of which must hold:
//
//  1. It must be under one of the library roots. Without this, a move of a file whose oldPath was
//     somehow outside the library could delete a directory in the user's home directory. The path is
//     compared after filepath.Clean, so "/library/../etc" cannot smuggle a prefix past it.
//  2. It must not BE a library root. Removing the library directory itself would be catastrophic, and
//     the walk-up makes this reachable: pruning the last empty subdirectory of the library walks
//     straight into the root.
//  3. It must not be the filesystem root, which filepath.Dir reaches by walking up far enough.
func (m *Mover) canPrune(dir string) bool {
	cleaned := filepath.Clean(dir)

	if cleaned == string(filepath.Separator) || cleaned == "." {
		return false
	}

	for _, root := range m.rootPaths {
		if root == "" {
			continue
		}
		cleanRoot := filepath.Clean(root)
		if cleaned == cleanRoot {
			// Condition 2: a root is never prunable.
			return false
		}
		if isUnder(cleaned, cleanRoot) {
			return true
		}
	}

	return false
}

// isUnder reports whether path is strictly beneath root, on a path-segment boundary.
//
// A plain strings.HasPrefix(path, root) is wrong: "/library2" has the prefix "/library", so a
// sibling directory would pass a containment test it must fail. Comparing with a trailing separator
// is what makes "/library/a" match while "/library2" does not.
func isUnder(path, root string) bool {
	if path == root {
		return false
	}
	withSep := root
	if !strings.HasSuffix(withSep, string(filepath.Separator)) {
		withSep += string(filepath.Separator)
	}
	return strings.HasPrefix(path, withSep)
}

// sortByDepthDesc orders paths deepest-first, so a walk-up from a leaf reaches its parents before
// they are considered in their own right.
//
// Path DEPTH rather than string length: /a/bb is one segment deeper than /a/bbbb despite being
// shorter, and ordering by length would process it first and leave /a/bbb stranded. Ties are broken
// lexically so the order is deterministic -- pruning order is not semantically important, but a
// non-deterministic order makes a bug in this function much harder to reproduce.
func sortByDepthDesc(paths []string) {
	depth := func(p string) int {
		return strings.Count(filepath.ToSlash(filepath.Clean(p)), "/")
	}

	// Insertion sort: n is the number of directories a single move can empty, which is small, and this
	// avoids pulling in a sort just for a rarely-called path.
	for i := 1; i < len(paths); i++ {
		for j := i; j > 0; j-- {
			a, b := depth(paths[j-1]), depth(paths[j])
			if a > b || (a == b && paths[j-1] <= paths[j]) {
				break
			}
			paths[j-1], paths[j] = paths[j], paths[j-1]
		}
	}
}

// pruneEmptyFoldersBelow is the exported entry point used by the "move and clean" call sites.
//
// It is separate from the Mover internals because the GraphQL mutation needs to invoke it with an
// explicit list of directories rather than relying on what the mover happened to observe.
func pruneEmptyFoldersBelow(dirs []string, roots []string, renamer DirMakerStatRenamer) (int, error) {
	pruned := 0
	for _, dir := range dirs {
		cleaned := filepath.Clean(dir)
		if cleaned == string(filepath.Separator) {
			continue
		}

		inLibrary := false
		for _, root := range roots {
			if root == "" {
				continue
			}
			if cleaned != filepath.Clean(root) && isUnder(cleaned, filepath.Clean(root)) {
				inLibrary = true
				break
			}
		}
		if !inLibrary {
			continue
		}

		entries, err := os.ReadDir(cleaned)
		if err != nil || len(entries) != 0 {
			continue
		}

		if renamer != nil {
			if err := renamer.Remove(cleaned); err != nil {
				return pruned, err
			}
		} else if err := os.Remove(cleaned); err != nil {
			return pruned, err
		}

		logger.Infof("#5631: removed directory %s, left empty by a move", cleaned)
		pruned++
	}

	return pruned, nil
}
