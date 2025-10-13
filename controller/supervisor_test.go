//go:build unix

package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/raykavin/xtrader/config"
)

// fakeBotPrefix, as the first argument of the test binary, makes it a fake
// process of the kind after it: the environment of a process is set, so the
// kind comes by its arguments.
const fakeBotPrefix = "fakebot:"

func TestMain(m *testing.M) {
	if len(os.Args) < 2 || !strings.HasPrefix(os.Args[1], fakeBotPrefix) {
		os.Exit(m.Run())
	}
	switch strings.TrimPrefix(os.Args[1], fakeBotPrefix) {
	case "crash": // exits at once, as a process failing
		fmt.Println("crashed", os.Getenv("FAKE_BOT_RUN"))
		os.Exit(1)
	case "serve": // a service answering on FAKE_BOT_ADDR after a while
		time.Sleep(300 * time.Millisecond)
		fmt.Println("serving")
		_ = http.ListenAndServe(os.Getenv("FAKE_BOT_ADDR"), http.NotFoundHandler())
		os.Exit(1)
	case "env": // tells its environment
		env := os.Environ()
		sort.Strings(env)
		fmt.Println(strings.Join(env, "\n"))
	case "stubborn": // ignores the interrupts
		signal.Ignore(os.Interrupt)
	case "go-run": // as `go run`: catches the interrupts, which its program doesn't, and waits for it
		signal.Notify(make(chan os.Signal, 1), os.Interrupt)
		program := exec.Command(os.Args[0], fakeBotPrefix+"bot")
		program.Stdout = os.Stdout
		if err := program.Start(); err != nil {
			os.Exit(1)
		}
		fmt.Printf("program %d\n", program.Process.Pid)
		_ = program.Wait()
		os.Exit(1)
	}
	fmt.Println("started")
	fmt.Fprint(os.Stderr, "no newline")
	select {} // until stopped
}

// lockedBuffer is a buffer safe for the tests to read while processes write
// it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// fakeSpec is the spec of a fake process of kind named name.
func fakeSpec(name, kind string) ProcessSpec {
	return ProcessSpec{Name: name, Command: []string{os.Args[0], fakeBotPrefix + kind}, Dir: os.TempDir()}
}

// testSupervisor returns a supervisor of the test, its processes stopped at
// its end, and its outputs.
func testSupervisor(t *testing.T, stopTimeout time.Duration) (*Supervisor, *lockedBuffer, *lockedBuffer) {
	t.Helper()
	var stdout, stderr lockedBuffer
	s := NewSupervisor(&stdout, &stderr, stopTimeout)
	t.Cleanup(s.StopAll)
	return s, &stdout, &stderr
}

func TestSupervisor_StartAndStop(t *testing.T) {
	s, stdout, stderr := testSupervisor(t, 5*time.Second)
	require.NoError(t, s.Start(fakeSpec("orb", "bot"), nil))
	require.NoError(t, s.Start(fakeSpec("vwap", "bot"), nil))
	require.NoError(t, s.Start(fakeSpec("orb", "bot"), nil), "running already: nothing more")
	require.Eventually(t, func() bool {
		return strings.Contains(stdout.String(), "[orb] started\n") &&
			strings.Contains(stdout.String(), "[vwap] started\n")
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, 1, strings.Count(stdout.String(), "[orb] started"))
	assert.True(t, s.State("orb").Running)
	assert.Positive(t, s.State("orb").PID)

	start := time.Now()
	s.Stop("orb")
	assert.Less(t, time.Since(start), 5*time.Second, "interrupted, not killed")
	assert.False(t, s.Running("orb"))
	assert.False(t, s.State("orb").Running)
	assert.NotEmpty(t, s.State("orb").LastExit)
	assert.True(t, s.Running("vwap"))
	assert.Contains(t, stderr.String(), "[orb] no newline\n", "the last line is written at the exit")
	s.Stop("orb") // exited already
}

func TestSupervisor_StartsAgainOnceStopped(t *testing.T) {
	s, stdout, _ := testSupervisor(t, 5*time.Second)
	require.NoError(t, s.Start(fakeSpec("llm", "bot"), nil))
	require.Eventually(t, func() bool { return strings.Contains(stdout.String(), "[llm] started") },
		5*time.Second, 10*time.Millisecond)
	first := s.State("llm").PID
	s.Stop("llm")
	require.NoError(t, s.Start(fakeSpec("llm", "bot"), nil), "enabled again")
	require.Eventually(t, func() bool { return strings.Count(stdout.String(), "[llm] started") == 2 },
		5*time.Second, 10*time.Millisecond)
	assert.NotEqual(t, first, s.State("llm").PID, "a process of its own")
}

func TestSupervisor_StopKillsTheProcessesStillRunning(t *testing.T) {
	s, stdout, _ := testSupervisor(t, 200*time.Millisecond)
	require.NoError(t, s.Start(fakeSpec("stubborn", "stubborn"), nil))
	require.NoError(t, s.Start(fakeSpec("orb", "bot"), nil))
	require.Eventually(t, func() bool { return strings.Count(stdout.String(), "started") == 2 },
		5*time.Second, 10*time.Millisecond)

	start := time.Now()
	s.StopAll()
	assert.GreaterOrEqual(t, time.Since(start), 200*time.Millisecond)
	assert.False(t, s.Running("stubborn"))
	assert.False(t, s.Running("orb"))
}

func TestSupervisor_StopReachesTheProgramOfGoRun(t *testing.T) {
	s, stdout, _ := testSupervisor(t, 5*time.Second)
	require.NoError(t, s.Start(fakeSpec("matrend", "go-run"), nil))

	var pid int
	require.Eventually(t, func() bool {
		_, err := fmt.Sscanf(stdout.String(), "[matrend] program %d", &pid)
		return err == nil && strings.Contains(stdout.String(), "[matrend] started")
	}, 5*time.Second, 10*time.Millisecond)

	start := time.Now()
	s.Stop("matrend")
	assert.Less(t, time.Since(start), 5*time.Second, "interrupted, not killed")
	require.Eventually(t, func() bool {
		return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
	}, 5*time.Second, 10*time.Millisecond, "program "+strconv.Itoa(pid)+" left running")
}

func TestSupervisor_StartFailure(t *testing.T) {
	s, _, _ := testSupervisor(t, time.Second)
	err := s.Start(ProcessSpec{Name: "missing", Command: []string{"/nonexistent/llmagent"}}, nil)
	require.ErrorContains(t, err, "missing")
	require.False(t, s.Running("missing"))
	require.ErrorContains(t, s.Start(ProcessSpec{Name: "empty"}, nil), "no command")

	failing := errors.New("no token")
	err = s.Start(fakeSpec("orb", "bot"), func() (map[string]string, func(), error) { return nil, nil, failing })
	require.ErrorIs(t, err, failing)
	require.False(t, s.Running("orb"))
}

// The processes never get the environment of the controller: its secrets,
// eg: the password of the database, stay out.
func TestSupervisor_TheEnvironmentOfTheProcesses(t *testing.T) {
	t.Setenv("XTRADER_SECRETS_KEY", "the-secrets-key")
	t.Setenv("XTRADER_DB_PASSWORD", "the-db-password")
	t.Setenv("TELEGRAM_TOKEN", "the-telegram-token")
	s, stdout, _ := testSupervisor(t, time.Second)
	spec := fakeSpec("llm", "env")
	spec.Env = map[string]string{"GOCACHE": "/tmp/go-cache"}
	prepare := func() (map[string]string, func(), error) {
		return map[string]string{"XTRADER_AGENT_TOKEN": "xta_1_token"}, nil, nil
	}
	require.NoError(t, s.Start(spec, prepare))
	require.Eventually(t, func() bool { return strings.Contains(stdout.String(), "XTRADER_AGENT_TOKEN") },
		5*time.Second, 10*time.Millisecond)
	env := stdout.String()
	assert.Contains(t, env, "[llm] GOCACHE=/tmp/go-cache\n")
	assert.Contains(t, env, "[llm] XTRADER_AGENT_TOKEN=xta_1_token\n")
	assert.Contains(t, env, "[llm] PATH=")
	for _, secret := range []string{"XTRADER_SECRETS_KEY", "XTRADER_DB_PASSWORD", "TELEGRAM_TOKEN", "the-"} {
		assert.NotContains(t, env, secret)
	}
}

// Each start gets what prepare gives it, eg: a token of its own, released
// once it exited.
func TestSupervisor_RestartsWithBackoffEachStartPrepared(t *testing.T) {
	previous := restartDelay
	restartDelay = 10 * time.Millisecond
	t.Cleanup(func() { restartDelay = previous })

	s, stdout, _ := testSupervisor(t, 5*time.Second)
	var prepared, released atomic.Int32
	prepare := func() (map[string]string, func(), error) {
		run := prepared.Add(1)
		return map[string]string{"FAKE_BOT_RUN": fmt.Sprint(run)}, func() { released.Add(1) }, nil
	}
	spec := fakeSpec("orb", "crash")
	spec.Restart = true
	require.NoError(t, s.Start(spec, prepare))
	require.NoError(t, s.Start(fakeSpec("vwap", "crash"), nil))
	require.Eventually(t, func() bool { return strings.Count(stdout.String(), "[orb] crashed") >= 3 },
		5*time.Second, 10*time.Millisecond, "started again whenever it exits")
	assert.Contains(t, stdout.String(), "[orb] crashed 1\n")
	assert.Contains(t, stdout.String(), "[orb] crashed 2\n", "the second start prepared again")
	require.Eventually(t, func() bool { return !s.Running("vwap") }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, 1, strings.Count(stdout.String(), "[vwap] crashed"), "without restart")

	start := time.Now()
	s.Stop("orb")
	assert.Less(t, time.Since(start), time.Second)
	runs := strings.Count(stdout.String(), "[orb] crashed")
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, runs, strings.Count(stdout.String(), "[orb] crashed"), "no longer started once stopped")
	assert.Equal(t, prepared.Load(), released.Load(), "each start released")
	assert.Positive(t, s.State("orb").Restarts)
}

func TestSupervisor_RestartedProcessIsStopped(t *testing.T) {
	previous := restartDelay
	restartDelay = 10 * time.Millisecond
	t.Cleanup(func() { restartDelay = previous })

	s, stdout, _ := testSupervisor(t, 5*time.Second)
	spec := fakeSpec("orb", "bot")
	spec.Restart = true
	require.NoError(t, s.Start(spec, nil))
	require.Eventually(t, func() bool { return s.State("orb").PID > 0 && strings.Contains(stdout.String(), "started") },
		5*time.Second, 10*time.Millisecond)

	// it crashes: killed by someone else, it is started again
	require.NoError(t, syscall.Kill(-s.State("orb").PID, syscall.SIGKILL))
	require.Eventually(t, func() bool { return strings.Count(stdout.String(), "[orb] started") == 2 },
		5*time.Second, 10*time.Millisecond)

	start := time.Now()
	s.Stop("orb")
	assert.Less(t, time.Since(start), 5*time.Second, "the process running, interrupted")
	assert.False(t, s.Running("orb"))
}

func TestStartServices_WaitsForTheirHealth(t *testing.T) {
	previous := healthTimeout
	healthTimeout = 200 * time.Millisecond
	t.Cleanup(func() { healthTimeout = previous })

	s, stdout, _ := testSupervisor(t, 5*time.Second)
	address := offlineAddress(t)
	serve := config.Service{Name: "mt5bridge", Exec: os.Args[0], Args: []string{fakeBotPrefix + "serve"},
		Env: config.Env{"FAKE_BOT_ADDR=" + address}, Health: "http://" + address + "/health", Wait: 10 * time.Second}
	launched, err := StartServices(context.Background(), s, []config.Service{serve}, os.TempDir())
	require.NoError(t, err)
	assert.Equal(t, []string{"mt5bridge"}, launched)
	assert.Contains(t, stdout.String(), "[mt5bridge] serving", "waited for")

	// up already: not launched again, nor stopped with the controller
	launched, err = StartServices(context.Background(), NewSupervisor(stdout, stdout, time.Second),
		[]config.Service{serve}, os.TempDir())
	require.NoError(t, err)
	assert.Empty(t, launched)

	_, err = StartServices(context.Background(), s, []config.Service{{Name: "missing",
		Exec: "/nonexistent/run.sh"}}, os.TempDir())
	require.ErrorContains(t, err, "missing")
}
