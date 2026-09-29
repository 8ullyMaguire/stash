package fsutil

import (
	"fmt"
	"path/filepath"
	"strings"
)

// SafeJoin joins a base directory with an untrusted relative path and returns
// an error if the result escapes the base directory.
//
// A zip or tar entry name is attacker-controlled. filepath.Join cleans the
// result, but it cleans "../../etc/passwd" to a perfectly valid path *outside*
// the base directory, so cleaning is not containment. This function is the
// containment check, and it is deliberately the only way untrusted archive
// entry names reach the filesystem.
//
// The check is lexical, not a filesystem probe: the joined path need not
// exist, and no stat is performed. That matters because the destination of an
// archive entry is usually a file about to be created, and a stat-based check
// would be bypassed by a symlink created earlier in the same archive.
func SafeJoin(baseDir, untrusted string) (string, error) {
	// An absolute path in an archive entry is never legitimate: entries are
	// relative to the archive root. Reject rather than silently discarding
	// the leading separator, so a malformed archive is loud.
	if filepath.IsAbs(untrusted) || strings.HasPrefix(untrusted, "/") || strings.HasPrefix(untrusted, `\`) {
		return "", fmt.Errorf("archive entry %q is an absolute path", untrusted)
	}
	// Windows drive-relative and UNC forms, which filepath.IsAbs does not
	// catch on a Linux build.
	if vol := filepath.VolumeName(untrusted); vol != "" {
		return "", fmt.Errorf("archive entry %q has a volume name", untrusted)
	}

	// Clean the base once so the prefix comparison below is meaningful.
	cleanBase := filepath.Clean(baseDir)
	joined := filepath.Join(cleanBase, untrusted)

	// The containment test. filepath.Rel returns a path starting with ".."
	// exactly when joined is outside base; a first-element check is required
	// rather than a strings.HasPrefix on the joined path, because
	// "/base-evil" has the prefix "/base".
	rel, err := filepath.Rel(cleanBase, joined)
	if err != nil {
		return "", fmt.Errorf("resolving %q against %q: %w", untrusted, baseDir, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes the destination directory", untrusted)
	}

	return joined, nil
}
