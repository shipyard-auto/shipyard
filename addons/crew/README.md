# shipyard-crew

create llm agents to automate tasks

## Overview

`shipyard-crew` is the agents addon of the Shipyard ecosystem. It lets you define individual LLM-backed agents (crew members) as inspectable directories on disk and run them on demand, on a cron schedule, or behind a webhook. Each agent wraps a backend (an external authenticated CLI such as Claude Code, or a direct call to the Anthropic API) together with a set of declarative tools that the agent can invoke.

The addon is opinionated about the wiring and unopinionated about the tools: Shipyard defines the execution protocol (lifecycle, conversation state, tool envelope), and the user writes the tools. Crew members never expose HTTP directly — the `fairway` addon is the public entrypoint, and the control plane of each running agent is a local Unix socket.

`crew` is an **optional addon**, distributed and installed under the same pattern as `fairway`. It is never a hard dependency of the core CLI: the core only reaches the addon through the public `shipyard-crew` subprocess contract or the JSON-RPC 2.0 socket.

## Architecture

```
┌─────────────┐   ┌──────────────────┐   ┌─────────────────┐
│   Trigger   │──▶│   Crew member    │──▶│  Output (tool)  │
│             │   │  (LLM + tools)   │   │                 │
└─────────────┘   └──────────────────┘   └─────────────────┘
  cron              backend:                  telegram_send
  webhook           - cli                     log_write
    (fairway)       - anthropic_api           webhook_post
  manual
```

- **Trigger** — entry point of a run (`shipyard crew run`, `shipyard cron`, or a `fairway` route).
- **Crew member** — the unit of configuration. A directory under `~/.shipyard/crew/<name>/` with `agent.yaml`, `prompt.md`, conversation state and free-form `memory/`.
- **Backend** — how the reasoning loop is executed: `cli` (pipe through an external authenticated CLI) or `anthropic_api` (Shipyard orchestrates the tool-use loop against the Anthropic API).
- **Tools** — user-declared actions the agent can invoke. Two protocols in v1: `exec` (subprocess) and `http` (native `net/http`).
- **Output** — whatever the invoked tool does; Shipyard only standardizes the JSON envelope.

## Install

```bash
shipyard crew install                   # install the version pinned to the core
shipyard crew install --version 0.1.0   # pin to a specific version
shipyard crew install --force           # reinstall / overwrite
shipyard crew version                   # check installed version
shipyard crew uninstall                 # remove the binary (preserves ~/.shipyard/crew/)
```

The binary is installed at `~/.local/bin/shipyard-crew`. The artifact is downloaded from the `crew-v<version>` release tag, SHA-256 verified against the published checksum manifest and written atomically into place. Supported platforms: `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`.

`shipyard crew uninstall` removes the binary from `~/.local/bin/shipyard-crew` only. The agents directory `~/.shipyard/crew/` is **preserved** so your crew members and conversation state survive a reinstall. If agents are still registered when you uninstall, the command emits a warning suggesting `shipyard crew fire <name>` first to deregister per-agent services cleanly.

## Quick start

1. Install the binary:
   ```bash
   shipyard crew install
   ```
2. Scaffold a crew member:
   ```bash
   shipyard crew hire promo-hunter
   ```
   This creates `~/.shipyard/crew/promo-hunter/` with an `agent.yaml`, a `prompt.md` and an empty `memory/` directory.
3. Edit the prompt:
   ```bash
   $EDITOR ~/.shipyard/crew/promo-hunter/prompt.md
   ```
4. Declare at least one tool in `agent.yaml`. Minimal examples:

   ```yaml
   tools:
     - name: auction_scraper
       protocol: exec
       command: ["/home/leo/bin/auction.py"]
       input_schema:
         category: string
         max_price: number

     - name: telegram_send
       protocol: http
       method: POST
       url: "http://localhost:9876/telegram/send"
       headers:
         Authorization: "Bearer {{env.TG_TOKEN}}"
       body: |
         {"chat_id": "{{input.chat_id}}", "text": "{{input.text}}"}
       input_schema:
         chat_id: string
         text: string
   ```
5. Run the agent once:
   ```bash
   shipyard crew run promo-hunter --input '{"user":"olá"}'
   ```

## Reusing tools across agents (tool library)

When the same tool shows up in more than one agent, promote it to the shared library under `~/.shipyard/crew/tools/` and reference it by name.

```bash
# Create an exec tool. Payload is read from stdin as JSON; envelope goes to stdout.
# Declare the expected input fields with --input-schema so the MCP bridge
# exposes them to the LLM — without it the model calls the tool with no args.
shipyard crew tool add echo \
  --protocol exec \
  --description "Echo the payload back" \
  --input-schema text=string \
  --output-schema echoed=string \
  --command /bin/sh \
  --command -c \
  --command 'read l; printf %s "{\"ok\":true,\"data\":{\"echoed\":$(printf %s "$l" | jq -Rs .)}}"'

# Or an HTTP tool.
shipyard crew tool add telegram_send \
  --protocol http --method POST \
  --url "http://localhost:9876/telegram/send" \
  --header "Authorization: Bearer {{env.TG_TOKEN}}" \
  --input-schema chat_id=string --input-schema text=string \
  --body '{"chat_id": "{{input.chat_id}}", "text": "{{input.text}}"}'

shipyard crew tool list             # table or --json
shipyard crew tool show echo        # raw YAML or --json
shipyard crew tool rm echo          # blocked if any agent uses `ref: echo`
shipyard crew tool rm echo --yes    # force removal
```

Each `tool add` writes `~/.shipyard/crew/tools/<name>.yaml` (mode `0600`) with the same schema used inline in `agent.yaml`. Reference it from any agent:

```yaml
tools:
  - ref: echo                  # picked up from the library
  - name: local_only           # inline still works, and can be mixed freely
    protocol: exec
    command: ["/bin/date"]
```

Validation rules: `ref` and inline fields are mutually exclusive on the same item; a `ref` pointing at a missing file fails `shipyard crew run` loudly (the error lists the available tools). Inline tool names cannot collide with a `ref` resolution — duplicates are rejected at load time.

## Using MCPs you already installed in Claude Code

Claude Code users typically have external MCPs configured in `~/.claude.json` (e.g. `chrome-devtools`, `@playwright/mcp`). An agent can re-use them without touching that file:

```yaml
# agent.yaml
tools:
  - ref: echo
mcp_servers:
  - ref: chrome-devtools     # must exist under ~/.claude.json mcpServers.<key>
```

At run-time, the crew backend copies the matching block **verbatim** from `~/.claude.json` into the synthesised `--mcp-config` handed to the external CLI. If the ref is missing, the run fails with the list of available keys (or `<none>` when `~/.claude.json` is absent) — there is no silent fallback. Only the root-level `mcpServers` is read in v1; per-project scopes (`projects.<path>.mcpServers`) are ignored (roadmap §1.7).

When the agent has neither tools nor `mcp_servers`, no `--mcp-config` is generated and the CLI is invoked exactly as before — so existing agents pay nothing for the new feature.

## Directory layout

```
~/.shipyard/
├── crew/
│   ├── config.yaml              # global config (pools, concurrency, queue)
│   ├── tools/
│   │   └── <tool>.yaml          # reusable tool definitions (library)
│   └── <name>/
│       ├── agent.yaml           # agent configuration
│       ├── prompt.md            # system prompt
│       ├── memory/              # free-form, agent-writable
│       ├── sessions.json        # key → cli session_id (backend=cli + stateful)
│       └── sessions/<key>.jsonl # conversation history (backend=anthropic_api + stateful)
├── run/crew/
│   ├── <name>.sock              # control socket (execution.mode=service)
│   └── <name>.pid               # PID file (execution.mode=service)
└── logs/crew/
    └── YYYY-MM-DD.jsonl         # per-day JSONL log files (transitional format)
```

Permissions: `~/.shipyard/crew/` and `~/.shipyard/run/crew/` are created with mode `0700`; scaffolded files are `0600`; sockets are `0600`.

## `agent.yaml` reference

All agents declare `schema_version: "1"` at the top. Unknown fields are rejected by the loader (`KnownFields(true)`).

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `schema_version` | string | yes | — | Must be `"1"` |
| `name` | string | yes | — | Must match `^[a-z0-9][a-z0-9_-]{0,62}$` |
| `description` | string | no | `""` | Short free-form description |
| `backend.type` | enum | yes | — | `cli` or `anthropic_api` |
| `backend.command` | []string | if `type=cli` | — | Argv of the external CLI (e.g. `["claude", "--print"]`) |
| `backend.model` | string | if `type=anthropic_api` | — | Anthropic model id (e.g. `claude-sonnet-4-6`) |
| `execution.mode` | enum | yes | — | `on-demand` or `service` |
| `execution.pool` | string | yes | — | Pool name declared in `config.yaml` |
| `conversation.mode` | enum | yes | — | `stateless` or `stateful` |
| `conversation.key` | string | if `stateful` | — | Template expression, e.g. `{{input.chat_id}}` |
| `triggers[].type` | enum | no | — | `cron` or `webhook` |
| `triggers[].schedule` | string | if `cron` | — | Cron expression (5-field standard) |
| `triggers[].route` | string | if `webhook` | — | Fairway route, must start with `/` |
| `tools[].name` | string | yes (if inline) | — | Must match `^[a-z][a-z0-9_]{0,62}$` |
| `tools[].ref` | string | yes (if library) | — | Loads `~/.shipyard/crew/tools/<ref>.yaml`. Mutually exclusive with inline fields. |
| `tools[].protocol` | enum | yes | — | `exec` or `http` |
| `tools[].description` | string | no | `""` | Human description of the tool |
| `tools[].input_schema` | map[string]string | no | `{}` | Field name → type (`string`, `number`, `boolean`, `object`, `array`) |
| `tools[].output_schema` | map[string]string | no | `{}` | Declares the shape inside `data` of the tool envelope, used to build the MCP `outputSchema` |
| `tools[].command` | []string | if `protocol=exec` | — | Argv to execute |
| `tools[].method` | string | if `protocol=http` | — | One of `GET`, `POST`, `PUT`, `PATCH`, `DELETE` |
| `tools[].url` | string | if `protocol=http` | — | Tool endpoint |
| `tools[].headers` | map[string]string | no | `{}` | HTTP headers; values support templates |
| `tools[].body` | string | no | `""` | HTTP body; supports templates |
| `mcp_servers[].ref` | string | no | — | Key from `~/.claude.json` `mcpServers` to expose verbatim on this agent's MCP config |

Validation rules enforced by the loader:

- `backend.type=cli` → `command` non-empty, `model` must be empty.
- `backend.type=anthropic_api` → `model` non-empty, `command` must be empty.
- `conversation.mode=stateless` → `key` must be empty.
- `tools[]` names must be unique within the agent.
- Duplicate triggers (same `type`+`schedule`+`route`) are rejected.

### Complete example

```yaml
schema_version: "1"
name: promo-hunter
description: Busca promoções em sites de leilão e alerta via Telegram

backend:
  type: cli
  command: ["claude", "--print"]

execution:
  mode: on-demand
  pool: cli

conversation:
  mode: stateless

triggers:
  - type: cron
    schedule: "0 */3 * * *"
  - type: webhook
    route: /promo-hunter

tools:
  - name: auction_scraper
    protocol: exec
    command: ["/home/leo/bin/auction.py"]
    description: "Busca leilões ativos. Retorna lista JSON."
    input_schema:
      category: string
      max_price: number

  - name: telegram_send
    protocol: http
    method: POST
    url: "http://localhost:9876/telegram/send"
    headers:
      Authorization: "Bearer {{env.TG_TOKEN}}"
    body: |
      {"chat_id": "{{input.chat_id}}", "text": "{{input.text}}"}
    input_schema:
      chat_id: string
      text: string
```

## Global config (`~/.shipyard/crew/config.yaml`)

Optional. Missing file uses defaults.

```yaml
concurrency:
  default_pool: cli
  pools:
    cli:
      max: 4
  queue:
    strategy: wait          # "wait" or "reject"
    max_wait: 30s
    max_queue_size: 16
```

Defaults: one `cli` pool with `max: 4`, queue strategy `wait`, `max_wait: 30s`, `max_queue_size: 16`.

## Tool contract

Every tool call exchanges a JSON envelope. The payload is identical regardless of protocol.

```json
{ "ok": true,  "data":    { ... } }
```

```json
{ "ok": false, "error":   "human message", "details": { ... } }
```

Protocol mapping:

- **`exec`** — input JSON is written to the tool's stdin; the envelope is read from stdout. Exit `0` means the tool tried to respond (shipyard parses `ok` from stdout). Non-zero exit means the tool crashed — shipyard surfaces `{ok: false, error: "tool crashed"}` to the agent and logs stderr.
- **`http`** — input JSON is the request body; the envelope is the response body. A `2xx` status means the tool tried to respond. Any other status is a hard failure; the body is attached to the log.

Shipyard never shells out to `curl`/`wget` — HTTP is native in Go. `exec` is reserved for code the user wrote.

### MCP bridge (backend `cli`)

When an agent with `backend.type=cli` runs, Shipyard inspects `tools` and `mcp_servers`. If either is non-empty, it writes a temporary `--mcp-config` JSON file and passes `--mcp-config <path> --strict-mcp-config` to the external CLI (e.g. `claude --print`).

The synthesised config always has the same shape:

```jsonc
{
  "mcpServers": {
    // Declared when the agent has any tools. Bridges the LLM back into
    // the crew process so tool calls hit `tools.Dispatcher` and produce
    // the exact envelope above.
    "shipyard-crew-internal": {
      "type": "stdio",
      "command": "<path to shipyard-crew>",
      "args": ["mcp-serve", "--agent", "<name>"]
    },
    // Any entry listed in agent.yaml::mcp_servers[] is copied byte-for-byte
    // from ~/.claude.json.mcpServers.<ref>. A missing ref is a hard error.
    "chrome-devtools": { "type": "stdio", "command": "npx", "args": ["-y", "chrome-devtools-mcp"] }
  }
}
```

Each tool's `input_schema` / `output_schema` is translated to a JSON-Schema `inputSchema` / `outputSchema` on the MCP descriptor; the envelope's `data` field becomes `CallToolResult.structuredContent`, `ok=false` becomes `isError: true`. Agents that declare no tools and no `mcp_servers` pay nothing — the CLI is invoked exactly as before.

## Template engine

Placeholders use `{{namespace.field}}`. Supported namespaces:

| Namespace | Example | Origin |
|---|---|---|
| `input` | `{{input.chat_id}}` | Payload the agent passed to the tool (or the run input for conversation key) |
| `env` | `{{env.TG_TOKEN}}` | Environment variable of the crew member process |
| `agent` | `{{agent.name}}`, `{{agent.dir}}` | Agent metadata |

No loops, no conditionals, no pipes. If you need logic, wrap it in a tool.

## Triggers

- **Manual.** `shipyard crew run <name> [--input JSON|--input-file PATH]` — always available.
- **Cron.** `triggers[].type=cron` with a `schedule` expression. Reconciled by `shipyard crew apply <name>` against `shipyard cron` using the name format `crew:<agent>:<idx>`.
- **Webhook.** `triggers[].type=webhook` with a `route` starting with `/`. Reconciled against `shipyard fairway`; the incoming request is dispatched to the crew member via subprocess or socket.

## Execution modes

- **`on-demand`** (default). Each run spawns a fresh `shipyard-crew` process: fork, execute one turn, exit. Good fit: low-frequency cron jobs, occasional webhooks, stateless tasks.
- **`service`**. A long-lived daemon: acquires a PID file, binds `~/.shipyard/run/crew/<name>.sock` (mode `0600`) and serves JSON-RPC 2.0 with a mandatory handshake as the first message. Managed via `shipyard service start|stop|status crew-<name>`. Good fit: multi-turn conversations with low latency (active Telegram chat), hot backends with warm context, frequent webhook streams.

Rule of thumb: if the process does nothing 99% of the time and start cost is tolerable, stay on `on-demand`. If you need a warm CLI session, a long-lived model context or sub-100ms dispatch latency, switch to `service`.

## Conversation modes

- **`stateless`** (default). Each run is independent; no replay.
- **`stateful`**. Shipyard keeps state keyed by `conversation.key`.
  - With `backend.type=cli`: `~/.shipyard/crew/<name>/sessions.json` maps each conversation key to an external CLI `session_id` (e.g. `claude --resume <session>`).
  - With `backend.type=anthropic_api`: `~/.shipyard/crew/<name>/sessions/<key>.jsonl` stores the full message history; Shipyard injects it on every call.

The free-form `memory/` directory is independent of conversation state — the agent can read and write files there as long-term notes regardless of the conversation mode.

## Command reference

```
shipyard crew install   [--version VER] [--force]
shipyard crew uninstall [--yes]
shipyard crew version   [--json]
shipyard crew hire      <name> [--backend cli|anthropic_api] [--mode on-demand|service] [--force] [--from ID]
shipyard crew fire      <name> [--yes] [--keep-logs]
shipyard crew apply     <name> [--dry-run] [--json]
shipyard crew list      [--json] [--long] [-v|--verbose]
shipyard crew run       <name> [--input JSON] [--input-file PATH] [--timeout DUR] [--json]
shipyard crew logs      <name> [-f|--follow] [--since DUR] [--tail N] [--json]
shipyard crew tool add  <name> --protocol exec|http [--command ... | --method/--url/--header/--body] [--input-schema KEY=TYPE ...] [--output-schema KEY=TYPE ...] [--description TEXT] [--force]
shipyard crew tool list [--json]
shipyard crew tool show <name> [--json]
shipyard crew tool rm   <name> [--yes]
```

The `tool` subcommand group manages the reusable tool library under `~/.shipyard/crew/tools/`. Any agent can reuse a library tool with `tools: [{ref: <tool-name>}]`; inline definitions remain supported and can be mixed freely with refs. `tool rm` refuses removal when an agent references the tool, unless `--yes` is passed.

Notes on flags:

- `hire --from <id>` is **reserved** in v1; it is accepted but does nothing (`--from` becomes functional when template presets land in v2 — see `docs/crew/roadmap.md` §3.3).
- `fire` refuses to run non-interactively unless `--yes` is provided. It deregisters the per-agent service (when `mode=service`), removes owned cron entries (names matching `crew:<agent>:*`), removes owned fairway routes, removes the PID file and socket, and deletes the agent directory. `--keep-logs` preserves `~/.shipyard/logs/crew/<name>/`.
- `run` dispatches to the socket when `mode=service` (with handshake); falls back to the `shipyard-crew` subprocess when the daemon is unreachable. When `--json` is set, the final result envelope is written to stdout; a one-line summary (`trace_id=... status=... duration=...ms`) always goes to stderr.
- `apply` is a thin proxy over `shipyard-crew reconcile`; exit codes are propagated verbatim.

### CLI exit codes (`shipyard crew run`)

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | Business error (the agent ran but returned `status=err`) |
| `2` | Invalid arguments or input (bad `--input` JSON, missing agent, invalid `execution.mode`) |
| `60` | Timeout (daemon didn't respond within `--timeout`) |
| `70` | Version mismatch between `shipyard` and the `shipyard-crew` daemon |
| `99` | Internal error |

### `shipyard-crew` binary exit codes

The runtime binary — invoked directly by `shipyard crew run`, `shipyard service` and integrators — uses this table as a public contract:

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | Business failure (tool returned error, backend failed semantically) |
| `2` | Invalid input (malformed stdin JSON or missing/invalid flag) |
| `3` | Internal error in on-demand mode |
| `10` | PID conflict — another live instance owns the PID file |
| `20` | Failed to load `agent.yaml` |
| `30` | Failed to build runtime (config / pool / backend / store) |
| `50` | Graceful shutdown exceeded the 15s budget |

Binary flags: `--agent <name>` (required), `--service` (run as daemon), `--config <path>`, `--log-dir <path>`, `--version`.

Subcommands (preceding flags):
- `shipyard-crew reconcile --agent <name>` — reconciles cron + fairway triggers.
- `shipyard-crew mcp-serve --agent <name>` — internal MCP stdio server spawned by the CLI backend when declared tools exist; not intended to be called directly by users.

## Observability

`shipyard crew logs <name>` reads the per-day JSONL files under `~/.shipyard/logs/crew/`, filters by agent and prints them. Flags:

- `--follow` / `-f` — tail new lines as files grow.
- `--since DUR` — only entries newer than the given duration (e.g. `1h`).
- `--tail N` — keep the last N entries (default `100`, `0` = all).
- `--json` — emit the raw JSONL line without reformatting.

The log format is **transitional**. See `docs/crew/roadmap.md` §1.1 for the open design. Fields currently respected by the logs command: `ts`, `level`, `message`, `trace_id`, `agent`, and a flexible `fields` map; unknown fields are ignored.

## Known limits (v1)

The roadmap at `docs/crew/roadmap.md` has the full list. Highlights:

- No subagents (§2.1).
- No multi-agent communication / `crew_call` tool (§2.2).
- No integrated secrets manager; use `{{env.*}}` and shell-level injection (§2.3).
- Backend `cli` exposes declared tools through an auto-provisioned MCP stdio bridge (Task 34), but:
  - `mcp_servers[]` does not yet support an `only:` filter; referencing an external MCP exposes all of its tools (§1.7).
  - Only the root `mcpServers` of `~/.claude.json` is consumed; per-project scopes (`projects.<path>.mcpServers`) are ignored (§1.7).
  - In `execution.mode: service`, the internal MCP subprocess is still cold-started per turn (§1.7).
- The `ollama` pool is documented as an example in `config.yaml` but has no dedicated backend driver in v1 (§2.5).
- Log format is provisional (§1.1).
- `shipyard crew uninstall` only warns (does not abort) when agents are still registered with OS services (§1.5.1).

## Troubleshooting

- **`shipyard crew run` returns exit 10 / "crew daemon already running".** A previous daemon left a PID file that still points to a live process. Verify with `cat ~/.shipyard/run/crew/<name>.pid` and `ps -p <pid>`; stop it via `shipyard service stop crew-<name>` or `kill <pid>` before retrying.
- **Tool returned `{ok: false}` or crashed.** Run `shipyard crew logs <name> --tail 50` and inspect stderr captured for the failing tool. For `exec`, a non-zero exit is a hard failure; for `http`, anything outside `2xx` is a hard failure. The stderr/body is recorded in the log line.
- **`shipyard crew run` reports "daemon not responding" / socket connection refused.** The service daemon is not running. Check with `shipyard service status crew-<name>`; start it with `shipyard service start crew-<name>`. `run` automatically falls back to the `shipyard-crew` subprocess when the socket is unreachable, so subsequent runs still succeed with warning logs.
- **`shipyard crew run` exits `70` with "version mismatch".** The daemon and the core CLI disagree on protocol version. Reinstall the addon: `shipyard crew install --force`, then restart the daemon (`shipyard service restart crew-<name>`).
- **"pool full" or `--timeout` exceeded while queued.** The configured concurrency pool has no free slot and the queue is full or the wait exceeded `max_wait`. Increase `concurrency.pools.<pool>.max` or `concurrency.queue.max_wait` in `~/.shipyard/crew/config.yaml`, or flip the strategy to `reject` if you prefer fast failures (e.g. for webhooks).
- **`~/.local/bin is not in your PATH` after install.** The installer warns when `~/.local/bin` is missing from `$PATH`. Add it to your shell rc file (e.g. `export PATH="$HOME/.local/bin:$PATH"`) and open a new shell.
- **`shipyard crew run` fails with "unknown tool ref `<name>`".** The agent's `tools: [{ref: <name>}]` does not match any file in `~/.shipyard/crew/tools/`. List with `shipyard crew tool list`, then either `shipyard crew tool add <name> …` or edit the agent to use a ref that exists.
- **`shipyard crew run` fails with "mcp_servers: ref `<k>` not found; available: …".** The ref does not match any key of `mcpServers` in `~/.claude.json`. The error message prints the available keys (or `<none>` when the file is absent). Add the MCP to `~/.claude.json` via `claude mcp add …` or remove the ref from the agent.

## Development

```bash
make build-crew                  # build local binary into dist/shipyard-crew
make test                        # run the full test suite
go test ./addons/crew/...        # addon packages only
make dist-crew                   # cross-compile release binaries
make package-crew checksums-crew # package tarballs and generate SHA-256 manifest
```

Architectural boundary: `addons/crew/internal/*` is **never** imported by the core. The only bridges are the `shipyard-crew` subprocess contract and the JSON-RPC 2.0 socket. Core-side code lives in `internal/cli/crew/` (cobra subcommands) and `internal/crewctl/` (installer, JSON-RPC client, binary resolver).
