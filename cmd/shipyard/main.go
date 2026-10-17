package main

import (
	"errors"
	"log"
	"os"
	"os/exec"

	"github.com/shipyard-auto/shipyard/internal/cli"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/tty"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		// An addon reached through a passthrough (comms, comms whatsapp)
		// already explained itself on stderr; keep its exit code, which
		// scripts rely on (e.g. 3 = send refused by policy).
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() > 0 {
			os.Exit(ee.ExitCode())
		}
		log.Printf("shipyard: %v", err)
		if errors.Is(err, tty.ErrNonInteractive) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}
