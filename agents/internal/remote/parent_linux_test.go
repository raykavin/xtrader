package remote

import (
	"bytes"
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

	"github.com/raykavin/xtrader/agentapi"
)

// processEnv is the variable that makes the test binary a process of the
// tests, see TestParentDeath_Process.
const processEnv = "REMOTE_TEST_PROCESS"

// TestParentDeath_Process is a process of the tests, the test binary run
// again: "go-run", as `go run`, runs its program, an "agent", a child of its
// own, and waits for it, or "go-run-exit" exits at once; the agent, started,
// tells it and waits a minute.
func TestParentDeath_Process(t *testing.T) {
	switch os.Getenv(processEnv) {
	case "go-run", "go-run-exit":
		program := exec.Command(os.Args[0], "-test.run=^TestParentDeath_Process$")
		program.Env = append(os.Environ(), processEnv+"=agent")
		program.Stdout = os.Stdout
		if err := program.Start(); err != nil {
			os.Exit(1)
		}
		fmt.Printf("program %d\n", program.Process.Pid)
		if os.Getenv(processEnv) == "go-run" {
			_ = program.Wait()
		}
		os.Exit(0)
	case "agent":
		fmt.Println("agent started")
		time.Sleep(time.Minute)
		os.Exit(0)
	case "controller": // a controller of a serving agent, see TestParentDeath_TheAgentStopsInOrderWithoutItsOutput
		os.Exit(fakeControllerProcess())
	case "serving-agent":
		os.Exit(servingAgent())
	default:
		t.Skip("a process of the tests")
	}
}

// alive reports whether the process pid runs: neither gone nor a zombie.
func alive(pid int) bool {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	end := bytes.LastIndexByte(stat, ')')
	return end > 0 && len(stat) > end+2 && stat[end+2] != 'Z' && stat[end+2] != 'X'
}

// goRun runs a `go run` of the tests of kind (see TestParentDeath_Process)
// with env, and returns it and the PID of its program, once started when
// started is true.
func goRun(t *testing.T, kind string, started bool, env ...string) (*exec.Cmd, int) {
	t.Helper()
	output, err := os.Create(filepath.Join(t.TempDir(), "output"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = output.Close() })
	wrapper := exec.Command(os.Args[0], "-test.run=^TestParentDeath_Process$")
	wrapper.Env = append(append(os.Environ(), processEnv+"="+kind), env...)
	wrapper.Stdout = output // a file: nothing waits for the program to close it
	wrapper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, wrapper.Start())
	t.Cleanup(func() {
		_ = syscall.Kill(-wrapper.Process.Pid, syscall.SIGKILL) // the program too, left running by a failure
		_ = wrapper.Wait()
	})
	var pid int
	require.Eventually(t, func() bool {
		text, _ := os.ReadFile(output.Name())
		_, err := fmt.Sscanf(string(text), "program %d", &pid)
		return err == nil && (!started || strings.Contains(string(text), "agent started"))
	}, 10*time.Second, 10*time.Millisecond)
	return wrapper, pid
}

// An agent the controller launched with `go run` stops once go run ends,
// killed with the controller, which reaches go run only (B11).
func TestParentDeath_TheProgramOfGoRunStopsWithIt(t *testing.T) {
	wrapper, pid := goRun(t, "go-run", true, agentapi.EnvToken+"=xta_1_token")
	require.True(t, alive(pid))

	require.NoError(t, wrapper.Process.Signal(syscall.SIGKILL))
	_ = wrapper.Wait()
	require.Eventually(t, func() bool { return !alive(pid) }, 5*time.Second, 10*time.Millisecond,
		"the agent %d left running without its parent", pid)
}

// An agent whose parent ended before it started stops too: launched just as
// the controller was killed.
func TestParentDeath_TheProgramOfAGoRunEndedAlreadyStops(t *testing.T) {
	wrapper, pid := goRun(t, "go-run-exit", false, agentapi.EnvToken+"=xta_1_token")
	_ = wrapper.Wait()
	require.Eventually(t, func() bool { return !alive(pid) }, 5*time.Second, 10*time.Millisecond,
		"the agent %d left running without its parent", pid)
}

// An agent run on its own, not by the controller, doesn't stop with its
// parent.
func TestParentDeath_TheAgentOnItsOwnStaysRunning(t *testing.T) {
	wrapper, pid := goRun(t, "go-run", true, agentapi.EnvToken+"=")
	require.NoError(t, wrapper.Process.Signal(syscall.SIGKILL))
	_ = wrapper.Wait()
	time.Sleep(300 * time.Millisecond)
	assert.True(t, alive(pid))
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
