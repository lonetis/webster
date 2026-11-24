package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
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
	return command(ctx, args, os.Stdout, os.Stderr)
}

func command(ctx context.Context, args []string, out, log io.Writer) error {
	name := "webster"
	if len(args) > 0 && slices.Contains([]string{"fetch", "speedtest", "scan"}, args[0]) {
		name += " " + args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(log)
	flags.Usage = func() {
		switch name {
		case "webster":
			fmt.Fprintln(log, "Usage: webster [options]                Open the interactive dashboard")
			fmt.Fprintln(log, "       webster scan [options]           Start a measurement immediately")
			fmt.Fprintln(log, "       webster fetch [-data directory]   Download the latest CrUX snapshot")
			fmt.Fprintln(log, "       webster speedtest [options]      Measure HTTPS latency and throughput")
		case "webster scan":
			fmt.Fprintln(log, "Usage: webster scan [options]\nStart an HTTP or DNS measurement and print the results.")
		case "webster fetch":
			fmt.Fprintln(log, "Usage: webster fetch [-data directory]")
		case "webster speedtest":
			fmt.Fprintln(log, "Usage: webster speedtest [options]\nMeasure HTTPS latency, download and upload speed to Apple’s network-quality service.")
		}
		flags.PrintDefaults()
	}
	parse := func() error {
		if err := flags.Parse(args); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return fmt.Errorf("unexpected argument %q (see %s -help)", flags.Arg(0), name)
		}
		return nil
	}
	if name == "webster speedtest" {
		options := speedFlags(flags, "")
		if err := parse(); err != nil {
			return err
		}
		if err := options.validate(); err != nil {
			return err
		}
		return printSpeedTest(ctx, out, *options)
	}
	var root string
	if userDataRoot != "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		root = filepath.Join(home, userDataRoot)
	}
	data := flags.String("data", filepath.Join(root, "data"), "directory for global CrUX snapshots")
	if name == "webster fetch" {
		if err := parse(); err != nil {
			return err
		}
		return fetchDataset(ctx, *data, out)
	}

	var config settings
	var speed *speedOptions
	if name == "webster" {
		speed = speedFlags(flags, "speed-")
	}
	profile := flags.String("config", "", "read measurement defaults from a JSON file (explicit flags take precedence)")
	input := flags.String("input", "", "use a local origin,rank CSV instead of the downloaded snapshot")
	targets := flags.String("targets", "1000", "number of origins to check (all or 0 for the entire input)")
	flags.IntVar(&config.processes, "processes", 8, "number of worker processes")
	timeout := flags.String("timeout", "10s", "per-origin deadline in whole seconds, e.g. 5 or 5s")
	flags.BoolVar(&config.dnsOnly, "dns-only", false, "only look up each origin's IPv4 address (DNS A record)")
	flags.StringVar(&config.mode, "dns", "system", "DNS mode for origin checks: system, recursive or iterative")
	provider := flags.String("provider", "cloudflare", "recursive DNS endpoints: all, or a comma-separated list of cloudflare, google, cloudflare-alt and google-alt")
	flags.String("results", "", "deprecated and ignored (scan results are kept in memory)")
	if err := parse(); err != nil {
		return err
	}

	if *profile != "" {
		if err := applyConfig(flags, *profile); err != nil {
			return err
		}
	}
	var err error
	if *targets != "all" {
		config.targets, err = strconv.Atoi(*targets)
		if err != nil || config.targets < 0 {
			return fmt.Errorf("invalid -targets %q: use a positive count or all", *targets)
		}
	}
	if config.processes < 1 {
		return errors.New("-processes must be at least 1")
	}
	config.timeout, err = strconv.Atoi(strings.TrimSuffix(*timeout, "s"))
	if err != nil || config.timeout < 1 || int64(config.timeout) > maxTimeoutSeconds {
		return fmt.Errorf("invalid -timeout %q: use a positive whole number of seconds up to %d", *timeout, maxTimeoutSeconds)
	}
	if !slices.Contains(dnsModes, config.mode) {
		return fmt.Errorf("invalid DNS mode %q: choose system, recursive or iterative", config.mode)
	}
	config.pool, err = parseProviderPool(*provider)
	if err != nil {
		return err
	}
	if speed != nil {
		if err := speed.validate(); err != nil {
			return err
		}
	}
	var meta snapshot
	if *input == "" {
		*input, meta, err = latestDataset(*data)
		if err != nil {
			return err
		}
	}
	if name == "webster scan" {
		return printScan(ctx, out, log, *input, meta, config)
	}
	return dashboard(ctx, out, *input, meta, config, *speed)
}

// Use the same flag setters for saved values and command-line values.
func applyConfig(flags *flag.FlagSet, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var options map[string]json.RawMessage
	if err := json.Unmarshal(data, &options); err != nil {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	if options == nil {
		return fmt.Errorf("config %s must be a JSON object", path)
	}
	explicit := make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	for key, raw := range options {
		switch key {
		case "data", "input", "targets", "processes", "timeout", "dns", "provider", "dns-only":
		default:
			return fmt.Errorf("config %s: unknown measurement option %q", path, key)
		}
		if explicit[key] {
			continue
		}
		value := string(raw)
		switch raw[0] {
		case '"':
			if err := json.Unmarshal(raw, &value); err != nil {
				return fmt.Errorf("config %s, %s: %w", path, key, err)
			}
		case '{', '[', 'n':
			return fmt.Errorf("config %s: %s requires a string, number or boolean", path, key)
		}
		if err := flags.Set(key, value); err != nil {
			return fmt.Errorf("config %s: %w", path, err)
		}
	}
	return nil
}

const maxTimeoutSeconds = int64((1<<63 - 1) / time.Second)

func main() {
	if err := run(); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "webster:", err)
		os.Exit(1)
	}
}
