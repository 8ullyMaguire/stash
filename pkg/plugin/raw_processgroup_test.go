package plugin

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/pkg/plugin/common"
)

// stash#5709, at the level that actually matters.
//
// The process-group tests in pkg/exec prove the primitives work. They cannot
// prove the PLUGIN uses them, and there is a version of this change that
// compiles, passes every test in pkg/exec, and leaves plugins just as orphaned as
// before -- because nothing connects "stop a plugin" to "kill a group". That
// version is the pre-fix code, and the gap between testing a helper and testing
// its caller is exactly where a fix goes to die.
//
// So this file drives a real rawPluginTask: Start() it, let it spawn a
// grandchild, Stop() it, and require the grandchild to be gone.
//
// Liveness is checked with kill(pid, 0), which delivers no signal, against a pid
// the grandchild recorded itself. A pgrep-by-name check would pass whether or not
// the fix works, because pgrep matches its own command line.

// pluginSpawningAGrandchild builds a rawPluginTask whose "plugin" is a shell that
// starts a detached helper writing its pid to pidFile -- the shape of a Python
// plugin that shells out.
func pluginSpawningAGrandchild(t *testing.T, pidFile string) *rawPluginTask {
	t.Helper()

	return &rawPluginTask{
		pluginTask: pluginTask{
			plugin: &Config{
				// Exec is the arg vector. "bash" is found on PATH, so
				// getExecCommand's LookPath succeeds and the plugin path is not
				// prepended -- which is what lets this run without a real plugin
				// directory on disk.
				Exec: []string{"bash", "-c",
					"bash -c 'echo $$ > " + pidFile + "; exec sleep 120' & wait"},
			},
			input: common.PluginInput{Args: common.ArgsMap{}},
		},
	}
}

// awaitPid waits for the grandchild to record its pid instead of sleeping a fixed
// interval -- a blind sleep is a race that surfaces as flakiness under load.
func awaitPid(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			pid, perr := strconv.Atoi(string(b[:len(b)-1]))
			if perr == nil {
				t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the plugin's grandchild never recorded its pid")
	return 0
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func waitGone(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return !alive(pid)
}

// THE FIX, end to end through the shipped plugin path. Stopping a plugin must
// take the helpers it started with it.
func TestStoppingAPluginAlsoStopsTheHelpersItSpawned(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("needs /proc and unix process groups")
	}

	pidFile := filepath.Join(t.TempDir(), "gc.pid")
	task := pluginSpawningAGrandchild(t, pidFile)

	require.NoError(t, task.Start(), "the plugin must start")
	gc := awaitPid(t, pidFile)
	require.True(t, alive(gc), "precondition: the helper must be alive before Stop")

	require.NoError(t, task.Stop(), "Stop must not error")

	assert.True(t, waitGone(gc, 10*time.Second),
		"the helper must die with the plugin: signalling only the plugin's own "+
			"process reparents the helper to init and leaves it running, which is "+
			"the orphan stash#5709 reports")
}

// Stop() must be safe to call twice and on a plugin that has already exited.
// A plugin stopped during shutdown, then again by a task cleanup, used to turn
// into a spurious error the second time.
func TestStoppingAPluginTwiceIsSafe(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "gc.pid")
	task := pluginSpawningAGrandchild(t, pidFile)
	require.NoError(t, task.Start())
	awaitPid(t, pidFile)

	require.NoError(t, task.Stop(), "first Stop")
	assert.NoError(t, task.Stop(), "second Stop on an already-stopped plugin")
}

// Stop() before Start() must be a no-op, not a nil dereference -- the plugin
// builder can fail between the two.
func TestStoppingAPluginThatNeverStartedIsSafe(t *testing.T) {
	task := &rawPluginTask{pluginTask: pluginTask{plugin: &Config{Name: "never started"}}}
	assert.NoError(t, task.Stop(), "Stop on a plugin with no process must be a no-op")
}
