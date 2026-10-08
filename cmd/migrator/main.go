// Command migrator prepares the PostgreSQL of xtrader, the database of
// configs/config.yml, the one of the controller:
//
//	# the migrations of sql/migrations, then the seeds of sql/seeds (the
//	# prompts, the Telegram bot off), run again harmlessly
//	go run ./cmd/migrator -config configs/config.yml up
//	# the settings and the data of the previous version, once: the agents of
//	# its agents.yml, the users, Telegram and the keys of its environment file,
//	# the orders, decisions and journals of its data/ (read from copies); run
//	# again, only what is missing, what the dashboard deleted or changed left
//	# as it is; the data of an agent that has orders already is a conflict
//	# (nothing imported) unless -force, which moves its order IDs
//	go run ./cmd/migrator -config configs/config.yml import -agents agents.yml \
//		-env ~/.config/automator/automator.env
//	# the same, nothing written: what would be imported
//	go run ./cmd/migrator -config configs/config.yml import -agents agents.yml -env automator.env -dry-run
//	# the same into the containers (deploy/docker), whose terminal is the
//	# service mt5: the agents.yml, the environment file and the data/ of the
//	# host copied to services/migrator/config/import, the bridge of every
//	# agent and the MCP server of the calendars replaced by the ones of mt5
//	docker compose run --rm migrator migrator -config /app/config/config.yml import \
//		-agents /app/config/import/agents.yml -env /app/config/import/import.env \
//		-bridge http://mt5:18812 -calendar http://mt5:22347/mcp
//
// The controller never migrates: it checks the version of the schema when it
// starts, and asks for this command when it is behind. The secrets imported
// are encrypted with the secrets_key of the configuration, never printed.
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
		_, _ = fmt.Fprintf(out, "usage: %s [-config file] up | import [flags]\n\n", os.Args[0])
		_, _ = fmt.Fprintln(out, "  up      apply the migrations and the seeds")
		_, _ = fmt.Fprintln(out, "  import  import the settings and the data of the previous version (import -h)")
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
	case "import":
		err = runImport(ctx, cfg, flag.Args()[1:], os.Stdout)
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
