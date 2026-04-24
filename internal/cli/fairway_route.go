package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/addon"
	"github.com/shipyard-auto/shipyard/internal/app"
	"github.com/shipyard-auto/shipyard/internal/fairwayctl"
	"github.com/shipyard-auto/shipyard/internal/ui"
	tuitype "github.com/shipyard-auto/shipyard/internal/ui/tui/tty"
)

type routeClient interface {
	Close() error
	RouteList(ctx context.Context) ([]fairwayctl.Route, error)
	RouteAdd(ctx context.Context, route fairwayctl.Route) error
	RouteDelete(ctx context.Context, path string) error
	RouteTest(ctx context.Context, path, method string, body []byte, headers map[string]string) (fairwayctl.TestResult, error)
}

type routeDeps struct {
	version       string
	socketPath    string
	dial          func(context.Context, fairwayctl.Opts) (routeClient, error)
	readFile      func(string) ([]byte, error)
	stdin         io.Reader
	stdinFD       func() uintptr
	isInteractive func(uintptr) bool
}

type addRouteInput struct {
	path       string
	authType   string
	authToken  string
	authValue  string
	authHeader string
	authQuery  string
	actionType string
	target     string
	provider   string
	url        string
	method     string
	timeout    string
	async      bool
	fromFile   string
}

func newFairwayRouteCmd() *cobra.Command {
	return newFairwayRouteCmdWith(routeDeps{})
}

func newFairwayRouteCmdWith(deps routeDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "route",
		Short: "Manage fairway routes through the daemon socket",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newFairwayRouteListCmdWith(deps))
	cmd.AddCommand(newFairwayRouteAddCmdWith(deps))
	cmd.AddCommand(newFairwayRouteDeleteCmdWith(deps))
	cmd.AddCommand(newFairwayRouteTestCmdWith(deps))
	return cmd
}

func newFairwayRouteListCmdWith(deps routeDeps) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:           "list",
		Short:         "List fairway routes",
		SilenceUsage:  true,
		SilenceErrors: true,
		PreRunE:       addon.RequirePreRun(addon.KindFairway),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := dialRouteClient(cmd.Context(), deps)
			if err != nil {
				return err
			}
			defer client.Close() //nolint:errcheck

			routes, err := client.RouteList(cmd.Context())
			if err != nil {
				return err
			}

			if jsonOutput {
				return renderRouteListJSON(cmd.OutOrStdout(), routes)
			}
			renderRouteListHuman(cmd.OutOrStdout(), routes)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print routes as JSON")
	return cmd
}

func newFairwayRouteAddCmdWith(deps routeDeps) *cobra.Command {
	input := &addRouteInput{}
	cmd := &cobra.Command{
		Use:           "add",
		Short:         "Add a new fairway route",
		SilenceUsage:  true,
		SilenceErrors: true,
		PreRunE:       addon.RequirePreRun(addon.KindFairway),
		RunE: func(cmd *cobra.Command, args []string) error {
			route, err := buildRouteFromAddInput(*input, deps)
			if err != nil {
				return err
			}
			if err := route.Validate(); err != nil {
				return fmt.Errorf("invalid route: %w", err)
			}

			client, err := dialRouteClient(cmd.Context(), deps)
			if err != nil {
				return err
			}
			defer client.Close() //nolint:errcheck

			if err := client.RouteAdd(cmd.Context(), route); err != nil {
				if errors.Is(err, fairwayctl.ErrDuplicatePath) {
					return fmt.Errorf("route %q already exists; run 'shipyard fairway route list' to see existing paths", route.Path)
				}
				return err
			}

			ui.Printf(cmd.OutOrStdout(), "%s %s\n", ui.Emphasis("Added fairway route"), ui.Highlight(route.Path))
			return nil
		},
	}

	cmd.Flags().StringVar(&input.path, "path", "", "Route path (must start with /)")
	cmd.Flags().StringVar(&input.authType, "auth", "", "Auth type: bearer, token, local-only")
	cmd.Flags().StringVar(&input.authToken, "auth-token", "", "Bearer token secret")
	cmd.Flags().StringVar(&input.authValue, "auth-value", "", "Token auth expected value")
	cmd.Flags().StringVar(&input.authHeader, "auth-header", "", "Token auth header name")
	cmd.Flags().StringVar(&input.authQuery, "auth-query", "", "Token auth query parameter name")
	cmd.Flags().StringVar(&input.actionType, "action", "", "Action type: cron.run, service.restart, message.send, telegram.handle, http.forward, crew.run, ...")
	cmd.Flags().StringVar(&input.target, "target", "", "Action target")
	cmd.Flags().StringVar(&input.provider, "provider", "", "Optional provider name for message.send")
	cmd.Flags().StringVar(&input.url, "url", "", "Destination URL for http.forward")
	cmd.Flags().StringVar(&input.method, "method", "", "HTTP method for http.forward")
	cmd.Flags().StringVar(&input.timeout, "timeout", "", "Per-route timeout (e.g. 30s)")
	cmd.Flags().BoolVar(&input.async, "async", false, "Respond 202 Accepted immediately and run the action detached in the background")
	cmd.Flags().StringVar(&input.fromFile, "from-file", "", "Load route definition from JSON file")
	return cmd
}

func newFairwayRouteDeleteCmdWith(deps routeDeps) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:           "delete <path>",
		Short:         "Delete a fairway route",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		PreRunE:       addon.RequirePreRun(addon.KindFairway),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			if !yes {
				if deps.withDefaults().isInteractive(deps.withDefaults().stdinFD()) {
					ok, err := confirmDeleteRoute(cmd.OutOrStdout(), deps.withDefaults().stdin, path)
					if err != nil {
						return err
					}
					if !ok {
						ui.Printf(cmd.OutOrStdout(), "Deletion cancelled.\n")
						return nil
					}
				} else {
					return errors.New("re-run with --yes to confirm deletion in non-interactive mode")
				}
			}

			client, err := dialRouteClient(cmd.Context(), deps)
			if err != nil {
				return err
			}
			defer client.Close() //nolint:errcheck

			if err := client.RouteDelete(cmd.Context(), path); err != nil {
				if errors.Is(err, fairwayctl.ErrRouteNotFound) {
					return fmt.Errorf("route %q was not found; run 'shipyard fairway route list' to see existing paths", path)
				}
				return err
			}

			ui.Printf(cmd.OutOrStdout(), "%s %s\n", ui.Emphasis("Deleted fairway route"), ui.Highlight(path))
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm deletion without prompting")
	return cmd
}

func newFairwayRouteTestCmdWith(deps routeDeps) *cobra.Command {
	var method string
	var bodyFile string
	var headerFlags []string

	cmd := &cobra.Command{
		Use:           "test <path>",
		Short:         "Test a fairway route through the daemon",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		PreRunE:       addon.RequirePreRun(addon.KindFairway),
		RunE: func(cmd *cobra.Command, args []string) error {
			headers, err := parseHeaders(headerFlags)
			if err != nil {
				return err
			}

			var body []byte
			if bodyFile != "" {
				body, err = deps.withDefaults().readFile(bodyFile)
				if err != nil {
					return fmt.Errorf("read body file %s: %w", bodyFile, err)
				}
			}

			client, err := dialRouteClient(cmd.Context(), deps)
			if err != nil {
				return err
			}
			defer client.Close() //nolint:errcheck

			result, err := client.RouteTest(cmd.Context(), args[0], method, body, headers)
			if err != nil {
				return err
			}

			renderRouteTestResult(cmd.OutOrStdout(), result)
			return nil
		},
	}

	cmd.Flags().StringVar(&method, "method", "POST", "HTTP method to simulate")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "Read request body from file")
	cmd.Flags().StringArrayVar(&headerFlags, "header", nil, "Extra request header in KEY=VALUE form")
	return cmd
}

func (d routeDeps) withDefaults() routeDeps {
	if d.version == "" {
		d.version = app.Version
	}
	if d.socketPath == "" {
		if homeDir, err := os.UserHomeDir(); err == nil {
			d.socketPath = filepath.Join(homeDir, ".shipyard", "run", "fairway.sock")
		}
	}
	if d.dial == nil {
		d.dial = func(ctx context.Context, opts fairwayctl.Opts) (routeClient, error) {
			return fairwayctl.Dial(ctx, opts)
		}
	}
	if d.readFile == nil {
		d.readFile = os.ReadFile
	}
	if d.stdin == nil {
		d.stdin = os.Stdin
	}
	if d.stdinFD == nil {
		d.stdinFD = tuitype.StdinFD
	}
	if d.isInteractive == nil {
		d.isInteractive = tuitype.IsInteractive
	}
	return d
}

func dialRouteClient(ctx context.Context, deps routeDeps) (routeClient, error) {
	deps = deps.withDefaults()
	return deps.dial(ctx, fairwayctl.Opts{
		SocketPath: deps.socketPath,
		Version:    deps.version,
	})
}

func buildRouteFromAddInput(input addRouteInput, deps routeDeps) (fairwayctl.Route, error) {
	deps = deps.withDefaults()
	if input.fromFile != "" {
		data, err := deps.readFile(input.fromFile)
		if err != nil {
			return fairwayctl.Route{}, fmt.Errorf("read route file %s: %w", input.fromFile, err)
		}
		var route fairwayctl.Route
		if err := json.Unmarshal(data, &route); err != nil {
			return fairwayctl.Route{}, fmt.Errorf("parse route file %s: %w", input.fromFile, err)
		}
		return route, nil
	}

	if strings.TrimSpace(input.path) == "" {
		return fairwayctl.Route{}, errors.New("missing required flag --path")
	}

	route := fairwayctl.Route{
		Path: input.path,
		Auth: fairwayctl.Auth{
			Type:   fairwayctl.AuthType(input.authType),
			Token:  input.authToken,
			Value:  input.authValue,
			Header: input.authHeader,
			Query:  input.authQuery,
		},
		Action: fairwayctl.Action{
			Type:     fairwayctl.ActionType(input.actionType),
			Target:   input.target,
			Provider: input.provider,
			URL:      input.url,
			Method:   input.method,
		},
		Async: input.async,
	}

	if input.timeout != "" {
		timeout, err := time.ParseDuration(input.timeout)
		if err != nil {
			return fairwayctl.Route{}, fmt.Errorf("invalid --timeout: %w", err)
		}
		route.Timeout = timeout
	}

	return route, nil
}

func renderRouteListHuman(w io.Writer, routes []fairwayctl.Route) {
	if len(routes) == 0 {
		ui.Printf(w, "%s\n", ui.Muted("No fairway routes configured."))
		return
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Path < routes[j].Path })
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PATH\tAUTH\tACTION\tTIMEOUT\tMODE")
	for _, route := range routes {
		timeout := "-"
		if route.Timeout > 0 {
			timeout = route.Timeout.String()
		}
		mode := "sync"
		if route.Async {
			mode = "async"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", route.Path, route.Auth.Type, formatRouteAction(route.Action), timeout, mode)
	}
	_ = tw.Flush()
}

func renderRouteListJSON(w io.Writer, routes []fairwayctl.Route) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(routes)
}

func parseHeaders(values []string) (map[string]string, error) {
	headers := make(map[string]string, len(values))
	for _, raw := range values {
		key, value, ok := strings.Cut(raw, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("invalid --header %q; expected KEY=VALUE", raw)
		}
		headers[strings.TrimSpace(key)] = value
	}
	return headers, nil
}

func renderRouteTestResult(w io.Writer, result fairwayctl.TestResult) {
	statusLabel := ui.Highlight(fmt.Sprintf("Status: %d", result.Status))
	switch {
	case result.Status >= 200 && result.Status < 300:
		statusLabel = ui.Paint(fmt.Sprintf("Status: %d", result.Status), ui.StyleBold, ui.StyleGreen)
	case result.Status >= 400 && result.Status < 500:
		statusLabel = ui.Paint(fmt.Sprintf("Status: %d", result.Status), ui.StyleBold, ui.StyleYellow)
	case result.Status >= 500:
		statusLabel = ui.Paint(fmt.Sprintf("Status: %d", result.Status), ui.StyleBold, ui.StyleRed)
	}
	ui.Printf(w, "%s\n", statusLabel)
	ui.Printf(w, "%s\n", result.Body)
}

func confirmDeleteRoute(out io.Writer, in io.Reader, path string) (bool, error) {
	if _, err := fmt.Fprintf(out, "Confirma remover %s? [y/N]: ", path); err != nil {
		return false, fmt.Errorf("write confirmation prompt: %w", err)
	}
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil {
		return false, fmt.Errorf("read confirmation prompt: %w", err)
	}
	answer = strings.TrimSpace(strings.ToLower(answer))
	return answer == "y" || answer == "yes", nil
}
