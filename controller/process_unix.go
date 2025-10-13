//go:build unix

package controller

import (
	"os"
	"os/exec"
	"syscall"
)

// group starts the process in a group of its own: the interrupt of the
// terminal reaches the controller only, which then stops its processes, and the
// signals reach the program `go run` runs, as it ignores the interrupts.
func group(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// interrupt interrupts the group of the process.
func interrupt(process *os.Process) error {
	return syscall.Kill(-process.Pid, syscall.SIGINT)
}

// kill kills the group of the process.
func kill(process *os.Process) error {
	return syscall.Kill(-process.Pid, syscall.SIGKILL)
}
