# AGENTS.md

Instructions for AI coding agents (Claude Code, Cursor, Aider, Codex,
etc.) working in this repository. Humans should read
[CONTRIBUTING.md](CONTRIBUTING.md).

## What this repo is

`activesync-mcp` is a Model Context Protocol (MCP) server that exposes
Email/Calendar/Contacts/Tasks/Notes against any
ActiveSync-compatible mailbox. It's an **application**, not a library —
the EAS protocol layer lives in the sibling
[`github.com/hstern/go-activesync`](https://github.com/hstern/go-activesync)
module.

Layout:

```
cmd/activesync-mcp/        CLI entry point + subcommands (serve / keyring / autodiscover / doctor)
cmd/easprobe/              standalone EAS protocol debug CLI
lib/config/                TOML parsing, secret resolution (keyring | command), per-account config
lib/server/                MCP server build + tool registration + per-account Manager
lib/store/                 bbolt-backed eas.StateStore (PolicyKey + per-folder SyncKey)
testdata/                  e2e config fixtures
.github/                   workflows, dependabot, issue templates
```

## Daily commands

```
make ci              # lint + race tests + coverage (PR gate)
make integration     # tier 2 — needs OS keyring; cross-platform
make e2e             # tier 3 — needs ../go-activesync/testenv up
make scenario        # tier 4 — multi-call agent-shaped flows
make fmt             # gofmt -w (fix drift)
```

## Hard rules

CI enforces most of these.

1. **No plaintext secrets, anywhere.** Config schema only accepts
   `secret.keyring_*` or `secret.command`. Tool handlers must never log
   passwords or write them to files.
2. **`Manager.CheckClass` is mandatory in every write tool handler.**
   The registration layer in `lib/server/server.go` enforces it at
   tool-registration time (write tools are only registered for
   accounts that allow them), but every handler must re-check for
   defence in depth.
3. **Tests for every change.** New tools need (a) fixture-driven unit
   tests, (b) one e2e entry exercising the tool against the testenv,
   and (c) any obvious agent-flow placement in the scenario suite.
4. **`gofmt`, `go vet`, `go mod tidy`, `govulncheck` clean.** `make ci`.
5. **Tool descriptions are user-facing.** They become the schema the
   model sees when picking tools. Be specific about what each tool
   does, what its inputs mean, and what the output shape looks like.
6. **No new external deps without a strong reason.** This is an
   application so the bar is lower than a library, but each dep is a
   supply-chain liability — note the rationale in the PR.
7. **Cross-platform.** Mac / Windows / Linux all matter; tier-2
   integration tests run in a per-OS matrix and will catch
   platform-specific regressions.

## Test taxonomy

Four tiers, each with a build tag (except unit). Don't grow a tier
beyond its remit; if a test needs a real EAS server it belongs in
tier 3, not tier 2.

| Tier | Tag | Catches | Where it runs |
|---|---|---|---|
| Unit | _(none)_ | tool handler logic, MCP boundary glue | always, all 3 OSes |
| Integration | `integration` | OS surfaces (keyring, bbolt, signals, CLI subcommands) | PRs + main, OS matrix (Linux × {gnome-keyring, kwallet} + mac + win) |
| End-to-end | `e2e` | binary-spawn + real Z-Push, one tool per test | PRs + main, Linux only |
| Scenario | `scenario` | multi-call agent-shaped flows + state-change-mid-flight | PRs + main, Linux only |

In-process MCP test pattern (used in tier 1 and tier 2):

```go
ct, st := mcp.NewInMemoryTransports()
s.Connect(t.Context(), st, nil)
c := mcp.NewClient(...)
cs, _ := c.Connect(t.Context(), ct, nil)
res, _ := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "...", Arguments: ...})
```

EAS layer in tier 1 tests is faked with
[`easmock`](https://pkg.go.dev/github.com/hstern/go-activesync/eas/easmock).
**Do not** use `httptest.Server` + WBXML fixtures in new tier-1 tool
handler tests — the easmock pattern is shorter, faster, and exposes
call arguments directly to assertions:

```go
mock := &easmock.Client{
    EmailClient: easmock.EmailClient{
        SyncEmailFunc: func(_ context.Context, fid string, _ eas.EmailSyncOptions) (*eas.EmailSyncResult, error) {
            return &eas.EmailSyncResult{Added: []eas.EmailItem{{Subject: "hi"}}}, nil
        },
    },
}
m := newMockManager(t, mock, mockManagerOpts{})
s := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
registerEmailReadTools(s, m.cfg, m)
out := callTool(t, s, "email_list", EmailListInput{Account: "alpha", FolderID: "inbox-id"})
```

`Manager.SetClientForTest` (defined in `lib/server/export_test.go`)
is the seam that pre-populates the client cache so tool handlers
never go through the real Provision path. `lib/server/easmock_helpers_test.go`
defines `newMockManager` and the shared `callTool`. `httptest.Server`
is still appropriate when the test specifically exercises the HTTP
transport (e.g. NTLM/Kerberos handshakes in `auth_schemes_test.go`).

Binary-spawn pattern (tier 3 + 4):

```go
bin := buildBinary(t)                                         // go build -o tmp/x ./cmd/activesync-mcp
cmd := exec.Command(bin, "serve", "--config", "testdata/...")
// hand pipes to the SDK's stdio transport, then mcp.NewClient as above
```

## Adding an MCP tool

Reference: `lib/server/tools_email.go` is the cleanest example.

1. Pick the surface and the underlying `eas.*` method in
   `github.com/hstern/go-activesync` you'll wrap.
2. Add input + output structs to `lib/server/tools_<surface>.go` with
   `jsonschema:"…"` struct tags.
3. Add `register<ToolName>(s *mcp.Server, m *Manager, accounts []string)`.
4. Inside the handler:
   - `m.CheckClass(input.Account, "<class>", needsWrite)` first.
   - `c, err := m.Client(ctx, input.Account)`.
   - Call the EAS method.
   - Translate result into the output struct.
5. Register the new tool from `register<Surface>Tools(...)`, gated on
   the per-class access list (`config.AccountsAllowing` helper).
6. Write fixture tests (`tools_<name>_test.go`) using
   `mcp.NewInMemoryTransports`.
7. Add an e2e entry in the appropriate `cmd/activesync-mcp/e2e_*_test.go`.
8. Update `README.md`'s tool catalog table.

## What NOT to do

- Don't add a new top-level test tier for marginal use cases — the
  four tiers were sized so each catches a distinct class of failure.
  Adding more tiers fragments coverage and slows iteration.
- Don't introduce planning, decision, or analysis docs unless asked.
  Work from conversation context; PR descriptions hold the *why*.
- Don't commit unless the user asks.
- Don't push to `main` without a green local `make ci`.
- Don't bypass the MCP boundary in tier 1/2 tests when the test could
  be expressed via `cs.CallTool` — direct handler calls miss the
  schema validation layer.
- Don't touch the deferred tools list in
  [the implementation plan](.../plans/i-want-to-build-tender-hare.md)
  without explicit go-ahead. They're tracked, not forgotten.

## Repo layout cheat sheet

```
cmd/activesync-mcp/
  main.go                             entry point; subcommand dispatch
  keyring.go                          OS keyring set/get/delete subcommand
  autodiscover.go                     EAS endpoint discovery subcommand
  doctor.go                           config validation + per-account probe
cmd/easprobe/main.go                  standalone EAS debug CLI
lib/config/
  config.go                           TOML schema + load
  secrets.go                          keyring | command secret resolution
  access.go                           per-class read/write access control
  tls.go                              optional mTLS client cert config
lib/server/
  server.go                           Build(): wires every register*Tools()
  manager.go                          per-account eas.Client cache + CheckClass
  ntlm.go / kerberos.go               auth-scheme transport wrappers
  push.go                             background Ping → MCP notifications/updated
  tools_email.go / tools_email_write.go
  tools_calendar.go
  tools_pim.go                        contacts + tasks + notes + GAL
  tools_extras.go                     folder management + OOF + ResolveRecipients + item_count
lib/store/bbolt.go                    eas.StateStore impl: PolicyKey + per-folder SyncKey
.github/workflows/
  ci.yml                              lint + unit + integration matrix + e2e + scenario
  release.yml                         tag v* → cross-platform binary builds
  codeql.yml                          security scan
```

## Pointers

- **PR gating**: `main` requires `lint` + `unit-test` (matrix) status checks.
- **Releases**: tag `v*` → `release.yml` builds linux/amd64, linux/arm64,
  darwin/amd64, darwin/arm64, windows/amd64 binaries attached to a GitHub
  Release.
- **Security**: see `SECURITY.md`. Private disclosure to henry@stern.ca.
- **Issue templates**: `.github/ISSUE_TEMPLATE/`. Bug reports must include
  EAS server type + auth scheme + MCP client.
