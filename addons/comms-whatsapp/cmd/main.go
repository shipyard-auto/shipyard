// shipyard-comms-whatsapp links this machine to the user's WhatsApp as a
// companion device ("Linked devices") and keeps a local, TTL-bound archive
// of the chats the user chooses to watch.
//
// Unlike shipyard-comms (stateless, one operation per run), WhatsApp needs
// a persistent connection, so this addon has a daemon (`serve`) that runs
// under `shipyard service`. The other subcommands work with the daemon
// running or stopped: they read and write the same SQLite files.
//
// Subcommands:
//
//	--version | -v        print the version
//	pair [--phone N]      link a device (QR code, or 8-char code by phone number)
//	serve                 run the daemon (what the service manager runs)
//	status [--json]       pairing, daemon and archive summary
//	chats                 list known chats
//	watch <chat>          start archiving a chat
//	unwatch <chat>        stop archiving a chat and delete its history
//	history <chat>        print archived messages
//	send <chat> --text T  send a message through the daemon's policy
//	allow|deny <chat>     edit the send allowlist
//	pause|resume          stop or restart all sending
//	tool <name>           crew exec tool (JSON stdin → envelope stdout)
//	tools install         write wa_* tool definitions for crew
//	logout [--local]      unlink the device and delete the local keys
//	config [set K V]      show or change settings
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/app"
	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "shipyard-comms-whatsapp:", err)
		var exit *wa.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.Code)
		}
		os.Exit(1)
	}
}

// env bundles what every subcommand needs; tests build one against a
// temp directory.
type env struct {
	paths  wa.Paths
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) > 0 && (args[0] == "--version" || args[0] == "-v" || args[0] == "version") {
		fmt.Fprintln(stdout, app.Version)
		return nil
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printHelp(stdout)
		return nil
	}
	paths, err := wa.DefaultPaths()
	if err != nil {
		return err
	}
	e := env{paths: paths, stdin: stdin, stdout: stdout, stderr: stderr}
	return dispatch(ctx, e, args)
}

func dispatch(ctx context.Context, e env, args []string) error {
	rest := args[1:]
	switch args[0] {
	case "pair":
		return runPair(ctx, e, rest)
	case "serve":
		return runServe(ctx, e, rest)
	case "status":
		return runStatus(ctx, e, rest)
	case "chats":
		return runChats(ctx, e, rest)
	case "watch":
		return runWatch(ctx, e, rest)
	case "unwatch":
		return runUnwatch(ctx, e, rest)
	case "history":
		return runHistory(ctx, e, rest)
	case "send":
		return runSend(ctx, e, rest)
	case "allow":
		return runAllow(ctx, e, rest, true)
	case "deny":
		return runAllow(ctx, e, rest, false)
	case "pause":
		return runPause(ctx, e, rest, true)
	case "resume":
		return runPause(ctx, e, rest, false)
	case "tool":
		return runTool(ctx, e, rest)
	case "tools":
		if len(rest) == 0 || rest[0] != "install" {
			return errors.New("usage: tools install [--dir D] [--force]")
		}
		return runToolsInstall(ctx, e, rest[1:])
	case "logout":
		return runLogout(ctx, e, rest)
	case "config":
		return runConfig(ctx, e, rest)
	default:
		printHelp(e.stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func printHelp(w io.Writer) {
	fmt.Fprintf(w, `%s

Usage: shipyard comms whatsapp <command> [flags]

Setup
  pair [--phone +55...] [--force]   link this machine under "Linked devices"
  logout [--local]                  unlink and delete the local keys
  config                            show settings
  config set <key> <value>          device-name | default-ttl | limit-per-chat |
                                    limit-global | max-chars | humanize

Daemon
  serve                             run the daemon (normally as a service)
  status [--json]                   pairing, daemon and archive summary

Archive
  chats [--search q] [--watched] [--json]
  watch <chat> [--ttl 7d]           archive a chat (name, JID or phone number)
  unwatch <chat>                    stop archiving and delete its history
  history <chat> [--since 24h] [--limit 200] [--json]

Sending (through the running daemon)
  send <chat> (--text T | --stdin) [--idempotency-key K] [--dry-run] [--json]
  allow <chat> | deny <chat>        send allowlist (contacts only, never groups)
  pause | resume                    stop or restart all sending immediately
  tools install [--force]           wa_send / wa_chats / wa_history for crew
  tool <send|chats|history>         the crew exec contract (stdin → envelope)

Messages are only archived for watched chats and expire after their TTL
(default %s, max %s). A send needs the chat on the allowlist and a prior
conversation; refusals exit with code 3.
`, app.Info(), wa.FormatDuration(wa.DefaultTTL), wa.FormatDuration(wa.MaxTTL))
}
