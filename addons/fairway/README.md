# Shipyard Fairway

`shipyard-fairway` is the HTTP gateway addon for Shipyard.

It exposes HTTP endpoints, authenticates incoming requests, resolves routes in memory, and dispatches actions into the Shipyard ecosystem. The addon is installed and managed by the core CLI through:

```bash
shipyard fairway <command>
```

Fairway is version-locked to the same `VERSION` as the core. Releases use a separate tag namespace so they do not collide with core releases:

- core: `v<VERSION>`
- fairway: `fairway-v<VERSION>`

## What Fairway Does

Fairway is the external entrypoint of Shipyard.

Typical flow:

```text
external webhook -> fairway HTTP listener -> auth -> route match -> action dispatch
```

Examples:

- GitHub webhook triggers a cron job
- internal system sends a signed HTTP event
- Telegram or message integrations forward work into Shipyard actions
- HTTP requests are forwarded to another upstream

Control plane and data plane are separate:

- HTTP listener: receives real traffic
- Unix socket: used by the CLI to manage routes, status, stats, logs, and the config wizard

## Install

For normal usage, install through the Shipyard CLI:

```bash
shipyard fairway install
```

Useful variants:

```bash
shipyard fairway install --force
shipyard fairway upgrade
shipyard fairway uninstall
shipyard fairway uninstall --purge
```

What installation does:

- downloads the `shipyard-fairway` release artifact matching your OS/arch
- verifies checksum
- installs the binary in `~/.local/bin/shipyard-fairway`
- registers the addon as a Shipyard-managed service

## Download Artifacts Directly

Release URL pattern:

```text
https://github.com/shipyard-auto/shipyard/releases/download/fairway-v<VERSION>/shipyard-fairway_<VERSION>_<goos>_<goarch>.tar.gz
```

Example for `0.22` on `linux/amd64`:

```text
https://github.com/shipyard-auto/shipyard/releases/download/fairway-v0.22/shipyard-fairway_0.22_linux_amd64.tar.gz
```

Checksum manifest:

```text
https://github.com/shipyard-auto/shipyard/releases/download/fairway-v<VERSION>/shipyard-fairway_<VERSION>_checksums.txt
```

## Commands

Main commands:

```bash
shipyard fairway install
shipyard fairway status
shipyard fairway config
shipyard fairway route list
shipyard fairway logs
shipyard fairway stats
shipyard fairway upgrade
shipyard fairway uninstall
```

### Status

See installation state, daemon state, route count and request stats summary:

```bash
shipyard fairway status
shipyard fairway status --json
```

### Interactive Config Wizard

Open the TUI wizard to create, edit and delete routes:

```bash
shipyard fairway config
```

Use the wizard when you want guided setup instead of memorizing flags.

### Route Management

List routes:

```bash
shipyard fairway route list
shipyard fairway route list --json
```

Add a bearer-protected route that runs a cron job:

```bash
shipyard fairway route add \
  --path /hooks/github \
  --auth bearer \
  --auth-token super-secret \
  --action cron.run \
  --target AB12CD \
  --timeout 30s
```

Add a token-based route using a custom header:

```bash
shipyard fairway route add \
  --path /hooks/partner \
  --auth token \
  --auth-value expected-token \
  --auth-header X-Partner-Token \
  --action service.restart \
  --target API01
```

Add a local-only internal route:

```bash
shipyard fairway route add \
  --path /internal/events \
  --auth local-only \
  --action telegram.handle
```

Add an HTTP forward route:

```bash
shipyard fairway route add \
  --path /hooks/forward \
  --auth bearer \
  --auth-token forward-secret \
  --action http.forward \
  --url https://example.com/inbox \
  --method POST
```

Load a route from JSON:

```bash
shipyard fairway route add --from-file ./route.json
```

Delete a route:

```bash
shipyard fairway route delete /hooks/github
shipyard fairway route delete /hooks/github --yes
```

Test a route through the daemon:

```bash
shipyard fairway route test /hooks/github
shipyard fairway route test /hooks/github --method POST --header X-Test=1
shipyard fairway route test /hooks/github --body-file ./payload.json
```

### Logs

Read JSONL request logs directly from disk:

```bash
shipyard fairway logs
shipyard fairway logs --pretty
shipyard fairway logs --json
shipyard fairway logs --since 10m
shipyard fairway logs --date 2026-04-17
shipyard fairway logs --follow
shipyard fairway logs --level error
```

### Stats

Read daemon counters from the socket:

```bash
shipyard fairway stats
shipyard fairway stats --json
shipyard fairway stats --by-route
shipyard fairway stats --by-status
```

## Important Paths

Default runtime paths:

```text
~/.local/bin/shipyard-fairway
~/.shipyard/fairway/
~/.shipyard/fairway/routes.json
~/.shipyard/fairway/config.json
~/.shipyard/logs/fairway/
~/.shipyard/run/fairway.sock
~/.shipyard/run/fairway.pid
```

Meaning:

- `routes.json`: persisted route definitions
- `config.json`: daemon config
- `logs/fairway/`: request logs in JSONL
- `fairway.sock`: control socket used by the CLI
- `fairway.pid`: single-instance lock

## Manual Test Guide

This is the fastest manual smoke test before heavier E2E work.

### 1. Install and Verify

```bash
shipyard fairway install
shipyard fairway status
```

Expected:

- install succeeds
- status shows fairway installed
- daemon responds through the socket

### 2. Create a Route

Using the wizard:

```bash
shipyard fairway config
```

Or using flags:

```bash
shipyard fairway route add \
  --path /hooks/github \
  --auth bearer \
  --auth-token super-secret \
  --action cron.run \
  --target AB12CD
```

Verify:

```bash
shipyard fairway route list
```

### 3. Test Through the Daemon

```bash
shipyard fairway route test /hooks/github
```

This validates route resolution, auth plumbing, action dispatch and response mapping through the socket-side test harness.

### 4. Hit the Real HTTP Listener

If fairway is listening on the default bind/port:

```bash
curl -i \
  -H 'Authorization: Bearer super-secret' \
  -X POST \
  http://127.0.0.1:9876/hooks/github
```

Then inspect:

```bash
shipyard fairway logs --pretty --since 5m
shipyard fairway stats
```

### 5. Exercise Lifecycle

```bash
shipyard fairway upgrade
shipyard fairway status
shipyard fairway uninstall
```

If you want to remove state too:

```bash
shipyard fairway uninstall --purge
```

## Example JSON Route

```json
{
  "path": "/hooks/github",
  "auth": {
    "type": "bearer",
    "token": "super-secret"
  },
  "action": {
    "type": "cron.run",
    "target": "AB12CD"
  },
  "timeout": 30000000000
}
```

`timeout` is encoded as a Go duration in nanoseconds when represented as raw JSON.

## Troubleshooting

### Fairway is installed but status says stopped

Run:

```bash
shipyard fairway status
shipyard fairway logs --since 10m
```

Check:

- whether the service was registered correctly
- whether the daemon socket exists
- whether the binary version matches the core version

### Version mismatch

If the CLI and daemon versions diverge, the socket handshake rejects the connection.

Fix:

```bash
shipyard fairway upgrade
```

### No TTY for the wizard

The config wizard requires a real terminal. In CI or scripts, use the non-interactive commands:

```bash
shipyard fairway route add ...
shipyard fairway route delete ... --yes
shipyard fairway route list --json
```

## Recommended Workflow

For day-to-day work:

1. `shipyard fairway install`
2. `shipyard fairway config`
3. `shipyard fairway route list`
4. trigger traffic
5. `shipyard fairway logs --pretty`
6. `shipyard fairway stats`

For automation:

1. `shipyard fairway install`
2. `shipyard fairway route add --from-file ...`
3. `shipyard fairway route list --json`
4. `shipyard fairway status --json`
