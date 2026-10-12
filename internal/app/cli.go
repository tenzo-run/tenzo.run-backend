package app

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"tenzo-run-backend/internal/config"
	"tenzo-run-backend/internal/lead"
	"tenzo-run-backend/internal/shared/logger"
	"tenzo-run-backend/internal/shared/postgres"
)

const defaultConfigPath = "config.yaml"

const usage = `usage: tenzo <command> [flags]

commands:
  serve                      run the http server (default)
  migrate up|down|status     manage database migrations
  leads [-limit 20]          print the latest project requests

run "tenzo <command> -h" to see command flags`

func Run(ctx context.Context, args []string) error {
	command, rest := "serve", args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, rest = args[0], args[1:]
	}

	switch command {
	case "serve":
		return runServe(ctx, rest)
	case "migrate":
		return runMigrate(ctx, rest)
	case "leads":
		return runLeads(ctx, rest)
	case "help":
		fmt.Println(usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", command, usage)
	}
}

type command struct {
	flags      *flag.FlagSet
	configPath *string
}

func newCommand(name string) *command {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	return &command{
		flags:      fs,
		configPath: fs.String("config", defaultConfigPath, "path to the yaml config file"),
	}
}

func (c *command) load(args []string) (config.Config, *slog.Logger, error) {
	if err := c.flags.Parse(args); err != nil {
		return config.Config{}, nil, err
	}

	explicit := false
	c.flags.Visit(func(f *flag.Flag) { explicit = explicit || f.Name == "config" })

	cfg, err := config.Load(*c.configPath, explicit)
	if err != nil {
		return config.Config{}, nil, err
	}

	log := logger.New(os.Stdout, cfg.Log.Level, cfg.Log.Format)
	slog.SetDefault(log)

	return cfg, log, nil
}

func runServe(ctx context.Context, args []string) error {
	cfg, log, err := newCommand("serve").load(args)
	if err != nil {
		return err
	}

	log.InfoContext(ctx, "starting", slog.String("version", Version), slog.String("env", cfg.Env))
	return serve(ctx, cfg, log)
}

func runMigrate(ctx context.Context, args []string) error {
	direction := "up"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		direction, args = args[0], args[1:]
	}

	cfg, log, err := newCommand("migrate").load(args)
	if err != nil {
		return err
	}

	m, err := postgres.NewMigrator(cfg.Database.URL, log)
	if err != nil {
		return err
	}
	defer m.Close()

	switch direction {
	case "up":
		return m.Up(ctx)
	case "down":
		return m.Down(ctx)
	case "status":
		return m.Status(ctx)
	default:
		return fmt.Errorf("unknown migrate direction %q, expected up, down or status", direction)
	}
}

func runLeads(ctx context.Context, args []string) error {
	cmd := newCommand("leads")
	limit := cmd.flags.Int("limit", lead.DefaultListLimit, fmt.Sprintf("how many latest leads to print, 1..%d", lead.MaxListLimit))

	cfg, log, err := cmd.load(args)
	if err != nil {
		return err
	}

	pool, err := postgres.Connect(ctx, cfg.Database, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	return printLatestLeads(ctx, lead.NewService(lead.NewRepository(pool), nil), *limit, os.Stdout)
}
