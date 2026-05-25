// shipyard-comms is the messaging addon binary for the Shipyard ecosystem.
// In v1 it is stateless: each invocation reads its configuration from
// ~/.shipyard/comms/, executes a single operation (send / channel mgmt) and
// exits. No daemon, no socket. See addons/comms/docs/product.md.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/shipyard-auto/shipyard/addons/comms/internal/app"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run is the test-friendly entrypoint. CM-02..CM-04 grow this; for now it
// only honors --version and prints a header. Real subcommands (channel
// add/list, send, normalize, status) arrive in épico 2.
func run(args []string, stdout, _ io.Writer) error {
	for _, a := range args {
		if a == "--version" || a == "-v" {
			fmt.Fprintln(stdout, app.Version)
			return nil
		}
	}
	fmt.Fprintf(stdout, "%s (stub)\n", app.Info())
	return nil
}
