package controller

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/raykavin/xtrader/config"
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

// ErrStopped is the error of a start once the supervisor stopped its
// processes, see Supervisor.StopAll.
var ErrStopped = errors.New("the controller is stopping: nothing starts anymore")

// ProcessSpec is a program the controller runs: an agent, or a service of the
// host the agents need. StopTimeout is how long it has to exit once
// interrupted before it is killed, the one of the supervisor when 0.
type ProcessSpec struct {
	Name        string
	Command     []string // the program and its arguments
	Dir         string
	Env         map[string]string // besides baseEnv, see Prepare
	Restart     bool
	StopTimeout time.Duration
}

// Prepare returns what a start of a process adds to its environment, eg: a
// token of its own, and release, called once that start exited, eg: to revoke
// the token: the one of its last start once the supervisor forgot the process
// (see Running), before Stop returns.
type Prepare func() (env map[string]string, release func(), err error)

// ProcessState is the state of a process of the supervisor: Restarts counts
// the times it was started again once exited, with Restart, and Restarting
// tells that it exited and waits for that, Running still.
type ProcessState struct {
	Running    bool
	PID        int
	Since      time.Time // of the start running
	Restarts   int
	Restarting bool
	LastExit   string // how its last start exited
}

// Supervisor runs processes by name, started and stopped on demand: a
// process stopped can be started again, a start being a process of its own,
// until StopAll. Their lines are written to the output of the controller,
// prefixed with their name. Each start runs in a group of its own, stopped
// whole, and on Linux stops with the controller, even killed (see group): what
// a controller killed elsewhere leaves running, the next one stops (see
// KeepPIDs).
type Supervisor struct {
	stdout, stderr io.Writer
	lines          sync.Mutex // the lines of the processes don't mix
	stopTimeout    time.Duration

	mu        sync.Mutex
	processes map[string]*process
	states    map[string]ProcessState // of the processes exited, their last start
	stopped   bool                    // by StopAll: nothing starts anymore
	pids      string                  // the directory of their pidfiles, see KeepPIDs
}

// NewSupervisor creates a supervisor writing the lines of its processes to
// stdout and stderr, which have stopTimeout to exit once interrupted, unless
// their spec says otherwise.
func NewSupervisor(stdout, stderr io.Writer, stopTimeout time.Duration) *Supervisor {
	return &Supervisor{stdout: stdout, stderr: stderr, stopTimeout: stopTimeout,
		processes: make(map[string]*process), states: make(map[string]ProcessState)}
}

// Start starts the process of spec, unless one of its name runs: prepare,
// when set, is called before each start of it, the first and the restarts.
// ErrStopped once StopAll was called.
func (s *Supervisor) Start(spec ProcessSpec, prepare Prepare) error {
	if len(spec.Command) == 0 {
		return fmt.Errorf("%s: no command", spec.Name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return fmt.Errorf("%s: %w", spec.Name, ErrStopped)
	}
	if _, running := s.processes[spec.Name]; running {
		return nil
	}
	prefix := []byte("[" + spec.Name + "] ")
	p := &process{spec: spec, prepare: prepare, stopTimeout: spec.StopTimeout, pidfile: s.pidfile(spec.Name),
		stopping: make(chan struct{}), done: make(chan struct{}),
		out: &output{mu: &s.lines, w: s.stdout, prefix: prefix}, errs: &output{mu: &s.lines, w: s.stderr, prefix: prefix}}
	if p.stopTimeout <= 0 {
		p.stopTimeout = s.stopTimeout
	}
	if err := p.start(); err != nil {
		return fmt.Errorf("%s: %w", spec.Name, err)
	}
	s.processes[spec.Name] = p
	go func() {
		release := p.run()
		s.forget(p)
		release() // what it triggers sees the process gone, eg: a Reconcile (see Manager.held)
		close(p.done)
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
// killed once its stop timeout passed.
func (s *Supervisor) Stop(name string) {
	s.mu.Lock()
	p := s.processes[name]
	s.mu.Unlock()
	if p != nil {
		p.stop()
		s.forget(p) // at once: a start right after starts a process of its own
	}
}

// StopAll stops every process, at once, and waits for them: nothing starts
// afterwards (see ErrStopped), eg: a restart in flight as the controller
// stops.
func (s *Supervisor) StopAll() {
	s.mu.Lock()
	s.stopped = true
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
			p.stop()
			s.forget(p)
		}()
	}
	wg.Wait()
}

// KillAll kills every process at once, without waiting for them, and nothing
// starts afterwards: the last resort of a controller out of time to stop, see
// config.Config.ShutdownTimeout.
func (s *Supervisor) KillAll() {
	s.mu.Lock()
	s.stopped = true
	processes := make([]*process, 0, len(s.processes))
	for _, p := range s.processes {
		processes = append(processes, p)
	}
	s.mu.Unlock()
	for _, p := range processes {
		p.mu.Lock()
		select {
		case <-p.stopping:
		default:
			close(p.stopping)
		}
		p.mu.Unlock()
		p.signal(kill)
	}
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
	spec        ProcessSpec
	prepare     Prepare
	out, errs   *output
	stopTimeout time.Duration
	pidfile     string // of its starts, see Supervisor.KeepPIDs: "" for none

	mu         sync.Mutex
	cmd        *exec.Cmd     // the one of its last start
	mark       string        // of its last start, see envLaunch
	gone       chan struct{} // of its last start: closed once its leader exited, see signal
	release    func()        // of its last start
	exited     <-chan error  // of its last start: how it exited
	started    time.Time
	restarts   int
	restarting bool // exited, waiting to be started again
	ended      bool // exited for good
	lastExit   string
	stopping   chan struct{} // closed by stop: no start anymore
	done       chan struct{} // closed once exited for good, its last start released

	// signals holds a signal sent to the group of a start until it is sent:
	// its leader isn't waited for meanwhile, see signal
	signals sync.Mutex
}

// start starts the process, its environment the base one, the one of its spec
// and the one prepare adds, and the mark of the start (see envLaunch), its
// pidfile written. The caller holds mu, or the process doesn't run yet.
func (p *process) start() error {
	mark, err := randomToken()
	if err != nil {
		return err
	}
	env := make(map[string]string)
	for _, name := range baseEnv {
		if value, ok := os.LookupEnv(name); ok {
			env[name] = value
		}
	}
	for name, value := range p.spec.Env {
		env[name] = value
	}
	env[envLaunch] = mark
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
	cmd.WaitDelay = config.OutputWait // for the output of a process left behind
	group(cmd)
	started, exited, gone := make(chan error, 1), make(chan error, 1), make(chan struct{})
	go func() {
		// the signal of the death of the parent (see group) comes with the end
		// of the thread that started the process: the thread is kept until the
		// process exited, never ended by another goroutine locked to it
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := cmd.Start(); err != nil {
			started <- err
			return
		}
		started <- nil
		// the number of the leader, and of its group, is its own until it is
		// waited for, which Wait does before it waits for the output: told
		// gone before, where the system tells its exit apart (see awaitExit)
		awaited := awaitExit(cmd.Process.Pid)
		if awaited {
			p.leave(gone)
		}
		err := cmd.Wait()
		if !awaited {
			p.leave(gone)
		}
		exited <- err
	}()
	if err := <-started; err != nil {
		release()
		return err
	}
	if err := p.writePID(cmd.Process.Pid, mark); err != nil {
		_ = kill(cmd.Process) // untracked, it would outlive a controller killed
		<-exited
		release()
		return fmt.Errorf("its pidfile: %w", err)
	}
	log.Infof("[SETUP] %s launched: %s", p.spec.Name, strings.Join(cmd.Args, " "))
	p.cmd, p.mark, p.gone, p.release, p.exited, p.started = cmd, mark, gone, release, exited, time.Now()
	return nil
}

// leave tells the leader of a start exited, gone its channel, the signals of
// the start sent before then (see signal).
func (p *process) leave(gone chan struct{}) {
	p.signals.Lock()
	defer p.signals.Unlock()
	close(gone)
}

// run waits for the process to exit and, with Restart, starts it again, until
// stop: it returns the release of its last start, for the caller to call once
// it forgot the process, see Supervisor.Start.
func (p *process) run() (release func()) {
	delay := restartDelay
	for {
		p.mu.Lock()
		exited, release, started := p.exited, p.release, p.started
		p.mu.Unlock()

		err := <-exited
		p.out.flush()
		p.errs.flush()
		p.removePID()
		exit := "exited"
		if err != nil {
			exit = err.Error()
			log.Warnf("%s exited: %v", p.spec.Name, err)
		} else {
			log.Infof("%s exited", p.spec.Name)
		}
		p.mu.Lock()
		p.lastExit, p.restarting = exit, p.spec.Restart
		select {
		case <-p.stopping:
			p.restarting = false
		default:
		}
		ended := !p.restarting
		p.ended = ended
		p.mu.Unlock()
		if ended {
			return release
		}
		release()

		if time.Since(started) >= restartMaxDelay {
			delay = restartDelay
		}
		for {
			log.Warnf("%s: restarting in %s", p.spec.Name, delay)
			select {
			case <-p.stopping:
				return p.end()
			case <-time.After(delay):
			}
			delay = min(2*delay, restartMaxDelay)

			p.mu.Lock()
			select {
			case <-p.stopping:
				p.mu.Unlock()
				return p.end()
			default:
			}
			err := p.start()
			if err == nil {
				p.restarts++
				p.restarting = false
			}
			p.mu.Unlock()
			if err == nil {
				break
			}
			log.Errorf("%s: %v", p.spec.Name, err)
		}
	}
}

// end ends the process stopped while it waited to be started again, its last
// start released already, and returns what is left to release: nothing.
func (p *process) end() func() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ended, p.restarting = true, false
	return func() {}
}

// stop stops the process: no more starts, its start running interrupted and
// killed after its stop timeout, and waits for it.
func (p *process) stop() {
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
	case <-time.After(p.stopTimeout):
		p.signal(kill)
		<-p.done
	}
}

// signal sends a signal to the group of the start of the process, by its
// number, while it runs: while its leader runs, the number its own until the
// leader is waited for; once the leader exited, only while processes of the
// start are left in the group (see members), eg: the program of `go run`,
// never to another program that took the number once they all exited, eg:
// while it waits to be started again.
func (p *process) signal(send func(*os.Process) error) {
	select {
	case <-p.done:
		return
	default:
	}
	p.mu.Lock()
	cmd, mark, gone := p.cmd, p.mark, p.gone
	p.mu.Unlock()
	p.signals.Lock()
	defer p.signals.Unlock()
	select {
	case <-gone:
		if left, err := members(cmd.Process.Pid, mark); err != nil || len(left) == 0 {
			return
		}
	default:
	}
	if err := send(cmd.Process); err != nil && !errors.Is(err, os.ErrProcessDone) {
		log.Debugf("%s: %v", p.spec.Name, err)
	}
}

// state returns the state of the process.
func (p *process) state() ProcessState {
	p.mu.Lock()
	defer p.mu.Unlock()
	state := ProcessState{Restarts: p.restarts, LastExit: p.lastExit, Since: p.started}
	if !p.ended {
		state.Running, state.Restarting = true, p.restarting
		if p.cmd != nil && p.cmd.Process != nil && !p.restarting {
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
