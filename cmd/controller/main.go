// Command controller runs xtrader: the agents of its database, launched and
// stopped as they are enabled, served on the web dashboard and on Telegram,
// with the settings of the controller on the dashboard. Its configuration is
// config.yml (see package config), everything else is in the database:
//
//	go run ./cmd/migrator -config configs/config.yml up   # once, and after each update
//	go run ./cmd/controller -config configs/config.yml
//
// The agents get their settings from the internal API of the controller, on
// the loopback (agents.api of config.yml), each start with a token of its own,
// and keep their orders, decisions and journal through it: the controller
// alone reaches the database, and an agent only reaches its own settings.
// The lines of the agents are shown prefixed with their name.
//
// The dashboard needs a user of the database: bootstrap in config.yml creates
// the first one on an empty database. Telegram, its token and the users it
// answers, the agents, their prompts and the users are set on the dashboard,
// applied at once: an agent enabled is launched, one disabled stopped, a
// prompt switched from the next analysis of the agent.
//
// services of config.yml are the processes of the host the agents need, eg:
// the bridge of MT5 out of a container, launched before the agents and stopped
// after them.
//
// The whole stop, on SIGINT or SIGTERM, takes shutdown_timeout of config.yml
// at most: past it, whatever still runs is killed. On Linux the agents and
// the services stop with the controller, even killed, and their pidfiles in
// data/pids of agents.dir make the next start stop what a controller killed
// left running elsewhere, before it launches anything.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/config"
	"github.com/raykavin/xtrader/controller"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/tools/log"
	"github.com/raykavin/xtrader/ui"
)

func main() {
	os.Exit(run())
}

// run runs the controller until it is stopped, and returns its exit code.
func run() int {
	configFile := flag.String("config", "configs/config.yml", "configuration of the controller: its dashboard, "+
		"its database and how it runs the agents")
	flag.Parse()

	cfg, err := config.Load(*configFile)
	if err != nil {
		log.Fatal(err)
	}
	level, err := logrus.ParseLevel(cfg.Log.Level)
	if err != nil {
		log.Fatal(fmt.Errorf("log.level: %w", err))
	}
	log.SetLevel(level)
	for _, warning := range cfg.Warnings {
		log.Warnf("config %s: %s", *configFile, warning)
	}

	key, err := cfg.Key()
	if err != nil {
		log.Fatal(err)
	}
	st, err := store.Open(cfg.Database, key)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := st.CheckSchema(ctx); err != nil {
		log.Fatal(err)
	}
	if cfg.Bootstrap.Username != "" {
		created, err := st.Bootstrap(ctx, cfg.Bootstrap.Username, cfg.Bootstrap.Password)
		if err != nil {
			log.Fatal(fmt.Errorf("bootstrap: %w", err))
		}
		if created {
			log.Infof("[SETUP] the first user of the dashboard, %s, created: remove the password of bootstrap "+
				"from the environment", cfg.Bootstrap.Username)
		}
	}

	proxies, err := cfg.UI.Proxies()
	if err != nil {
		log.Fatal(err)
	}
	options := []controller.DashboardOption{controller.WithUsers(st), controller.WithTrustedProxies(proxies)}
	if cfg.UI.Dir != "" {
		options = append(options, controller.WithUIDir(cfg.UI.Dir))
	}
	if cfg.UI.Version != "" {
		options = append(options, controller.WithUIVersion(cfg.UI.Version))
	}
	dashboard, err := controller.NewDashboard(nil, options...)
	if err != nil {
		log.Fatal(err)
	}

	// what a controller before left running as it died, eg: killed, stopped
	// before anything is launched: the agents, then the services they use
	pids := filepath.Join(cfg.Agents.Dir, "data", "pids")
	unlock, err := controller.LockPIDs(pids)
	if err != nil {
		log.Fatal(err)
	}
	defer unlock()
	agents := controller.NewSupervisor(os.Stdout, os.Stderr, cfg.Agents.StopTimeout)
	services := controller.NewSupervisor(os.Stdout, os.Stderr, cfg.Agents.StopTimeout)
	if err := agents.KeepPIDs(filepath.Join(pids, "agents")); err != nil {
		log.Fatal(err)
	}
	if err := services.KeepPIDs(filepath.Join(pids, "services")); err != nil {
		log.Fatal(err)
	}
	agents.StopLeftovers()
	services.StopLeftovers()

	var manager *controller.Manager
	bot := controller.NewTelegramRunner(ctx, dashboard, controller.WithPrompts(st, func() { manager.Trigger() }))
	manager = controller.NewManager(st, cfg.Agents, agents, dashboard, bot)
	dashboard.ServeSettings(st, manager, bot)
	supervision := dashboard.ServeSupervision(st, manager)
	backtests := dashboard.ServeBacktests(st, cfg.Agents, cfg.Backtest)

	// the internal API first, then the dashboard: nothing is launched unless
	// both can serve
	internalListener, err := net.Listen("tcp", cfg.Agents.API)
	if err != nil {
		log.Fatal(fmt.Errorf("agents.api: %w", err))
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		log.Fatal(err)
	}
	life := &lifecycle{cancel: cancel, timeout: cfg.ShutdownTimeout, exit: os.Exit, kill: func() {
		agents.KillAll()
		services.KillAll()
	}}
	internal := ui.NewHTTPServer("", manager.InternalHandler())
	life.serve("the internal API", internal, internalListener)
	server := ui.NewHTTPServer("", dashboard.Handler(ctx))
	// the requests end as the controller stops, eg: the events streamed, or a
	// restart in flight, which launches nothing anymore (controller.ErrStopped)
	server.BaseContext = func(net.Listener) context.Context { return ctx }
	life.serve("the dashboard", server, listener)
	fmt.Printf("Dashboard available at http://%s\n", cfg.Listen)

	// the services may take minutes to start, eg: the terminal of MT5: the
	// dashboard serves meanwhile, the agents offline
	if _, err := controller.StartServices(ctx, services, cfg.Services, cfg.Agents.Dir); err != nil {
		life.fail(err)
	}
	// the accounts of MT5 of the bridges imported, and the terminals logged in
	// the active ones, before the agents are launched; kept so meanwhile
	dashboard.PrepareMT5Accounts(ctx)
	go dashboard.RunMT5Accounts(ctx)
	managed := make(chan struct{})
	go func() {
		manager.Run(ctx) // until ctx is done, then stops the agents
		close(managed)
	}()
	go supervision.Run(ctx) // the analyses of the agents, until ctx is done
	backtested := make(chan struct{})
	go func() {
		backtests.Run(ctx) // the backtests queued, until ctx is done, then the ones running interrupted
		close(backtested)
	}()

	<-ctx.Done()
	return life.stop(func() {
		// no request anymore, the ones in flight waited for while the manager
		// stops the agents, eg: a restart waiting for its agent to stop
		served := make(chan struct{})
		go func() {
			defer close(served)
			shutdown, cancelShutdown := context.WithTimeout(context.Background(),
				cfg.Agents.StopTimeout+2*time.Second)
			defer cancelShutdown()
			if err := server.Shutdown(shutdown); err != nil {
				_ = server.Close()
			}
		}()
		<-managed          // the agents stopped, their records sent through the internal API
		<-backtested       // the backtests interrupted, their processes gone
		services.StopAll() // after the agents, which use them, each with its stop timeout
		<-served
		_ = internal.Close()
	})
}

// lifecycle is the end of the controller: a server failing stops it as a
// signal does (cancel), its processes stopped in order, then it exits with an
// error; the whole stop within timeout, shutdown_timeout, as who stops the
// controller waits that long at most, eg: the stop_grace_period of the
// compose: past it, its processes are killed (kill) and it exits at once with
// an error (exit).
type lifecycle struct {
	cancel  context.CancelFunc
	timeout time.Duration
	kill    func()
	exit    func(code int)
	failed  atomic.Bool
}

// fail stops the controller for err, see lifecycle.
func (l *lifecycle) fail(err error) {
	log.Errorf("controller: %v: stopping", err)
	l.failed.Store(true)
	l.cancel()
}

// serve serves srv on listener, apart: anything but its close fails the
// controller.
func (l *lifecycle) serve(name string, srv *http.Server, listener net.Listener) {
	go func() {
		if err := srv.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
			l.fail(fmt.Errorf("%s: %w", name, err))
		}
	}()
}

// stop runs the stop of the controller, its context done, within the timeout,
// and returns its exit code: 1 once a server failed.
func (l *lifecycle) stop(stop func()) int {
	deadline := time.AfterFunc(l.timeout, func() {
		log.Errorf("controller: not stopped %s after the signal (shutdown_timeout): its processes killed",
			l.timeout)
		l.kill()
		l.exit(1)
	})
	defer deadline.Stop()
	stop()
	if l.failed.Load() {
		return 1
	}
	return 0
}
