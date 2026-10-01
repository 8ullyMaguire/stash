//go:build linux

package ed2ktransfer

import "syscall"

// freeBytes reports the bytes available to an unprivileged user on the
// volume holding dir.
//
// # WHY BAVAIL AND NOT BFREE
//
// bfree is blocks free INCLUDING the root reserve, which on ext4 defaults
// to 5% of the filesystem. A transfer told it may use bfree will fill the
// volume to the reserve and then every other process on the machine starts
// failing to write -- for a file the operator asked for. bavail is what a
// non-root process may actually use, which is what this needs.
//
// # AND WHY IT CAN RETURN known == false
//
// An unprivileged Statfs on a filesystem the user cannot read the superblock
// of returns an error. Rather than guess, the answer is "unknown" and
// checkSpace lets the transfer proceed: the write will fail on its own with
// a real error naming the real problem, and refusing on an unmeasurable
// volume would be refusing on a guess.
//
// This is the third build-tagged file in the plugin and the shape is the
// same as the others: the platform detail is isolated to one function so
// the policy above it is written once and tested once.
func freeBytes(dir string) (free int64, known bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	// Bavail is in units of Bsize, and either can overflow a naive
	// multiplication on a large filesystem, so the division comes first.
	return int64(st.Bavail) * int64(st.Bsize), true
}
