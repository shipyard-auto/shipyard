package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	fairwayapp "github.com/shipyard-auto/shipyard/addons/fairway/internal/app"
	"github.com/shipyard-auto/shipyard/addons/fairway/internal/fairway"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("shipyard-fairway", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var configPath string
	var socketPath string
	var pidfilePath string
	var shipyardBinary string
	var showVersion bool
	var showHelp bool

	fs.StringVar(&configPath, "config", "", "path to config.json")
	fs.StringVar(&socketPath, "socket", "", "path to fairway control socket")
	fs.StringVar(&pidfilePath, "pidfile", "", "path to fairway pidfile")
	fs.StringVar(&shipyardBinary, "shipyard-bin", "", "path to shipyard binary")
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

	if showHelp {
		fs.Usage()
		return 0
	}

	if showVersion {
		_, _ = fmt.Fprintln(stdout, fairwayapp.Info())
		return 0
	}

	daemon, err := fairway.NewDaemon(fairway.BootstrapConfig{
		ConfigPath:     configPath,
		SocketPath:     socketPath,
		PIDFilePath:    pidfilePath,
		ShipyardBinary: shipyardBinary,
		Version:        fairwayapp.Version,
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "failed to configure fairway daemon: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := daemon.Run(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "fairway daemon failed: %v\n", err)
		return 1
	}

	return 0
}
