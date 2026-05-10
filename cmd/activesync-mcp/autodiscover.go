package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/hstern/go-activesync/eas"
)

// runAutodiscover queries the EAS Autodiscover endpoints for an email
// address and prints the suggested config block. The password is prompted
// on the terminal (no echo) and never persisted.
func runAutodiscover(argv []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("autodiscover", flag.ContinueOnError)
	fs.SetOutput(stderr)
	email := fs.String("email", "", "email address to look up (required)")
	insecure := fs.Bool("insecure", false, "skip TLS verification (testing only)")
	if err := fs.Parse(argv); err != nil {
		return exitUsageErr
	}
	if *email == "" {
		fmt.Fprintln(stderr, "activesync-mcp autodiscover: --email is required")
		return exitUsageErr
	}

	pw, err := hookReadPassword(
		fmt.Sprintf("Password for %s (used only for the discovery request, not stored): ", *email),
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

	hc := &http.Client{}
	if *insecure {
		hc.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}

	res, err := eas.Autodiscover(context.Background(), *email, pw, eas.AutodiscoverOptions{
		HTTPClient: hc,
	})
	if err != nil {
		fmt.Fprintf(stderr, "activesync-mcp autodiscover: %v\n", err)
		return exitRuntime
	}

	fmt.Fprintln(stdout, "# Add this block to your config.toml, then run:")
	fmt.Fprintf(stdout, "#   activesync-mcp keyring set --account %s\n#\n", suggestAccountName(*email))
	writeAutodiscoverSnippet(stdout, *email, res)
	return exitOK
}

func writeAutodiscoverSnippet(w io.Writer, email string, res *eas.AutodiscoverResult) {
	name := suggestAccountName(email)
	fmt.Fprintln(w, "[[account]]")
	fmt.Fprintf(w, "name       = %q\n", name)
	fmt.Fprintf(w, "server_url = %q\n", res.URL)
	fmt.Fprintf(w, "username   = %q\n", email)
	fmt.Fprintln(w, `secret     = { keyring_service = "activesync-mcp", keyring_account = "`+name+`" }`)
	fmt.Fprintln(w, "# default_access = \"ro\" (default; opt into writes per-class via [account.access])")
	if res.DisplayName != "" {
		fmt.Fprintf(w, "# server reported display name: %q\n", res.DisplayName)
	}
}

// suggestAccountName picks a short account name from an email address.
// Uses the local part with the domain's first label appended for collision
// safety: henry@example.com → henry-example.
func suggestAccountName(email string) string {
	at := -1
	for i := 0; i < len(email); i++ {
		if email[i] == '@' {
			at = i
			break
		}
	}
	if at < 0 {
		return email
	}
	local := email[:at]
	rest := email[at+1:]
	dot := -1
	for i := 0; i < len(rest); i++ {
		if rest[i] == '.' {
			dot = i
			break
		}
	}
	if dot < 0 {
		return local + "-" + rest
	}
	return local + "-" + rest[:dot]
}
