package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/shipyard-auto/shipyard/addons/comms/internal/app"
	"github.com/shipyard-auto/shipyard/addons/comms/internal/comms"
)

// runSend parses `send` flags, builds a comms.Message, resolves the
// target channel + provider, performs the send and prints a receipt.
//
// The --channel value can be either the channel's Name or its ID; Name
// wins on ambiguity (Validate keeps both name-space and id-space
// distinct, but the precedence is documented for the reader).
func runSend(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	fs.SetOutput(stderr)

	channelRef := fs.String("channel", "", "channel name or ID to send through (required)")
	to := fs.String("to", "", "recipient address in provider-native form (e.g. chat_id or @username); required")
	text := fs.String("text", "", "message body (required unless --stdin-body is set)")
	stdinBody := fs.Bool("stdin-body", false, "read the message body from stdin instead of --text")
	from := fs.String("from", "", "optional sender identifier (defaults to the channel id)")
	traceID := fs.String("trace-id", "", "optional trace id, propagated into the envelope for log correlation")
	jsonOutput := fs.Bool("json", false, "emit the receipt as JSON")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *channelRef == "" {
		return errors.New("comms: --channel is required")
	}
	if *to == "" {
		return errors.New("comms: --to is required")
	}

	body := *text
	if *stdinBody {
		if body != "" {
			return errors.New("comms: --text and --stdin-body are mutually exclusive")
		}
		raw, err := io.ReadAll(stdin)
		if err != nil {
			return fmt.Errorf("comms: read stdin body: %w", err)
		}
		body = string(raw)
	}
	if body == "" {
		return errors.New("comms: message body is required (use --text or --stdin-body)")
	}

	home, err := comms.CommsHome()
	if err != nil {
		return err
	}
	channels, err := comms.NewChannelStore(home).Load()
	if err != nil {
		return err
	}
	ch, ok := resolveChannel(channels, *channelRef)
	if !ok {
		return fmt.Errorf("comms: no channel found with name or id %q", *channelRef)
	}

	rawSecret, err := comms.NewSecretStore(home).Read(ch.ID)
	if err != nil {
		return fmt.Errorf("comms: load credentials for channel %s: %w", ch.ID, err)
	}

	prov, err := app.Providers().Lookup(ch.Type)
	if err != nil {
		return err
	}

	fromAddr := comms.Address{Channel: ch.ID, Raw: ch.ID}
	if *from != "" {
		fromAddr.Raw = *from
	}
	msg := comms.Message{
		SchemaVersion: comms.MessageSchemaVersion,
		TraceID:       *traceID,
		Channel:       ch.ID,
		Direction:     comms.DirectionOutbound,
		From:          fromAddr,
		To:            []comms.Address{{Channel: ch.ID, Raw: *to}},
		Body:          body,
		Timestamp:     time.Now().UTC(),
	}
	if err := msg.Validate(); err != nil {
		return fmt.Errorf("comms: built invalid message: %w", err)
	}

	receipt, err := prov.Send(ctx, ch, rawSecret, msg)
	if err != nil {
		return err
	}

	if *jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Channel           string    `json:"channel"`
			ChannelName       string    `json:"channel_name"`
			ProviderMessageID string    `json:"provider_message_id"`
			SentAt            time.Time `json:"sent_at"`
		}{
			Channel:           ch.ID,
			ChannelName:       ch.Name,
			ProviderMessageID: receipt.ProviderMessageID,
			SentAt:            receipt.SentAt,
		})
	}
	fmt.Fprintf(stdout, "Sent via %s (provider_message_id=%s, at=%s).\n",
		ch.Name, receipt.ProviderMessageID, receipt.SentAt.Format(time.RFC3339))
	return nil
}

// resolveChannel finds a channel by Name first (case-sensitive — Name
// validation rejects mixed-case duplicates anyway), then by ID.
func resolveChannel(channels []comms.Channel, ref string) (comms.Channel, bool) {
	for _, c := range channels {
		if c.Name == ref {
			return c, true
		}
	}
	for _, c := range channels {
		if c.ID == ref {
			return c, true
		}
	}
	return comms.Channel{}, false
}
