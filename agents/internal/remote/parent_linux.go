package remote

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/raykavin/xtrader/agentapi"
)

// The agent launched by the controller gets SIGTERM, and stops as when the
// controller stops it, once its parent ends: the controller, or `go run`,
// which the controller's own signal of the death of the parent reaches alone
// (see controller.group), exiting at once on it. Set at the start of the
// program, on its main thread, which lasts as long as it: the signal belongs
// to the thread that sets it.
//
// Its output is then a pipe nothing reads anymore: SIGPIPE is ignored, its
// lines lost rather than the first one killing it in the middle of that stop,
// which then goes as any other (see Agent.Serve), the bot stopped before the
// outbox is sent and closed.
func init() {
	if os.Getenv(agentapi.EnvToken) == "" {
		return // on its own
	}
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_PDEATHSIG, uintptr(syscall.SIGTERM),
		0); errno != 0 {
		return
	}
	signal.Ignore(syscall.SIGPIPE)
	// a parent that ended before is told by the group: the controller makes
	// one for each start, led by the program it runs, eg: go run, whose
	// processes stay in it, while the process the agent goes to once its
	// parent ended, eg: init, is in another one. Leading the group, the agent
	// is the program of the controller, whose own signal covers that
	if group := syscall.Getpgrp(); group != os.Getpid() {
		if parent, err := syscall.Getpgid(os.Getppid()); err != nil || parent != group {
			_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		}
	}
}
