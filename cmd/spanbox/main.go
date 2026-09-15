package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/Ray0907/spanbox/internal/config"
	"github.com/Ray0907/spanbox/internal/pricing"
)

var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := pricing.Load(cfg.PricingFile); err != nil {
		log.Fatal(err)
	}
	log.Printf("spanbox %s configuration loaded on port %d", version, cfg.Port)
}
