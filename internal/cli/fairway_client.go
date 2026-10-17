package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
	"github.com/shipyard-auto/shipyard/internal/signing"
	"github.com/shipyard-auto/shipyard/internal/ui"
)

// clientAPI is the slice of the fairway control client used here.
type clientAPI interface {
	Close() error
	ClientList(ctx context.Context) ([]fairwayctl.ClientInfo, error)
	ClientAdd(ctx context.Context, id string, scopes []string) (fairwayctl.IssuedClient, error)
	ClientRotate(ctx context.Context, id string) (fairwayctl.IssuedClient, error)
	ClientSetScopes(ctx context.Context, id string, scopes []string) (fairwayctl.ClientInfo, error)
	ClientDelete(ctx context.Context, id string) error
	ClientSetDisabled(ctx context.Context, id string, disabled bool) error
}

type clientDeps struct {
	route     routeDeps
	dial      func(context.Context, fairwayctl.Opts) (clientAPI, error)
	writeFile func(path string, data []byte, perm os.FileMode) error
	now       func() time.Time
}

func (d clientDeps) withDefaults() clientDeps {
	d.route = d.route.withDefaults()
	if d.dial == nil {
		d.dial = func(ctx context.Context, opts fairwayctl.Opts) (clientAPI, error) {
			return fairwayctl.Dial(ctx, opts)
		}
	}
	if d.writeFile == nil {
		d.writeFile = os.WriteFile
	}
	if d.now == nil {
		d.now = time.Now
	}
	return d
}

func (d clientDeps) connect(ctx context.Context) (clientAPI, error) {
	return d.dial(ctx, fairwayctl.Opts{SocketPath: d.route.socketPath, Version: d.route.version})
}

func newFairwayClientCmd() *cobra.Command { return newFairwayClientCmdWith(clientDeps{}) }

func newFairwayClientCmdWith(deps clientDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "client",
		Short: "Manage clients allowed to call signed routes",
		Long: `Clients are external systems (a promotions manager, a dashboard, another
service) that call fairway routes created with '--auth signed --scope <s>'.
Each client has an id, a secret and a list of scopes.

Scopes are opaque strings "<name>[:<resource>]" with '*' as a wildcard:
whatsapp.read, whatsapp.send:120363@g.us, cron.run:backup, events.*.
Fairway checks the name against the route's required scope; the resource
part is passed to the service behind the route, which narrows further.

Requests are signed with HMAC-SHA256 (Shipyard v1). A client sends:
  X-Shipyard-Client     its id
  X-Shipyard-Timestamp  unix seconds (must be within 5 minutes)
  X-Shipyard-Nonce      a random value, never reused
  X-Shipyard-Signature  "v1=" + hex(HMAC-SHA256(secret, string-to-sign))
string-to-sign = "v1\n" + METHOD + "\n" + path + "\n" + raw query + "\n" +
                 timestamp + "\n" + nonce + "\n" + hex(SHA-256(body))
'shipyard fairway client sign' prints these headers for testing.

The service receives the identity as X-Shipyard-Client-Id / -Scopes headers
(http.forward) or SHIPYARD_CLIENT_ID / SHIPYARD_CLIENT_SCOPES env vars
(other actions). Secrets are shown once, at add and rotate.`,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newClientAddCmd(deps), newClientListCmd(deps), newClientRotateCmd(deps),
		newClientScopesCmd(deps), newClientToggleCmd(deps, true), newClientToggleCmd(deps, false),
		newClientRemoveCmd(deps), newClientSignCmd(deps))
	return cmd
}

// withClient dials the daemon, runs fn and closes the connection.
func withClient(cmd *cobra.Command, deps clientDeps, fn func(clientAPI) error) error {
	deps = deps.withDefaults()
	c, err := deps.connect(cmd.Context())
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return fn(c)
}

// deliverSecret prints the secret once or writes it to a 0600 file.
func deliverSecret(w io.Writer, deps clientDeps, issued fairwayctl.IssuedClient, secretFile, verb string) error {
	if secretFile != "" {
		if err := deps.writeFile(secretFile, []byte(issued.Secret+"\n"), 0o600); err != nil {
			return fmt.Errorf("write secret file: %w", err)
		}
		ui.Printf(w, "Client %s %s. Secret written to %s (mode 0600).\n", issued.ID, verb, secretFile)
		return nil
	}
	ui.Printf(w, "Client %s %s.\n\n  secret: %s\n\n", issued.ID, verb, issued.Secret)
	ui.Printf(w, "%s\n", ui.Muted("Store it now: it is not shown again ('client rotate' issues a new one)."))
	return nil
}

func newClientAddCmd(deps clientDeps) *cobra.Command {
	var scopes []string
	var secretFile string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "add <id> --scope <scope> [--scope ...]",
		Short: "Register a client and issue its secret",
		Example: `  shipyard fairway client add promo-manager --scope whatsapp.read --scope events.subscribe:whatsapp.message
  shipyard fairway client add backup-bot --scope cron.run:backup --secret-file ~/.config/backup-bot/secret`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, deps, func(c clientAPI) error {
				issued, err := c.ClientAdd(cmd.Context(), args[0], scopes)
				if err != nil {
					return err
				}
				if asJSON && secretFile == "" {
					return writeIndentedJSON(cmd.OutOrStdout(), issued)
				}
				return deliverSecret(cmd.OutOrStdout(), deps.withDefaults(), issued, secretFile, "registered")
			})
		},
	}
	cmd.Flags().StringArrayVar(&scopes, "scope", nil, "Scope granted to the client (repeatable)")
	cmd.Flags().StringVar(&secretFile, "secret-file", "", "Write the secret to this file (0600) instead of printing it")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the client, secret included, as JSON")
	_ = cmd.MarkFlagRequired("scope")
	return cmd
}

func newClientListCmd(deps clientDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List clients and their scopes (never secrets)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(cmd, deps, func(c clientAPI) error {
				list, err := c.ClientList(cmd.Context())
				if err != nil {
					return err
				}
				if asJSON {
					return writeIndentedJSON(cmd.OutOrStdout(), list)
				}
				w := cmd.OutOrStdout()
				if len(list) == 0 {
					ui.Printf(w, "%s\n", ui.Muted("No clients. Add one with 'shipyard fairway client add'."))
					return nil
				}
				sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
				tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "ID\tSTATE\tSCOPES\tCREATED")
				for _, ci := range list {
					state := "active"
					if ci.Disabled {
						state = "disabled"
					}
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", ci.ID, state, strings.Join(ci.Scopes, ", "), ci.CreatedAt.Local().Format("2006-01-02"))
				}
				return tw.Flush()
			})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print as JSON")
	return cmd
}

func newClientRotateCmd(deps clientDeps) *cobra.Command {
	var secretFile string
	cmd := &cobra.Command{
		Use:   "rotate <id>",
		Short: "Issue a new secret; the old one stops working immediately",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, deps, func(c clientAPI) error {
				issued, err := c.ClientRotate(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				return deliverSecret(cmd.OutOrStdout(), deps.withDefaults(), issued, secretFile, "rotated")
			})
		},
	}
	cmd.Flags().StringVar(&secretFile, "secret-file", "", "Write the new secret to this file (0600) instead of printing it")
	return cmd
}

func newClientScopesCmd(deps clientDeps) *cobra.Command {
	var scopes []string
	cmd := &cobra.Command{
		Use:     "scopes <id> --scope <scope> [--scope ...]",
		Short:   "Replace a client's scopes (secret unchanged)",
		Example: `  shipyard fairway client scopes promo-manager --scope whatsapp.read --scope whatsapp.send:120363@g.us`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, deps, func(c clientAPI) error {
				info, err := c.ClientSetScopes(cmd.Context(), args[0], scopes)
				if err != nil {
					return err
				}
				ui.Printf(cmd.OutOrStdout(), "Client %s now holds: %s\n", info.ID, strings.Join(info.Scopes, ", "))
				return nil
			})
		},
	}
	cmd.Flags().StringArrayVar(&scopes, "scope", nil, "Scope (repeatable); replaces the current list")
	_ = cmd.MarkFlagRequired("scope")
	return cmd
}

func newClientToggleCmd(deps clientDeps, disable bool) *cobra.Command {
	use, short, done := "enable <id>", "Re-enable a disabled client", "enabled"
	if disable {
		use, short, done = "disable <id>", "Block a client without deleting it", "disabled"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, deps, func(c clientAPI) error {
				if err := c.ClientSetDisabled(cmd.Context(), args[0], disable); err != nil {
					return err
				}
				ui.Printf(cmd.OutOrStdout(), "Client %s %s.\n", args[0], done)
				return nil
			})
		},
	}
}

func newClientRemoveCmd(deps clientDeps) *cobra.Command {
	return &cobra.Command{
		Use:     "rm <id>",
		Aliases: []string{"delete"},
		Short:   "Delete a client; its secret stops working immediately",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, deps, func(c clientAPI) error {
				if err := c.ClientDelete(cmd.Context(), args[0]); err != nil {
					return err
				}
				ui.Printf(cmd.OutOrStdout(), "Client %s deleted.\n", args[0])
				return nil
			})
		},
	}
}

// newClientSignCmd signs a request offline (no daemon needed) — for curl
// tests and as an executable reference of the scheme.
func newClientSignCmd(deps clientDeps) *cobra.Command {
	var clientID, secret, secretFile, method, rawURL, body, bodyFile string
	var curl bool
	cmd := &cobra.Command{
		Use:   "sign --client <id> --url <url> [--method POST] [--body s | --body-file f]",
		Short: "Print signature headers (or a curl command) for a request",
		Long: `Signs a request with a client's secret and prints the four X-Shipyard-*
headers, or a ready curl command with --curl. Works offline. The secret
comes from --secret-file, --secret or $SHIPYARD_CLIENT_SECRET. Each run
uses a fresh nonce and timestamp, so the output is valid once and for 5
minutes.`,
		Example: `  shipyard fairway client sign --client promo --secret-file secret \
    --url http://127.0.0.1:9876/api/wa/history --body '{"chat":"Promo"}' --curl`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := deps.withDefaults()
			key, err := resolveSecret(secret, secretFile)
			if err != nil {
				return err
			}
			u, err := url.Parse(rawURL)
			if err != nil || u.Path == "" {
				return fmt.Errorf("--url must be a full URL with a path, e.g. http://127.0.0.1:9876/api/x")
			}
			payload := []byte(body)
			if bodyFile != "" {
				if payload, err = d.route.readFile(bodyFile); err != nil {
					return err
				}
			}
			h := signing.SignRequest(clientID, key, method, u.Path, u.RawQuery, payload, d.now())
			w := cmd.OutOrStdout()
			names := []string{signing.HeaderClient, signing.HeaderTimestamp, signing.HeaderNonce, signing.HeaderSignature}
			if !curl {
				for _, n := range names {
					fmt.Fprintf(w, "%s: %s\n", n, h[n])
				}
				return nil
			}
			parts := []string{"curl", "-sS", "-X", strings.ToUpper(method)}
			for _, n := range names {
				parts = append(parts, "-H", shellQuote(n+": "+h[n]))
			}
			if len(payload) > 0 {
				parts = append(parts, "-H", shellQuote("Content-Type: application/json"), "--data-binary", shellQuote(string(payload)))
			}
			parts = append(parts, shellQuote(rawURL))
			fmt.Fprintln(w, strings.Join(parts, " "))
			return nil
		},
	}
	cmd.Flags().StringVar(&clientID, "client", "", "Client id")
	cmd.Flags().StringVar(&secret, "secret", "", "Client secret (prefer --secret-file or $SHIPYARD_CLIENT_SECRET)")
	cmd.Flags().StringVar(&secretFile, "secret-file", "", "File holding the client secret")
	cmd.Flags().StringVar(&method, "method", "POST", "HTTP method")
	cmd.Flags().StringVar(&rawURL, "url", "", "Full request URL")
	cmd.Flags().StringVar(&body, "body", "", "Request body")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "Read the request body from a file")
	cmd.Flags().BoolVar(&curl, "curl", false, "Print a curl command instead of headers")
	_ = cmd.MarkFlagRequired("client")
	_ = cmd.MarkFlagRequired("url")
	return cmd
}

func resolveSecret(flag, file string) (string, error) {
	switch {
	case file != "":
		raw, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("read secret file: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	case flag != "":
		return flag, nil
	case os.Getenv("SHIPYARD_CLIENT_SECRET") != "":
		return os.Getenv("SHIPYARD_CLIENT_SECRET"), nil
	default:
		return "", errors.New("no secret: pass --secret-file, --secret or set SHIPYARD_CLIENT_SECRET")
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func writeIndentedJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
