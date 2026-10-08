//go:build !unix

package controller

import (
	"errors"
	"os"
	"os/exec"
)

// group leaves the process in the group of the controller: without groups,
// the program `go run` runs isn't stopped with it.
func group(*exec.Cmd) {}

// interrupt kills the process: without groups, the interrupt can't be sent.
func interrupt(process *os.Process) error {
	return process.Kill()
}

// kill kills the process.
func kill(process *os.Process) error {
	return process.Kill()
}

// interruptGroup can't reach a group: see members, which tells none.
func interruptGroup(int) error {
	return errors.ErrUnsupported
}

// killGroup can't reach a group: see members, which tells none.
func killGroup(int) error {
	return errors.ErrUnsupported
}

// members can't tell the processes of a group: the leftovers of a controller
// before are left alone, see StopLeftovers.
func members(int, string) ([]int, error) {
	return nil, errors.ErrUnsupported
}

// lockFile doesn't lock: one controller per agents.dir is up to its user.
func lockFile(*os.File) error {
	return nil
}

// awaitExit can't wait for the process to exit without waiting for it: its
// exit is told once waited for.
func awaitExit(int) bool {
	return false
}
