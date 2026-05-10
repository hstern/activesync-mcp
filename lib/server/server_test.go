package server

import (
	"reflect"
	"sort"
	"testing"

	"activesync-mcp/lib/config"
)

func TestBuildAccountsList(t *testing.T) {
	cfg := &config.Config{
		Accounts: []config.Account{
			{
				Name:          "personal",
				ServerURL:     "https://p.example",
				Username:      "p",
				ASVersion:     "14.1",
				DefaultAccess: config.AccessRO,
			},
			{
				Name:          "work",
				ServerURL:     "https://w.example",
				Username:      "w",
				ASVersion:     "14.1",
				DefaultAccess: config.AccessRO,
				Access: map[string]string{
					"calendar": config.AccessRW,
					"tasks":    config.AccessRW,
				},
			},
		},
	}
	out := buildAccountsList(cfg)
	if len(out.Accounts) != 2 {
		t.Fatalf("want 2 accounts, got %d", len(out.Accounts))
	}

	// personal: no overrides, no writable classes
	p := out.Accounts[0]
	if p.Name != "personal" {
		t.Errorf("name: %q", p.Name)
	}
	if p.AccessOverride != nil {
		t.Errorf("personal should have no override map, got %v", p.AccessOverride)
	}
	if len(p.Writable) != 0 {
		t.Errorf("personal writable: %v", p.Writable)
	}

	// work: overrides + writable = [calendar, tasks]
	w := out.Accounts[1]
	if !reflect.DeepEqual(w.AccessOverride, map[string]string{
		"calendar": config.AccessRW, "tasks": config.AccessRW,
	}) {
		t.Errorf("work override: %v", w.AccessOverride)
	}
	got := append([]string(nil), w.Writable...)
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"calendar", "tasks"}) {
		t.Errorf("work writable: %v", got)
	}
}

func TestBuild_registersAccountsList(t *testing.T) {
	cfg := &config.Config{
		Accounts: []config.Account{
			{
				Name:          "a",
				ServerURL:     "https://x",
				Username:      "u",
				ASVersion:     "14.1",
				DefaultAccess: config.AccessRO,
			},
		},
	}
	s := Build(cfg, nil)
	if s == nil {
		t.Fatal("Build returned nil server")
	}
	// We don't have direct access to the registered tool list via the SDK
	// surface, so this test mostly proves Build doesn't panic with a valid
	// config. Tool wiring is exercised by the buildAccountsList unit test
	// and by the main_test end-to-end smoke test.
}
