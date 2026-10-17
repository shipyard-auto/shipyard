package cli

import (
	"context"
	"fmt"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
	"github.com/shipyard-auto/shipyard/internal/ui"
)

type subscriptionAPI interface {
	Close() error
	SubscriptionList(ctx context.Context, clientID string) ([]fairwayctl.Subscription, error)
	SubscriptionAdd(ctx context.Context, clientID, topic, url string, allowHTTP bool) (fairwayctl.Subscription, error)
	SubscriptionDelete(ctx context.Context, id string) error
	SubscriptionTest(ctx context.Context, id string) (string, error)
}

type subscriptionDeps struct {
	route routeDeps
	dial  func(context.Context, fairwayctl.Opts) (subscriptionAPI, error)
}

func (d subscriptionDeps) run(cmd *cobra.Command, fn func(subscriptionAPI) error) error {
	d.route = d.route.withDefaults()
	if d.dial == nil {
		d.dial = func(ctx context.Context, opts fairwayctl.Opts) (subscriptionAPI, error) {
			return fairwayctl.Dial(ctx, opts)
		}
	}
	c, err := d.dial(cmd.Context(), fairwayctl.Opts{SocketPath: d.route.socketPath, Version: d.route.version})
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return fn(c)
}

func newFairwaySubscriptionCmd() *cobra.Command {
	return newFairwaySubscriptionCmdWith(subscriptionDeps{})
}

func newFairwaySubscriptionCmdWith(deps subscriptionDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "subscription",
		Aliases: []string{"subscriptions", "sub"},
		Short:   "Deliver published events to clients as signed webhooks",
		Long: `Local producers (addons such as comms-whatsapp) publish events to fairway
(POST /_shipyard/v1/events, loopback only). Fairway sends each event, as a
signed webhook, to every subscription of its topic whose client holds both
"events.subscribe:<topic>" and the event's own scope (e.g. an event scoped
"whatsapp.read:<group>" only reaches clients allowed to read that group).
Scopes are re-checked on every delivery.

Webhooks are POSTed with the client's own secret (same v1 scheme as
requests) plus X-Shipyard-Event-Id, X-Shipyard-Event-Topic and
X-Shipyard-Delivery-Attempt. Body:
  {"id":"evt_…","topic":"…","occurred_at":"…","source":"…","scope":"…","data":{…}}
Delivery is at-least-once while fairway runs: network errors, 5xx, 408 and
429 retry after 2s, 10s, 30s, 2m, 5m; other 4xx are final. Deduplicate by
event id. The queue is in memory: events pending at a restart are lost, so
consumers recover through the producer's query API.

URLs must be https, except http to loopback (or --allow-http, admin only).
Clients can also manage their own subscriptions with signed requests:
GET/POST /_shipyard/v1/subscriptions, DELETE /_shipyard/v1/subscriptions/{id}.`,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}

	var allowHTTP bool
	add := &cobra.Command{
		Use:     "add <client> --topic <topic> --url <url>",
		Short:   "Subscribe a client to a topic",
		Example: "  shipyard fairway subscription add promo-manager --topic whatsapp.message --url http://127.0.0.1:9100/hooks/whatsapp",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			topic, _ := cmd.Flags().GetString("topic")
			url, _ := cmd.Flags().GetString("url")
			return deps.run(cmd, func(c subscriptionAPI) error {
				sub, err := c.SubscriptionAdd(cmd.Context(), args[0], topic, url, allowHTTP)
				if err != nil {
					return err
				}
				ui.Printf(cmd.OutOrStdout(), "Subscription %s: %s events → %s (signed for %s).\n", sub.ID, sub.Topic, sub.URL, sub.ClientID)
				ui.Printf(cmd.OutOrStdout(), "%s\n", ui.Muted("Send a test event with 'shipyard fairway subscription test "+sub.ID+"'."))
				return nil
			})
		},
	}
	add.Flags().String("topic", "", "Topic, '*' allowed (e.g. whatsapp.message, whatsapp.*)")
	add.Flags().String("url", "", "Webhook URL (https, or http to loopback)")
	add.Flags().BoolVar(&allowHTTP, "allow-http", false, "Accept plain http to a non-loopback host")
	_ = add.MarkFlagRequired("topic")
	_ = add.MarkFlagRequired("url")

	var asJSON bool
	var byClient string
	list := &cobra.Command{
		Use:   "list",
		Short: "List subscriptions with delivery stats",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return deps.run(cmd, func(c subscriptionAPI) error {
				subs, err := c.SubscriptionList(cmd.Context(), byClient)
				if err != nil {
					return err
				}
				if asJSON {
					return writeIndentedJSON(cmd.OutOrStdout(), subs)
				}
				w := cmd.OutOrStdout()
				if len(subs) == 0 {
					ui.Printf(w, "%s\n", ui.Muted("No subscriptions."))
					return nil
				}
				sort.Slice(subs, func(i, j int) bool { return subs[i].ID < subs[j].ID })
				tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "ID\tCLIENT\tTOPIC\tURL\tDELIVERED\tFAILED\tLAST")
				for _, s := range subs {
					last := "-"
					if !s.Stats.LastAttemptAt.IsZero() {
						last = fmt.Sprintf("%d %s ago", s.Stats.LastStatus, time.Since(s.Stats.LastAttemptAt).Round(time.Second))
						if s.Stats.LastError != "" {
							last += " (" + s.Stats.LastError + ")"
						}
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%s\n", s.ID, s.ClientID, s.Topic, s.URL, s.Stats.Delivered, s.Stats.Failed, last)
				}
				return tw.Flush()
			})
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "Print as JSON")
	list.Flags().StringVar(&byClient, "client", "", "Only this client's subscriptions")

	rm := &cobra.Command{
		Use:     "rm <id>",
		Aliases: []string{"delete"},
		Short:   "Remove a subscription",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return deps.run(cmd, func(c subscriptionAPI) error {
				if err := c.SubscriptionDelete(cmd.Context(), args[0]); err != nil {
					return err
				}
				ui.Printf(cmd.OutOrStdout(), "Subscription %s removed.\n", args[0])
				return nil
			})
		},
	}

	test := &cobra.Command{
		Use:   "test <id>",
		Short: "Send a signed shipyard.ping event to one subscription",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return deps.run(cmd, func(c subscriptionAPI) error {
				id, err := c.SubscriptionTest(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				ui.Printf(cmd.OutOrStdout(), "Queued test event %s; check 'shipyard fairway subscription list' for the result.\n", id)
				return nil
			})
		},
	}
	cmd.AddCommand(add, list, rm, test)
	return cmd
}
