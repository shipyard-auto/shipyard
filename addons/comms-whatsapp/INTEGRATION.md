# Integrating a system with WhatsApp through Shipyard

This guide is for whoever builds a **client**: a separate system, such as a
promotions manager, that receives the messages Shipyard's WhatsApp daemon
captures, fetches their images, and optionally sends messages back. It
covers both sides:

- the operator, who sets up the machine;
- the client developer, who writes code against the contract.

## 1. How it fits together

```
 WhatsApp ─(persistent WebSocket)─▶ shipyard-comms-whatsapp daemon
                                      │  archives watched chats, downloads media
                                      │
              (1) each new message of ▼ the chats selected by 'publish'
                     POST 127.0.0.1:9876/_shipyard/v1/events   (loopback only)
                                      │
                                   fairway ── the only network-facing component
                                      │
              (2) webhook, signed with the client's own secret
                                      ▼
                              your client  ──(3) signed requests──▶ fairway
                                                 /api/whatsapp/media     fetch an image
                                                 /api/whatsapp/messages  catch up after downtime
                                                 /api/whatsapp/send      send (optional)
```

- **Push, not polling.** The daemon holds a WebSocket to WhatsApp and
  publishes each new message as an event. Fairway delivers it to your
  webhook within seconds. Your client never asks "anything new?".
- **Only selected chats are published.** The operator chooses them with
  `publish` (usually `watched`). Nothing else reaches fairway, so a chat the
  client posts into, such as its own broadcast group, can stay out of the
  event stream entirely.
- **One protocol for both directions.** Fairway signs what it sends you,
  and you sign what you send to fairway, with the same secret and the same
  scheme (§3).
- **Fairway stays domain-agnostic.** It knows clients, scopes and topics,
  not chats. The WhatsApp daemon decides which chats a client may see.
- **The pull API is for recovery only.** Webhooks are retried for about
  8 minutes, from fairway's in-memory queue. A client that was down longer
  reads what it missed through the cursor-paginated `/messages` endpoint,
  once, at startup.

## 2. Operator setup (once per machine)

Shipyard 1.3.32+ and comms-whatsapp 0.9+, with the daemon paired and
running, and the chats of interest watched (`shipyard comms whatsapp watch
<chat>`). Only watched chats have messages and media.

```bash
# Choose what is published to fairway's event bus. "watched" follows
# 'watch'; a chat name/JID or '*' (everything) also work.
shipyard comms whatsapp publish add watched

# The daemon's API (loopback), plus ready fairway route definitions
shipyard comms whatsapp api enable
shipyard service restart <whatsapp service id>      # the API starts with the daemon

# Register the routes the client uses (signed; fairway checks the scope name)
for r in messages chats media; do
  shipyard fairway route add --from-file ~/.shipyard/comms/whatsapp/api-routes/$r.json
done
# Only if the client sends:
for r in send upload limits; do
  shipyard fairway route add --from-file ~/.shipyard/comms/whatsapp/api-routes/$r.json
done

# Create the client. The secret is printed ONCE; hand it over securely.
shipyard fairway client add promo-manager \
  --scope events.subscribe:whatsapp.message \
  --scope 'whatsapp.read:120363408656714802@g.us' \
  --scope 'whatsapp.read:554197440172-1534527143@g.us'
#  add --scope 'whatsapp.send:<group jid>' per chat it may post to
```

**Scopes.** Each scope is `name:resource`, and `*` works as a glob. A scope
without a resource covers every chat.

| Scope | Grants |
|---|---|
| `events.subscribe:whatsapp.message` | registering a webhook for new messages |
| `whatsapp.read:<chat jid>` | receiving that chat's events; reading its messages and media |
| `whatsapp.send:<chat jid>` | sending to that chat (`/send`), uploading (`/upload`), `/limits` |

Find JIDs with `shipyard comms whatsapp chats --watched`. Use
`fairway client scopes <id> --scope …` to replace a client's scopes,
`client rotate` to issue a new secret, and `client disable` to block it.

**Sending needs the owner's consent as well as the scope.** The chat must be
on the allowlist (`shipyard comms whatsapp allow <chat>`). Groups also need
`config set allow-groups true`. Rate limits apply: `limits`, `limits set
per-group 30/h`. Every send is audited with the client id:
`shipyard comms whatsapp sent --client promo-manager`.

## 3. Signing (Shipyard v1)

Every request to fairway, and every webhook fairway sends you, carries:

```
X-Shipyard-Client:    <client id>
X-Shipyard-Timestamp: <unix seconds>
X-Shipyard-Nonce:     <random, never reused, e.g. 32 hex chars>
X-Shipyard-Signature: v1=<hex(HMAC-SHA256(secret, string_to_sign))>
```

`string_to_sign` is these seven lines joined by `\n`:

```
v1
<METHOD, upper case>
<path, URL-decoded, "/" if empty>     e.g. /api/whatsapp/messages
<raw query string, "" if none>
<the timestamp header>
<the nonce header>
<hex(SHA-256(exact body bytes))>       body "" → e3b0c442…b855
```

- The HMAC key is the **whole secret string** as bytes, `shps_` prefix
  included.
- Hash the body bytes you actually send. Don't re-serialize the JSON.
- The verifier rejects a timestamp more than **5 minutes** from its clock,
  a reused nonce, an unknown or disabled client, and a signature mismatch.
  All of these return `401` with the same message. Keep the clock in sync
  (NTP).
- Test vector (it must match before anything else will work):
  - secret `shps_secret`, `POST /api/wa/history?chat=x`
  - ts `1792267200`, nonce `abc123`, body `{"since":"1h"}`
  - expected signature `v1=2d813834e16ac500f0a133ec5270e201d086e40b0f96d7cf8e48db64f9a9b360`
  - Make it a unit test in your client. The CLI can also produce a valid
    request to compare against: `shipyard fairway client sign --client <id>
    --secret-file <f> --url <url> --body '<json>' --curl`.

```python
def sign(secret, method, path, query, ts, nonce, body: bytes) -> str:
    sts = "\n".join(["v1", method.upper(), path or "/", query, str(ts), nonce,
                     hashlib.sha256(body).hexdigest()])
    return "v1=" + hmac.new(secret.encode(), sts.encode(), hashlib.sha256).hexdigest()
```

```js
const sts = ["v1", method.toUpperCase(), path || "/", query, String(ts), nonce,
             crypto.createHash("sha256").update(body).digest("hex")].join("\n");
const sig = "v1=" + crypto.createHmac("sha256", secret).update(sts).digest("hex");
```

## 4. Receiving messages

### 4.1 Register the webhook (at every startup, idempotently)

Your client exposes an HTTP endpoint and tells fairway about it. The
endpoint is normally on loopback when it runs on the same machine, since
plain `http` is only accepted for loopback; anything else needs `https`.

```
GET  /_shipyard/v1/subscriptions                 → [{id, clientId, topic, url, createdAt, stats}]
POST /_shipyard/v1/subscriptions  {"topic":"whatsapp.message","url":"http://127.0.0.1:9100/hooks/whatsapp"}
                                                 → 201 {id, …}
DELETE /_shipyard/v1/subscriptions/{id}          → 204
```

All three requests are signed. Registering the same URL twice creates two
subscriptions, which means two deliveries per message. So **list first and
create only if missing**. The operator can also manage subscriptions with
`shipyard fairway subscription add|list|test|rm`. `test` sends a
`shipyard.ping` event.

### 4.2 The webhook

`POST <your url>`, signed with **your** secret. Besides the four signature
headers it carries `X-Shipyard-Event-Id`, `X-Shipyard-Event-Topic` and
`X-Shipyard-Delivery-Attempt`. Body:

```json
{
  "id": "evt_wa_3EB05D6D9931CFE721994A",
  "topic": "whatsapp.message",
  "scope": "whatsapp.read:120363408656714802@g.us",
  "source": "comms-whatsapp",
  "occurred_at": "2026-10-19T17:21:20Z",
  "data": {
    "schema_version": 1,
    "id": "3EB05D6D9931CFE721994A",
    "channel": "whatsapp",
    "direction": "inbound",
    "from": {"channel": "whatsapp", "raw": "5511…@s.whatsapp.net", "display": "Herói"},
    "to": [{"channel": "whatsapp", "raw": "<own number>@s.whatsapp.net"}],
    "body": "*A TESTOSTERONA QUE TE FALTA* …",
    "metadata": {
      "chat_jid": "120363408656714802@g.us",
      "chat_name": "Herói da Promo #1213",
      "chat_kind": "group",
      "from_me": "false",
      "kind": "image",
      "reply_to": "",
      "media_status": "stored",
      "media_mime_type": "image/jpeg",
      "media_size": "224670",
      "media_sha256": "…"
    },
    "ts": "2026-10-19T17:21:20Z"
  }
}
```

- `data` is the comms Message envelope, the same shape `/messages` returns.
- `metadata.kind` is one of text, image, audio, video, file, sticker,
  location, contact, poll or other. `body` holds the text or caption.
- Media messages add these `media_*` fields: `media_status`,
  `media_mime_type`, `media_size`, `media_seconds`, `media_voice_note`,
  `media_file_name`, `media_sha256` and `media_reason`.
- Fields with empty values are omitted.

**A correct receiver:**
1. Recomputes the signature with **your webhook's path** and the raw body,
   and checks the client id, the ±5 min window and that the nonce is unused.
   On any failure, answer `401`.
2. **Answers 2xx quickly**, then processes asynchronously. Fairway waits
   10 s. A timeout, a network error, `408`, `429` or `5xx` is retried after
   2 s, 10 s, 30 s, 2 min and 5 min, and then dropped. Other `4xx` answers
   are not retried.
3. **Dedupes by `data.id`.** Delivery is at-least-once, and the catch-up
   (§4.4) overlaps with webhooks by design.
4. Ignores topics it does not handle, such as `shipyard.ping`, but still
   answers 2xx.

A media message is published only after its download ends, so it can
arrive a few seconds after later text messages. Order by `data.ts` if
order matters.

### 4.3 Fetching an attachment

When `metadata.media_status` is `stored`:

```
POST /api/whatsapp/media  {"chat": "<metadata.chat_jid>", "id": "<data.id>", "offset": 0}
→ {"chat","id","mime_type","file_name","size","sha256","offset","length","eof","data":"<base64>"}
```

- Each response carries up to 2 MiB.
- Repeat with `offset = offset + length` until `eof` is true, then check
  `sha256` over the whole file.
- Other statuses:
  - `skipped`: the owner's download policy. `media_reason` says why, and
    `/media` answers `404 media_skipped`.
  - `failed`: `404 media_failed`.
  - `409 media_pending` is rare, because events wait for the download.
    Retry after a few seconds.
- Files expire with their message (the chat's TTL; `default-ttl` is 14
  days), so fetch what you need when the event arrives.

### 4.4 Catching up after downtime (once, at startup)

```
POST /api/whatsapp/messages  {"after": "<stored cursor>", "limit": 500}
     first run, no cursor:   {"since": "24h", "limit": 500}
→ {"messages": [<envelope>…], "next_cursor": "…", "has_more": true|false}
```

- Page until `has_more` is false. Store `next_cursor` whenever it is
  non-empty; it advances even when no message on the page is yours.
- Messages come in archive arrival order. A history sync can add old
  messages late.
- Optional filter: `"chat": "<jid|name>"`. Other optional fields: `since`
  as a duration (`7d`) or an RFC 3339 time, and `limit` from 1 to 500.
- **Register the webhook before catching up.** Then nothing falls between
  the two, and the overlap is removed by deduping.
- The cursor only moves when you call `/messages`. Don't call it in a loop:
  webhooks are the live path.

`POST /api/whatsapp/chats {"search"?, "kind"?, "watched_only"?}` lists the
chats you may read.

## 5. Sending (optional)

Requires `whatsapp.send:<chat>`, the chat on the owner's allowlist, and
`allow-groups` for groups (§2).

```
POST /api/whatsapp/send
{"chat": "<jid|name|phone>", "text": "Ração 30% off", "idempotency_key": "promo-881-grupoA",
 "dry_run": false, "media": {…}?}
→ {"msg_id", "kind", "chat", "chat_name", "sent_at", "duplicate"}
```

- **Always pass `idempotency_key`.** Retrying with the same key returns the
  first result (`duplicate: true`) and never sends twice. Keys are per
  client.
- **Media.** `text` becomes the caption. Use
  `"media": {"data": "<base64>"}` up to 2 MiB, or
  `"media": {"upload_id": "up_…"}` for larger files. Optional fields:
  `mime_type`, `file_name` (for documents) and `voice_note` (Ogg/Opus).
  - Images are JPEG or PNG and get a preview.
  - Video is MP4.
  - Audio carries no caption.
  - Anything else goes as a document.
  - The limit is the owner's `media-send-max-size`, 16 MB by default.
- **Large files** are uploaded in chunks first, because fairway caps one
  request at 4 MB:

  ```
  POST /api/whatsapp/upload {"offset":0, "data":"<base64 ≤2 MiB>"}            → {"upload_id","size","sha256","expires_at"}
  POST /api/whatsapp/upload {"upload_id":"up_…", "offset":<size>, "data":"…"} → … until done
  ```

  `409 offset_mismatch` tells you the size to resume from. An upload is
  yours alone and can be sent to several chats. It expires an hour after
  its last chunk, and at most 10 can be open at once.
- **Broadcast.** Send once per group, each with its own idempotency key.
  The daemon reuses the uploaded file for 30 minutes, so the bytes go to
  WhatsApp only once.
- **Never read what you post.** If the client reposts what it receives
  (for example into its own broadcast group), give it only
  `whatsapp.send:<target>` and never `whatsapp.read:<target>`. Don't use
  read globs that could match the target either: JIDs share prefixes, so
  `whatsapp.read:120363*` matches many groups. Keep the target out of
  `publish` and keep `include-own` off. In the client, also drop events
  whose `metadata.chat_jid` is the target or whose `metadata.from_me` is
  `"true"`. Any one of these stops a feedback loop; together they survive a
  misconfiguration.
- **Check before a batch:**
  `POST /api/whatsapp/limits {"chat": "<jid>"}` returns `can_send_now`,
  `blockers` and `retry_after_seconds`, plus each limit's usage.

Refusals carry a code. Branch on the code, never on the message:

| HTTP | `error.code` | Meaning / what to do |
|---|---|---|
| 400 | `invalid`, `too_long` | fix the request; do not retry |
| 403 | `forbidden` | the client lacks the scope for this chat |
| 403 | `not_allowed`, `groups_disabled` | the owner has not allowed this chat or groups; do not retry |
| 404 | `unknown_upload` | the upload expired or belongs to another client |
| 409 | `no_history` | WhatsApp requires a prior conversation in that chat |
| 413 | `too_long` | the file is over the limit; upload, or shrink it |
| 429 | `rate_limited` | wait for `Retry-After` (also `retry_after_seconds`), then retry with the same key |
| 502 | `send_failed` | WhatsApp rejected the send; retry later with the same key |
| 503 | `paused`, `not_connected` | the owner paused sending, or the daemon is offline; retry later |

## 6. Errors and troubleshooting

There are two error shapes.

- **Fairway** (authentication, route scope) answers `{"error": "<message>"}`:
  - `401` covers missing headers, a bad signature, clock skew, a replayed
    nonce and an unknown client.
  - `403 client lacks scope …` means the route's scope name is missing.
  - `404` means the route is not registered: the operator skipped a
    `route add`.
- **The WhatsApp API** answers `{"error": {"code", "message"}}`, as in the
  table above. It also uses `403 forbidden` for a chat that is not yours,
  and the same `403` for chats that don't exist.

| Symptom | Usual cause |
|---|---|
| Every request returns 401 | signature built over something else: re-encoded body, path with the host or query, missing `shps_` in the key, or the clock off by more than 5 min. Check your code against the test vector (§3). |
| Webhooks never arrive | no subscription (`GET /_shipyard/v1/subscriptions`), chat not selected by `publish list` (with `watched`: not watched), or no `whatsapp.read:<chat>` scope. `shipyard fairway subscription list` shows delivery stats and the last error. |
| Webhook arrives twice | two subscriptions for the same URL; list and delete one |
| Image event, but `/media` returns 404 | `media_status` is not `stored` (see `media_reason`), or the file expired with its message |
| `/send` returns 413 from fairway | body over 4 MB: use `/upload` |

## 7. Checklist for a production client

- [ ] Secret read from a file or secret store, never logged; supports rotation.
- [ ] Clock synced (NTP).
- [ ] Startup: register the webhook if missing, then catch up from the stored cursor.
- [ ] Webhook: verify the signature, timestamp window and nonce; answer 2xx fast; process asynchronously.
- [ ] Dedupe by message id; persist the cursor and recently seen ids.
- [ ] Fetch `stored` media promptly and check its sha256.
- [ ] Reposting: no read scope on the target chat, and own or target messages dropped in code (§5).
- [ ] Sending: an idempotency key per message and destination; honour `Retry-After`; check `/limits` before batches; never work around `not_allowed` or `groups_disabled`.
- [ ] Monitor `shipyard fairway subscription list` (failures) and `shipyard comms whatsapp status`.
