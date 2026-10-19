package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa"
	"github.com/shipyard-auto/shipyard/addons/comms-whatsapp/internal/wa/archive"
)

// ExitRefused is the exit code of `send` when the daemon's policy refused
// the message (as opposed to 1 for usage or transport errors), so scripts
// can tell "not allowed" from "broken".
const ExitRefused = 3

// ── send ─────────────────────────────────────────────────────────────────

func runSend(ctx context.Context, e env, args []string) error {
	fs := newFlags("send", e)
	text := fs.String("text", "", "message text")
	fromStdin := fs.Bool("stdin", false, "read the message text from stdin")
	key := fs.String("idempotency-key", "", "makes retries safe: a repeated key returns the first result instead of sending again")
	dryRun := fs.Bool("dry-run", false, "run every check without sending")
	file := fs.String("file", "", "attach an image (JPEG/PNG), video (MP4), audio or any file; --text becomes the caption")
	voiceNote := fs.Bool("voice-note", false, "send an Ogg/Opus --file as a voice note")
	mimeType := fs.String("mime", "", "MIME type of --file (default: from its name, then its content)")
	fileName := fs.String("file-name", "", "name the recipient sees for a document (default: the file's name)")
	asJSON := fs.Bool("json", false, "emit JSON")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: send <chat> (--text T | --stdin | --file F [--text caption]) [--idempotency-key K] [--dry-run] [--json]")
	}
	media, err := cliMedia(*file, *mimeType, *fileName, *voiceNote)
	if err != nil {
		return err
	}
	body := *text
	if *fromStdin {
		if body != "" {
			return errors.New("--text and --stdin are mutually exclusive")
		}
		raw, err := io.ReadAll(io.LimitReader(e.stdin, wa.MaxMessageChars*4+1))
		if err != nil {
			return err
		}
		body = strings.TrimRight(string(raw), "\n")
	}

	res, err := sendVia(ctx, e, pos[0], wa.SendRequest{Text: body, Media: media, IdempotencyKey: *key, DryRun: *dryRun})
	if err != nil {
		var se *wa.SendError
		if errors.As(err, &se) {
			return &wa.ExitError{Code: ExitRefused, Err: errors.New(refusalMessage(se))}
		}
		return err
	}
	what := "a message"
	switch res.Kind {
	case wa.SendKindImage, wa.SendKindAudio:
		what = "an " + res.Kind
	case wa.SendKindVideo, wa.SendKindFile:
		what = "a " + res.Kind
	}
	if *asJSON {
		return writeJSON(e.stdout, res)
	}
	switch {
	case res.DryRun:
		fmt.Fprintf(e.stdout, "Dry run: %s to %s would be accepted.\n", what, res.ChatName)
	case res.Duplicate:
		fmt.Fprintf(e.stdout, "Already sent to %s at %s (id %s); not sent again.\n", res.ChatName, res.SentAt.Local().Format("15:04:05"), res.MsgID)
	default:
		fmt.Fprintf(e.stdout, "Sent %s to %s (id %s).\n", what, res.ChatName, res.MsgID)
	}
	return nil
}

// cliMedia builds the attachment of a local send. The daemon reads the
// file, so the path is made absolute here (its working directory differs).
func cliMedia(file, mimeType, fileName string, voiceNote bool) (*wa.OutMedia, error) {
	if file == "" {
		if mimeType != "" || fileName != "" || voiceNote {
			return nil, errors.New("--mime, --file-name and --voice-note need --file")
		}
		return nil, nil
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("--file: %w", err)
	}
	return &wa.OutMedia{Path: abs, MimeType: mimeType, FileName: fileName, VoiceNote: voiceNote}, nil
}

// sendVia resolves a chat name/phone/JID locally, then asks the daemon to
// send. Policy refusals come back as *wa.SendError.
func sendVia(ctx context.Context, e env, chatQuery string, req wa.SendRequest) (wa.SendResult, error) {
	arch, err := openArchive(ctx, e)
	if err != nil {
		return wa.SendResult{}, err
	}
	chat, err := resolveChat(ctx, arch, chatQuery)
	_ = arch.Close()
	if err != nil {
		return wa.SendResult{}, err
	}
	req.Chat = chat.JID
	var res wa.SendResult
	if err := wa.CallRPC(ctx, e.paths.Socket, "send", req, &res); err != nil {
		var re *wa.RPCError
		if errors.As(err, &re) {
			if se := re.SendError(); se != nil {
				return wa.SendResult{}, se
			}
		}
		return wa.SendResult{}, err
	}
	return res, nil
}

// ── allow / deny ─────────────────────────────────────────────────────────

func runAllow(ctx context.Context, e env, args []string, allowed bool) error {
	name := "allow"
	if !allowed {
		name = "deny"
	}
	fs := newFlags(name, e)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: %s <chat>", name)
	}
	arch, err := openArchive(ctx, e)
	if err != nil {
		return err
	}
	defer func() { _ = arch.Close() }()
	chat, err := resolveChat(ctx, arch, pos[0])
	if err != nil {
		return err
	}
	if chat.Kind == archive.KindGroup && allowed {
		cfg, err := loadConfig(e)
		if err != nil {
			return err
		}
		if !cfg.AllowGroups {
			return errors.New("sending to groups is off; enable it first with 'config set allow-groups true' " +
				"(only for groups you run — posting into others' groups is what WhatsApp bans most)")
		}
	}
	if err := arch.SetSendAllowed(ctx, chat.JID, allowed); err != nil {
		return err
	}
	if allowed {
		fmt.Fprintf(e.stdout, "%s (%s) may now receive messages from Shipyard.\n", chat.Name, chat.JID)
		if chat.LastActivity.IsZero() {
			fmt.Fprintln(e.stdout, "No conversation with this chat has been seen yet: sends stay refused until a message is sent or received there from the phone.")
		}
	} else {
		fmt.Fprintf(e.stdout, "%s removed from the send allowlist.\n", chat.Name)
	}
	return nil
}

// ── pause / resume ───────────────────────────────────────────────────────

func runPause(_ context.Context, e env, args []string, paused bool) error {
	name := "resume"
	if paused {
		name = "pause"
	}
	pos, err := parse(newFlags(name, e), args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return fmt.Errorf("usage: %s", name)
	}
	cfg, err := loadConfig(e)
	if err != nil {
		return err
	}
	cfg.Paused = paused
	if err := e.paths.Ensure(); err != nil {
		return err
	}
	if err := wa.SaveConfig(e.paths.Config, cfg); err != nil {
		return err
	}
	if paused {
		fmt.Fprintln(e.stdout, "Sending paused. Archiving continues; 'resume' to send again.")
	} else {
		fmt.Fprintln(e.stdout, "Sending resumed.")
	}
	return nil
}

// ── tool: native crew exec tools ─────────────────────────────────────────
//
// `tool <name>` speaks the crew exec contract directly: JSON input on
// stdin, a {ok, data|error, details} envelope on stdout, exit 0. No shell
// wrapper and no message text in argv templates.

type envelope struct {
	OK      bool   `json:"ok"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
	Details any    `json:"details,omitempty"`
}

func runTool(ctx context.Context, e env, args []string) error {
	pos, err := parse(newFlags("tool", e), args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: tool <send|chats|history|limits>  (JSON on stdin, envelope on stdout)")
	}
	raw, err := io.ReadAll(io.LimitReader(e.stdin, 1<<20))
	if err != nil {
		return writeJSON(e.stdout, envelope{Error: "read input: " + err.Error()})
	}
	var out envelope
	switch pos[0] {
	case "send":
		out = toolSend(ctx, e, raw)
	case "chats":
		out = toolChats(ctx, e, raw)
	case "history":
		out = toolHistory(ctx, e, raw)
	case "limits":
		out = toolLimits(ctx, e, raw)
	default:
		out = envelope{Error: "unknown tool " + pos[0]}
	}
	return writeJSON(e.stdout, out)
}

func decodeInput(raw []byte, v any) error {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	return json.Unmarshal(raw, v)
}

func toolSend(ctx context.Context, e env, raw []byte) envelope {
	var in struct {
		Chat           string `json:"chat"`
		Text           string `json:"text"`
		File           string `json:"file"`
		VoiceNote      bool   `json:"voice_note"`
		IdempotencyKey string `json:"idempotency_key"`
		DryRun         bool   `json:"dry_run"`
	}
	if err := decodeInput(raw, &in); err != nil {
		return envelope{Error: "invalid input: " + err.Error()}
	}
	if in.Chat == "" || (in.Text == "" && in.File == "") {
		return envelope{Error: "chat and text (or file) are required"}
	}
	media, err := cliMedia(in.File, "", "", in.VoiceNote)
	if err != nil {
		return envelope{Error: err.Error()}
	}
	res, err := sendVia(ctx, e, in.Chat, wa.SendRequest{Text: in.Text, Media: media, IdempotencyKey: in.IdempotencyKey, DryRun: in.DryRun})
	if err != nil {
		var se *wa.SendError
		if errors.As(err, &se) {
			details := map[string]any{"code": string(se.Code)}
			if se.RetryAfter > 0 {
				details["retry_after_seconds"] = int(se.RetryAfter.Seconds())
			}
			return envelope{Error: se.Msg, Details: details}
		}
		return envelope{Error: err.Error()}
	}
	return envelope{OK: true, Data: res}
}

func toolChats(ctx context.Context, e env, raw []byte) envelope {
	var in struct {
		Search  string `json:"search"`
		Watched bool   `json:"watched"`
	}
	if err := decodeInput(raw, &in); err != nil {
		return envelope{Error: "invalid input: " + err.Error()}
	}
	arch, err := openArchive(ctx, e)
	if err != nil {
		return envelope{Error: err.Error()}
	}
	defer func() { _ = arch.Close() }()
	chats, err := arch.ListChats(ctx, archive.ListFilter{Search: in.Search, WatchedOnly: in.Watched})
	if err != nil {
		return envelope{Error: err.Error()}
	}
	type row struct {
		JID         string           `json:"jid"`
		Name        string           `json:"name"`
		Kind        archive.ChatKind `json:"kind"`
		Watched     bool             `json:"watched"`
		SendAllowed bool             `json:"send_allowed"`
	}
	const maxRows = 50 // keep tool output small enough for a model's context
	rows := make([]row, 0, min(len(chats), maxRows))
	for _, c := range chats {
		if len(rows) == maxRows {
			break
		}
		rows = append(rows, row{c.JID, c.Name, c.Kind, c.Watched, c.SendAllowed})
	}
	return envelope{OK: true, Data: map[string]any{"chats": rows, "total": len(chats), "truncated": len(chats) > maxRows}}
}

func toolHistory(ctx context.Context, e env, raw []byte) envelope {
	in := struct {
		Chat  string `json:"chat"`
		Since string `json:"since"`
		Limit int    `json:"limit"`
	}{Since: "24h", Limit: 200}
	if err := decodeInput(raw, &in); err != nil {
		return envelope{Error: "invalid input: " + err.Error()}
	}
	if in.Chat == "" {
		return envelope{Error: "chat is required"}
	}
	since, err := wa.ParseDuration(in.Since)
	if err != nil {
		return envelope{Error: err.Error()}
	}
	if in.Limit <= 0 || in.Limit > 500 {
		in.Limit = 500
	}
	arch, err := openArchive(ctx, e)
	if err != nil {
		return envelope{Error: err.Error()}
	}
	defer func() { _ = arch.Close() }()
	chat, err := resolveChat(ctx, arch, in.Chat)
	if err != nil {
		return envelope{Error: err.Error()}
	}
	if !chat.Watched {
		return envelope{Error: fmt.Sprintf("%s is not watched, so nothing is archived for it", chat.Name)}
	}
	msgs, err := arch.History(ctx, chat.JID, time.Now().Add(-since), in.Limit)
	if err != nil {
		return envelope{Error: err.Error()}
	}
	return envelope{OK: true, Data: map[string]any{"chat": map[string]string{"jid": chat.JID, "name": chat.Name}, "messages": nonNil(msgs)}}
}

// ── tools install ────────────────────────────────────────────────────────

type toolDef struct {
	name, description string
	input, output     map[string]string
}

var toolDefs = []toolDef{
	{
		name: "wa_send",
		description: "Send a WhatsApp message from the linked number: text, or a file (absolute path: image JPEG/PNG, video MP4, audio, document) with text as its caption. " +
			"The chat must be on the allowlist and have a prior conversation; " +
			"refusals return details.code (not_allowed, groups_disabled, no_history, rate_limited, paused, too_long, unknown_chat, not_connected) and must not be worked around; " +
			"rate_limited also returns details.retry_after_seconds (check wa_limits before batches). " +
			"Pass an idempotency_key so a retry never sends twice. Full rules: `shipyard-comms-whatsapp help sending`.",
		input:  map[string]string{"chat": "string", "text": "string", "file": "string", "voice_note": "boolean", "idempotency_key": "string", "dry_run": "boolean"},
		output: map[string]string{"msg_id": "string", "kind": "string", "chat": "string", "chat_name": "string", "sent_at": "string", "duplicate": "boolean", "dry_run": "boolean"},
	},
	{
		name:        "wa_chats",
		description: "List known WhatsApp chats (name, jid, kind, watched, send_allowed). Optional search ignores case and accents.",
		input:       map[string]string{"search": "string", "watched": "boolean"},
		output:      map[string]string{"chats": "array", "total": "number", "truncated": "boolean"},
	},
	{
		name: "wa_limits",
		description: "Show WhatsApp send limits and live usage: per contact, per group and overall, with remaining slots and when the next opens. " +
			"With chat, also says whether a send to it would be accepted now and which refusal codes apply.",
		input:  map[string]string{"chat": "string"},
		output: map[string]string{"per_chat": "object", "per_group": "object", "global": "object", "allow_groups": "boolean", "paused": "boolean", "chat": "object"},
	},
	{
		name:        "wa_history",
		description: "Read archived messages of a watched WhatsApp chat, oldest first. since like 2h or 3d (default 24h); limit up to 500.",
		input:       map[string]string{"chat": "string", "since": "string", "limit": "number"},
		output:      map[string]string{"chat": "object", "messages": "array"},
	},
}

func runToolsInstall(_ context.Context, e env, args []string) error {
	fs := newFlags("tools", e)
	dir := fs.String("dir", "", "crew tools library (default $SHIPYARD_HOME/crew/tools or ~/.shipyard/crew/tools)")
	force := fs.Bool("force", false, "overwrite existing wa_* tool files")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || pos[0] != "install" {
		return fmt.Errorf("usage: tools install [--dir D] [--force] (see '%s help tools')", progName())
	}
	if *dir == "" {
		// e.paths.Dir is <shipyard home>/comms/whatsapp.
		*dir = filepath.Join(filepath.Dir(filepath.Dir(e.paths.Dir)), "crew", "tools")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return err
	}
	for _, t := range toolDefs {
		path := filepath.Join(*dir, t.name+".yaml")
		if _, err := os.Stat(path); err == nil && !*force {
			return fmt.Errorf("%s exists; pass --force to overwrite", path)
		}
		if err := os.WriteFile(path, []byte(renderToolYAML(t, self)), 0o600); err != nil {
			return err
		}
		fmt.Fprintf(e.stdout, "wrote %s\n", path)
	}
	fmt.Fprintln(e.stdout, "Reference them from agent.yaml with `- ref: wa_send` (etc.) and set `builtin_tools: []` on agents that send.")
	return nil
}

func renderToolYAML(t toolDef, binary string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Generated by shipyard-comms-whatsapp tools install.\nname: %s\nprotocol: exec\ndescription: %q\n", t.name, t.description)
	fmt.Fprintf(&b, "command:\n  - %q\n  - tool\n  - %s\n", binary, strings.TrimPrefix(t.name, "wa_"))
	writeSchema(&b, "input_schema", t.input)
	writeSchema(&b, "output_schema", t.output)
	return b.String()
}

func writeSchema(b *strings.Builder, key string, fields map[string]string) {
	fmt.Fprintf(b, "%s:\n", key)
	// Stable output keeps diffs of the generated files readable.
	for _, k := range slices.Sorted(maps.Keys(fields)) {
		fmt.Fprintf(b, "  %s: %s\n", k, fields[k])
	}
}
