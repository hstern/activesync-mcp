# activesync-mcp

[![ci](https://github.com/hstern/activesync-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/hstern/activesync-mcp/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/hstern/activesync-mcp/branch/main/graph/badge.svg)](https://codecov.io/gh/hstern/activesync-mcp)
[![Go Report Card](https://goreportcard.com/badge/github.com/hstern/activesync-mcp)](https://goreportcard.com/report/github.com/hstern/activesync-mcp)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

An **MCP server** that exposes Email, Calendar, Contacts, Tasks, and Notes
from any **Microsoft Exchange ActiveSync (EAS)** mailbox. Connects MCP-aware
clients (Claude Desktop, Claude Code, Cortex, Cline, Zed, etc.) to whatever
EAS-compatible server you can authenticate against — Z-Push, SOGo,
on-premises Exchange, Office 365, Kerio, mailcow.

Built on top of [`github.com/hstern/go-activesync`](https://github.com/hstern/go-activesync).

## Install

Pre-built binaries for Linux, macOS, and Windows are attached to each
release: <https://github.com/hstern/activesync-mcp/releases>.

Or build from source:

```sh
go install github.com/hstern/activesync-mcp/cmd/activesync-mcp@latest
```

## Quick start

### 1. Discover your EAS endpoint

```sh
activesync-mcp autodiscover --email you@example.com
# → https://mail.example.com/Microsoft-Server-ActiveSync
```

### 2. Write a config

`~/.config/activesync-mcp/config.toml` (Linux / macOS) or
`%APPDATA%\activesync-mcp\config.toml` (Windows):

```toml
state_dir = "~/.local/state/activesync-mcp"
log_level = "info"

[[account]]
name             = "work"
server_url       = "https://mail.example.com/Microsoft-Server-ActiveSync"
username         = "you@example.com"
as_version       = "14.1"

# Resolve the password from the OS keyring (recommended).
secret = { keyring_service = "activesync-mcp", keyring_account = "work" }

# Per-class write access. Default is read-only for every class.
default_access = "ro"
[account.access]
calendar = "rw"
tasks    = "rw"
```

### 3. Store your password

```sh
activesync-mcp keyring set --account work
# (no-echo prompt for the password)
```

### 4. Verify

```sh
activesync-mcp doctor
# → work: OK (12.0, 12.1, 14.0 supported; negotiated 14.0)
```

### 5. Wire it into your MCP client

Claude Desktop (`~/Library/Application Support/Claude/claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "activesync": {
      "command": "/usr/local/bin/activesync-mcp",
      "args": ["serve"]
    }
  }
}
```

Restart Claude Desktop and the tools below appear in the tool picker.

## Tools

42 tools across 10 surfaces. All take an `account` argument (the name from
`config.toml`); write tools are only registered for accounts that allow them.

| Surface | Tools |
|---|---|
| Admin | `accounts_list` |
| Folder mgmt | `folder_create`, `folder_delete`, `folder_empty`, `folder_rename` |
| Email | `email_list_folders`, `email_list`, `email_get`, `email_search`, `email_send`, `email_reply`, `email_forward`, `email_move`, `email_set_flags`, `email_delete` |
| Calendar | `calendar_list_folders`, `calendar_list_events`, `calendar_create_event`, `calendar_update_event`, `calendar_delete_event`, `calendar_respond_invite` |
| Contacts | `contacts_list_folders`, `contacts_list`, `contacts_create`, `contacts_update`, `contacts_delete` |
| Tasks | `tasks_list_folders`, `tasks_list`, `tasks_create`, `tasks_update`, `tasks_complete`, `tasks_delete` |
| Notes | `notes_list_folders`, `notes_list`, `notes_create`, `notes_update`, `notes_delete` |
| Directory | `gal_search`, `resolve_recipients` |
| Settings | `oof_get`, `oof_set` |
| Misc | `item_count` |

Push notifications are also wired: tools that should reflect server-side
changes (new mail arriving, calendar updates from another client) trigger
MCP `notifications/resources/updated` events that the client can subscribe to.

## Configuration reference

Config lives in TOML at the platform-default path
(`$XDG_CONFIG_HOME/activesync-mcp/config.toml` on Linux,
`~/Library/Application Support/activesync-mcp/config.toml` on macOS,
`%APPDATA%\activesync-mcp\config.toml` on Windows) or wherever
`--config` points.

```toml
state_dir = "~/.local/state/activesync-mcp"   # bbolt state DB lives here
log_level = "info"                             # debug | info | warn | error

[[account]]
name             = "work"
server_url       = "https://…/Microsoft-Server-ActiveSync"
username         = "henry@stern.ca"
device_type      = "MCP"                       # default; override per server policy
as_version       = "14.1"                      # default
user_agent       = "activesync-mcp/0.1"        # default
allow_insecure   = false                       # skip TLS verify; off by default

# Pick exactly one secret source:
secret = { keyring_service = "activesync-mcp", keyring_account = "work" }
# secret = { command = ["pass", "show", "mail/work"] }
# secret = { command = ["op", "read", "op://Personal/work/password"] }

default_access = "ro"     # ro | rw, default ro
[account.access]
calendar = "rw"
tasks    = "rw"
# email, contacts, notes inherit default_access (= ro here)

# Optional: per-account auth scheme overrides.
# auth = "basic"   # default
# auth = "bearer"  # OAuth — use bearer_command callback to refresh tokens
# auth = "ntlm"    # legacy on-prem Exchange
# auth = "negotiate"  # SPNEGO/Kerberos
# tls_client_cert = { ... }  # mTLS
```

**Plaintext passwords are not allowed.** The schema only accepts
`secret.keyring_*` or `secret.command`; anything else fails config load.

## Subcommands

```
activesync-mcp serve [--config PATH]      # run the MCP server (default)
activesync-mcp keyring set --account NAME # store an account password
activesync-mcp keyring get --account NAME # report whether one is set
activesync-mcp keyring delete --account NAME
activesync-mcp autodiscover --email ADDR  # probe EAS endpoint for an email
activesync-mcp doctor [--config PATH]     # validate config + probe each account
```

## Testing

Four test tiers; details and how to run each in
[CONTRIBUTING.md](CONTRIBUTING.md).

```sh
make test            # tier 1 — fast unit tests
make integration     # tier 2 — real OS surfaces (keyring, bbolt, signals)
make e2e             # tier 3 — binary against real Z-Push (needs ../go-activesync/testenv)
make scenario        # tier 4 — multi-call agent-shaped flows
make ci              # tier 1 + lint
```

## Contributing

PRs welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) for setup, test
tiers, coding standards, and how to add a new MCP tool. Bug reports use
the [issue templates](.github/ISSUE_TEMPLATE/); security disclosures are
private — see [SECURITY.md](SECURITY.md). Project participants agree to
the [Code of Conduct](CODE_OF_CONDUCT.md).

## License

MIT — see [LICENSE](LICENSE).
