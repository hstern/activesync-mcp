package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"activesync-mcp/lib/config"

	"github.com/zalando/go-keyring"
	"golang.org/x/term"
)

// runKeyring dispatches keyring sub-subcommands.
func runKeyring(argv []string, configPath *string, stdout, stderr *os.File) int {
	if len(argv) == 0 {
		fmt.Fprintln(stderr, "activesync-mcp keyring: missing action (set | get | delete)")
		return exitUsageErr
	}
	action, rest := argv[0], argv[1:]

	fs := flag.NewFlagSet("keyring "+action, flag.ContinueOnError)
	fs.SetOutput(stderr)
	accountName := fs.String("account", "", "account name from config")
	fs.StringVar(configPath, "config", *configPath, "path to config.toml")
	if err := fs.Parse(rest); err != nil {
		return exitUsageErr
	}
	if *accountName == "" {
		fmt.Fprintln(stderr, "activesync-mcp keyring: --account is required")
		return exitUsageErr
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "activesync-mcp: %v\n", err)
		return exitConfig
	}
	a := cfg.FindAccount(*accountName)
	if a == nil {
		fmt.Fprintf(stderr, "activesync-mcp: no account named %q in config\n", *accountName)
		return exitConfig
	}
	if a.Secret.KeyringService == "" {
		fmt.Fprintf(stderr,
			"activesync-mcp: account %q does not use the keyring (it uses secret.command); nothing to %s\n",
			*accountName, action)
		return exitConfig
	}

	svc, user := a.Secret.KeyringService, a.Secret.KeyringAccount
	switch action {
	case "set":
		return keyringSet(svc, user, stdout, stderr)
	case "get":
		return keyringGet(svc, user, stdout, stderr)
	case "delete":
		return keyringDelete(svc, user, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "activesync-mcp keyring: unknown action %q\n", action)
		return exitUsageErr
	}
}

// PasswordReader reads a password from a TTY (no echo) or from a non-TTY
// stream (one line). Tests inject a fake reader.
type PasswordReader func(prompt string, in io.Reader, out io.Writer) (string, error)

// keyringSetter is the storage hook (default: keyring.Set).
type keyringSetter func(service, user, password string) error

// keyringGetter is the lookup hook (default: keyring.Get).
type keyringGetter func(service, user string) (string, error)

// keyringDeleter is the deletion hook (default: keyring.Delete).
type keyringDeleter func(service, user string) error

// hooks let tests swap out the real keyring + password prompt.
var (
	hookReadPassword PasswordReader = readPasswordTTY
	hookSet          keyringSetter  = keyring.Set
	hookGet          keyringGetter  = keyring.Get
	hookDelete       keyringDeleter = keyring.Delete
)

func keyringSet(svc, user string, stdout, stderr *os.File) int {
	pw, err := hookReadPassword(
		fmt.Sprintf("Password for keyring entry %q/%q: ", svc, user),
		os.Stdin, stderr,
	)
	if err != nil {
		fmt.Fprintf(stderr, "activesync-mcp: %v\n", err)
		return exitRuntime
	}
	if pw == "" {
		fmt.Fprintln(stderr, "activesync-mcp: empty password rejected")
		return exitUsageErr
	}
	if err := hookSet(svc, user, pw); err != nil {
		fmt.Fprintf(stderr, "activesync-mcp: keyring set: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "stored: service=%q account=%q\n", svc, user)
	return exitOK
}

func keyringGet(svc, user string, stdout, stderr *os.File) int {
	// We never print the value — only existence. This is a deliberate guard
	// against accidental shell-history leaks.
	_, err := hookGet(svc, user)
	switch {
	case err == nil:
		fmt.Fprintf(stdout, "set: service=%q account=%q\n", svc, user)
		return exitOK
	case errors.Is(err, keyring.ErrNotFound):
		fmt.Fprintf(stdout, "not set: service=%q account=%q\n", svc, user)
		return exitOK
	default:
		fmt.Fprintf(stderr, "activesync-mcp: keyring get: %v\n", err)
		return exitRuntime
	}
}

func keyringDelete(svc, user string, stdout, stderr *os.File) int {
	if err := hookDelete(svc, user); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			fmt.Fprintf(stdout, "already absent: service=%q account=%q\n", svc, user)
			return exitOK
		}
		fmt.Fprintf(stderr, "activesync-mcp: keyring delete: %v\n", err)
		return exitRuntime
	}
	fmt.Fprintf(stdout, "deleted: service=%q account=%q\n", svc, user)
	return exitOK
}

// readPasswordTTY prompts for a password without echoing.
//
// If `in` is a terminal, golang.org/x/term reads with echo off. Otherwise
// (piped stdin in scripts/CI) it reads a single line plain.
func readPasswordTTY(prompt string, in io.Reader, out io.Writer) (string, error) {
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(out, prompt)
		bs, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(out)
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
		return strings.TrimRight(string(bs), "\r\n"), nil
	}
	// Non-TTY: read one line. Useful for piping in scripts.
	buf := make([]byte, 0, 256)
	one := make([]byte, 1)
	for {
		n, err := in.Read(one)
		if n == 1 {
			if one[0] == '\n' {
				break
			}
			buf = append(buf, one[0])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read password: %w", err)
		}
	}
	return strings.TrimRight(string(buf), "\r"), nil
}
