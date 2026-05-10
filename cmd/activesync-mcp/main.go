// Command activesync-mcp is the MCP server entry point.
//
// Subcommands:
//
//	serve          (default) run the stdio MCP server
//	keyring set    store an account password in the OS keyring
//	keyring get    report whether a keyring entry is set
//	keyring delete remove a keyring entry
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"activesync-mcp/lib/config"
	"activesync-mcp/lib/server"
	"activesync-mcp/lib/store"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// exit codes
const (
	exitOK       = 0
	exitConfig   = 2
	exitRuntime  = 3
	exitUsageErr = 64
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entry point. It parses argv (without the program name),
// dispatches to a subcommand, and returns an exit code.
func run(argv []string, stdout, stderr *os.File) int {
	configPath := config.DefaultPath()

	// Subcommand dispatch is positional: the first non-flag arg picks the
	// subcommand. A bare invocation runs `serve`.
	sub, rest := splitSubcommand(argv)
	switch sub {
	case "", "serve":
		return runServe(rest, &configPath, stderr)
	case "keyring":
		return runKeyring(rest, &configPath, stdout, stderr)
	case "autodiscover":
		return runAutodiscover(rest, stdout, stderr)
	case "doctor":
		return runDoctor(rest, &configPath, stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "activesync-mcp: unknown subcommand %q\n\n", sub)
		printUsage(stderr)
		return exitUsageErr
	}
}

// splitSubcommand pulls a leading positional arg out of argv; the rest is
// passed to the subcommand for further flag parsing.
func splitSubcommand(argv []string) (sub string, rest []string) {
	if len(argv) == 0 || (len(argv[0]) > 0 && argv[0][0] == '-') {
		return "", argv
	}
	return argv[0], argv[1:]
}

func runServe(argv []string, configPath *string, stderr *os.File) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(configPath, "config", *configPath, "path to config.toml")
	if err := fs.Parse(argv); err != nil {
		return exitUsageErr
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "activesync-mcp: %v\n", err)
		return exitConfig
	}

	db, err := store.Open(filepath.Join(cfg.StateDir, "state.db"))
	if err != nil {
		fmt.Fprintf(stderr, "activesync-mcp: %v\n", err)
		return exitConfig
	}
	defer db.Close()

	mgr, err := server.NewDefaultManager(cfg, db, hostnameSeed())
	if err != nil {
		fmt.Fprintf(stderr, "activesync-mcp: %v\n", err)
		return exitConfig
	}
	srv := server.Build(cfg, mgr)

	ctx, cancel := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer cancel()

	push := server.NewPushController(cfg, mgr, srv)
	defer push.Close()
	push.Start(ctx)

	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(stderr, "activesync-mcp: %v\n", err)
		return exitRuntime
	}
	return exitOK
}

// hostnameSeed returns a per-installation seed string for device-id
// derivation: hostname combined with the user's home directory. Stable
// across process restarts on the same machine; unique enough across
// installations that two laptops won't collide.
func hostnameSeed() string {
	host, _ := os.Hostname()
	home, _ := os.UserHomeDir()
	return host + "|" + home
}

func printUsage(w *os.File) {
	fmt.Fprintf(w, `activesync-mcp — MCP server bridging Claude to ActiveSync (Z-Push, SOGo)

Usage:
  activesync-mcp [serve] [--config PATH]
  activesync-mcp keyring set    --account NAME [--config PATH]
  activesync-mcp keyring get    --account NAME [--config PATH]
  activesync-mcp keyring delete --account NAME [--config PATH]
  activesync-mcp autodiscover --email EMAIL [--insecure]
  activesync-mcp doctor [--config PATH]

Default config path: %s

The 'serve' subcommand starts the MCP stdio server (this is the default).
The 'keyring' subcommands manage the OS-keyring entries referenced by each
account's secret = { keyring_service = ..., keyring_account = ... } block.
The 'autodiscover' subcommand probes Autodiscover endpoints for an email
address and prints a config snippet you can paste into config.toml. The
'doctor' subcommand validates the config and runs an HTTP OPTIONS request
against each account's server (no Provision, no state mutation).
`, config.DefaultPath())
}
