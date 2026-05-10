package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"

	"activesync-mcp/lib/config"
	"github.com/hstern/go-activesync/eas"
)

// runDoctor validates the configuration and probes each account against
// its configured server with an HTTP OPTIONS request. It does NOT call
// Provision (which mutates server state) or any data command.
//
// Exit code reflects the worst per-account result: 0 if all healthy,
// exitRuntime otherwise.
func runDoctor(argv []string, configPath *string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(configPath, "config", *configPath, "path to config.toml")
	if err := fs.Parse(argv); err != nil {
		return exitUsageErr
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "activesync-mcp doctor: %v\n", err)
		return exitConfig
	}

	resolver := config.DefaultResolver()
	worst := exitOK
	for i := range cfg.Accounts {
		a := &cfg.Accounts[i]
		fmt.Fprintf(stdout, "==> account %q (%s)\n", a.Name, a.ServerURL)
		switch checkAccount(stdout, a, resolver) {
		case exitOK:
			fmt.Fprintln(stdout, "    OK")
		default:
			worst = exitRuntime
		}
	}
	return worst
}

// checkAccount performs the per-account health check and prints results to w.
// Returns exitOK if all checks passed.
func checkAccount(w io.Writer, a *config.Account, resolver *config.SecretResolver) int {
	ctx := context.Background()

	pw, err := resolver.Resolve(ctx, a)
	if err != nil {
		fmt.Fprintf(w, "    FAIL credentials: %v\n", err)
		return exitRuntime
	}

	hc := &http.Client{}
	if a.AllowInsecure {
		hc.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}
	deviceID := a.DeviceID
	if deviceID == "" {
		deviceID = "doctor00000000000000000000000000"
	}
	c, err := eas.NewClient(eas.Config{
		ServerURL:  a.ServerURL,
		Username:   a.Username,
		Password:   pw,
		DeviceID:   deviceID,
		DeviceType: a.DeviceType,
		ASVersion:  a.ASVersion,
		UserAgent:  a.UserAgent,
		HTTPClient: hc,
		State:      eas.NewMemoryState(),
	})
	if err != nil {
		fmt.Fprintf(w, "    FAIL client setup: %v\n", err)
		return exitRuntime
	}
	opts, err := c.Options(ctx)
	if err != nil {
		fmt.Fprintf(w, "    FAIL OPTIONS: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(w, "    versions: %v\n", opts.ProtocolVersions)
	if !opts.Supports(a.ASVersion) {
		fmt.Fprintf(w, "    FAIL server does not advertise version %s\n", a.ASVersion)
		return exitRuntime
	}
	return exitOK
}
