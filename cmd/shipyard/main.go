package main

import (
	"log"
	"os"

	"github.com/shipyard-auto/shipyard/internal/cli"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		log.Printf("shipyard: %v", err)
		os.Exit(1)
	}
}
