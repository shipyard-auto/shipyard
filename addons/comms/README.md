# Shipyard Comms

`shipyard-comms` is the messaging addon for [Shipyard](../../README.md). It sends and (via fairway-delivered webhooks) receives messages through external providers like Telegram, Slack and WhatsApp.

It is installed and managed by the core CLI through:

```bash
shipyard comms <command>
```

## What Comms Does

Comms is the **transport layer** for external messaging. It owns:

- the catalog of configured channels (per-provider connections),
- the credentials for each channel,
- the normalized `Message` envelope schema,
- the `send` operation invoked by any caller (scripts, cron jobs, crew agents, fairway actions).

Comms does **not** route inbound events or match patterns. Webhook traffic enters through `fairway`, which dispatches the configured action (an `exec` or `crew.run`). The action then decides what to do with the payload — and, if it wants to reply, calls `shipyard comms send`.

This separation keeps `comms` and `fairway` as independent siblings: neither imports the other.

## Providers

| Type | Credentials | How it sends |
|---|---|---|
| `telegram` | bot token (`--telegram-token`) | HTTPS to the Bot API |
| `whatsapp` | none | thin client of the `shipyard-comms-whatsapp` daemon's control socket (`<comms home>/whatsapp/run/wa.sock`) |

WhatsApp needs a persistent linked-device connection, so it lives in its own
binary and daemon (see [`addons/comms-whatsapp`](../comms-whatsapp/README.md)),
coupled to comms only by the socket contract. Sending through comms applies
the daemon's policy — allowlist, prior conversation, rate limits, pause — and
refusals come back as `whatsapp: refused (<code>): …`.

```bash
shipyard comms channel add --type whatsapp --name whatsapp
shipyard comms send --channel whatsapp --to "+55 12 98201-6477" --text "oi" --idempotency-key job-42
```

`--to` takes a JID, a phone number or (daemon ≥ 0.3.0) a chat name.
`--idempotency-key` is forwarded to providers that support it (whatsapp): a
repeated key returns the first result instead of sending twice.

## Stateless in v1

`comms` is invoked, runs a single operation, and exits. No daemon, no socket. The v1 providers (Telegram, Slack Events, WhatsApp Cloud API) are all stateless HTTPS calls, so per-invocation cold start is the right trade.

Persistent-connection providers do not turn `comms` into a daemon: each runs as its own binary (today `shipyard-comms-whatsapp`) and `comms` reaches it through a local socket.

## Layout

- `cmd/`             — `shipyard-comms` binary entry point.
- `internal/comms/`  — domain model (`Message` envelope, `Channel` config, storage).
- `internal/app/`    — composition root.
- `docs/product.md`  — short product overview.

## Build

```bash
GOTOOLCHAIN=go1.26.2 go build ./addons/comms/cmd/...
```

## Plan

The architectural plan and refinements are tracked locally under `docs/comms/` and `docs/tasks/` in the repo root.
