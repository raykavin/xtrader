package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/raykavin/xtrader/tools/log"
)

// envLaunch is the variable of the mark of a start of a process, random: what
// tells its processes, the program of `go run` included, from the ones of
// another program that took their PIDs, see members.
const envLaunch = "XTRADER_LAUNCH"

var (
	// errLocked is the error of a lock another process holds, see lockFile.
	errLocked = errors.New("locked by another process")

	// leftoverPoll is how often the processes of a group being stopped are
	// looked for, and leftoverKillWait how long they have to vanish once
	// killed.
	leftoverPoll     = 50 * time.Millisecond
	leftoverKillWait = 5 * time.Second
)

// pidRecord is the pidfile of a start of a process, see Supervisor.KeepPIDs.
type pidRecord struct {
	Name        string        `json:"name"`
	PID         int           `json:"pid"`    // and its group
	Launch      string        `json:"launch"` // see envLaunch
	StopTimeout time.Duration `json:"stop_timeout"`
	Started     time.Time     `json:"started"`
}

// LockPIDs takes the directory of the pidfiles of a controller, dir, for this
// one alone until release: another controller running from the same
// directory, eg: of the same agents.dir, fails it, as each one would take the
// processes of the other for leftovers (see Supervisor.StopLeftovers).
func LockPIDs(dir string) (release func(), err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("the pidfiles: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(dir, "controller.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("the pidfiles: %w", err)
	}
	if err := lockFile(file); err != nil {
		_ = file.Close()
		if errors.Is(err, errLocked) {
			return nil, fmt.Errorf("another controller runs the processes of %s: one controller per agents.dir", dir)
		}
		return nil, fmt.Errorf("the pidfiles: %w", err)
	}
	return func() { _ = file.Close() }, nil
}

// KeepPIDs makes the supervisor keep the pidfile of each start of its
// processes in dir, removed once the start is over: a controller that died,
// eg: killed, leaves them, for the next one to stop what it left running (see
// StopLeftovers). Called before the first start.
func (s *Supervisor) KeepPIDs(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("the pidfiles: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pids = dir
	return nil
}

// pidfile returns the pidfile of the process of name, "" without KeepPIDs.
// The caller holds mu.
func (s *Supervisor) pidfile(name string) string {
	if s.pids == "" {
		return ""
	}
	return filepath.Join(s.pids, url.PathEscape(name)+".pid")
}

// StopLeftovers stops what the starts of the pidfiles of the supervisor (see
// KeepPIDs) left running: the processes of a controller that died, which
// went on without it, eg: the program of `go run`, or any one on a system
// other than Linux (see parentDeath). Each group is stopped as Stop does,
// interrupted, then killed once its stop timeout passed, and its pidfile
// removed; a PID of another program now is left alone (see members). Called
// once, before the first start.
func (s *Supervisor) StopLeftovers() {
	s.mu.Lock()
	dir := s.pids
	s.mu.Unlock()
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Errorf("controller: the pidfiles of %s: %v", dir, err)
		return
	}
	var wg sync.WaitGroup
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".pid") && !strings.HasSuffix(name, ".pid.tmp") {
			continue
		}
		wg.Go(func() { s.stopLeftover(filepath.Join(dir, name)) })
	}
	wg.Wait()
}

// stopLeftover stops what the start of the pidfile file left running, see
// StopLeftovers.
func (s *Supervisor) stopLeftover(file string) {
	defer func() {
		if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Warnf("controller: %v", err)
		}
	}()
	var record pidRecord
	data, err := os.ReadFile(file)
	if err == nil {
		err = json.Unmarshal(data, &record)
	}
	if err != nil || record.PID <= 0 || record.Launch == "" {
		log.Warnf("controller: %s isn't a pidfile: removed", file)
		return
	}
	left, err := members(record.PID, record.Launch)
	switch {
	case err != nil:
		log.Warnf("controller: %s (pid %d), of a controller before: whether it still runs is unknown (%v), "+
			"see its group %d", record.Name, record.PID, err, record.PID)
		return
	case len(left) == 0:
		return
	}
	timeout := record.StopTimeout
	if timeout <= 0 {
		timeout = s.stopTimeout
	}
	log.Warnf("controller: %s, of a controller before, still runs without it (pid %d, %d processes in its "+
		"group): stopped", record.Name, record.PID, len(left))
	if !stopGroup(record.PID, record.Launch, timeout) {
		log.Errorf("controller: %s, of a controller before, still runs once killed (its group %d)", record.Name,
			record.PID)
	}
}

// writePID writes the pidfile of the start of pid, marked mark, when the
// process keeps them: whole or not at all, as a controller may die anytime.
func (p *process) writePID(pid int, mark string) error {
	if p.pidfile == "" {
		return nil
	}
	data, err := json.Marshal(pidRecord{Name: p.spec.Name, PID: pid, Launch: mark, StopTimeout: p.stopTimeout,
		Started: time.Now()})
	if err != nil {
		return err
	}
	temp := p.pidfile + ".tmp"
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temp, p.pidfile)
}

// removePID removes the pidfile of the process, its start over.
func (p *process) removePID() {
	if p.pidfile == "" {
		return
	}
	if err := os.Remove(p.pidfile); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Warnf("%s: %v", p.spec.Name, err)
	}
}

// stopGroup stops the processes of the group pgid marked mark (see members):
// interrupted, then killed once timeout passed. It reports whether none is
// left. A group whose processes can't be told is left alone.
func stopGroup(pgid int, mark string, timeout time.Duration) bool {
	running := func() bool {
		left, err := members(pgid, mark)
		return err == nil && len(left) > 0
	}
	if !running() {
		return true
	}
	if err := interruptGroup(pgid); err != nil {
		log.Debugf("controller: the group %d: %v", pgid, err)
	}
	if waitFor(func() bool { return !running() }, timeout) {
		return true
	}
	if running() { // the group is still the one of the mark: its number not taken again
		if err := killGroup(pgid); err != nil {
			log.Debugf("controller: the group %d: %v", pgid, err)
		}
	}
	return waitFor(func() bool { return !running() }, leftoverKillWait)
}

// waitFor waits for done to report true, for timeout at most.
func waitFor(done func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if done() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(min(leftoverPoll, time.Until(deadline)))
	}
}
