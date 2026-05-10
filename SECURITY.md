# Security Policy

## Supported versions

activesync-mcp is pre-1.0 and ships from the `main` branch. Security
fixes land on `main` first; the most recent tagged release is patched
as a follow-up.

| Version | Supported          |
|---------|--------------------|
| latest `main` | :white_check_mark: |
| latest tagged release | :white_check_mark: |
| earlier tags | :x: (please upgrade) |

## Reporting a vulnerability

**Please do not file public issues for security bugs.**

Use GitHub's private vulnerability reporting:
<https://github.com/hstern/activesync-mcp/security/advisories/new>

Or email **henry@stern.ca**. Encrypt with the PGP key on
keys.openpgp.org for `henry@stern.ca` if the issue is sensitive.

Please include:

- A description of the issue and its impact
- Steps to reproduce (or a proof-of-concept) — including the EAS server
  type and auth scheme in play if relevant
- The affected commit / release
- Whether you've already disclosed publicly anywhere

I'll acknowledge receipt within **5 business days** and aim to publish
a fix and advisory within **30 days** of confirming the issue.
Coordinated disclosure is welcome — I'm happy to credit reporters in
the advisory unless you prefer otherwise.

## Scope

This policy covers the activesync-mcp server in this repository, including:

- The MCP tool surface and its handlers under `lib/server/`
- The bbolt-backed `StateStore` under `lib/store/`
- The configuration loader and secret resolution under `lib/config/`
- The `activesync-mcp` CLI subcommands (`serve`, `keyring`, `autodiscover`,
  `doctor`) under `cmd/activesync-mcp/`

Issues in the underlying EAS protocol library
(`github.com/hstern/go-activesync`) belong to that project's security
policy; please report there. Issues in the integration testenv
(Z-Push/Dovecot/Postfix/Radicale Docker stack referenced from
`go-activesync`) are out of scope unless they enable an attack against
the MCP server itself.

## Threat model notes for triage

The MCP server holds **long-lived credentials** for one or more EAS
accounts and exposes a **JSON-RPC tool surface over stdio** to whatever
MCP-aware client the user wires it into. Particular attention to:

- **Secret resolution paths** (`lib/config/secrets.go`): keyring lookup
  and `secret.command` callback. Misuse could leak passwords to logs or
  to attacker-controlled processes.
- **bbolt state file permissions**: the SyncKey / PolicyKey store
  contains material that, while not directly exploitable, can let an
  attacker who reads the file impersonate the device against the EAS
  server in some configurations.
- **MCP tool input validation**: tool handlers translate untrusted
  JSON-RPC arguments into EAS commands. Malformed input shouldn't crash
  the server or bypass per-class write-access checks
  (`Manager.CheckClass`).
- **Per-account access control**: each tool checks `Manager.CheckClass`
  before mutating; bypassing this would let a connected client write to
  an account marked read-only.
