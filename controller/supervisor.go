package controller

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/raykavin/xtrader/tools/log"
)

var (
	// restartDelay is how long a process with Restart waits to be started
	// again, doubled on every exit up to restartMaxDelay, and back to it once
	// the process ran for restartMaxDelay.
	restartDelay    = 5 * time.Second
	restartMaxDelay = time.Minute
)

// baseEnv are the variables of the environment of the controller its
// processes get, besides the ones of their spec: never a secret.
var baseEnv = []string{"PATH", "HOME", "USER", "LOGNAME", "TZ", "LANG", "LC_ALL", "TMPDIR"}

// ProcessSpec is a program the controller runs: an agent, or a service of the
// host the agents need.
type ProcessSpec struct {
	Name    string
	Command []string // the program and its arguments
	Dir     string
	Env     map[string]string // besides baseEnv, see Prepare
	Restart bool
}

// Prepare returns what a start of a process adds to its environment, eg: a
// token of its own, and release, called once that start exited, eg: to revoke
// the token.
type Prepare func() (env map[string]string, release func(), err error)

// ProcessState is the state of a process of the supervisor.
type ProcessState struct {
	Running  bool
	PID      int
	Since    time.Time // of the start running
	Restarts int
	LastExit string // how its last start exited
}

// Supervisor runs processes by name, started and stopped on demand: a
// process stopped can be started again, a start being a process of its own.
// Their lines are written to the output of the controller, prefixed with their
// name.
type Supervisor struct {
	stdout, stderr io.Writer
	lines          sync.Mutex // the lines of the processes don't mix
	stopTimeout    time.Duration

	mu        sync.Mutex
	processes map[string]*process
	states    map[string]ProcessState // of the processes exited, their last start
}

// NewSupervisor creates a supervisor writing the lines of its processes to
// stdout and stderr, which have stopTimeout to exit once interrupted.
func NewSupervisor(stdout, stderr io.Writer, stopTimeout time.Duration) *Supervisor {
	return &Supervisor{stdout: stdout, stderr: stderr, stopTimeout: stopTimeout,
		processes: make(map[string]*process), states: make(map[string]ProcessState)}
}

// Start starts the process of spec, unless one of its name runs: prepare,
// when set, is called before each start of it, the first and the restarts.
func (s *Supervisor) Start(spec ProcessSpec, prepare Prepare) error {
	if len(spec.Command) == 0 {
		return fmt.Errorf("%s: no command", spec.Name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, running := s.processes[spec.Name]; running {
		return nil
	}
	prefix := []byte("[" + spec.Name + "] ")
	p := &process{spec: spec, prepare: prepare, stopping: make(chan struct{}), done: make(chan struct{}),
		out: &output{mu: &s.lines, w: s.stdout, prefix: prefix}, errs: &output{mu: &s.lines, w: s.stderr, prefix: prefix}}
	if err := p.start(); err != nil {
		return fmt.Errorf("%s: %w", spec.Name, err)
	}
	s.processes[spec.Name] = p
	go func() {
		p.run()
		s.forget(p)
	}()
	return nil
}

// forget drops the process p once exited, keeping its last state.
func (s *Supervisor) forget(p *process) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.processes[p.spec.Name] == p {
		delete(s.processes, p.spec.Name)
	}
	s.states[p.spec.Name] = p.state()
}

// Stop stops the process of name, if it runs, and waits for it: interrupted,
// killed once the stop timeout passed.
func (s *Supervisor) Stop(name string) {
	s.mu.Lock()
	p := s.processes[name]
	s.mu.Unlock()
	if p != nil {
		p.stop(s.stopTimeout)
		s.forget(p) // at once: a start right after starts a process of its own
	}
}

// StopAll stops every process, at once, and waits for them.
func (s *Supervisor) StopAll() {
	s.mu.Lock()
	processes := make([]*process, 0, len(s.processes))
	for _, p := range s.processes {
		processes = append(processes, p)
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range processes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.stop(s.stopTimeout)
			s.forget(p)
		}()
	}
	wg.Wait()
}

// Running reports whether the process of name runs, or waits to be started
// again.
func (s *Supervisor) Running(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.processes[name]
	return ok
}

// State returns the state of the process of name, the one of its last start
// once exited.
func (s *Supervisor) State(name string) ProcessState {
	s.mu.Lock()
	p, ok := s.processes[name]
	state := s.states[name]
	s.mu.Unlock()
	if ok {
		return p.state()
	}
	return state
}

// process is a program of the supervisor, with its starts: started again with
// a backoff when it exits, with Restart, until stopped.
type process struct {
	spec      ProcessSpec
	prepare   Prepare
	out, errs *output

	mu       sync.Mutex
	cmd      *exec.Cmd // the one of its last start
	release  func()    // of its last start
	started  time.Time
	restarts int
	lastExit string
	stopping chan struct{} // closed by stop: no start anymore
	done     chan struct{} // closed once exited for good
}

// start starts the process, its environment the base one, the one of its spec
// and the one prepare adds. The caller holds mu, or the process doesn't run
// yet.
func (p *process) start() error {
	env := make(map[string]string)
	for _, name := range baseEnv {
		if value, ok := os.LookupEnv(name); ok {
			env[name] = value
		}
	}
	for name, value := range p.spec.Env {
		env[name] = value
	}
	release := func() {}
	if p.prepare != nil {
		extra, r, err := p.prepare()
		if err != nil {
			return err
		}
		for name, value := range extra {
			env[name] = value
		}
		if r != nil {
			release = r
		}
	}
	cmd := exec.Command(p.spec.Command[0], p.spec.Command[1:]...)
	cmd.Dir = p.spec.Dir
	cmd.Env = make([]string, 0, len(env)) // never nil: nothing of the controller's inherited
	for name, value := range env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	cmd.Stdout, cmd.Stderr = p.out, p.errs
	cmd.WaitDelay = time.Second // for the output of a process left behind
	group(cmd)
	if err := cmd.Start(); err != nil {
		release()
		return err
	}
	log.Infof("[SETUP] %s launched: %s", p.spec.Name, strings.Join(cmd.Args, " "))
	p.cmd, p.release, p.started = cmd, release, time.Now()
	return nil
}

// run waits for the process to exit and, with Restart, starts it again, until
// stop.
func (p *process) run() {
	defer close(p.done)

	delay := restartDelay
	for {
		p.mu.Lock()
		cmd, release, started := p.cmd, p.release, p.started
		p.mu.Unlock()

		err := cmd.Wait()
		p.out.flush()
		p.errs.flush()
		release()
		exit := "exited"
		if err != nil {
			exit = err.Error()
			log.Warnf("%s exited: %v", p.spec.Name, err)
		} else {
			log.Infof("%s exited", p.spec.Name)
		}
		p.mu.Lock()
		p.lastExit = exit
		p.mu.Unlock()
		if !p.spec.Restart {
			return
		}

		if time.Since(started) >= restartMaxDelay {
			delay = restartDelay
		}
		for {
			select {
			case <-p.stopping:
				return
			default:
			}
			log.Warnf("%s: restarting in %s", p.spec.Name, delay)
			select {
			case <-p.stopping:
				return
			case <-time.After(delay):
			}
			delay = min(2*delay, restartMaxDelay)

			p.mu.Lock()
			select {
			case <-p.stopping:
				p.mu.Unlock()
				return
			default:
			}
			err := p.start()
			if err == nil {
				p.restarts++
			}
			p.mu.Unlock()
			if err == nil {
				break
			}
			log.Errorf("%s: %v", p.spec.Name, err)
		}
	}
}

// stop stops the process: no more starts, its start running interrupted and
// killed after timeout, and waits for it.
func (p *process) stop(timeout time.Duration) {
	p.mu.Lock()
	select {
	case <-p.stopping:
	default:
		close(p.stopping)
	}
	p.mu.Unlock()
	p.signal(interrupt)
	select {
	case <-p.done:
	case <-time.After(timeout):
		p.signal(kill)
		<-p.done
	}
}

// signal sends a signal to the process while it runs.
func (p *process) signal(send func(*os.Process) error) {
	select {
	case <-p.done:
		return
	default:
	}
	p.mu.Lock()
	process := p.cmd.Process
	p.mu.Unlock()
	if err := send(process); err != nil && !errors.Is(err, os.ErrProcessDone) {
		log.Debugf("%s: %v", p.spec.Name, err)
	}
}

// state returns the state of the process.
func (p *process) state() ProcessState {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := ProcessState{Restarts: p.restarts, LastExit: p.lastExit, Since: p.started}
	select {
	case <-p.done:
	default:
		state.Running = true
		if p.cmd != nil && p.cmd.Process != nil {
			state.PID = p.cmd.Process.Pid
		}
	}
	return state
}

// output writes the lines of a process prefixed with its name, a whole line
// at a time, as the processes share the output of the controller.
type output struct {
	mu     *sync.Mutex
	w      io.Writer
	prefix []byte
	line   []byte // the part of a line not written yet
}

func (o *output) Write(p []byte) (int, error) {
	o.line = append(o.line, p...)
	for {
		end := bytes.IndexByte(o.line, '\n')
		if end < 0 {
			return len(p), nil
		}
		o.write(o.line[:end+1])
		o.line = o.line[end+1:]
	}
}

// flush writes the last line, when it doesn't end with a newline.
func (o *output) flush() {
	if len(o.line) > 0 {
		o.write(append(o.line, '\n'))
		o.line = nil
	}
}

func (o *output) write(line []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	// a failure to write the output must not stop the process
	_, _ = o.w.Write(append(append([]byte(nil), o.prefix...), line...))
}
