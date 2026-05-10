package server

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"activesync-mcp/lib/config"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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

func TestRegisterAccountsList_throughMCP(t *testing.T) {
	cfg := &config.Config{
		Accounts: []config.Account{{
			Name: "alpha", ServerURL: "https://x", Username: "u",
			ASVersion: "14.1", DefaultAccess: config.AccessRO,
			Access: map[string]string{"calendar": config.AccessRW},
		}},
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	registerAccountsList(s, cfg)

	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := s.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := c.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{
		Name: "accounts_list", Arguments: AccountsListInput{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError: %+v", res)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type %T", res.Content[0])
	}
	var got AccountsListOutput
	if err := json.Unmarshal([]byte(tc.Text), &got); err != nil {
		t.Fatalf("unmarshal: %v\nbody=%s", err, tc.Text)
	}
	if len(got.Accounts) != 1 || got.Accounts[0].Name != "alpha" {
		t.Errorf("accounts = %+v", got.Accounts)
	}
}

func TestBuild_withManager_registersAllToolGroups(t *testing.T) {
	cfg := &config.Config{
		Accounts: []config.Account{{
			Name: "alpha", ServerURL: "https://x", Username: "u",
			ASVersion: "14.1", DefaultAccess: config.AccessRW,
			Secret: config.SecretRef{KeyringService: "x", KeyringAccount: "alpha"},
		}},
	}
	m := NewManager(cfg, &fakeStateProvider{},
		&fakeResolver{pw: map[string]string{"alpha": "p"}},
		staticDeviceIDs{"alpha": "abc"})
	s := Build(cfg, m)
	if s == nil {
		t.Fatal("nil server")
	}
	// Smoke: connect a client and confirm at least one of each tool
	// family was registered.
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := s.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	cs, err := c.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, tool := range tools.Tools {
		have[tool.Name] = true
	}
	for _, want := range []string{
		"accounts_list", "email_list_folders", "calendar_list_folders",
		"contacts_list_folders", "tasks_list_folders", "notes_list_folders",
		"item_count", "oof_get",
	} {
		if !have[want] {
			t.Errorf("missing tool %q", want)
		}
	}
}
