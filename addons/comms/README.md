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

## Stateless in v1

`comms` is invoked, runs a single operation, and exits. No daemon, no socket. The v1 providers (Telegram, Slack Events, WhatsApp Cloud API) are all stateless HTTPS calls, so per-invocation cold start is the right trade.

Persistent-connection providers (Baileys, IMAP, Discord gateway) are not supported in v1 — when they enter, `comms` grows a daemon mode.

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
