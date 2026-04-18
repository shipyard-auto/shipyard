# Shipyard

```sh
curl -fsSL https://raw.githubusercontent.com/shipyard-auto/shipyard/main/scripts/install.sh | sh
```

`shipyard` is a standalone Go CLI for local automation and OS-integrated operations on Linux and macOS.

This project is intentionally local-first. The current focus is:

- installing, updating, and uninstalling the CLI
- managing Shipyard-owned cron jobs for the current user
- storing structured local logs for Shipyard operations

## Fast Context

If you are another engineer or an AI entering this repository, the quickest accurate model is:

- entrypoint: [`cmd/shipyard/main.go`](./cmd/shipyard/main.go)
- root command wiring: [`internal/cli/root.go`](./internal/cli/root.go)
- main implemented subsystem: [`internal/cron`](./internal/cron)
- reusable observability foundation: [`internal/logs`](./internal/logs)
- terminal rendering/help styling: [`internal/ui`](./internal/ui)
- release source of truth: [`manifest`](./manifest)

The CLI already has real OS integration. `cron` commands modify the current user's crontab. `logs` writes JSONL event files under `~/.shipyard/logs/`.

## Implemented Commands

Top-level commands:

- `shipyard version`
- `shipyard update`
- `shipyard uninstall`
- `shipyard cron ...`
- `shipyard service ...`
- `shipyard logs ...`

`shipyard cron` currently supports:

- `list`
- `show`
- `add`
- `update`
- `delete`
- `enable`
- `disable`
- `run`

`shipyard logs` currently supports:

- `list`
- `show`
- `tail`
- `prune`
- `config`
- `config set retention-days <n>`

`shipyard service` currently supports:

- `list`
- `show`
- `add`
- `update`
- `delete`
- `enable`
- `disable`
- `start`
- `stop`
- `restart`
- `status`
- `config`

### `shipyard cron config`

`shipyard cron config` opens an interactive full-screen control panel for Shipyard-managed cron jobs.
Use it for guided add, browse, update, enable, disable, run, and delete flows.
The wizard only manages jobs created by Shipyard and preserves external crontab entries.

### `shipyard logs config`

`shipyard logs config` opens an interactive logs control panel when running in a real terminal with no extra args.
Use it to inspect sources, review recent events, tail live events, change retention, and prune old files.
For scripts and automation, `shipyard logs config set retention-days <n>` remains the non-interactive path.

### `shipyard service config`

`shipyard service config` opens an interactive service control panel for Shipyard-managed user services.
Use it for guided add, browse, update, lifecycle, enable/disable, status, and delete flows.
The wizard only manages services created by Shipyard and preserves external units or launch agents.

## How Shipyard Works

### CLI structure

- `cmd/shipyard`: binary entrypoint
- `internal/cli`: command definitions and help rendering
- `internal/ui`: splash/help formatting helpers

### Cron subsystem

`shipyard cron` is the main OS-facing feature today.

Key rules:

- it manages only jobs created by Shipyard
- it operates on the current user's crontab only
- it preserves external cron entries
- it does not import non-Shipyard jobs automatically
- local state is stored in `~/.shipyard/crons.json`

Shipyard-managed jobs are rendered into the crontab with Shipyard markers so they can be updated or removed safely without touching unrelated entries.

### Logs subsystem

`shipyard logs` is the foundation for future observability across cron, services, and later agent runtimes.

Current model:

- config file: `~/.shipyard/logs.json`
- log root: `~/.shipyard/logs/`
- file format: JSONL
- layout: `~/.shipyard/logs/<source>/YYYY-MM-DD.jsonl`
- initial source: `cron`
- default retention: `14` days

Logs are normalized structured events. The storage format is intentionally source-neutral so future modules like `service` or `agent` can reuse the same event pipeline.

### Service subsystem

`shipyard service` is the local service/process management layer for user-scoped services.

Key rules:

- it manages only services created by Shipyard
- it operates only in user scope
- Linux uses `systemd --user`
- macOS uses user `launchd` agents
- local state is stored in `~/.shipyard/services.json`
- service unit files are derived projections, not the source of truth

Shipyard-managed units are identified by stable prefixes:

- Linux: `shipyard-<ID>.service`
- macOS: `com.shipyard.service.<ID>`

## Local State

Shipyard currently uses `~/.shipyard/` as its local base directory.

Important files and directories:

- `~/.shipyard/install.json`
- `~/.shipyard/crons.json`
- `~/.shipyard/services.json`
- `~/.shipyard/logs.json`
- `~/.shipyard/logs/`

Important behavior:

- local directories are auto-created when needed
- logs auto-initialize on first write
- `crons.json` is Shipyard state, not a user-facing manual config surface

## Build And Validation

Primary toolchain target:

- Go `1.26.2`

Useful commands:

```bash
GOTOOLCHAIN=go1.26.2 go test ./...
GOTOOLCHAIN=go1.26.2 go build ./cmd/shipyard
```

## Release Model

- release versions are read from [`manifest`](./manifest) (`shipyard=` and `fairway=` lines)
- GitHub Actions publishes releases from `main`
- shipyard tags use `v<version>`, fairway tags use `fairway-v<version>`
- version bumps are manual and intentional

## Active Engineering Rules

These constraints are already reflected in the codebase and should stay true:

- keep shell scripts minimal; core behavior belongs in Go
- keep business logic in `internal/`
- keep OS boundaries explicit and easy to test
- preserve non-Shipyard system state
- prefer structured local state over hidden behavior
- prefer modular subsystems that future services can plug into

## Near-Term Product Direction

The current foundation is:

1. local CLI operations
2. cron management
3. service management
4. structured local logs

The intended next layers are:

1. service/process management
2. richer observability
3. agent and subagent runtime features

## Repo-Specific Notes

- [`AGENTS.md`](../AGENTS.md) defines repo-specific operating constraints for coding agents working from the project root.
- This repository is the standalone `shipyard` project. Do not assume it is a subdirectory of another Go module when editing code or release workflow files.
