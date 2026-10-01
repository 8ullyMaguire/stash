package exec_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	stashExec "github.com/stashapp/stash/pkg/exec"
)

// stash#5709: "Zombie process left (Python defunct)".
//
// The claim under test is NOT that a process becomes a zombie -- it is that
// stopping a plugin leaves its DESCENDANTS running. A Python plugin spawns
// helpers; killing the direct child reparents those helpers to init and they
// keep running with no parent to reap them, which is what a user sees as an
// orphan. (A true zombie is a different thing entirely: a child that exited but
// was never waited on. cmd.Wait() is called in raw.go, so that half is already
// handled and asserting it here would be asserting something that is not broken.)
//
// WHY THE TEST CHECKS A PID RATHER THAN A PROCESS NAME. The obvious version of
// this -- start a `sleep`, kill, then `pgrep -f sleep` -- passes whether or not
// the fix works, because pgrep matches its own command line and because a
// second `sleep 120` from an earlier run is indistinguishable from this run's.
// So the grandchild records its OWN pid and liveness is checked with kill(pid, 0),
// which signals nothing and simply reports existence.
//
// The mutation control for this test is the harness's own default: without
// IsolateProcessGroup the grandchild survives. Measured both ways on the same
// machine, which is what makes the assertion meaningful rather than decorative.

// grandchildAlive reports whether pid exists. Signal 0 performs the permission
// and existence checks without delivering a signal.
func grandchildAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// killGroup is KillProcessGroup for a bare pid: it builds the *exec.Cmd shape
// the function reads and hands it over, so the test exercises the shipped
// implementation rather than re-implementing the group kill itself.
func killGroup(pid int) error {
	return stashExec.KillProcessGroup(&exec.Cmd{Process: &os.Process{Pid: pid}})
}

// spawnGrandchild starts a shell that spawns a detached long-lived grandchild,
// which writes its own pid to pidFile. Returns the parent's pid.
//
// The shape mirrors a plugin: the direct child is the plugin, and the
// grandchild is the helper the plugin started.
func spawnGrandchild(t *testing.T, dir string) (parentPid int, pidFile string) {
	t.Helper()

	pidFile = filepath.Join(dir, "grandchild.pid")

	// The inner `sleep 120` is bounded anyway, so a failing test cannot leave a
	// stray process behind for longer than two minutes.
	script := "bash -c 'echo $$ > " + pidFile + "; exec sleep 120' & wait"

	cmd := stashExec.Command("bash", "-c", script)
	stashExec.IsolateProcessGroup(cmd)
	require.NoError(t, cmd.Start(), "the plugin process must start")

	// Wait for the grandchild to record its pid rather than sleeping blindly: a
	// fixed sleep is a race that shows up as a flaky failure on a loaded machine.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, err := os.ReadFile(pidFile); err == nil && len(b) > 0 {
			pid, perr := strconv.Atoi(string(b[:len(b)-1]))
			require.NoError(t, perr, "the grandchild must record a parseable pid")
			t.Cleanup(func() {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				_ = cmd.Wait()
			})
			return cmd.Process.Pid, pidFile
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("the grandchild never wrote its pid to %s", pidFile)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readPid(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err, "the grandchild's pid file must exist")
	pid, err := strconv.Atoi(string(b[:len(b)-1]))
	require.NoError(t, err)
	return pid
}

// waitsForExit polls liveness rather than sleeping a fixed interval, so the test
// asserts the end state instead of racing it.
func waitsForExit(pid int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if !grandchildAlive(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return !grandchildAlive(pid)
}

// THE FIX. Stopping a plugin takes its whole process tree with it.
func TestKillingAProcessGroupAlsoKillsItsGrandchildren(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("needs /proc and unix process groups")
	}

	parentPid, pidFile := spawnGrandchild(t, t.TempDir())
	gc := readPid(t, pidFile)

	require.True(t, grandchildAlive(gc),
		"precondition: the grandchild must be alive before the kill, or this "+
			"test would pass for the wrong reason")

	require.NoError(t, killGroup(parentPid),
		"killing the group must not error")

	assert.True(t, waitsForExit(gc, 5*time.Second),
		"the grandchild must be gone after the group is killed: it is reparented "+
			"to init and never reaped if only the direct child is signalled, which "+
			"is exactly the orphan stash#5709 reports")
}

// THE CONTROL, and the reason the test above is worth anything.
//
// This is the pre-fix behaviour, spelled out: the direct child is killed and the
// grandchild is left running. It is asserted, not just run, because a test that
// only shows the fix working cannot distinguish "fixed" from "the grandchild
// never existed".
func TestKillingOnlyTheDirectChildLeavesTheGrandchildRunning(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("needs /proc and unix process groups")
	}

	// Same spawn, deliberately WITHOUT IsolateProcessGroup -- i.e. what the code
	// did before the fix.
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	cmd := stashExec.Command("bash", "-c",
		"bash -c 'echo $$ > "+pidFile+"; exec sleep 120' & wait")
	require.NoError(t, cmd.Start(), "the plugin process must start")

	var gc int
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, err := os.ReadFile(pidFile); err == nil && len(b) > 0 {
			gc, _ = strconv.Atoi(string(b[:len(b)-1]))
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatal("the grandchild never wrote its pid")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(gc, syscall.SIGKILL)
		_ = cmd.Wait()
	})

	require.True(t, grandchildAlive(gc), "precondition: the grandchild must be alive")
	require.NoError(t, cmd.Process.Kill(), "the direct child kills cleanly")

	assert.False(t, waitsForExit(gc, 2*time.Second),
		"THIS IS THE BUG. Signalling only the direct child must leave the "+
			"grandchild running; if this ever fails, the orphan mechanism has "+
			"changed and the fix above is no longer proving anything")
}

// Stop() on a plugin that has already exited must not report an error. A kill
// that returns ESRCH for a plugin which just finished normally is not a failure,
// and propagating it would turn a clean shutdown into a noisy one.
func TestKillingAnAlreadyDeadProcessGroupIsNotAnError(t *testing.T) {
	cmd := stashExec.Command("true")
	stashExec.IsolateProcessGroup(cmd)
	require.NoError(t, cmd.Start())
	require.NoError(t, cmd.Wait()) // reaped, so the pid is definitively gone

	assert.NoError(t, stashExec.KillProcessGroup(cmd),
		"killing a finished process must be a no-op, not an error")

	// And the nil cases, because Stop() reaches here on a plugin that never started.
	assert.NoError(t, stashExec.KillProcessGroup(nil))
	assert.NoError(t, stashExec.KillProcessGroup(&exec.Cmd{}))
}

// procOf builds an *exec.Cmd carrying only a Process, which is all
// KillProcessGroup reads. Written as a helper so the test does not have to
// manufacture a process it does not own.
func procOf(pid int) exec.Cmd {
	c := exec.Cmd{Process: &os.Process{Pid: pid}}
	return c
}
