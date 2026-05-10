package main

import (
	"strings"
	"testing"
)

func TestSplitSubcommand(t *testing.T) {
	cases := []struct {
		argv     []string
		wantSub  string
		wantRest []string
	}{
		{nil, "", nil},
		{[]string{}, "", []string{}},
		{[]string{"--config", "x"}, "", []string{"--config", "x"}},
		{[]string{"serve"}, "serve", []string{}},
		{[]string{"keyring", "set", "--account", "a"}, "keyring", []string{"set", "--account", "a"}},
	}
	for i, tc := range cases {
		sub, rest := splitSubcommand(tc.argv)
		if sub != tc.wantSub {
			t.Errorf("case %d: sub = %q, want %q", i, sub, tc.wantSub)
		}
		if len(rest) != len(tc.wantRest) {
			t.Errorf("case %d: rest = %v, want %v", i, rest, tc.wantRest)
			continue
		}
		for j := range rest {
			if rest[j] != tc.wantRest[j] {
				t.Errorf("case %d: rest[%d] = %q, want %q", i, j, rest[j], tc.wantRest[j])
			}
		}
	}
}

func TestRun_helpExits0(t *testing.T) {
	out, errF, read := tempStdio(t)
	code := run([]string{"help"}, out, errF)
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	stdout, _ := read()
	if !strings.Contains(stdout, "MCP server") {
		t.Errorf("stdout missing usage: %q", stdout)
	}
}

func TestRun_unknownSubcommand(t *testing.T) {
	out, errF, read := tempStdio(t)
	code := run([]string{"frobnicate"}, out, errF)
	if code != exitUsageErr {
		t.Fatalf("exit %d", code)
	}
	_, e := read()
	if !strings.Contains(e, "unknown subcommand") {
		t.Errorf("stderr: %q", e)
	}
}

func TestRun_serveBadConfig(t *testing.T) {
	out, errF, read := tempStdio(t)
	code := run([]string{"serve", "--config", "/no/such/path/config.toml"}, out, errF)
	if code != exitConfig {
		t.Fatalf("exit %d", code)
	}
	_, e := read()
	if !strings.Contains(e, "open config") {
		t.Errorf("stderr: %q", e)
	}
}
