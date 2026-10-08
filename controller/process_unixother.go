//go:build unix && !linux

package controller

import (
	"errors"
	"syscall"
)

// parentDeath does nothing: only Linux tells a process its parent ended. A
// controller killed leaves its processes running until the next one starts,
// see StopLeftovers.
func parentDeath(*syscall.SysProcAttr) {}

// members can't tell the processes of a group without /proc: the leftovers
// of a controller before are left alone, see StopLeftovers.
func members(int, string) ([]int, error) {
	return nil, errors.ErrUnsupported
}

// awaitExit can't wait for the process to exit without waiting for it: its
// exit is told once waited for.
func awaitExit(int) bool {
	return false
}
