package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	fairwayapp "github.com/shipyard-auto/shipyard/addons/fairway/internal/app"
)

const stubMessage = "daemon stub — implementado nas tarefas seguintes"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("shipyard-fairway", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var configPath string
	var showVersion bool
	var showHelp bool

	fs.StringVar(&configPath, "config", "", "path to config.json")
	fs.BoolVar(&showVersion, "version", false, "print version and exit")
	fs.BoolVar(&showHelp, "help", false, "show help and exit")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: %s [flags]\n\n", fs.Name())
		fmt.Fprintln(stderr, "Flags:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return 2
	}

	_ = configPath

	if showHelp {
		fs.Usage()
		return 0
	}

	if showVersion {
		_, _ = fmt.Fprintln(stdout, fairwayapp.Info())
		return 0
	}

	_, _ = fmt.Fprintln(stdout, stubMessage)
	return 0
}
