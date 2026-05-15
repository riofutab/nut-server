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
	"nut-server/internal/master"
)

func main() {
	configPath := flag.String("config", "config/master.yaml", "path to master config")
	flag.Parse()

	logger := logging.Init("nut-master")

	cfg, err := config.LoadMasterConfig(*configPath)
	if err != nil {
		logger.Error("load master config failed", "path", *configPath, "err", err)
		os.Exit(1)
	}

	logger.Info("starting", "config", *configPath, "dry_run", cfg.DryRun)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	server := master.NewServer(cfg)
	if err := server.Run(ctx); err != nil {
		slog.Error("run master failed", "err", err)
		os.Exit(1)
	}
}
