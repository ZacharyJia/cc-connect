package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ZacharyJia/cx-connect/config"
	"github.com/ZacharyJia/cx-connect/forgejowatch"
)

func runForgejoWatch(args []string) {
	if len(args) == 0 {
		printForgejoWatchUsage()
		os.Exit(1)
	}

	switch args[0] {
	case "run":
		runForgejoWatchRun(args[1:])
	case "list":
		runForgejoWatchList(args[1:])
	case "help", "--help", "-h":
		printForgejoWatchUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown forgejo-watch subcommand: %s\n\n", args[0])
		printForgejoWatchUsage()
		os.Exit(1)
	}
}

func runForgejoWatchRun(args []string) {
	fs := flag.NewFlagSet("forgejo-watch run", flag.ExitOnError)
	configFile := fs.String("config", "", "path to config file")
	name := fs.String("name", "", "watcher name")
	once := fs.Bool("once", false, "run only one sync cycle")
	fs.Parse(args)

	if *name == "" {
		fmt.Fprintln(os.Stderr, "Error: --name is required")
		os.Exit(1)
	}

	cfg, watcherCfg := mustLoadForgejoWatcherConfig(*configFile, *name)
	runner, err := buildForgejoWatcherRunner(cfg, watcherCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if *once {
		if err := runner.Sync(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := runner.Run(ctx); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func runForgejoWatchList(args []string) {
	fs := flag.NewFlagSet("forgejo-watch list", flag.ExitOnError)
	configFile := fs.String("config", "", "path to config file")
	fs.Parse(args)

	cfg, err := config.Load(resolveConfigPath(*configFile))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	if len(cfg.ForgejoWatchers) == 0 {
		fmt.Println("No forgejo watchers configured.")
		return
	}

	for _, watcher := range cfg.ForgejoWatchers {
		statePath := filepath.Join(cfg.DataDir, "forgejo-watch", watcher.Name+".json")
		summary, err := forgejowatch.LoadSummary(statePath, watcher.Name)
		if err != nil {
			fmt.Printf("- %s: state error: %v\n", watcher.Name, err)
			continue
		}
		lastPoll := "-"
		if !summary.LastPollAt.IsZero() {
			lastPoll = summary.LastPollAt.Format(time.RFC3339)
		}
		fmt.Printf("- %s  tracked=%d clusters=%d pending_clusters=%d pending_events=%d last_poll=%s\n",
			summary.Name,
			summary.TrackedCount,
			summary.ClusterCount,
			summary.PendingCluster,
			summary.PendingCount,
			lastPoll,
		)
	}
}

func buildForgejoWatcherRunner(cfg *config.Config, raw config.ForgejoWatcherConfig) (*forgejowatch.Runner, error) {
	token := raw.Token
	if token == "" && raw.TokenEnv != "" {
		token = os.Getenv(raw.TokenEnv)
	}
	if token == "" {
		return nil, fmt.Errorf("watcher %q token is empty", raw.Name)
	}

	pollInterval := time.Minute
	if raw.PollInterval != "" {
		d, err := time.ParseDuration(raw.PollInterval)
		if err != nil {
			return nil, fmt.Errorf("watcher %q poll_interval: %w", raw.Name, err)
		}
		pollInterval = d
	}

	statePath := filepath.Join(cfg.DataDir, "forgejo-watch", raw.Name+".json")
	return forgejowatch.NewRunner(forgejowatch.Config{
		Name:                  raw.Name,
		BaseURL:               raw.BaseURL,
		Token:                 token,
		Username:              raw.Username,
		SessionKey:            raw.SessionKey,
		PollInterval:          pollInterval,
		TriggerOnSelfActivity: raw.TriggerOnSelfActivity,
		WorkDir:               raw.WorkDir,
		State:                 raw.State,
	}, statePath, resolveSocketPath(cfg.DataDir))
}

func mustLoadForgejoWatcherConfig(configFile, name string) (*config.Config, config.ForgejoWatcherConfig) {
	cfg, err := config.Load(resolveConfigPath(configFile))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	for _, watcher := range cfg.ForgejoWatchers {
		if watcher.Name == name {
			return cfg, watcher
		}
	}

	fmt.Fprintf(os.Stderr, "Error: forgejo watcher %q not found\n", name)
	os.Exit(1)
	return nil, config.ForgejoWatcherConfig{}
}

func printForgejoWatchUsage() {
	fmt.Println(`Usage: cx-connect forgejo-watch <command> [options]

Commands:
  run     Run a configured Forgejo watcher
  list    List configured watchers and local state summary

Examples:
  cx-connect forgejo-watch list
  cx-connect forgejo-watch run --name ops
  cx-connect forgejo-watch run --name ops --once`)
}
