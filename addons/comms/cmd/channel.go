package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/shipyard-auto/shipyard/addons/comms/internal/comms"
)

// runChannel dispatches `channel <subcommand>`. The first positional
// after "channel" picks the action; help is shown when the dispatcher
// recognises no subcommand.
func runChannel(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printChannelHelp(stdout)
		return nil
	}
	switch args[0] {
	case "add":
		return runChannelAdd(ctx, args[1:], stdout, stderr)
	case "list":
		return runChannelList(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown channel subcommand: %s\n\n", args[0])
		printChannelHelp(stderr)
		return fmt.Errorf("comms: unknown channel subcommand %q", args[0])
	}
}

func printChannelHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage: shipyard-comms channel <subcommand>")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Subcommands:")
	fmt.Fprintln(w, "  add     register a new channel and store its credentials")
	fmt.Fprintln(w, "  list    list configured channels (table or --json)")
}

// runChannelAdd parses the flags for `channel add`, builds and persists
// a comms.Channel + the provider-specific secret payload.
//
// Provider-specific knobs are namespaced under the provider name
// (`--telegram-token`, `--telegram-bot-username`) so the flag surface
// scales when new providers land without colliding.
func runChannelAdd(_ context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("channel add", flag.ContinueOnError)
	fs.SetOutput(stderr)

	typeFlag := fs.String("type", "", "channel type (one of: "+listKnownTypes()+")")
	name := fs.String("name", "", "human-friendly channel name (unique)")
	telegramToken := fs.String("telegram-token", "", "Telegram bot token (required when --type=telegram)")
	telegramBotUsername := fs.String("telegram-bot-username", "", "Telegram bot username (optional, stored on the channel)")
	jsonOutput := fs.Bool("json", false, "emit the registered channel as JSON instead of human-readable text")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *typeFlag == "" {
		return errors.New("comms: --type is required")
	}
	if *name == "" {
		return errors.New("comms: --name is required")
	}

	ct := comms.ChannelType(*typeFlag)
	if !ct.IsKnown() {
		return fmt.Errorf("comms: unknown channel type %q (known: %s)", *typeFlag, listKnownTypes())
	}

	// Per-type validation + secret payload construction.
	var settings map[string]any
	var secretBytes []byte
	switch ct {
	case comms.ChannelTypeTelegram:
		if *telegramToken == "" {
			return errors.New("comms: --telegram-token is required for --type=telegram")
		}
		if *telegramBotUsername != "" {
			settings = map[string]any{"bot_username": *telegramBotUsername}
		}
		var err error
		secretBytes, err = json.Marshal(map[string]string{"token": *telegramToken})
		if err != nil {
			return fmt.Errorf("comms: encode telegram secret: %w", err)
		}
	default:
		return fmt.Errorf("comms: type %q is known but has no add-flow wired (internal bug)", ct)
	}

	id, err := newChannelID()
	if err != nil {
		return err
	}
	now := time.Now().UTC()

	ch := comms.Channel{
		ID:        id,
		Name:      *name,
		Type:      ct,
		Settings:  settings,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := ch.Validate(); err != nil {
		return fmt.Errorf("comms: built invalid channel: %w", err)
	}

	home, err := comms.CommsHome()
	if err != nil {
		return err
	}
	store := comms.NewChannelStore(home)
	existing, err := store.Load()
	if err != nil {
		return err
	}
	// Save validates per-channel and rejects duplicate names before any
	// disk write happens; we let it own that check.
	if err := store.Save(append(existing, ch)); err != nil {
		return err
	}

	secrets := comms.NewSecretStore(home)
	if err := secrets.Write(ch.ID, secretBytes); err != nil {
		// Best-effort rollback: drop the channel we just added so the
		// listing doesn't show a channel without credentials.
		if rbErr := store.Save(existing); rbErr != nil {
			return fmt.Errorf("comms: write secret (%w); channel rollback also failed: %v", err, rbErr)
		}
		return fmt.Errorf("comms: write secret: %w", err)
	}

	if *jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(ch)
	}
	fmt.Fprintf(stdout, "Registered channel %s (id=%s, type=%s).\n", ch.Name, ch.ID, ch.Type)
	return nil
}

// runChannelList prints every configured channel. Default format is a
// human-readable table; --json emits the slice as a single JSON document
// for scripting consumers.
func runChannelList(_ context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("channel list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOutput := fs.Bool("json", false, "emit channels as a JSON array")
	if err := fs.Parse(args); err != nil {
		return err
	}

	home, err := comms.CommsHome()
	if err != nil {
		return err
	}
	channels, err := comms.NewChannelStore(home).Load()
	if err != nil {
		return err
	}
	// Deterministic order: alphabetical by Name so two runs produce the
	// same output even when the on-disk order shifts (e.g. after a
	// future migration that rewrites channels.json).
	sort.Slice(channels, func(i, j int) bool { return channels[i].Name < channels[j].Name })

	if *jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if channels == nil {
			channels = []comms.Channel{}
		}
		return enc.Encode(channels)
	}

	if len(channels) == 0 {
		fmt.Fprintln(stdout, "No channels configured. Add one with 'shipyard comms channel add'.")
		return nil
	}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTYPE\tID\tCREATED")
	for _, c := range channels {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", c.Name, c.Type, c.ID, c.CreatedAt.Format(time.RFC3339))
	}
	return tw.Flush()
}

// newChannelID returns a fresh channel identifier matching the regex
// enforced by Channel.Validate: 16-32 lowercase alphanumeric chars.
// crypto/rand → 16 random bytes → hex-encoded gives 32 hex chars, all in
// [0-9a-f] — a strict subset of [a-z0-9].
func newChannelID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("comms: generate channel id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// listKnownTypes returns a comma-separated list of supported channel
// types, used in error messages and help text.
func listKnownTypes() string {
	types := comms.KnownChannelTypes()
	out := make([]string, len(types))
	for i, t := range types {
		out[i] = string(t)
	}
	// Sort for stability — KnownChannelTypes already returns a stable
	// order, but make the contract explicit so adding a type later
	// doesn't reorder error messages unpredictably.
	sort.Strings(out)
	if len(out) == 0 {
		return "(none)"
	}
	res := out[0]
	for _, s := range out[1:] {
		res += ", " + s
	}
	return res
}
