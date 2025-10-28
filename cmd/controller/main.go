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
// the first one on an empty database, or cmd/migrator import brings the one of
// the previous version. Telegram, its token and the users it answers, the
// agents, their prompts and the users are set on the dashboard, applied at
// once: an agent enabled is launched, one disabled stopped, a prompt switched
// from the next analysis of the agent.
//
// services of config.yml are the processes of the host the agents need, eg:
// the bridge of MT5 out of a container, launched before the agents and stopped
// after them.
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
	"syscall"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/raykavin/xtrader/config"
	"github.com/raykavin/xtrader/controller"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/tools/log"
)

func main() {
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

	options := []controller.DashboardOption{controller.WithUsers(st)}
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

	agents := controller.NewSupervisor(os.Stdout, os.Stderr, cfg.Agents.StopTimeout)
	var manager *controller.Manager
	bot := controller.NewTelegramRunner(ctx, dashboard, controller.WithPrompts(st, func() { manager.Trigger() }))
	manager = controller.NewManager(st, cfg.Agents, agents, dashboard, bot)
	dashboard.ServeSettings(st, manager, bot)

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
	internal := &http.Server{Handler: manager.InternalHandler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := internal.Serve(internalListener); !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	server := &http.Server{Handler: dashboard.Handler(ctx), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	fmt.Printf("Dashboard available at http://%s\n", cfg.Listen)

	// the services may take minutes to start, eg: the terminal of MT5: the
	// dashboard serves meanwhile, the agents offline
	services := controller.NewSupervisor(os.Stdout, os.Stderr, cfg.Agents.StopTimeout)
	if _, err := controller.StartServices(ctx, services, cfg.Services, cfg.Agents.Dir); err != nil {
		log.Fatal(err)
	}
	managed := make(chan struct{})
	go func() {
		manager.Run(ctx) // until ctx is done, then stops the agents
		close(managed)
	}()

	<-ctx.Done()
	_ = server.Close()
	<-managed          // the agents stopped, their records sent through the internal API
	services.StopAll() // after the agents, which use them
	_ = internal.Close()
}
