//go:build linux

package controller

import (
	"bytes"
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

// parentDeath makes the process get SIGTERM once the thread of the controller
// that started it ends, the controller killed included: process.start keeps
// that thread until the process exited. The program `go run` runs gets
// nothing, as go run exits at once on SIGTERM: the agents set it themselves
// (see agents/internal/remote), and StopLeftovers stops what is left.
func parentDeath(attr *syscall.SysProcAttr) {
	attr.Pdeathsig = syscall.SIGTERM
}

// awaitExit waits for the process pid, a child of the controller, to exit,
// without waiting for it (WNOWAIT): its number, and the one of its group, stay
// its own until then. It reports false when the system can't tell.
func awaitExit(pid int) bool {
	var info [128]byte // a siginfo_t, unread
	for {
		_, _, errno := syscall.Syscall6(syscall.SYS_WAITID, 1 /* P_PID */, uintptr(pid),
			uintptr(unsafe.Pointer(&info[0])), syscall.WEXITED|syscall.WNOWAIT, 0, 0)
		switch errno {
		case 0:
			return true
		case syscall.EINTR:
			continue
		default:
			return false
		}
	}
}

// members returns the processes of the group pgid with mark in their
// environment (see envLaunch), from /proc: the zombies, the ones not readable
// and the ones of another program that took the number of the group once the
// processes of the controller exited are left out.
func members(pgid int, mark string) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	group := strconv.Itoa(pgid)
	want := []byte(envLaunch + "=" + mark)
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue // exited meanwhile
		}
		// pid (comm) state ppid pgrp ..., comm of any character, parentheses
		// included
		end := bytes.LastIndexByte(stat, ')')
		if end < 0 {
			continue
		}
		fields := bytes.Fields(stat[end+1:])
		if len(fields) < 3 || string(fields[2]) != group || bytes.Equal(fields[0], []byte("Z")) ||
			bytes.Equal(fields[0], []byte("X")) {
			continue
		}
		environ, err := os.ReadFile("/proc/" + entry.Name() + "/environ")
		if err != nil {
			continue
		}
		for variable := range bytes.SplitSeq(environ, []byte{0}) {
			if bytes.Equal(variable, want) {
				pids = append(pids, pid)
				break
			}
		}
	}
	return pids, nil
}
