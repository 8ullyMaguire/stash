//go:build linux || darwin || !windows
// +build linux darwin !windows

package exec

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// isolateProcessGroup puts the command in its OWN process group, so the whole
// tree it spawns can be signalled at once.
//
// stash#5709. Without this, stopping a plugin kills only the direct child. A
// Python plugin that itself started a helper leaves that helper running,
// reparented to init — which is what a user sees as "zombie process left
// (Python defunct)".
//
// Measured, both directions, checking the grandchild's own pid rather than a
// pattern that pgrep could match against its own command line:
//
//	without Setpgid: cmd.Process.Kill() -> grandchild ALIVE
//	with    Setpgid: kill(-pid)         -> grandchild gone
//
// Setpgid must be set before Start: it makes the child a group leader, so its
// pgid equals its pid and a negative pid to kill(2) reaches every descendant.
// It is deliberately NOT Pdeathsig, which would tie the plugin's lifetime to
// this one goroutine and kill plugins on reload.
func isolateProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup SIGKILLs the command's whole process group.
//
// A negative pid is the signal(2)/kill(2) convention for "every process in this
// group", and it is what makes isolateProcessGroup worth having. It falls back
// to killing just the process when the group is already gone, because a kill
// that reports ESRCH for a plugin that has just exited normally is not an error
// worth propagating to a Stop() caller.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
		return nil
	}

	// The group is gone, or was never distinct. Fall back to the single process so
	// the common case still works.
	//
	// ESRCH IS NOT PROPAGATED, and that is the whole point of this branch: a plugin
	// that exited normally has already been waited on, so its group is empty and
	// Process.Kill() answers "os: process already finished". Reporting that to a
	// Stop() caller would turn every clean shutdown into a spurious error. The
	// first version of this function DID propagate it, and the test named for this
	// case caught it -- which is why the comment above promised the behaviour the
	// code did not implement.
	//
	// Any other error (EPERM, say) is real and is returned.
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) &&
		!errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
