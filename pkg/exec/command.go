// Package exec provides functions that wrap os/exec functions. These functions prevent external commands from opening windows on the Windows platform.
package exec

import (
	"context"
	"os/exec"
)

// Command wraps the exec.Command function, preventing Windows from opening a window when starting.
func Command(name string, arg ...string) *exec.Cmd {
	ret := exec.Command(name, arg...)
	hideExecShell(ret)
	return ret
}

// CommandContext wraps the exec.CommandContext function, preventing Windows from opening a window when starting.
func CommandContext(ctx context.Context, name string, arg ...string) *exec.Cmd {
	ret := exec.CommandContext(ctx, name, arg...)
	hideExecShell(ret)
	return ret
}

// IsolateProcessGroup puts a command in its own process group so that every
// process it spawns can be signalled together with it.
//
// It must be called before Start, and it composes with the wrappers above --
// hideExecShell only touches CreationFlags on Windows, while this sets Setpgid
// on unix, so applying both is safe. Plugins need it because a Python plugin
// spawns helpers, and stopping the plugin has to take them with it (stash#5709).
func IsolateProcessGroup(cmd *exec.Cmd) {
	isolateProcessGroup(cmd)
}

// KillProcessGroup kills a command and everything it spawned.
//
// Pair it with IsolateProcessGroup: without the group, a negative-pid signal has
// no group to reach and this degrades to killing the single process.
func KillProcessGroup(cmd *exec.Cmd) error {
	return killProcessGroup(cmd)
}
