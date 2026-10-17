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

Every command documents itself, written for people and for agents that
arrive without context:

```bash
shipyard-comms-whatsapp help                 # overview, concepts, rules for agents
shipyard-comms-whatsapp help send            # same as: send --help / send -h
shipyard-comms-whatsapp help sending         # topics: sending, agent-tools, files, exit-codes
```

Flags in help come from the real flag definitions, so they never drift. When
invoked through a wrapper (the core CLI's `shipyard comms whatsapp`), the
wrapper sets `SHIPYARD_PROG` and help shows that name.

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

shipyard-comms-whatsapp allow "Vini Eduqo"      # send allowlist (contacts only)
shipyard-comms-whatsapp send "Vini Eduqo" --text "oi" [--idempotency-key K] [--dry-run]
shipyard-comms-whatsapp pause | resume          # emergency stop for all sending
shipyard-comms-whatsapp route set "Vini Eduqo" /wa/vini   # deliver received messages to fairway
shipyard-comms-whatsapp tools install           # wa_send / wa_chats / wa_history for crew
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

## Sending

Only the running daemon sends. It owns the session, so `send`, the crew
tool and `shipyard comms send --channel <whatsapp>` are thin clients of its
control socket (`run/wa.sock`, 0600, JSON-RPC). The `chat` may be a JID, a
name or a phone number; the daemon resolves it. Every message passes the same checks, in this order, whoever
asks — a CLI user, a crew tool, or an agent with a shell:

| Check | Refusal code |
|---|---|
| idempotency key already used → return the first result, send nothing | — |
| empty / invalid UTF-8 / ambiguous chat name / not a user or group JID | `invalid` |
| longer than `max_chars` (default 4096) | `too_long` |
| chat not in the catalog | `unknown_chat` |
| `pause` in effect | `paused` |
| chat not on the allowlist (`allow`; groups are never allowed) | `not_allowed` |
| no conversation ever seen in that chat | `no_history` |
| more than `limits.per_chat` (6/10m) or `limits.global` (30/1h) | `rate_limited` |
| daemon not connected | `not_connected` |
| WhatsApp rejected the send | `send_failed` |

- Limits count rows in the `sent` audit table (kept 30 days), not memory, so
  a crash-looping daemon cannot reset them.
- With `humanize` (default on) the daemon shows "typing…" for
  1.5s + 40ms/char (max 8s) before sending.
- Config is re-read on every send: `pause`, `resume` and limit changes apply
  without a restart.
- "Conversation seen" is a per-chat timestamp updated by every message in
  either direction, watched or not (no content is kept for unwatched chats),
  and by the initial history sync.
- `send` exits 3 on a policy refusal and 1 on other errors. The crew tool
  returns `{ok:false, error, details:{code}}`.
- Pass an idempotency key from agents: a tool retried after a timeout then
  returns the original message instead of sending it twice.

## Receiving (inbound delivery)

`route set <chat|default> <path>` makes the daemon POST every new message of
that chat to fairway as a comms `Message` envelope (WhatsApp specifics in
`metadata`: `chat_jid`, `chat_name`, `chat_kind`, `from_me`, `kind`,
`reply_to`), with `X-Trace-Id`, so a crew agent can react — typically a
`crew.run` route created with `--async`. Delivery runs on a 256-event queue so
receiving never blocks; network errors, 5xx and 429 are retried 3 times
(2s/4s/8s), 4xx are not. Own messages are skipped unless
`config set include-own true`. The fairway address comes from fairway's
`routes.json` (default `127.0.0.1:9876`) or `config set fairway-addr`. Full
payload: `help receiving`.

## Crew tools

`tools install` writes `wa_send.yaml`, `wa_chats.yaml` and `wa_history.yaml`
into the crew tool library. They run `shipyard-comms-whatsapp tool <name>`,
which speaks the crew exec contract natively (JSON on stdin, envelope on
stdout) — no wrapper script and no message text in argv. Agents that send
should also set `builtin_tools: []` (crew ≥ 0.7.0) so their only way out is
the policy-checked tool.

## On disk

```
~/.shipyard/comms/whatsapp/        0700
  config.json                       device_name, default_ttl
  session.db                        linked-device keys (whatsmeow store)
  archive.db                        chat catalog + archived messages
  run/status.json                   written by the daemon every 30s
  run/wa.sock                       control socket (send, status)
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

Chat ownership (lease/handoff for flows) and media come later. See `docs/comms/whatsapp-design.md` (kept
locally).
