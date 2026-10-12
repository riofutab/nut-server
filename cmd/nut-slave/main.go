package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"nut-server/internal/config"
	"nut-server/internal/logging"
	"nut-server/internal/shutdowncheck"
	"nut-server/internal/slave"
)

func main() {
	configPath := flag.String("config", "config/slave.yaml", "path to slave config")
	checkOnly := flag.Bool("check-config", false, "validate the config and the shutdown command permissions, then exit")
	flag.Parse()

	if *checkOnly {
		if err := checkConfig(*configPath); err != nil {
			fmt.Fprintf(os.Stderr, "config check failed: %s: %v\n", *configPath, err)
			os.Exit(1)
		}
		fmt.Printf("config ok: %s\n", *configPath)
		return
	}

	logger := logging.Init("nut-slave")

	cfg, err := config.LoadSlaveConfig(*configPath)
	if err != nil {
		logger.Error("load slave config failed", "path", *configPath, "err", err)
		os.Exit(1)
	}

	logger.Info("starting", "config", *configPath, "dry_run", cfg.DryRun)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client := slave.NewClient(cfg)
	if err := client.Run(ctx); err != nil {
		slog.Error("run slave failed", "err", err)
		os.Exit(1)
	}
}

// checkConfig loads the config and runs the same shutdown command self-check
// as startup, so install scripts can refuse to (re)start a broken service.
func checkConfig(path string) error {
	cfg, err := config.LoadSlaveConfig(path)
	if err != nil {
		return err
	}
	if cfg.DryRun {
		return nil
	}
	if err := shutdowncheck.Check(context.Background(), cfg.ShutdownCommand, nil); err != nil {
		return fmt.Errorf("shutdown_command self-check: %w", err)
	}
	return nil
}
