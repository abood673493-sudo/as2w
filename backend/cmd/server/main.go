// Command ssh-manager-panel-server is the entry point of the SSH Manager Panel.
//
// Usage:
//
//	ssh-manager-panel-server serve                     start the API server
//	ssh-manager-panel-server --migrate=up              apply pending migrations
//	ssh-manager-panel-server --migrate=down            revert the last migration
//	ssh-manager-panel-server --migrate=status          print migration status
//	ssh-manager-panel-server --version                 print build information
//
// Configuration is read from the process environment; a `.env` file in the
// working directory (or its parent) is loaded first when present.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/joho/godotenv"

	"github.com/sshmp/ssh-manager-panel/internal/config"
	"github.com/sshmp/ssh-manager-panel/internal/database"
	"github.com/sshmp/ssh-manager-panel/internal/logging"
	"github.com/sshmp/ssh-manager-panel/internal/server"
	"github.com/sshmp/ssh-manager-panel/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	loadDotEnv()

	flags := flag.NewFlagSet("ssh-manager-panel-server", flag.ContinueOnError)
	migrateMode := flags.String("migrate", "", "migration action: up|down|status")
	steps := flags.Int("steps", 0, "limit the number of migrations (0 = all pending for up, 1 for down)")
	printVersion := flags.Bool("version", false, "print build information and exit")
	if err := flags.Parse(args); err != nil {
		return err
	}

	if *printVersion {
		fmt.Printf("ssh-manager-panel %s (commit %s, built %s)\n",
			version.Version, version.Commit, version.Date)
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := logging.New(cfg.App.LogLevel, cfg.IsProduction())
	slog.SetDefault(logger)

	command := "serve"
	if rest := flags.Args(); len(rest) > 0 {
		command = rest[0]
	}
	if *migrateMode != "" {
		command = "migrate"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch command {
	case "serve":
		return runServe(ctx, cfg, logger)
	case "migrate":
		return runMigrate(ctx, cfg, logger, *migrateMode, *steps)
	case "config":
		return runConfig(cfg)
	default:
		return fmt.Errorf("unknown command %q (expected: serve, config, or --migrate=up|down|status)", command)
	}
}

func runServe(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
	db, err := openDatabase(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	if cfg.Database.MigrateOnStart {
		applied, err := db.Migrate(ctx, database.MigrateOptions{
			Direction: database.DirectionUp,
			Logger:    logger,
		})
		if err != nil {
			return fmt.Errorf("apply migrations: %w", err)
		}
		if len(applied) > 0 {
			logger.Info("database schema updated", "applied", applied)
		} else {
			logger.Info("database schema already up to date")
		}
	} else {
		pending, err := db.PendingMigrations(ctx)
		if err != nil {
			return fmt.Errorf("check pending migrations: %w", err)
		}
		if len(pending) > 0 {
			logger.Warn("database has pending migrations; start with --migrate=up or run `make migrate-up`",
				"pending", len(pending))
		}
	}

	logger.Debug("configuration loaded", "config", cfg.Redacted())

	srv := server.New(cfg, db, logger)
	return srv.ListenAndServe(ctx)
}

func runMigrate(ctx context.Context, cfg *config.Config, logger *slog.Logger, mode string, steps int) error {
	db, err := openDatabase(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "up":
		applied, err := db.Migrate(ctx, database.MigrateOptions{
			Direction: database.DirectionUp,
			Steps:     steps,
			Logger:    logger,
		})
		if err != nil {
			return err
		}
		if len(applied) == 0 {
			fmt.Println("no pending migrations")
		} else {
			fmt.Printf("applied %d migration(s): %s\n", len(applied), strings.Join(applied, ", "))
		}
		return nil

	case "down":
		reverted, err := db.Migrate(ctx, database.MigrateOptions{
			Direction: database.DirectionDown,
			Steps:     steps,
			Logger:    logger,
		})
		if err != nil {
			return err
		}
		if len(reverted) == 0 {
			fmt.Println("nothing to revert")
		} else {
			fmt.Printf("reverted %d migration(s): %s\n", len(reverted), strings.Join(reverted, ", "))
		}
		return nil

	case "status":
		statuses, err := db.Status(ctx)
		if err != nil {
			return err
		}
		sort.Slice(statuses, func(i, j int) bool { return statuses[i].Version < statuses[j].Version })
		fmt.Printf("%-10s %-32s %-10s %s\n", "VERSION", "NAME", "STATE", "APPLIED AT")
		for _, st := range statuses {
			state := "pending"
			if st.Applied {
				state = "applied"
			}
			if st.Applied && !st.ChecksumMatches {
				state = "MODIFIED"
			}
			appliedAt := st.AppliedAt
			if appliedAt == "" {
				appliedAt = "-"
			}
			fmt.Printf("%-10s %-32s %-10s %s\n", st.Version, st.Name, state, appliedAt)
		}
		for _, st := range statuses {
			if st.Applied && !st.ChecksumMatches {
				return fmt.Errorf("migration %s_%s was modified after being applied", st.Version, st.Name)
			}
		}
		return nil

	default:
		return fmt.Errorf("unknown --migrate value %q (expected up, down or status)", mode)
	}
}

func runConfig(cfg *config.Config) error {
	keys := cfg.Redacted()
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Printf("%-22s %v\n", name, keys[name])
	}
	return nil
}

func openDatabase(ctx context.Context, cfg *config.Config) (*database.DB, error) {
	db, err := database.Open(ctx, database.Options{
		URL:             cfg.Database.URL,
		MaxConns:        cfg.Database.MaxConns,
		MinConns:        cfg.Database.MinConns,
		ConnTimeout:     cfg.Database.ConnTimeout,
		ApplicationName: "ssh-manager-panel",
	})
	if err != nil {
		return nil, err
	}
	return db, nil
}

// loadDotEnv loads the first `.env` file found in the working directory or one
// of its parents. godotenv never overwrites variables that are already set, so
// systemd Environment= values always win.
func loadDotEnv() {
	for _, path := range []string{".env", "../.env", "../../.env"} {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			_ = godotenv.Load(path)
			return
		}
	}
}