package main

import (
	"flag"
	"log"

	"nut-server/internal/config"
	"nut-server/internal/logging"
	"nut-server/internal/master"
)

func main() {
	configPath := flag.String("config", "config/master.yaml", "path to master config")
	flag.Parse()

	cfg, err := config.LoadMasterConfig(*configPath)
	if err != nil {
		log.Fatalf("load master config: %v", err)
	}

	logger := logging.New("nut-master")
	logger.Printf("starting with config %s dry_run=%t", *configPath, cfg.DryRun)

	server := master.NewServer(cfg)
	if err := server.Run(); err != nil {
		logger.Fatalf("run master: %v", err)
	}
}
