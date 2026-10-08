package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// alive reports whether the process pid runs: neither gone nor a zombie.
func alive(pid int) bool {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	end := bytes.LastIndexByte(stat, ')')
	return end > 0 && len(stat) > end+2 && stat[end+2] != 'Z' && stat[end+2] != 'X'
}

// The agents stop with their controller, even killed: none trades on without
// the dashboard and the database (B11).
func TestOrphans_TheAgentsStopWithTheControllerKilled(t *testing.T) {
	controller, out, pid := startController(t, "bot")
	require.True(t, alive(pid))

	killController(t, controller)
	require.Eventually(t, func() bool { return !alive(pid) }, 5*time.Second, 10*time.Millisecond,
		"the agent %d left running without its controller: %s", pid, out)
}

// programOf returns the PID of the program of the fake `go run` of name, once
// started, from the output of its controller.
func programOf(t *testing.T, out *lockedBuffer, name string) int {
	t.Helper()
	var pid int
	require.Eventually(t, func() bool {
		for line := range strings.Lines(out.String()) {
			if _, err := fmt.Sscanf(line, "["+name+"] program %d", &pid); err == nil {
				return strings.Contains(out.String(), "["+name+"] started")
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond, "the output: %s", out)
	return pid
}

// The program of `go run`, which the signal of the death of the controller
// doesn't reach (go run exits at once on it), is stopped by the next
// controller, before it launches anything (B11).
func TestOrphans_TheNextControllerStopsWhatTheKilledOneLeft(t *testing.T) {
	dir := t.TempDir()
	controller, out, goRun := startController(t, "go-run", "FAKE_BOT_PIDS="+dir)
	program := programOf(t, out, "orb")
	t.Cleanup(func() { _ = syscall.Kill(program, syscall.SIGKILL) }) // left running by a failure

	killController(t, controller)
	require.Eventually(t, func() bool { return !alive(goRun) }, 5*time.Second, 10*time.Millisecond,
		"go run stops with the controller")
	time.Sleep(200 * time.Millisecond)
	require.True(t, alive(program), "the program of go run, left running")
	require.Equal(t, []string{"orb.pid"}, pidfiles(t, dir))

	next := NewSupervisor(io.Discard, io.Discard, 5*time.Second)
	require.NoError(t, next.KeepPIDs(dir))
	start := time.Now()
	next.StopLeftovers()
	assert.Less(t, time.Since(start), 5*time.Second, "interrupted, not killed")
	assert.False(t, alive(program), "stopped")
	assert.Empty(t, pidfiles(t, dir))
}

// leftover starts a process of kind of the tests in a group of its own,
// marked mark, as a controller before would have, and writes its pidfile in
// dir, with stopTimeout.
func leftover(t *testing.T, dir, name, kind, mark string, stopTimeout time.Duration) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], fakeBotPrefix+kind)
	cmd.Env = append(os.Environ(), envLaunch+"="+mark)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out lockedBuffer
	cmd.Stdout = &out
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "started") }, 5*time.Second,
		10*time.Millisecond)
	data, err := json.Marshal(pidRecord{Name: name, PID: cmd.Process.Pid, Launch: "mine-" + name,
		StopTimeout: stopTimeout})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".pid"), data, 0o600))
	return cmd.Process.Pid
}

// StopLeftovers stops the processes of the mark of their pidfile, killed
// after their stop timeout, and leaves alone a program that took the number
// of a group once the processes of the controller exited.
func TestSupervisor_StopLeftoversOfTheirMarkOnly(t *testing.T) {
	dir := t.TempDir()
	stubborn := leftover(t, dir, "orb", "stubborn", "mine-orb", 300*time.Millisecond)
	another := leftover(t, dir, "vwap", "bot", "of-another-start", time.Second)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "junk.pid"), []byte("{"), 0o600))

	s := NewSupervisor(io.Discard, io.Discard, time.Minute)
	require.NoError(t, s.KeepPIDs(dir))
	start := time.Now()
	s.StopLeftovers()
	assert.GreaterOrEqual(t, time.Since(start), 300*time.Millisecond, "interrupted first")
	assert.Less(t, time.Since(start), 10*time.Second, "killed after the stop timeout of its pidfile")
	assert.False(t, alive(stubborn), "killed")
	assert.True(t, alive(another), "not a process of the pidfile: left alone")
	assert.Empty(t, pidfiles(t, dir), "the pidfiles removed")
}

// The signal of the death of the parent comes with the end of the thread that
// started the process: a process started from a goroutine locked to its
// thread, which ends with it, keeps running.
func TestSupervisor_TheProcessOutlivesTheThreadThatStartedIt(t *testing.T) {
	s, stdout, _ := testSupervisor(t, 5*time.Second)
	started := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // never unlocked: the thread ends with the goroutine
		started <- s.Start(fakeSpec("orb", "bot"), nil)
	}()
	require.NoError(t, <-started)
	require.Eventually(t, func() bool { return strings.Contains(stdout.String(), "[orb] started") },
		5*time.Second, 10*time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	assert.True(t, s.Running("orb"))
	assert.True(t, alive(s.State("orb").PID))
}

// leftOf starts the process name of the fake kind "leave", with Restart, and
// returns the PID of the process it leaves in its group, once it waits to be
// started again.
func leftOf(t *testing.T, s *Supervisor, stdout *lockedBuffer, name string, env ...string) int {
	t.Helper()
	spec := fakeSpec(name, "leave")
	spec.Restart = true
	spec.Env = map[string]string{}
	for _, variable := range env {
		key, value, _ := strings.Cut(variable, "=")
		spec.Env[key] = value
	}
	require.NoError(t, s.Start(spec, nil))
	var pid int
	require.Eventually(t, func() bool {
		for line := range strings.Lines(stdout.String()) {
			if _, err := fmt.Sscanf(line, "["+name+"] left %d", &pid); err == nil {
				return s.State(name).Restarting
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "the output: %s", stdout)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	require.True(t, alive(pid))
	return pid
}

// B10/B11: once the leader of a start exited, eg: waiting to be started
// again, the supervisor signals its group, by its number, only while
// processes of the start are left in it: the number may be another
// program's, simulated by a process without the mark of the start.
func TestSupervisor_NoSignalToTheGroupOfAStartExited(t *testing.T) {
	previous := restartDelay
	restartDelay = time.Minute
	t.Cleanup(func() { restartDelay = previous })

	t.Run("killed all", func(t *testing.T) {
		s, stdout, _ := testSupervisor(t, time.Second)
		other := leftOf(t, s, stdout, "orb")
		ours := leftOf(t, s, stdout, "vwap", "FAKE_BOT_MARKED=1")
		s.KillAll()
		require.Eventually(t, func() bool { return !alive(ours) }, 5*time.Second, 10*time.Millisecond,
			"a process of the start left, killed")
		assert.Never(t, func() bool { return !alive(other) }, 300*time.Millisecond, 10*time.Millisecond,
			"another program, its group of the number of the leader, killed")
	})

	t.Run("stopped", func(t *testing.T) {
		s, stdout, _ := testSupervisor(t, time.Second)
		other := leftOf(t, s, stdout, "orb")
		s.Stop("orb")
		assert.False(t, s.Running("orb"))
		assert.Never(t, func() bool { return !alive(other) }, 300*time.Millisecond, 10*time.Millisecond,
			"another program, its group of the number of the leader, interrupted")
	})
}
