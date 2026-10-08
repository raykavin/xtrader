// Command migrator prepares the PostgreSQL of xtrader, the database of
// configs/config.yml, the one of the controller:
//
//	# the migrations of sql/migrations, then the seeds of sql/seeds (the
//	# Telegram bot off, the prompts and the agents of a new database, off and
//	# without their secrets), run again harmlessly
//	go run ./cmd/migrator -config configs/config.yml up
//
// The settings live in the database only: the agents, their secrets, the
// users and Telegram are changed on the page of the settings of the
// dashboard. The controller never migrates: it checks the version of the
// schema when it starts, and asks for this command when it is behind.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/raykavin/gobox/database/migrate"

	"github.com/raykavin/xtrader/config"
	"github.com/raykavin/xtrader/store"
	"github.com/raykavin/xtrader/tools/log"
)

func main() {
	configFile := flag.String("config", "configs/config.yml", "configuration of the controller: its database, "+
		"migrations and secrets key")
	flag.Usage = func() {
		out := flag.CommandLine.Output()
		_, _ = fmt.Fprintf(out, "usage: %s [-config file] up\n\n", os.Args[0])
		_, _ = fmt.Fprintln(out, "  up  apply the migrations and the seeds")
		_, _ = fmt.Fprintln(out)
		flag.PrintDefaults()
	}
	flag.Parse()

	cfg, err := config.Load(*configFile)
	if err != nil {
		log.Fatal(err)
	}
	for _, warning := range cfg.Warnings {
		log.Warnf("config %s: %s", *configFile, warning)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	switch flag.Arg(0) {
	case "up":
		err = up(ctx, cfg)
	default:
		flag.Usage()
		os.Exit(2)
	}
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		log.Fatal(err)
	}
}

// up applies the migrations and then the seeds, and checks the schema the
// store needs.
func up(ctx context.Context, cfg *config.Config) error {
	m, err := migrate.New(migrate.MigrateConfig{DSN: cfg.Database.DSN, Dialector: "postgres",
		MigrationsPath: cfg.Migrations.Path, PopulationPath: cfg.Migrations.Seeds})
	if err != nil {
		return fmt.Errorf("migrator: %w", err)
	}
	if err := m.Migrate(ctx); err != nil {
		return fmt.Errorf("migrator: %w", err)
	}
	log.Infof("[MIGRATOR] migrations of %s applied", cfg.Migrations.Path)
	if cfg.Migrations.Seeds != "" {
		if err := m.Populate(ctx); err != nil {
			return fmt.Errorf("migrator: %w", err)
		}
		log.Infof("[MIGRATOR] seeds of %s applied", cfg.Migrations.Seeds)
	}

	key, err := cfg.Key()
	if err != nil {
		return err
	}
	s, err := store.Open(cfg.Database, key)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	if err := s.CheckSchema(ctx); err != nil {
		return err
	}
	log.Infof("[MIGRATOR] schema at version %d at least, as the controller needs", store.SchemaVersion)
	return nil
}
