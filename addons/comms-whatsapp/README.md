# Shipyard Comms — WhatsApp

`shipyard-comms-whatsapp` links a machine to your WhatsApp as a companion
device (the same mechanism as WhatsApp Web, listed under **Linked devices**
on the phone) and keeps a local, TTL-bound archive of the chats you choose to
watch. Agents read that archive to summarize conversations.

It is a sibling of [`shipyard-comms`](../comms/README.md), not part of it:
`shipyard-comms` is stateless (one operation per run), while WhatsApp needs a
persistent connection. That connection lives in this binary's daemon, which
runs under `shipyard service`.

> **Unofficial protocol.** This uses the linked-device protocol through
> [whatsmeow](https://github.com/tulir/whatsmeow), not Meta's Business API.
> It is against WhatsApp's terms of service and carries a risk of the number
> being banned. Keep usage personal and low-volume.

## Commands

```bash
shipyard-comms-whatsapp pair                    # QR code in the terminal
shipyard-comms-whatsapp pair --phone +5511…     # 8-char code typed on the phone (headless servers)
shipyard-comms-whatsapp serve                   # the daemon; run it as a service
shipyard-comms-whatsapp status [--json]

shipyard-comms-whatsapp chats [--search família] [--watched] [--json]
shipyard-comms-whatsapp watch "Família" --ttl 7d
shipyard-comms-whatsapp history "Família" --since 24h [--json]
shipyard-comms-whatsapp unwatch "Família"       # also deletes its archived messages

shipyard-comms-whatsapp config set default-ttl 14d
shipyard-comms-whatsapp logout [--local]
```

`<chat>` accepts a JID, a chat name (case and accents ignored) or a phone
number. Running the daemon as a service:

```bash
shipyard service add --name whatsapp --auto-restart \
  --command "$(command -v shipyard-comms-whatsapp) serve"
```

## What gets stored

- **Catalog of chats** (name, kind, watched, TTL) — filled from contacts,
  joined groups and the initial history sync, so `chats` and `watch <name>`
  work right after pairing.
- **Messages of watched chats only**, from the moment they are watched.
  Messages from the initial history sync are not archived. Edits update the
  stored text; "delete for everyone" removes the row.
- **TTL is mandatory**: 1h to 90d, default 14d. The daemon prunes hourly.
- Media is recorded as kind + caption; files are not downloaded.
- Status updates, broadcast lists and channels are ignored.

Message bodies never go to the logs (`~/.shipyard/logs/comms/`), only chat ids
and counts.

## On disk

```
~/.shipyard/comms/whatsapp/        0700
  config.json                       device_name, default_ttl
  session.db                        linked-device keys (whatsmeow store)
  archive.db                        chat catalog + archived messages
  run/status.json                   written by the daemon every 30s
  run/wa.lock                       one process owns the session at a time
```

`session.db` and `archive.db` are SQLite in WAL mode, so the CLI reads and
writes them while the daemon runs.

## Daemon behaviour

- Never announces presence, so the phone keeps getting notifications.
- Exit codes: `10` already running, `20` not paired, `30` runtime error
  (including another client taking over the session), `40` logged out by the
  phone — pair again.
- A temporary ban is reported in `status` and does not stop the daemon.
- A linked device is dropped by WhatsApp when the phone stays offline for
  about 14 days.

## Build

```bash
GOTOOLCHAIN=go1.26.2 make build-comms-whatsapp
```

The SQLite driver is pure Go (`modernc.org/sqlite`); keep `CGO_ENABLED=0`.

## Roadmap

Sending (with allowlist, rate limits and chat ownership enforced in the
daemon), delivery of incoming messages to fairway, and the `shipyard comms
whatsapp` passthrough in the core CLI come in the next slices. See
`docs/comms/whatsapp-design.md` (kept locally).
