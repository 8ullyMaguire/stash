//go:build !linux

package ed2ktransfer

// freeBytes reports that the free space is UNKNOWN on this platform.
//
// # WHY THIS EXISTS RATHER THAN A PORTABLE IMPLEMENTATION
//
// The three things a portable answer needs -- ask the filesystem, get bytes
// available to an unprivileged user, and notice when the answer is
// unavailable -- are one syscall with a signature that differs by build tag.
// Rather than carry a third platform's syscall in a plugin that only ever
// runs on the two it is tested on, the honest answer on an untested platform
// is "unknown", and checkSpace proceeds.
//
// That is a real limitation and it is stated in the type: the function
// returns a `known` flag precisely so a caller cannot treat an absent answer
// as zero free bytes. A version that returned 0 would make every transfer
// refuse on a platform nobody tested, which would be a refusal invented
// rather than a real one.
func freeBytes(dir string) (free int64, known bool) { return 0, false }
