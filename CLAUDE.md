# Shipyard — Context for Coding Agents

## What Is Shipyard

Shipyard is a standalone Go CLI for local automation and OS-integrated operations on Linux and macOS. It is intentionally local-first: no cloud account required, no background service mandatory for core features.

The binary is named `shipyard`. Install it once per machine and manage cron jobs, user services, structured logs, HTTP routing (fairway addon), and LLM agents (crew addon) from a single tool.

## Repository Layout

| Path | Role |
|------|------|
| `cmd/shipyard/main.go` | Binary entrypoint |
| `internal/cli/` | All command definitions (cobra) |
| `internal/cli/crew/` | `shipyard crew` subcommands |
| `internal/cron/` | Cron subsystem business logic |
| `internal/service/` | Service subsystem business logic |
| `internal/logs/` | Log subsystem business logic |
| `internal/ui/` | Terminal rendering and TUI wizards |
| `internal/fairwayctl/` | Fairway HTTP gateway control logic |
| `internal/crewctl/` | Crew addon control logic |
| `addons/fairway/` | Fairway HTTP gateway addon (separate Go module) |
| `addons/crew/` | Crew LLM agent addon (separate Go module) |
| `manifest` | Release source of truth (`shipyard=` and `fairway=` lines) |

## Local State

Shipyard stores all state under `~/.shipyard/`:

| File / Dir | Purpose |
|------------|---------|
| `~/.shipyard/install.json` | Installation metadata |
| `~/.shipyard/crons.json` | Shipyard-managed cron jobs |
| `~/.shipyard/services.json` | Shipyard-managed user services |
| `~/.shipyard/logs.json` | Logs subsystem config |
| `~/.shipyard/logs/` | JSONL log files per source |
| `~/.shipyard/fairway/` | Fairway addon state |
| `~/.shipyard/crew/` | Crew addon state and agent definitions |
| `~/.shipyard/crew/tools/` | Reusable tool definitions for crew agents |

## Build and Test

```bash
# build the binary
GOTOOLCHAIN=go1.26.2 go build ./cmd/shipyard

# run all tests
GOTOOLCHAIN=go1.26.2 go test ./...

# test a specific subsystem
cd addons/fairway && GOTOOLCHAIN=go1.26.2 go test ./... -count=1
cd addons/crew    && GOTOOLCHAIN=go1.26.2 go test ./... -count=1
```

## CLI Reference

### Top-Level Commands

```
shipyard [command] [flags]
```

| Command | Purpose |
|---------|---------|
| `version` | Print the installed version, commit, and build date |
| `update` | Download and replace the binary with the latest release |
| `uninstall` | Remove the binary and `~/.shipyard/` directory |
| `cron` | Manage Shipyard-owned cron jobs |
| `service` | Manage Shipyard-owned user services |
| `logs` | Inspect structured local logs |
| `fairway` | Manage the shipyard-fairway HTTP gateway addon |
| `crew` | Manage the shipyard-crew LLM agent addon |

---

### `shipyard version`

Print version, commit hash, and build date.

```bash
shipyard version
```

---

### `shipyard update`

Download the latest published release for this platform and replace the running binary. If shipyard-fairway is installed, it is also updated.

```bash
shipyard update
```

---

### `shipyard uninstall`

Remove Shipyard completely. Deletes the binary and `~/.shipyard/`. Prompts for confirmation unless `--yes` is passed.

```bash
shipyard uninstall
shipyard uninstall --yes   # skip confirmation prompt
```

---

### `shipyard cron`

Create and manage cron jobs stored in `~/.shipyard/crons.json` and synchronized to the current user's crontab. Shipyard only touches jobs it created; external crontab entries are never modified.

#### `shipyard cron list`

List all Shipyard-managed cron jobs in a table.

```bash
shipyard cron list
shipyard cron list --json   # emit as JSON array
```

#### `shipyard cron show <id>`

Show full metadata for a single cron job.

```bash
shipyard cron show AB12CD
```

#### `shipyard cron add`

Create a new cron job. Provide fields via flags or load a JSON definition from disk.

```bash
shipyard cron add \
  --name "Backup" \
  --schedule "0 * * * *" \
  --command "/usr/local/bin/backup-home"

shipyard cron add --file ./backup-cron.json
```

Flags: `--name`, `--description`, `--schedule`, `--command`, `--enabled` (default `true`), `--file`.

#### `shipyard cron update <id>`

Update fields of an existing cron job.

```bash
shipyard cron update AB12CD --schedule "30 2 * * *"
shipyard cron update AB12CD --file ./updated.json
```

#### `shipyard cron enable <id>` / `disable <id>`

Toggle the enabled state of a cron job without deleting it.

```bash
shipyard cron enable  AB12CD
shipyard cron disable AB12CD
```

#### `shipyard cron run <id>`

Run a cron job immediately, outside of its schedule. Useful for testing.

```bash
shipyard cron run AB12CD
```

#### `shipyard cron delete <id>`

Remove a Shipyard cron job and the corresponding crontab entry.

```bash
shipyard cron delete AB12CD
```

#### `shipyard cron config`

Open the interactive full-screen TUI for cron management. Supports add, browse, update, enable/disable, run, and delete flows. Requires a real TTY; not suitable for scripts.

```bash
shipyard cron config
```

---

### `shipyard service`

Create and manage user-scoped services stored in `~/.shipyard/services.json` and projected into the platform service manager. Uses `systemd --user` on Linux and user `launchd` agents on macOS. Shipyard only manages services it created; external units are never touched.

#### `shipyard service list`

List all Shipyard-managed services with current runtime state.

```bash
shipyard service list
```

#### `shipyard service show <id>`

Show full metadata and runtime status for a service.

```bash
shipyard service show AB12CD
```

#### `shipyard service add`

Create a new managed service. Provide fields via flags or load a JSON definition.

```bash
shipyard service add \
  --name "My Worker" \
  --command "/usr/local/bin/my-worker"

shipyard service add --file ./worker.json
```

Flags: `--name`, `--description`, `--command`, `--working-dir`, `--env KEY=VAL` (repeatable), `--auto-restart`, `--enabled`, `--file`.

#### `shipyard service update <id>`

Update fields of an existing service.

```bash
shipyard service update AB12CD --command "/usr/local/bin/my-worker-v2"
```

#### `shipyard service delete <id>`

Stop, disable, and remove a Shipyard service and its unit file.

```bash
shipyard service delete AB12CD
```

#### `shipyard service enable <id>` / `disable <id>`

Enable or disable a service at boot without removing it.

```bash
shipyard service enable  AB12CD
shipyard service disable AB12CD
```

#### `shipyard service start <id>` / `stop <id>` / `restart <id>`

Control the running state of a service.

```bash
shipyard service start   AB12CD
shipyard service stop    AB12CD
shipyard service restart AB12CD
```

#### `shipyard service status <id>`

Display runtime state, PID, last exit code, and uptime.

```bash
shipyard service status AB12CD
```

#### `shipyard service config`

Open the interactive full-screen TUI for service management. Requires a real TTY.

```bash
shipyard service config
```

---

### `shipyard logs`

Inspect structured local logs stored in `~/.shipyard/logs/` as JSONL files. The log model is shared across subsystems (cron, service, future agent runtimes).

#### `shipyard logs list`

Show all known log sources with file count, total size, and newest file.

```bash
shipyard logs list
```

#### `shipyard logs show`

Print recent log entries. Defaults to the `cron` source.

```bash
shipyard logs show --source cron
shipyard logs show --source cron --id AB12CD --limit 20
shipyard logs show --level error
```

Flags: `--source` (default `cron`), `--id`, `--level`, `--limit` (default `50`).

#### `shipyard logs tail`

Stream live log entries as they are written. Press `Ctrl+C` to stop.

```bash
shipyard logs tail --source cron
shipyard logs tail --source cron --id AB12CD
```

Flags: `--source`, `--id`, `--level`.

#### `shipyard logs prune`

Delete log files older than the configured retention period. Reports deleted file count and freed bytes.

```bash
shipyard logs prune
```

#### `shipyard logs config`

Show or update logs configuration. When run interactively without extra arguments, opens a TUI control panel. For scripting, use the `set` subcommand.

```bash
shipyard logs config                         # TUI (requires TTY)
shipyard logs config set retention-days 30   # non-interactive
```

---

### `shipyard fairway`

Manage the shipyard-fairway HTTP gateway addon. Fairway is a separate daemon that exposes HTTP routes and forwards requests to crew agents, shell commands, or upstream URLs.

#### `shipyard fairway install`

Download and install the fairway daemon for this platform.

```bash
shipyard fairway install
shipyard fairway install --version 1.2.3   # pin to a specific version
shipyard fairway install --force           # reinstall even if already present
```

#### `shipyard fairway uninstall`

Stop and remove the fairway daemon.

```bash
shipyard fairway uninstall
```

#### `shipyard fairway status`

Show daemon state, installed version, bound address, and connected routes.

```bash
shipyard fairway status
```

#### `shipyard fairway stats`

Show request statistics from the running daemon.

```bash
shipyard fairway stats
shipyard fairway stats --json
shipyard fairway stats --by-route
shipyard fairway stats --by-status
```

#### `shipyard fairway logs`

Inspect fairway access and event logs.

```bash
shipyard fairway logs show
shipyard fairway logs tail
```

#### `shipyard fairway route`

Manage HTTP routes registered in the daemon.

```bash
shipyard fairway route list
shipyard fairway route add --path /hook --action crew.run --target my-agent
shipyard fairway route delete /hook
```

#### `shipyard fairway config`

Open the interactive TUI for fairway route management. Requires a real TTY and a running daemon.

```bash
shipyard fairway config
```

---

### `shipyard crew`

Manage the shipyard-crew LLM agent addon. Crew lets you define, run, and monitor LLM-backed agents that automate tasks. Agents are defined as YAML files under `~/.shipyard/crew/<name>/`.

#### `shipyard crew install`

Download and install the crew daemon binary.

```bash
shipyard crew install
```

#### `shipyard crew uninstall`

Remove the crew daemon binary and associated state.

```bash
shipyard crew uninstall
```

#### `shipyard crew version`

Print the installed crew addon version.

```bash
shipyard crew version
```

#### `shipyard crew hire <name>`

Scaffold a new agent definition under `~/.shipyard/crew/<name>/`. Creates `agent.yaml` and `prompt.md`.

```bash
shipyard crew hire my-agent
shipyard crew hire my-agent --backend anthropic_api   # use Anthropic API directly
shipyard crew hire my-agent --mode service            # run as a persistent service
shipyard crew hire my-agent --force                   # overwrite existing scaffold
```

Flags: `--backend` (`cli` | `anthropic_api`, default `cli`), `--mode` (`on-demand` | `service`, default `on-demand`), `--force`.

#### `shipyard crew fire <name>`

Remove an agent definition and stop it if it is running as a service.

```bash
shipyard crew fire my-agent
```

#### `shipyard crew apply [name]`

Reconcile agent definitions with the running crew daemon. Without a name, reconciles all agents.

```bash
shipyard crew apply
shipyard crew apply my-agent
shipyard crew apply --dry-run   # show what would change without applying
shipyard crew apply --json      # machine-readable output
```

#### `shipyard crew list`

List all defined crew agents with backend, mode, triggers, and current state.

```bash
shipyard crew list
shipyard crew list --json
```

#### `shipyard crew run <name>`

Invoke an on-demand agent and stream its output. Sends stdin to the agent if provided.

```bash
shipyard crew run my-agent
echo "summarize this" | shipyard crew run my-agent
```

#### `shipyard crew logs [name]`

Show or tail log output from crew agents.

```bash
shipyard crew logs
shipyard crew logs my-agent
shipyard crew logs my-agent --tail
```

#### `shipyard crew tool`

Manage the reusable tool library available to crew agents. Tools live in `~/.shipyard/crew/tools/<name>.yaml`.

```bash
shipyard crew tool list
shipyard crew tool show my-tool
shipyard crew tool add --name my-tool --protocol exec --command '["curl", "-s", "https://example.com"]'
shipyard crew tool rm my-tool
```

`--protocol` is `exec` (shell command) or `http` (HTTP request).

---

## Architecture Rules

- Keep business logic in `internal/`; keep `cmd/` thin.
- OS boundaries (crontab, systemd, launchd) belong in subsystem packages, not in CLI handlers.
- The CLI must not import `addons/*/internal/`; communicate with addons via subprocess contracts and JSON-RPC sockets.
- Shipyard only manages state it created. Never modify external crontab entries, units, or agents automatically.
- Prefer structured local state (`~/.shipyard/*.json`) over hidden side effects.
- Never bump version fields in `manifest` from code; that is a manual human step.
- Never touch `.github/workflows/` or `.github/actions/` unless the task is explicitly about CI.
