//go:build windows
// +build windows

package exec

import "os/exec"

// isolateProcessGroup does nothing on Windows.
//
// The unix version relies on Setpgid plus kill(-pgid), which have no Windows
// equivalent. The Windows answer is a Job Object, which is the correct tool and
// is NOT implemented here: it needs an open handle held for the command's
// lifetime, and a plugin stopped from one goroutine while Start/Wait runs in
// another is exactly the race a half-built handle owner gets wrong.
//
// Leaving it a no-op preserves today's behaviour — kill the direct child —
// rather than pretending to fix #5709 on one platform. If Windows orphan
// processes turn out to be reported, this is the file to fill in, and the test
// for it is the same one the unix side has.
func isolateProcessGroup(cmd *exec.Cmd) {
}

// killProcessGroup kills just the process on Windows, for the reason given in
// isolateProcessGroup.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
