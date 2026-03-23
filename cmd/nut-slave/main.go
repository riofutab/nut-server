package main

import (
	"flag"
	"log"

	"nut-server/internal/config"
	"nut-server/internal/logging"
	"nut-server/internal/slave"
)

func main() {
	configPath := flag.String("config", "config/slave.yaml", "path to slave config")
	flag.Parse()

	cfg, err := config.LoadSlaveConfig(*configPath)
	if err != nil {
		log.Fatalf("load slave config: %v", err)
	}

	logger := logging.New("nut-slave")
	logger.Printf("starting with config %s dry_run=%t", *configPath, cfg.DryRun)

	client := slave.NewClient(cfg)
	if err := client.Run(); err != nil {
		logger.Fatalf("run slave: %v", err)
	}
}
