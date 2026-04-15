package main

import (
	"errors"
	"log"
	"os"

	"github.com/shipyard-auto/shipyard/internal/cli"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/tty"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		log.Printf("shipyard: %v", err)
		if errors.Is(err, tty.ErrNonInteractive) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}
