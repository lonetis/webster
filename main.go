package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

// Release packages set this path relative to the user's home directory.
// Source builds use the working directory, including existing project snapshots.
var userDataRoot string

func run() error {
	args := os.Args[1:]
	if len(args) == 1 && args[0] == "_worker" {
		return serveWorker(os.Stdin, os.Stdout)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	fetch := len(args) > 0 && args[0] == "fetch"
	if fetch {
		args = args[1:]
	}
	flags := flag.NewFlagSet("webster", flag.ContinueOnError)
	var root string
	if userDataRoot != "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		root = filepath.Join(home, userDataRoot)
	}
	data := flags.String("data", filepath.Join(root, "data"), "directory for global CrUX snapshots")
	var input, dnsMode, provider string
	if !fetch {
		flags.StringVar(&input, "input", "", "use a local origin,rank CSV instead of the downloaded snapshot")
		flags.String("results", "", "deprecated and ignored (scan results are kept in memory)")
		flags.StringVar(&dnsMode, "dns", "system", "DNS mode for origin checks: system, recursive or iterative")
		flags.StringVar(&provider, "provider", "cloudflare", "recursive DNS provider: cloudflare, google, cloudflare-alt or google-alt")
	}
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: webster [options]  |  webster fetch [-data directory]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		flags.Usage()
		return fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if fetch {
		return fetchDataset(ctx, *data, os.Stdout)
	}
	if dnsMode != "system" && dnsMode != "recursive" && dnsMode != "iterative" {
		return fmt.Errorf("invalid DNS mode %q: choose system, recursive or iterative", dnsMode)
	}
	if _, ok := providers[provider]; !ok {
		return fmt.Errorf("unknown recursive provider %q: choose cloudflare, google, cloudflare-alt or google-alt", provider)
	}
	config := settings{targets: 1000, processes: 8, timeout: 10, mode: dnsMode, provider: provider}
	var err error
	var meta snapshot
	if input == "" {
		input, meta, err = latestDataset(*data)
		if err != nil {
			return err
		}
	}
	return dashboard(ctx, input, meta, config)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "webster:", err)
		os.Exit(1)
	}
}
