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

shipyard-comms-whatsapp allow "Vini Eduqo"      # send allowlist (groups also need allow-groups)
shipyard-comms-whatsapp limits ["Vini Eduqo"]   # limits, live usage, can I send now?
shipyard-comms-whatsapp limits set per-group 30/h
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
- **Attachments of watched chats** (image, audio/voice note, video, document;
  stickers opt-in) are downloaded in the background into `media/` next to
  `archive.db` (0700/0600) and deleted with their message: TTL, `unwatch` and
  "delete for everyone" all remove the file. `config set media-download
  image,audio` picks the kinds (`off` disables), `config set media-max-size
  50MB` the cap (default 25MB, max 100MB). Every media message records a
  status — `stored`, `pending`, `skipped` (policy) or `failed` — shown by
  `history` and carried in envelopes as `metadata.media_*`. Route deliveries
  of a media message wait for its download and add `media_path`, the local
  file. Receiving only: sending media is not supported. Full detail:
  `help media`.
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
| chat is a group and `allow-groups` is off | `groups_disabled` |
| chat not on the allowlist (`allow`) | `not_allowed` |
| no conversation ever seen in that chat | `no_history` |
| over the chat's limit (`per_chat` 6/10m for contacts, `per_group` 20/1h for groups) or `global` (60/1h) | `rate_limited` (+ `retry_after_seconds`) |
| daemon not connected | `not_connected` |
| WhatsApp rejected the send | `send_failed` |

- `sent [--since 24h] [--client ID|local]` lists that audit log: when, which
  chat, how long, and who asked (a fairway client or `local`).
- Limits count rows in the `sent` audit table (kept 30 days), not memory, so
  a crash-looping daemon cannot reset them. `limits` shows them in plain words
  with usage, remaining slots and when the next opens; `limits <chat>` says
  whether a send there would be accepted and why not; `limits set|reset`
  changes them (`20/h`, `6/10m`, `100/day`). Agents read the same through the
  `wa_limits` tool or the socket's `limits` method.
- Sending to groups is off by default (`config set allow-groups true`); use it
  for groups you run, such as your own promotions group.
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

For external systems, `config set publish-events true` also publishes every
new message to fairway's events API as a `whatsapp.message` event with
`scope: whatsapp.read:<chat_jid>`. Fairway delivers it as a signed webhook
only to subscribed clients holding that scope — e.g. a client granted
`whatsapp.read:<group jid>` receives that group and nothing else. Event ids
are stable per message (`evt_wa_<msg id>`) for deduplication.

## API for external systems

`api enable` lets another system read the archive and send messages through
fairway, limited to the chats it was granted. Reads are cursor-paginated —
the pull-side complement to `publish-events`: events deliver live, the
cursor recovers whatever a consumer missed while it was down.

```
client ──signed──▶ fairway route (auth signed, scope whatsapp.read)
       ──http.forward + API token──▶ daemon API (loopback only)
```

- `api enable [--addr 127.0.0.1:9877] [--prefix /api/whatsapp]` sets
  `api_addr`, creates `api.token` (0600) and writes ready fairway route
  definitions under `api-routes/`, then prints the
  `shipyard fairway route add --from-file …` commands. Restart the daemon
  after the first enable.
- Fairway authenticates the client and forwards its identity; the daemon
  enforces the chat: `whatsapp.read:<chat jid>` for one chat, a glob like
  `whatsapp.read:120363*@g.us`, or `whatsapp.read` for all. Chats outside
  the client's scopes answer 403 whether or not they exist.
- `POST <prefix>/messages` `{"chat", "since", "after", "limit"}` returns
  `{"messages": [<comms Message envelope>…], "next_cursor", "has_more"}` in
  archive arrival order — the same envelope routes and events carry. Store
  `next_cursor` and send it back as `after`.
- `POST <prefix>/chats` `{"search", "kind", "watched_only"}` lists the chats
  the client may read.
- `POST <prefix>/send` `{"chat", "text", "idempotency_key", "dry_run"}`
  needs `whatsapp.send:<chat jid>` and then passes every check of the send
  policy — allowlist, `allow-groups`, limits, pause, humanize — so the scope
  is the client's permission and the owner keeps the last word. Refusals
  carry the refusal code (429 `rate_limited` adds `Retry-After`). Keys are
  per client, and every send is audited with the client id: `sent`.
- `POST <prefix>/limits` `{"chat"}` returns the `limits` report (usage,
  `can_send_now`, blockers, `retry_after_seconds`) to check before a
  broadcast.
- `POST <prefix>/media` `{"chat", "id", "offset"}` (`whatsapp.read:<chat>`)
  returns the attachment in base64 chunks of up to 2 MiB (fairway caps
  forwarded responses at 4 MB): call again with `offset + length` until
  `eof`, then check `sha256`.
- `api rotate-token` replaces the token and rewrites the route files;
  `api disable` turns it off. Full contract: `help api`.

Only watched chats have messages; the archive's sequence numbers are never
reused, so a stored cursor stays valid across pruning and restarts.

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

Chat ownership (lease/handoff for flows) and sending media come later. See `docs/comms/whatsapp-design.md` (kept
locally).
