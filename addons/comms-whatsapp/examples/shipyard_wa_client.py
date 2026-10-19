#!/usr/bin/env python3
"""Reference client for shipyard-comms-whatsapp through fairway.

Standard library only (Python 3.9+). It shows everything a real client must
do; see ../INTEGRATION.md for the protocol it implements.

  serve                 receive whatsapp.message webhooks (registers the
                        subscription, catches up with the cursor, verifies
                        every webhook, dedupes, downloads attachments)
  messages [json]       one page of POST /api/whatsapp/messages
  chats                 POST /api/whatsapp/chats
  media <chat> <id> <out>
  send <chat> [--text T] [--file F] [--key K] [--dry-run]
  limits [chat]
  selftest              check the signing code against the known vector

Configuration (environment):
  SHIPYARD_CLIENT_ID          client id                       (required)
  SHIPYARD_CLIENT_SECRET_FILE file holding the shps_… secret  (required)
  FAIRWAY_URL                 default http://127.0.0.1:9876
  WA_API_PREFIX               default /api/whatsapp
  WEBHOOK_LISTEN              default 127.0.0.1:9100
  WEBHOOK_PATH                default /hooks/whatsapp
  STATE_DIR                   default ./wa-client-state (cursor, seen ids, media)
"""
import base64
import hashlib
import hmac
import http.server
import json
import os
import secrets
import sys
import threading
import time
import urllib.error
import urllib.request
from urllib.parse import unquote, urlsplit

FAIRWAY = os.environ.get("FAIRWAY_URL", "http://127.0.0.1:9876").rstrip("/")
PREFIX = os.environ.get("WA_API_PREFIX", "/api/whatsapp").rstrip("/")
LISTEN = os.environ.get("WEBHOOK_LISTEN", "127.0.0.1:9100")
HOOK_PATH = os.environ.get("WEBHOOK_PATH", "/hooks/whatsapp")
STATE = os.environ.get("STATE_DIR", "./wa-client-state")
MAX_SKEW = 300          # seconds, same window fairway enforces
CHUNK = 2 * 1024 * 1024  # largest chunk the API takes or returns


def client_id():
    return os.environ["SHIPYARD_CLIENT_ID"]


def secret():
    with open(os.environ["SHIPYARD_CLIENT_SECRET_FILE"]) as f:
        return f.read().strip()


# ── signing (Shipyard v1) ─────────────────────────────────────────────────

def string_to_sign(method, path, raw_query, ts, nonce, body):
    return "\n".join(["v1", method.upper(), path or "/", raw_query, str(ts), nonce,
                      hashlib.sha256(body).hexdigest()])


def signature(key, method, path, raw_query, ts, nonce, body):
    mac = hmac.new(key.encode(), string_to_sign(method, path, raw_query, ts, nonce, body).encode(), hashlib.sha256)
    return "v1=" + mac.hexdigest()


def sign_headers(method, url, body):
    u = urlsplit(url)
    ts, nonce = int(time.time()), secrets.token_hex(16)
    return {
        "X-Shipyard-Client": client_id(),
        "X-Shipyard-Timestamp": str(ts),
        "X-Shipyard-Nonce": nonce,
        "X-Shipyard-Signature": signature(secret(), method, unquote(u.path), u.query, ts, nonce, body),
    }


class APIError(Exception):
    def __init__(self, status, body):
        self.status, self.body = status, body
        # fairway answers {"error": "…"}; the WhatsApp API {"error": {"code", "message"}}
        err = body.get("error") if isinstance(body, dict) else body
        self.code = err.get("code") if isinstance(err, dict) else None
        super().__init__(f"HTTP {status}: {err}")


def call(method, path, payload=None, timeout=90):
    """One signed request to fairway. Returns the decoded JSON body."""
    url = FAIRWAY + path
    body = b"" if payload is None else json.dumps(payload).encode()
    headers = sign_headers(method, url, body)
    if payload is not None:
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=body if payload is not None else None, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            raw = r.read()
            return json.loads(raw) if raw else None
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            parsed = json.loads(raw)
        except ValueError:
            parsed = raw.decode(errors="replace")
        err = APIError(e.code, parsed)
        err.retry_after = e.headers.get("Retry-After")
        raise err from None


# ── local state: cursor and seen ids ──────────────────────────────────────

class State:
    def __init__(self):
        os.makedirs(os.path.join(STATE, "media"), mode=0o700, exist_ok=True)
        self.lock = threading.Lock()
        self.path = os.path.join(STATE, "state.json")
        try:
            with open(self.path) as f:
                d = json.load(f)
        except (OSError, ValueError):
            d = {}
        self.cursor = d.get("cursor", "")
        self.seen = d.get("seen", [])  # recent message ids, newest last

    def save(self):
        tmp = self.path + ".tmp"
        with open(tmp, "w") as f:
            json.dump({"cursor": self.cursor, "seen": self.seen[-5000:]}, f)
        os.replace(tmp, self.path)

    def first_time(self, msg_id):
        """True the first time msg_id is seen (webhook retries and the
        catch-up both redeliver)."""
        with self.lock:
            if msg_id in self.seen:
                return False
            self.seen.append(msg_id)
            self.save()
            return True


# ── what to do with a message: replace with your logic ────────────────────

def handle_message(st, env):
    md = env.get("metadata") or {}
    print(f"[{md.get('chat_name')}] {md.get('kind')}: {(env.get('body') or '')[:80]!r}", flush=True)
    if md.get("media_status") == "stored":
        out = os.path.join(STATE, "media", env["id"])
        try:
            download_media(md["chat_jid"], env["id"], out)
        except APIError as e:
            if e.code == "media_pending":  # rare: retry a bit later
                time.sleep(3)
                download_media(md["chat_jid"], env["id"], out)
            else:
                print(f"  media not fetched: {e}", flush=True)


def process(st, env):
    if st.first_time(env["id"]):
        handle_message(st, env)


# ── API calls ─────────────────────────────────────────────────────────────

def download_media(chat, msg_id, out):
    h, offset, info = hashlib.sha256(), 0, None
    with open(out + ".part", "wb") as f:
        while True:
            info = call("POST", PREFIX + "/media", {"chat": chat, "id": msg_id, "offset": offset})
            chunk = base64.b64decode(info["data"])
            f.write(chunk)
            h.update(chunk)
            offset += info["length"]
            if info["eof"]:
                break
    if h.hexdigest() != info["sha256"]:
        os.remove(out + ".part")
        raise RuntimeError(f"sha256 mismatch for {msg_id}")
    os.replace(out + ".part", out)
    print(f"  saved {out} ({offset} bytes, {info.get('mime_type')})", flush=True)


def catch_up(st):
    """Read everything archived since the stored cursor (first run: 24h)."""
    req = {"after": st.cursor, "limit": 500} if st.cursor else {"since": "24h", "limit": 500}
    while True:
        page = call("POST", PREFIX + "/messages", req)
        for env in page["messages"]:
            process(st, env)
        if page["next_cursor"]:
            st.cursor = page["next_cursor"]
            st.save()
        if not page["has_more"]:
            return
        req = {"after": st.cursor, "limit": 500}


def ensure_subscription(url):
    subs = call("GET", "/_shipyard/v1/subscriptions")
    for s in subs:
        if s["topic"] == "whatsapp.message" and s["url"] == url:
            return s["id"]
    return call("POST", "/_shipyard/v1/subscriptions", {"topic": "whatsapp.message", "url": url})["id"]


def send(chat, text="", file=None, key=None, dry_run=False):
    req = {"chat": chat, "text": text, "dry_run": dry_run}
    if key:
        req["idempotency_key"] = key
    if file:
        with open(file, "rb") as f:
            data = f.read()
        media = {"file_name": os.path.basename(file)}
        if len(data) <= CHUNK:
            media["data"] = base64.b64encode(data).decode()
        else:
            media["upload_id"] = upload(data)
        req["media"] = media
    while True:
        try:
            return call("POST", PREFIX + "/send", req)
        except APIError as e:
            if e.code == "rate_limited" and getattr(e, "retry_after", None):
                wait = int(e.retry_after)
                print(f"rate limited; retrying in {wait}s (same idempotency key, never duplicates)", flush=True)
                time.sleep(wait)
                continue
            raise


def upload(data):
    upload_id, offset = "", 0
    while offset < len(data):
        req = {"offset": offset, "data": base64.b64encode(data[offset:offset + CHUNK]).decode()}
        if upload_id:
            req["upload_id"] = upload_id
        info = call("POST", PREFIX + "/upload", req)
        upload_id, offset = info["upload_id"], info["size"]
    if info["sha256"] != hashlib.sha256(data).hexdigest():
        raise RuntimeError("upload sha256 mismatch")
    return upload_id


# ── webhook receiver ──────────────────────────────────────────────────────

class Nonces:
    """Nonces seen within the skew window: a captured webhook cannot be replayed."""

    def __init__(self):
        self.lock, self.items = threading.Lock(), {}

    def first_use(self, nonce, now):
        with self.lock:
            for k in [k for k, t in self.items.items() if now - t > 2 * MAX_SKEW]:
                del self.items[k]
            if nonce in self.items:
                return False
            self.items[nonce] = now
            return True


def make_handler(st, nonces, key):
    class Hook(http.server.BaseHTTPRequestHandler):
        def do_POST(self):
            u = urlsplit(self.path)
            if u.path != HOOK_PATH:
                return self.reply(404)
            body = self.rfile.read(int(self.headers.get("Content-Length") or 0))
            h, now = self.headers, time.time()
            try:
                ts, nonce = int(h["X-Shipyard-Timestamp"]), h["X-Shipyard-Nonce"]
                want = signature(key, "POST", unquote(u.path), u.query, ts, nonce, body)
                ok = (h["X-Shipyard-Client"] == client_id() and abs(now - ts) <= MAX_SKEW
                      and hmac.compare_digest(want, h["X-Shipyard-Signature"]) and nonces.first_use(nonce, now))
            except (TypeError, ValueError, KeyError):
                ok = False
            if not ok:
                return self.reply(401)
            ev = json.loads(body)
            # Answer fast: fairway waits 10s and retries non-2xx answers.
            self.reply(204)
            if ev.get("topic") == "whatsapp.message":
                threading.Thread(target=process, args=(st, ev["data"]), daemon=True).start()

        def reply(self, code):
            self.send_response(code)
            self.send_header("Content-Length", "0")
            self.end_headers()

        def log_message(self, *a):
            pass

    return Hook


def serve():
    st, key = State(), secret()
    host, port = LISTEN.rsplit(":", 1)
    srv = http.server.ThreadingHTTPServer((host, int(port)), make_handler(st, Nonces(), key))
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    url = f"http://{LISTEN}{HOOK_PATH}"
    print(f"webhook listening on {url}; subscription {ensure_subscription(url)}", flush=True)
    catch_up(st)  # after subscribing, so nothing falls between the two
    print("caught up; waiting for webhooks", flush=True)
    try:
        threading.Event().wait()
    except KeyboardInterrupt:
        srv.shutdown()


def selftest():
    got = signature("shps_secret", "post", "/api/wa/history", "chat=x", 1792267200, "abc123", b'{"since":"1h"}')
    want = "v1=2d813834e16ac500f0a133ec5270e201d086e40b0f96d7cf8e48db64f9a9b360"
    print("signing ok" if got == want else f"signing BROKEN: {got}")
    return got == want


def main(argv):
    if not argv:
        print(__doc__)
        return 2
    cmd, args = argv[0], argv[1:]
    try:
        if cmd == "serve":
            serve()
        elif cmd == "messages":
            print(json.dumps(call("POST", PREFIX + "/messages", json.loads(args[0]) if args else {}), ensure_ascii=False, indent=1))
        elif cmd == "chats":
            print(json.dumps(call("POST", PREFIX + "/chats", {}), ensure_ascii=False, indent=1))
        elif cmd == "media":
            download_media(*args)
        elif cmd == "send":
            opts = {"text": "", "file": None, "key": None, "dry_run": False}
            chat, rest = args[0], args[1:]
            while rest:
                flag = rest.pop(0)
                if flag == "--dry-run":
                    opts["dry_run"] = True
                else:
                    opts[{"--text": "text", "--file": "file", "--key": "key"}[flag]] = rest.pop(0)
            print(json.dumps(send(chat, **opts), ensure_ascii=False))
        elif cmd == "limits":
            print(json.dumps(call("POST", PREFIX + "/limits", {"chat": args[0]} if args else {}), ensure_ascii=False, indent=1))
        elif cmd == "selftest":
            return 0 if selftest() else 1
        else:
            print(__doc__)
            return 2
    except APIError as e:
        print(e, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
