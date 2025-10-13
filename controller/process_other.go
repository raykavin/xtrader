//go:build !unix

package controller

import (
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
