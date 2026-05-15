package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"nut-server/internal/config"
	"nut-server/internal/logging"
	"nut-server/internal/slave"
)

func main() {
	configPath := flag.String("config", "config/slave.yaml", "path to slave config")
	flag.Parse()

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
