# Contributing to activesync-mcp

Thanks for considering a contribution. The bar is "ship working code
people want to use," not "follow a rigid process" — small PRs welcome.

## Before you open an issue

- **Bugs**: please use the [bug report template](.github/ISSUE_TEMPLATE/bug_report.yml).
  EAS server type, account auth scheme, and the MCP-aware client you're
  driving the server from (Claude Desktop / Claude Code / Cortex / etc.)
  are the three fields we'll always need.
- **Security issues**: do **not** file publicly. See
  [SECURITY.md](SECURITY.md) for private disclosure.
- **Questions / design discussion**: open a
  [Discussion](https://github.com/hstern/activesync-mcp/discussions)
  rather than an issue.

## Development setup

You'll need:

- Go ≥ 1.26 (we track the latest 1.26.x patch in CI)
- `make` (everything's wrapped in the top-level Makefile)
- Docker + `docker-compose` (only if you want to run the e2e or
  scenario tests locally)
- A sibling clone of `github.com/hstern/go-activesync` at
  `../go-activesync` (only for e2e / scenario tests — they reuse its
  Z-Push testenv stack)

```sh
git clone https://github.com/hstern/activesync-mcp
cd activesync-mcp
make            # prints all available targets
make test       # unit tests under -race with coverage
make ci         # everything CI runs (lint + race tests + vulncheck)
```

## Test tiers

The project has four test tiers, each with a build tag, run independently:

| Tier | Tag | What it covers | Runs in CI |
|------|-----|----------------|------------|
| Unit | _(none)_ | pure logic, faked HTTP / OS | always, all 3 OSes |
| Integration | `integration` | real OS surfaces (keyring, bbolt, signals, CLI subcommands) | PRs + main, all 3 OSes |
| End-to-end | `e2e` | binary spawn + real Z-Push, one tool per test | PRs + main, Linux only |
| Scenario | `scenario` | multi-call agent-shaped flows | PRs + main, Linux only |

```sh
make test            # tier 1 — fast, no deps
make integration     # tier 2 — needs OS keyring; runs in OS matrix in CI
make e2e             # tier 3 — needs ../go-activesync/testenv up
make scenario        # tier 4 — needs ../go-activesync/testenv up
```

`make integration` on Linux will pick up whichever D-Bus Secret Service
provider is currently registered (gnome-keyring or kwalletd6); CI runs
the suite once per provider.

## Coding standards

- **`gofmt`** is mandatory; `make fmt` will fix any drift. CI fails on
  drift via `make gofmt-check`.
- **`go vet` clean.** `make vet`.
- **Tests for every change.** New tools need fixture-driven unit tests
  at minimum; ideally also one entry in the e2e suite (Tier 3) and any
  obvious agent-flow placement in scenario tests (Tier 4).
- **`Manager.CheckClass` is mandatory in every write tool handler**
  before any mutating EAS call. The tool-registration helpers in
  `lib/server/server.go` enforce this at registration time (a write
  tool is only registered for accounts that allow it), but handlers
  must re-check for defence in depth — see the existing handlers as
  reference.
- **No external deps without a strong reason.** This is an application,
  so dep additions are easier to justify than they would be for the
  library, but each one is a supply-chain liability — flag the
  rationale in the PR description.
- **Doc comments on every exported symbol.** Match the prose style of
  what's already there.
- **No bare TODOs in `lib/`.** TODOs are acceptable in `cmd/` and tests;
  in `lib/` packages, file an issue and link it.

## Adding a new MCP tool

The general shape (using `email_set_flags` as the reference):

1. Pick the surface and codepage. Read the corresponding `eas.*`
   methods in `github.com/hstern/go-activesync` to know what's
   available.
2. Add input + output structs to the appropriate `lib/server/tools_*.go`
   file. Use `jsonschema:"…"` struct tags so the SDK generates the schema
   the MCP client sees.
3. Add a `register<ToolName>(s *mcp.Server, m *Manager, accounts []string)`
   helper in the same file. Call `mcp.AddTool(s, &mcp.Tool{Name: …,
   Description: …, …}, handler)`.
4. In the handler: `m.CheckClass(input.Account, "<class>", needsWrite)`
   first; then `c, err := m.Client(ctx, input.Account)`; then call
   the EAS method; then translate the result into the output struct.
5. Register the new tool from `registerXyzTools(...)` in the appropriate
   file, gated on the per-class access list (`config.AccountsAllowing`
   helper).
6. Write fixture-based unit tests in `tools_<name>_test.go` using the
   existing `mcp.NewInMemoryTransports` pattern (see
   `tools_calendar_test.go:240` for the template).
7. Add a one-line e2e test in the appropriate `cmd/activesync-mcp/e2e_*_test.go`
   that exercises the tool against the testenv.
8. Update `README.md`'s tool catalog.

## Submitting a PR

1. Fork + branch (any name).
2. Make your change. Run `make ci` before pushing — it catches the
   same things CI will. If you touched anything that affects the e2e
   surface, also run `make e2e` (requires testenv up).
3. Open the PR against `main` with a description that says **what** and
   **why**.
4. CI will run lint + unit tests across mac/win/linux, plus integration
   on the OS matrix and e2e + scenario on Linux. PRs that affect
   production code without test coverage will get nudged for tests.
5. One approving review + green CI = merge. We squash on merge to keep
   `main` tidy.

## Releases (maintainer notes)

Tags follow [SemVer](https://semver.org/). Push a tag like `v0.2.0` to
trigger the [release workflow](.github/workflows/release.yml), which
runs the lint+test gate and builds cross-platform binaries
(linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64)
attached to the GitHub Release.

## Code of conduct

By participating in this project you agree to abide by the
[Code of Conduct](CODE_OF_CONDUCT.md).
