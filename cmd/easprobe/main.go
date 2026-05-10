// Command easprobe runs OPTIONS, Provision, and FolderSync against a real
// EAS server using credentials from an activesync-mcp config file. It is
// the smoke-test CLI for Phase 3 — useful when bringing up a Z-Push or
// SOGo instance to confirm the client speaks the protocol correctly.
//
// Usage:
//
//	easprobe -account NAME [-config PATH] [-debug]
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"activesync-mcp/lib/config"
	"github.com/hstern/go-activesync/eas"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("easprobe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", config.DefaultPath(), "path to config.toml")
	accountName := fs.String("account", "", "account name from config (required)")
	debug := fs.Bool("debug", false, "enable verbose debug logging to stderr")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	if *accountName == "" {
		fmt.Fprintln(stderr, "easprobe: -account is required")
		return 2
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "easprobe: %v\n", err)
		return 2
	}
	acct := cfg.FindAccount(*accountName)
	if acct == nil {
		fmt.Fprintf(stderr, "easprobe: no account named %q\n", *accountName)
		return 2
	}

	logLevel := slog.LevelInfo
	if *debug {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: logLevel}))

	ctx, cancel := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer cancel()

	pw, err := config.DefaultResolver().Resolve(ctx, acct)
	if err != nil {
		fmt.Fprintf(stderr, "easprobe: %v\n", err)
		return 3
	}

	httpClient := &http.Client{}
	if acct.AllowInsecure {
		httpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}

	deviceID := acct.DeviceID
	if deviceID == "" {
		deviceID = "easprobe000000000000000000000000"
	}

	c, err := eas.NewClient(eas.Config{
		ServerURL:  acct.ServerURL,
		Username:   acct.Username,
		Password:   pw,
		DeviceID:   deviceID,
		DeviceType: acct.DeviceType,
		ASVersion:  acct.ASVersion,
		UserAgent:  acct.UserAgent,
		HTTPClient: httpClient,
		Logger:     logger,
		State:      eas.NewMemoryState(), // one-shot run; no persistence needed
	})
	if err != nil {
		fmt.Fprintf(stderr, "easprobe: %v\n", err)
		return 3
	}

	fmt.Fprintln(stdout, "==> OPTIONS")
	opts, err := c.Options(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "easprobe: OPTIONS: %v\n", err)
		return 3
	}
	fmt.Fprintf(stdout, "    versions: %v\n", opts.ProtocolVersions)
	fmt.Fprintf(stdout, "    commands: %v\n", opts.Commands)
	if !opts.Supports(acct.ASVersion) {
		fmt.Fprintf(stderr, "easprobe: server does not advertise version %s\n", acct.ASVersion)
	}

	fmt.Fprintln(stdout, "==> Provision")
	if err := c.Provision(ctx); err != nil {
		fmt.Fprintf(stderr, "easprobe: Provision: %v\n", err)
		return 3
	}
	fmt.Fprintln(stdout, "    OK (policy key persisted to memory)")

	fmt.Fprintln(stdout, "==> FolderSync")
	res, err := c.FolderSync(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "easprobe: FolderSync: %v\n", err)
		return 3
	}
	fmt.Fprintf(stdout, "    new SyncKey: %s\n", res.SyncKey)
	fmt.Fprintf(stdout, "    folders: %d added, %d updated, %d deleted\n",
		len(res.Added), len(res.Updated), len(res.Deleted))
	for _, f := range res.Added {
		fmt.Fprintf(stdout, "      [%s] %-30s id=%s parent=%s\n",
			f.Type, f.DisplayName, f.ServerID, f.ParentID)
	}

	return 0
}
