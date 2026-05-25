// shipyard-comms is the messaging addon binary for the Shipyard ecosystem.
// In v1 it is stateless: each invocation reads its configuration from
// ~/.shipyard/comms/, executes a single operation and exits. No daemon,
// no socket. See addons/comms/docs/product.md.
//
// Subcommands (selected as the first positional argument):
//
//	(none)               print header
//	--version | -v       print just the version string
//	channel add          register a new channel + write its credentials
//	channel list         list configured channels
//	send                 deliver a message via the channel's provider
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/shipyard-auto/shipyard/addons/comms/internal/app"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run is the test-friendly entrypoint. It dispatches the first positional
// argument to the matching subcommand handler. Unknown subcommands fall
// through to the header (preserving the v1 behavior where bare invocation
// just printed identity).
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	// Top-level --version short-circuits before subcommand dispatch.
	for _, a := range args {
		if a == "--version" || a == "-v" {
			fmt.Fprintln(stdout, app.Version)
			return nil
		}
		// Stop scanning at the first non-flag token so e.g.
		// `shipyard-comms send --version` doesn't hijack the
		// subcommand.
		if len(a) > 0 && a[0] != '-' {
			break
		}
	}

	if len(args) == 0 {
		fmt.Fprintf(stdout, "%s (stub)\n", app.Info())
		return nil
	}

	switch args[0] {
	case "send":
		return runSend(ctx, args[1:], stdin, stdout, stderr)
	case "channel":
		return runChannel(ctx, args[1:], stdin, stdout, stderr)
	default:
		fmt.Fprintf(stdout, "%s (stub)\n", app.Info())
		return nil
	}
}
