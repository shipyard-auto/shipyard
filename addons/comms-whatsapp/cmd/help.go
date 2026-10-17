package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/app"
	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa"
)

// The help system is written for two readers: a person at a terminal and,
// just as much, a coding agent that runs `help` to learn how to drive this
// binary without other context. So help is complete rather than terse:
// every command documents what it does, what it changes, its flags (taken
// from the real FlagSet, so they never drift) and examples; topics cover the
// cross-cutting rules.

// ProgEnv lets a wrapper announce how users invoke this binary. The core
// CLI sets it to "shipyard comms whatsapp" when passing a command through,
// so help never tells anyone to run a command that does not exist.
const ProgEnv = "SHIPYARD_PROG"

func progName() string {
	if p := strings.TrimSpace(os.Getenv(ProgEnv)); p != "" {
		return p
	}
	return filepath.Base(os.Args[0])
}

type cmdDoc struct {
	name     string
	group    string
	usage    string // after the program name
	summary  string // one line, for the overview
	details  string // full description: behaviour, side effects, caveats
	examples []string
}

var groups = []string{"Setup", "Daemon", "Archive", "Receiving", "Sending", "Agents"}

var cmdDocs = []cmdDoc{
	{
		name: "pair", group: "Setup",
		usage:   "pair [--phone +5511999999999] [--force]",
		summary: "link this machine to a WhatsApp account as a companion device",
		details: `Links this machine under "Linked devices" on the phone, like WhatsApp Web.
Without --phone a QR code is printed in the terminal; scan it from WhatsApp →
Settings → Linked devices → Link a device. With --phone an 8-character code is
printed instead, to type on the phone under "Link with phone number instead"
(useful on a headless server).

After linking it stays connected ~45s to catalogue chat names (contacts,
groups, initial history sync) and then exits. Nothing is archived yet: watch
chats afterwards. Must not run while the daemon runs (both need the session
lock). Fails if a session exists unless --force, which discards the local keys;
the old device must then be removed on the phone by hand.

Pairing by passkey is not supported; if WhatsApp asks for it, pair by QR.`,
		examples: []string{"pair", "pair --phone +55 11 99999-9999"},
	},
	{
		name: "logout", group: "Setup",
		usage:   "logout [--local]",
		summary: "unlink the device and delete the local session keys",
		details: `Tells WhatsApp to remove this linked device, then deletes session.db. The
archive (archive.db) is kept and still expires by TTL. --local skips telling
WhatsApp (for offline machines): only the local keys go, and the device must
be removed on the phone. Must not run while the daemon runs.`,
		examples: []string{"logout", "logout --local"},
	},
	{
		name: "config", group: "Setup",
		usage:   "config [show] | config set <key> <value>",
		summary: "show or change settings (TTL, send limits, typing delay)",
		details: `Shows config.json as JSON, or changes one key. The daemon re-reads the file on
every send, so changes apply without a restart.

Keys:
  device-name     name shown under "Linked devices" (next pairing only)
  default-ttl     retention for watched chats without their own TTL (1h-90d)
  limit-per-chat  max sends to each contact, "<count>/<window>" (default 6/10m)
  limit-per-group max sends to each group (default 20/1h)
  limit-global    max sends across all chats (default 60/1h)
  allow-groups    true/false: permit sending to allowlisted groups (default false)
  max-chars       longest accepted message (1-4096)
  humanize        true/false: show "typing…" before each send
  fairway-addr    host:port for inbound deliveries (default: from fairway)
  include-own     true/false: also deliver messages the account itself sent
  publish-events  true/false: publish every new message to fairway's events
                  API for signed webhook subscribers (default false)

Pausing is not a key: use pause / resume.`,
		examples: []string{"config", "config set default-ttl 7d", "config set allow-groups true"},
	},
	{
		name: "serve", group: "Daemon",
		usage:   "serve",
		summary: "run the daemon (normally under shipyard service, not by hand)",
		details: `Runs the long-lived daemon: holds the WhatsApp connection (a single WebSocket
with keepalive pings; no polling), archives messages of watched chats, records
per-chat activity, prunes expired messages hourly, writes run/status.json every
30s and serves the control socket run/wa.sock — the only way to send.

Exit codes: 0 clean stop, 10 already running, 20 not paired, 30 runtime error
(including another client taking over the session), 40 logged out by the phone
(pair again). Run it under the service manager with auto-restart.`,
		examples: []string{
			"serve",
			`shipyard service add --name whatsapp --auto-restart --command "$(command -v shipyard-comms-whatsapp) serve"`,
		},
	},
	{
		name: "status", group: "Daemon",
		usage:   "status [--json]",
		summary: "pairing, daemon state, archive and sending summary",
		details: `Reads status.json, session.db and archive.db; never talks to the daemon and
never creates files. Reports: paired account, whether the daemon is running and
connected (a stale heartbeat counts as not running), archive counts, default
TTL, allowlisted chats, sends in the last hour and whether sending is paused.
A daemon state of temporary_ban means WhatsApp suspended the account: stop all
automation.`,
		examples: []string{"status", "status --json"},
	},
	{
		name: "chats", group: "Archive",
		usage:   "chats [--search q] [--watched] [--json]",
		summary: "list known chats (name, kind, watched, TTL, allowlist, JID)",
		details: `Lists the chat catalog: saved contacts with WhatsApp, joined groups and the
self chat, refreshed on every daemon connect. --search matches name or JID,
ignoring case and accents ("familia" finds "Família"). Names come from the
phone's address book first, then the profile name, then the number.`,
		examples: []string{"chats --search vini", "chats --watched", "chats --json"},
	},
	{
		name: "watch", group: "Archive",
		usage:   "watch <chat> [--ttl 7d]",
		summary: "start archiving a chat's messages (from now on)",
		details: `Turns archiving on for a chat. Only messages received after this are stored;
the initial history sync is never archived. --ttl sets this chat's retention
(1h-90d); without it the config default applies. <chat> is a JID, a name
(case/accent-insensitive; ambiguous names list the candidates) or a phone
number, which adds the chat to the catalog if missing.`,
		examples: []string{`watch "Família" --ttl 7d`, "watch 120363408656714802@g.us"},
	},
	{
		name: "unwatch", group: "Archive",
		usage:   "unwatch <chat>",
		summary: "stop archiving a chat and delete its archived messages",
		details: `Turns archiving off and deletes every archived message of the chat. The chat
stays in the catalog.`,
		examples: []string{`unwatch "Família"`},
	},
	{
		name: "history", group: "Archive",
		usage:   "history <chat> [--since 24h] [--limit 200] [--json]",
		summary: "print archived messages of a watched chat, oldest first",
		details: `Prints messages of a watched chat within --since (e.g. 2h, 3d), keeping the
newest --limit (0 = no limit). Media appears as [image] / [audio] / … plus the
caption; files are not downloaded. Messages sent by Shipyard carry
via_shipyard=true in JSON.`,
		examples: []string{`history "Família" --since 2h`, "history 120363@g.us --limit 50 --json"},
	},
	{
		name: "route", group: "Receiving",
		usage:   "route list [--json] | route set <chat|default> <path> | route rm <chat|default>",
		summary: "deliver received messages of a chat to a fairway route",
		details: `Makes the daemon POST each new message received in a chat to fairway, so an
agent can react (reply, summarize, alert). 'default' catches every chat without
its own route. The payload is a comms Message envelope (see 'help receiving');
fairway passes it to the route's action, typically crew.run.

Create the fairway route as async so the daemon gets 202 at once:
  shipyard fairway route add --path /wa/vini --auth local-only     --action crew.run --target <agent> --async

Routing is independent of watch: a routed chat is delivered whether or not it
is archived. Messages the account sends itself are skipped unless
'config set include-own true'. Changes apply live, without a restart.`,
		examples: []string{`route set "Vini Eduqo" /wa/vini`, "route set default /wa/inbox", "route list", `route rm "Vini Eduqo"`},
	},
	{
		name: "send", group: "Sending",
		usage:   "send <chat> (--text T | --stdin) [--idempotency-key K] [--dry-run] [--json]",
		summary: "send a text message through the daemon's policy",
		details: `Asks the running daemon to send a text message. Every check of 'help sending'
applies; refusals exit with code 3 and print the refusal code. --dry-run runs
all checks without sending. --idempotency-key makes retries safe: a repeated
key returns the first result (duplicate=true) instead of sending again —
always pass one from automation. Shows "typing…" first when humanize is on.

The daemon must be running; with it stopped, send fails (exit 1).`,
		examples: []string{
			`send "Vini Eduqo" --text "Fala Vini!" --dry-run`,
			`send "Vini Eduqo" --text "Fala Vini!" --idempotency-key alert-2026-10-17-1`,
			`echo "texto longo" | {prog} send 5512982016477 --stdin --json`,
		},
	},
	{
		name: "allow", group: "Sending",
		usage:   "allow <chat>",
		summary: "add a chat to the send allowlist",
		details: `Lets Shipyard send to this chat. Groups additionally need
'config set allow-groups true' first — use it for groups you run (e.g. your
own promotions group); posting into other people's groups is the pattern
WhatsApp bans most. Allowing is not enough on its own: the chat also needs a
prior conversation, and sends count against the limits (see 'help sending',
'limits').`,
		examples: []string{`allow "Vini Eduqo"`, `config set allow-groups true && allow "Minhas Promos"`},
	},
	{
		name: "deny", group: "Sending",
		usage:    "deny <chat>",
		summary:  "remove a chat from the send allowlist",
		details:  `Removes the chat from the allowlist; later sends are refused with not_allowed.`,
		examples: []string{`deny "Vini Eduqo"`},
	},
	{
		name: "limits", group: "Sending",
		usage:   "limits [chat] [--json] | limits set <per-chat|per-group|global> <count>/<window> | limits reset",
		summary: "show send limits with live usage; change or reset them",
		details: `Shows, in plain words, how many messages may be sent per contact, per group
and overall, how many were sent in the current window, how many are left and
when the next slot opens. With a chat, also answers "can I send there right
now?" and lists what blocks it (paused, groups_disabled, not_allowed,
no_history, rate_limited) with the fix for each.

Limits are sliding windows over the send log (they survive restarts):

  per-chat   each contact          default 6 per 10 minutes
  per-group  each group            default 20 per hour
  global     all chats together    default 60 per hour

'limits set' takes <count>/<window>; windows accept 10m, 1h, 1d or a bare
unit (20/h, 60/hour, 100/day), between 1 minute and 1 day. Changes apply to
the next send without a restart. 'limits reset' restores the defaults.
Agents can read the same data with the wa_limits tool.`,
		examples: []string{"limits", `limits "Vini Eduqo"`, "limits set per-group 30/h", "limits reset", "limits --json"},
	},
	{
		name: "pause", group: "Sending",
		usage:   "pause",
		summary: "emergency stop: refuse every send until resume",
		details: `Sets paused=true in config.json. Takes effect on the next send, without a
restart. Archiving continues.`,
		examples: []string{"pause"},
	},
	{
		name: "resume", group: "Sending",
		usage:    "resume",
		summary:  "lift a pause",
		details:  `Sets paused=false in config.json.`,
		examples: []string{"resume"},
	},
	{
		name: "tools", group: "Agents",
		usage:   "tools install [--dir D] [--force]",
		summary: "write wa_send / wa_chats / wa_history definitions for crew",
		details: `Writes wa_send.yaml, wa_chats.yaml and wa_history.yaml into the crew tool
library ($SHIPYARD_HOME/crew/tools by default). Each runs '<this binary> tool
<name>'. Refuses to overwrite without --force. See 'help agent-tools'.`,
		examples: []string{"tools install", "tools install --force"},
	},
	{
		name: "tool", group: "Agents",
		usage:   "tool <send|chats|history|limits>",
		summary: "run one crew exec tool (JSON on stdin, envelope on stdout)",
		details: `Implements the crew exec tool contract directly: reads one JSON object from
stdin and prints {"ok":true,"data":…} or {"ok":false,"error":…,"details":…}.
Always exits 0 so the envelope reaches the agent. See 'help agent-tools' for inputs
and outputs.`,
		examples: []string{`echo '{"search":"vini"}' | tool chats`},
	},
}

type topicDoc struct{ name, summary, body string }

var topics = []topicDoc{
	{"sending", "the send policy, refusal codes and how to send safely", `Only the running daemon sends. send, the wa_send tool and 'shipyard comms send
--channel <whatsapp>' are thin clients of its control socket (run/wa.sock);
one place applies every check, whoever asks — a person, a crew tool, or an
agent with a shell. The chat may be a JID, a name or a phone number; the
daemon resolves it against the catalog. Checks, in order (the first failure
wins):

  1. idempotency key already used → return that send, send nothing    —
  2. empty text, invalid UTF-8, ambiguous name, not a user/group JID   invalid
  3. longer than max_chars                                             too_long
  4. chat not in the catalog                                           unknown_chat
  5. sending paused                                                    paused
  6. chat is a group and allow-groups is off                           groups_disabled
     chat not on the allowlist                                         not_allowed
  7. no conversation ever seen in the chat                             no_history
  8. over the chat's limit (per-chat / per-group) or the global one    rate_limited
  9. daemon not connected to WhatsApp                                  not_connected
  10. WhatsApp rejected the message                                    send_failed

"Conversation seen" (no_history) means at least one message in either
direction was observed in that chat — watched or not — since the daemon began
tracking, or reported by the initial history sync. To unblock a new chat, a
person sends or receives one message there from the phone. This is deliberate:
a fresh number opening conversations automatically looks like spam.

Rate limits count the persistent send log, so restarts do not reset them.
'limits' (or the wa_limits tool) shows them with live usage. rate_limited
refusals carry retry_after_seconds (error.data, details, or the CLI message).
A refusal is final for that request: do not retry not_allowed,
groups_disabled, no_history or paused; retry rate_limited only after
retry_after_seconds. Never work around a
refusal (another chat, another number, splitting the text).

Safe sending from automation:
  - pass an idempotency key derived from the work item (e.g. alert id), so a
    retry after a timeout never sends twice;
  - dry-run first when the text was generated, and have a person approve;
  - one message, short, no links or media from a new number;
  - check 'status' for temporary_ban and stop everything if present.`},
	{"receiving", "how received messages reach agents (routes, payload, retries)", `The daemon receives every message of the account. For each new message it:
  1. archives it if the chat is watched (see 'help files');
  2. records the chat's last activity (unblocks sending, see 'help sending');
  3. if the chat has a route ('route set'), POSTs an event to fairway at
     http://<fairway_addr><path>, with header X-Trace-Id;
  4. if 'config set publish-events true', publishes it to fairway's events
     API (see "Events for external systems" below).

Event (comms Message envelope, schema v1):
  {"schema_version":1, "id":"<msg id>", "trace_id":"…", "channel":"whatsapp",
   "direction":"inbound",
   "from":{"channel":"whatsapp","raw":"5512…@s.whatsapp.net","display":"Vini Eduqo"},
   "to":[{"channel":"whatsapp","raw":"<own number>@s.whatsapp.net"}],
   "body":"<text or caption>",
   "metadata":{"chat_jid":"…","chat_name":"…","chat_kind":"dm|group|self",
               "from_me":"false","kind":"text|image|audio|…","reply_to":"…"},
   "ts":"2026-10-17T17:13:36Z"}

To answer, an agent sends to metadata.chat_jid with wa_send (the chat must be
allowlisted). Use metadata.chat_jid as the crew conversation key for
stateful agents: key: "{{input.metadata.chat_jid}}".

Delivery runs on a background queue (256 events) so receiving never waits.
Network errors, 5xx and 429 are retried 3 times (2s, 4s, 8s); a 4xx is a
configuration error and is not retried. Undeliverable events are logged
(wa_delivery_failed) and dropped; watched chats keep them in the archive.
Edits and deletions are not delivered. fairway_addr defaults to the bind/port
in fairway's routes.json (127.0.0.1:9876); override with
'config set fairway-addr host:port'.

Events for external systems:
With publish-events on, every new message of every chat (own messages only
with include-own) is POSTed to http://<fairway_addr>/_shipyard/v1/events as
  {"id":"evt_wa_<msg id>", "topic":"whatsapp.message",
   "scope":"whatsapp.read:<chat_jid>", "source":"comms-whatsapp",
   "occurred_at":"…", "data":<the envelope above>}
Fairway forwards it, signed, to each subscription for the topic whose client
also holds the scope, so a system subscribed with 'whatsapp.read:<group jid>'
gets that group only and 'whatsapp.read:*' gets everything. Grant and
subscribe on the fairway side:
  shipyard fairway client scopes <client> \
      --scope events.subscribe:whatsapp.message --scope 'whatsapp.read:<chat_jid>'
  shipyard fairway subscription add <client> --topic whatsapp.message \
      --url https://…
The event id is stable per message, so subscribers can deduplicate retries.
Publishing uses the same queue and retries as routes.`},
	{"agent-tools", "crew tool contract for wa_send, wa_chats and wa_history", `'tools install' writes crew tool definitions that run '<binary> tool <name>'.
Input: one JSON object on stdin. Output: an envelope on stdout, exit 0.

wa_send     in:  chat (name, phone or JID), text, idempotency_key?, dry_run?
            out: msg_id, chat, chat_name, sent_at, duplicate, dry_run
            refusal: {"ok":false,"error":"…","details":{"code":"no_history"}}
wa_chats    in:  search?, watched?
            out: chats[{jid,name,kind,watched,send_allowed}] (≤50), total, truncated
wa_limits   in:  chat?
            out: per_chat, per_group, global {limit, human, used, remaining,
                 next_free_at, retry_after_seconds}, allow_groups, paused,
                 chat {can_send_now, blockers[], usage, retry_after_seconds}
wa_history  in:  chat, since? (default 24h), limit? (default 200, max 500)
            out: chat{jid,name}, messages[{id,sender_jid,sender_name,from_me,
                 ts,kind,text,reply_to,via_shipyard}]  (watched chats only)

Agents that send should also set 'builtin_tools: []' in agent.yaml (crew ≥
0.7.0) so the policy-checked tool is their only way out. The daemon enforces
the policy either way.`},
	{"files", "what is stored where, and what is never stored", `All state lives in $SHIPYARD_HOME/comms/whatsapp (default ~/.shipyard/comms/
whatsapp), mode 0700:

  config.json       settings (see 'help config')
  session.db        the linked device's keys — whoever has this file can act
                    as the device; protect it like a password
  archive.db        SQLite: chat catalog (name, kind, watched, TTL, allowlist,
                    last activity), messages of watched chats only, and the
                    send log (30 days)
  run/status.json   daemon heartbeat and connection state
  run/wa.sock       control socket (0600)
  run/wa.lock       one process owns the session at a time

Message content is stored only for watched chats and is deleted after its TTL
(max 90d); unwatch deletes it at once. Message bodies never go to the logs
(~/.shipyard/logs/comms/), only chat ids and counts. Media files are not
downloaded.`},
	{"exit-codes", "exit codes of every command", `  0   success
  1   usage or runtime error (daemon not running, chat not found, …)
  3   send refused by the policy (send only; the code is in the message)
  10  serve: another process owns the session (daemon already running)
  20  serve: not paired
  30  serve: runtime error, or the session was taken over by another client
  40  serve: logged out by the phone — pair again`},
}

func findCmd(name string) (cmdDoc, bool) {
	i := slices.IndexFunc(cmdDocs, func(c cmdDoc) bool { return c.name == name })
	if i < 0 {
		return cmdDoc{}, false
	}
	return cmdDocs[i], true
}

// printOverview is `help` with no arguments: enough context for a reader
// (human or agent) that knows nothing about this tool.
func printOverview(w io.Writer) {
	prog := progName()
	fmt.Fprintf(w, `%s

Links a WhatsApp account to this machine as a companion device (like WhatsApp
Web), keeps a local, TTL-bound archive of the chats you choose to watch, and
sends text messages only through a daemon that enforces an allowlist, a
prior-conversation rule and rate limits. Part of the Shipyard comms family.

Usage: %s <command> [flags]
       %s help <command|topic>     (or <command> --help)

Concepts
  catalog     every known chat: contacts, groups, the self chat
  watch       archive a chat's messages from now on; TTL deletes them later
  daemon      'serve' holds the connection; required for sending
  allowlist   chats Shipyard may message ('allow'); groups also need allow-groups
  limits      per contact / per group / overall send caps ('limits')
  no_history  a chat needs a prior human conversation before the first send

`, app.Info(), prog, prog)
	for _, g := range groups {
		fmt.Fprintln(w, g)
		for _, c := range cmdDocs {
			if c.group == g {
				fmt.Fprintf(w, "  %-9s %s\n", c.name, c.summary)
			}
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "Topics")
	for _, t := range topics {
		fmt.Fprintf(w, "  %-11s %s\n", t.name, t.summary)
	}
	fmt.Fprintf(w, `
Rules for agents
  - Read 'help sending' before sending anything; never work around a refusal.
  - Always pass an idempotency key when sending from automation.
  - If 'status' shows temporary_ban, stop all automation and tell a person.
  - session.db is a credential; never copy or print it.

Version %s · state in %s
`, app.Version, stateDir())
}

func stateDir() string {
	if h, err := wa.Home(); err == nil {
		return h
	}
	return "~/.shipyard/comms/whatsapp"
}

// printCmdHelp prints one command's help. fs, when non-nil, supplies the
// authoritative flag list.
func printCmdHelp(w io.Writer, c cmdDoc, fs *flag.FlagSet) {
	prog := progName()
	fmt.Fprintf(w, "%s %s — %s\n\nUsage: %s %s\n\n%s\n", prog, c.name, c.summary, prog, c.usage, c.details)
	if fs != nil {
		var flags strings.Builder
		fs.SetOutput(&flags)
		fs.PrintDefaults()
		if flags.Len() > 0 {
			fmt.Fprintf(w, "\nFlags:\n%s", flags.String())
		}
	}
	if len(c.examples) > 0 {
		fmt.Fprintln(w, "\nExamples:")
		// Examples are prefixed with the program name unless they place it
		// themselves ({prog}, e.g. after a pipe) or run another command.
		for _, ex := range c.examples {
			switch {
			case strings.Contains(ex, "{prog}"):
				ex = strings.ReplaceAll(ex, "{prog}", prog)
			case !strings.HasPrefix(ex, "shipyard "):
				ex = prog + " " + ex
			}
			fmt.Fprintf(w, "  %s\n", ex)
		}
	}
}

// runHelp is `help [command|topic]`. Command help is produced by running
// the command itself with --help, so the flags shown are the real ones.
func runHelp(ctx context.Context, e env, args []string) error {
	switch {
	case len(args) == 0:
		printOverview(e.stdout)
		return nil
	case len(args) > 1:
		return errors.New("usage: help [command|topic]")
	}
	if _, ok := findCmd(args[0]); ok {
		return dispatch(ctx, e, []string{args[0], "--help"})
	}
	if i := slices.IndexFunc(topics, func(t topicDoc) bool { return t.name == args[0] }); i >= 0 {
		fmt.Fprintf(e.stdout, "%s — %s\n\n%s\n", topics[i].name, topics[i].summary, topics[i].body)
		return nil
	}
	return fmt.Errorf("no help for %q; run '%s help' for the list", args[0], progName())
}

// errHelpShown signals that a command printed its help; run() maps it to
// exit 0.
var errHelpShown = errors.New("help shown")

// cmdFlags is a command's FlagSet plus what it needs to print that
// command's help on -h/--help.
type cmdFlags struct {
	*flag.FlagSet
	doc cmdDoc
	out io.Writer
}

func newFlags(name string, e env) *cmdFlags {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.Usage = func() {} // help is printed by parse, errors get a hint instead
	doc, _ := findCmd(strings.Fields(name)[0])
	return &cmdFlags{FlagSet: fs, doc: doc, out: e.stdout}
}

// parse runs the FlagSet over args while allowing flags after positionals
// (`watch Família --ttl 7d`), which the flag package alone stops at.
// -h/--help prints the command's help and returns errHelpShown.
func parse(fs *cmdFlags, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				printCmdHelp(fs.out, fs.doc, fs.FlagSet)
				return nil, errHelpShown
			}
			return nil, fmt.Errorf("%w (see '%s %s --help')", err, progName(), fs.doc.name)
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}
