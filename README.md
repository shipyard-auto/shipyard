# Shipyard

[![CI](https://github.com/shipyard-auto/shipyard/actions/workflows/ci.yml/badge.svg)](https://github.com/shipyard-auto/shipyard/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/shipyard-auto/shipyard/branch/main/graph/badge.svg)](https://codecov.io/gh/shipyard-auto/shipyard)
[![Go Report Card](https://goreportcard.com/badge/github.com/shipyard-auto/shipyard)](https://goreportcard.com/report/github.com/shipyard-auto/shipyard)

```sh
curl -fsSL https://raw.githubusercontent.com/shipyard-auto/shipyard/main/scripts/install.sh | sh
```

`shipyard` is a standalone Go CLI for **local automation and OS-integrated
operations** on Linux and macOS. Local-first by design: no cloud account, no
mandatory background service, no remote control plane.

Today it covers five subsystems behind a single binary:

| Subsystem | What it owns |
|---|---|
| **cron** | Shipyard-managed entries in the current user's crontab. |
| **service** | User-scope services (`systemd --user` on Linux, `launchd` agents on macOS). |
| **logs** | Structured JSONL events shared by every subsystem. |
| **fairway** *(addon)* | HTTP gateway daemon that forwards routes to crew agents, shell actions or upstream URLs. |
| **crew** *(addon)* | LLM-backed agents with tool dispatching, MCP bridges, cron/webhook triggers, and stateful conversations. |

Both addons ship as separate binaries (`shipyard-fairway`, `shipyard-crew`)
that the main `shipyard` CLI installs, upgrades and controls.

## Fast Context

If you are an engineer or AI entering this repository, the quickest accurate
mental model:

- **Entrypoint**: [`cmd/shipyard/main.go`](./cmd/shipyard/main.go)
- **Root command wiring**: [`internal/cli/root.go`](./internal/cli/root.go)
- **Subsystems (live in `internal/`)**: [`cron`](./internal/cron),
  [`service`](./internal/service), [`logs`](./internal/logs),
  [`fairwayctl`](./internal/fairwayctl) (talks to fairway daemon),
  [`crewctl`](./internal/crewctl) (talks to crew daemon).
- **Addons (separate Go modules under `addons/`)**:
  [`addons/fairway`](./addons/fairway),
  [`addons/crew`](./addons/crew). The core never imports
  `addons/*/internal/*`; communication is via subprocess contracts and
  JSON-RPC sockets.
- **Terminal rendering / TUI wizards**: [`internal/ui`](./internal/ui).
- **Release source of truth**: [`manifest`](./manifest) — three lines:
  `shipyard=`, `fairway=`, `crew=`.
- **Agent-specific operating contract**: [`CLAUDE.md`](./CLAUDE.md)
  (Claude-Code-specific extension). A local `AGENTS.md` lives in the
  working tree but is intentionally gitignored — it's a per-machine
  reference for the maintainer's agent stack.

The CLI already has real OS integration. `cron` modifies the current user's
crontab. `service` writes systemd / launchd units. `logs` writes JSONL event
files under `~/.shipyard/logs/`. `fairway` binds an HTTP listener (default
`127.0.0.1:9876`). `crew` spawns subprocesses against `claude --print` or
the Anthropic API.

## Quick Start by Subsystem

```bash
# Cron
shipyard cron list
shipyard cron add --name "Backup" --schedule "0 * * * *" --command "/usr/local/bin/backup"

# Service
shipyard service add --name "Worker" --command "/usr/local/bin/worker"
shipyard service status <id>

# Logs (reads all sources by default)
shipyard logs show
shipyard logs tail --source crew --trace <trace-id>

# Fairway (HTTP gateway addon)
shipyard fairway install
shipyard fairway route add --path /hook --action crew.run --target my-agent --auth bearer --auth-token <secret>

# Crew (LLM agent addon)
shipyard crew install
shipyard crew hire my-agent
shipyard crew apply my-agent
shipyard crew run my-agent "do the thing"
```

The interactive control panels are reachable via `shipyard <subsystem>
config` (cron, service, logs, fairway, crew) and require a real TTY.

Full per-command reference lives in [`CLAUDE.md`](./CLAUDE.md).

## Local State

Shipyard keeps everything under `~/.shipyard/`:

| Path | Purpose |
|---|---|
| `~/.shipyard/install.json` | Installation metadata |
| `~/.shipyard/crons.json` | Shipyard-managed cron jobs |
| `~/.shipyard/services.json` | Shipyard-managed user services |
| `~/.shipyard/logs.json` | Logs subsystem config |
| `~/.shipyard/logs/<source>/YYYY-MM-DD.jsonl` | Daily JSONL log files |
| `~/.shipyard/fairway/` | Fairway addon state (config, routes, stats) |
| `~/.shipyard/crew/<name>/` | Crew agent definition (`agent.yaml`, `prompt.md`) |
| `~/.shipyard/crew/<name>/sessions/` | Stateful conversation history (JSONL) |
| `~/.shipyard/crew/tools/` | Reusable tool definitions for crew agents |
| `~/.shipyard/run/` | Unix sockets and PID files for daemons |

State files are auto-created on demand. Shipyard never touches anything
outside `~/.shipyard/` automatically — external crontab entries, third-party
launchd agents and unrelated `~/.claude.json` MCP servers stay untouched.

## Building From Source

```bash
# Build the main binary
GOTOOLCHAIN=go1.26.2 go build ./cmd/shipyard

# Run all tests (root module)
GOTOOLCHAIN=go1.26.2 go test ./...

# Test a specific addon
cd addons/fairway && GOTOOLCHAIN=go1.26.2 go test ./... -count=1
cd addons/crew    && GOTOOLCHAIN=go1.26.2 go test ./... -count=1
```

Toolchain pin: **Go 1.26.2**. CI enforces it; local builds should match.

## Continuous Integration

Every pull request triggers [`.github/workflows/ci.yml`](.github/workflows/ci.yml):

- **Lint**: `gofmt -l` (must be empty), `go vet ./...`, `golangci-lint run`
  (config in [`.golangci.yml`](./.golangci.yml)).
- **Test matrix**: `ubuntu-latest` and `macos-latest`. Full suite with
  `-race -coverprofile`, 5-minute timeout.
- **Coverage**: uploaded to Codecov from the ubuntu run.

Main is protected: PRs cannot merge unless all three checks (Lint, Test
ubuntu, Test macOS) are green and the branch is up to date with main. Push
to main and force-push are blocked.

Release workflows ([`shipyard-release.yml`](.github/workflows/shipyard-release.yml),
[`fairway-release.yml`](.github/workflows/fairway-release.yml),
[`crew-release.yml`](.github/workflows/crew-release.yml)) fire on push to
main and build artifacts only when the corresponding line in `manifest`
changes. Each release runs a post-publish smoke matrix that installs from
the published artifact on ubuntu + macOS and exercises the binary.

Before opening a PR, run locally:

```bash
gofmt -w .
go vet ./...
golangci-lint run --timeout=5m
GOTOOLCHAIN=go1.26.2 go test ./... -race
```

## Release Model

Three independently-versioned components, all tracked in [`manifest`](./manifest):

- `shipyard=` — main CLI binary. Tag: `v<version>`.
- `fairway=` — HTTP gateway daemon. Tag: `fairway-v<version>`.
- `crew=` — LLM agent daemon. Tag: `crew-v<version>`.

A release fires when its line in `manifest` changes and the commit lands on
main. Bumps are deliberate human actions (or part of the
[tech-debt protocol](./docs/protocolos/debito-tecnico.md) for agents).

Pre-1.0 components (`crew` today) can ship breaking changes in minor bumps;
post-1.0 components follow strict semver.

## Architecture Rules

Reflected in the codebase and intended to stay true:

- Business logic lives in `internal/`; `cmd/` is thin.
- OS boundaries (crontab, systemd, launchd, sockets) belong in subsystem
  packages, not in CLI handlers.
- The core CLI must not import `addons/*/internal/*`. Communication with
  addons is by subprocess contracts (`shipyard-crew reconcile`,
  `shipyard-crew mcp-serve`, `shipyard-crew mcp-filter`) and JSON-RPC
  Unix sockets.
- Shipyard manages only state it created. External crontab entries,
  third-party systemd units and unrelated `~/.claude.json` keys are never
  touched.
- Prefer structured local state (`~/.shipyard/*.json`,
  `~/.shipyard/crew/<name>/.reconciled.json`) over hidden side effects.
- Keep shell scripts minimal; real logic belongs in Go.

## Where to Look Next

- Operating contract for AI coding agents: [`CLAUDE.md`](./CLAUDE.md).
  (A local-only `AGENTS.md` may also be present in the maintainer's
  working tree.)
- Per-command CLI reference: [`CLAUDE.md`](./CLAUDE.md).
- Internal documentation (gitignored): `docs/` — debt backlog, test
  scenarios, protocols.

This repository is the standalone `shipyard` project. Even though it lives
inside a larger monorepo on the author's machine, do not assume it is a
subdirectory of another Go module when editing code or release workflows.
