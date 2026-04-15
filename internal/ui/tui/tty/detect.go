package tty

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

var (
	ErrNonInteractive = errors.New("interactive terminal required")
	StdinFD           = func() uintptr { return os.Stdin.Fd() }
)

func IsInteractive(fd uintptr) bool {
	return term.IsTerminal(int(fd))
}

func RequireTTY(_ io.Writer, stderr io.Writer) error {
	if IsInteractive(StdinFD()) {
		return nil
	}

	if stderr != nil {
		_, _ = fmt.Fprintln(stderr, "This command requires an interactive terminal.")
		_, _ = fmt.Fprintln(stderr, "Use the non-interactive commands instead, for example:")
		_, _ = fmt.Fprintln(stderr, "  shipyard cron add --name ... --schedule ... --command ...")
	}
	return ErrNonInteractive
}
