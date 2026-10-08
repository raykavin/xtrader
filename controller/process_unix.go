//go:build unix

package controller

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// group starts the process in a group of its own: the interrupt of the
// terminal reaches the controller only, which then stops its processes, and the
// signals reach the program `go run` runs, as it ignores the interrupts. On
// Linux, the process also stops with the controller, see parentDeath.
func group(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	parentDeath(cmd.SysProcAttr)
}

// interrupt interrupts the group of the process.
func interrupt(process *os.Process) error {
	return interruptGroup(process.Pid)
}

// kill kills the group of the process.
func kill(process *os.Process) error {
	return killGroup(process.Pid)
}

// interruptGroup interrupts the group pgid.
func interruptGroup(pgid int) error {
	return syscall.Kill(-pgid, syscall.SIGINT)
}

// killGroup kills the group pgid.
func killGroup(pgid int) error {
	return syscall.Kill(-pgid, syscall.SIGKILL)
}

// lockFile locks f for this process alone, errLocked when another one holds
// it: unlocked once f is closed, or the process gone, killed included.
func lockFile(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return errLocked
	}
	return err
}
