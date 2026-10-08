//go:build unix

package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// controllerMain is a controller of its own, the test binary run by
// startController: it runs a process of FAKE_BOT_KIND named orb, its pidfiles
// in FAKE_BOT_PIDS when set, tells "agent <pid>" and waits to be killed.
func controllerMain() {
	s := NewSupervisor(os.Stdout, os.Stderr, 5*time.Second)
	if dir := os.Getenv("FAKE_BOT_PIDS"); dir != "" {
		if err := s.KeepPIDs(dir); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
	}
	if err := s.Start(fakeSpec("orb", os.Getenv("FAKE_BOT_KIND")), nil); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Printf("agent %d\n", s.State("orb").PID)
	select {}
}

// startController starts a controller of its own (see controllerMain) running
// a process of kind, with env, and returns it, its output and the PID of its
// process once started.
func startController(t *testing.T, kind string, env ...string) (*exec.Cmd, *lockedBuffer, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], fakeBotPrefix+"controller")
	cmd.Env = append(append(os.Environ(), "FAKE_BOT_KIND="+kind), env...)
	var out lockedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	var pid int
	require.Eventually(t, func() bool {
		for line := range strings.Lines(out.String()) {
			if _, err := fmt.Sscanf(line, "agent %d", &pid); err == nil {
				return strings.Contains(out.String(), "[orb] started")
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond, "the fake controller: %s", &out)
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) }) // left running by a failure
	return cmd, &out, pid
}

// killController kills the controller cmd at once, as the OOM killer or a
// kill -9 would: nothing of it runs afterwards, its StopAll included.
func killController(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	require.NoError(t, cmd.Process.Signal(syscall.SIGKILL))
	_ = cmd.Wait()
}

// pidfiles returns the names of the files of dir.
func pidfiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// Each process has its own stop timeout, eg: a service shorter than the
// agents (R9).
func TestSupervisor_EachProcessItsStopTimeout(t *testing.T) {
	s, stdout, _ := testSupervisor(t, time.Minute)
	spec := fakeSpec("mt5bridge", "stubborn")
	spec.StopTimeout = 200 * time.Millisecond
	require.NoError(t, s.Start(spec, nil))
	require.Eventually(t, func() bool { return strings.Contains(stdout.String(), "[mt5bridge] started") },
		5*time.Second, 10*time.Millisecond)

	start := time.Now()
	s.StopAll()
	assert.GreaterOrEqual(t, time.Since(start), 200*time.Millisecond, "interrupted first")
	assert.Less(t, time.Since(start), 5*time.Second, "killed after its own stop timeout, not the minute")
	assert.False(t, s.Running("mt5bridge"))
}

// KillAll kills the processes at once, the last resort of a controller out
// of its shutdown_timeout, and nothing starts afterwards (R9).
func TestSupervisor_KillAll(t *testing.T) {
	s, stdout, _ := testSupervisor(t, time.Minute)
	require.NoError(t, s.Start(fakeSpec("orb", "stubborn"), nil))
	require.NoError(t, s.Start(fakeSpec("vwap", "stubborn"), nil))
	require.Eventually(t, func() bool { return strings.Count(stdout.String(), "started") == 2 },
		5*time.Second, 10*time.Millisecond)
	orb, vwap := s.State("orb").PID, s.State("vwap").PID

	start := time.Now()
	s.KillAll()
	assert.Less(t, time.Since(start), time.Second, "without waiting")
	require.Eventually(t, func() bool { return !s.Running("orb") && !s.Running("vwap") }, 5*time.Second,
		10*time.Millisecond)
	assert.True(t, errors.Is(syscall.Kill(orb, 0), syscall.ESRCH) || !s.Running("orb"))
	assert.NotEqual(t, orb, vwap)
	require.ErrorIs(t, s.Start(fakeSpec("orb", "bot"), nil), ErrStopped)
}

// The pidfile of a start lasts as long as it, with the mark of its
// environment, and a second controller can't take the pidfiles of one running.
func TestSupervisor_KeepsThePidfileOfEachStart(t *testing.T) {
	dir := t.TempDir()
	release, err := LockPIDs(dir)
	require.NoError(t, err)
	_, err = LockPIDs(dir)
	require.ErrorContains(t, err, "another controller runs the processes of")
	release()
	release, err = LockPIDs(dir)
	require.NoError(t, err, "free once released")
	release()

	s, stdout, _ := testSupervisor(t, 5*time.Second)
	pids := filepath.Join(dir, "agents")
	require.NoError(t, s.KeepPIDs(pids))
	require.NoError(t, s.Start(fakeSpec("vwap", "env"), nil))
	require.Eventually(t, func() bool { return strings.Contains(stdout.String(), "[vwap] started") },
		5*time.Second, 10*time.Millisecond)

	data, err := os.ReadFile(filepath.Join(pids, "vwap.pid"))
	require.NoError(t, err)
	var record pidRecord
	require.NoError(t, json.Unmarshal(data, &record))
	assert.Equal(t, "vwap", record.Name)
	assert.Equal(t, s.State("vwap").PID, record.PID)
	assert.Equal(t, 5*time.Second, record.StopTimeout)
	require.NotEmpty(t, record.Launch)
	assert.Contains(t, stdout.String(), "[vwap] "+envLaunch+"="+record.Launch+"\n", "its mark in its environment")

	require.NoError(t, s.Start(fakeSpec("orb", "crash"), nil))
	require.Eventually(t, func() bool { return !s.Running("orb") }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"vwap.pid"}, pidfiles(t, pids), "removed once exited")

	s.Stop("vwap")
	assert.Empty(t, pidfiles(t, pids), "removed once stopped")
}

// A start whose pidfile can't be written isn't left running: untracked, it
// would outlive a controller killed.
func TestSupervisor_StartWithoutItsPidfile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	s, _, _ := testSupervisor(t, 5*time.Second)
	pids := t.TempDir()
	require.NoError(t, s.KeepPIDs(pids))
	require.NoError(t, os.Chmod(pids, 0o500))
	t.Cleanup(func() { _ = os.Chmod(pids, 0o700) })

	err := s.Start(fakeSpec("orb", "bot"), nil)
	require.ErrorContains(t, err, "its pidfile")
	assert.False(t, s.Running("orb"))
}
