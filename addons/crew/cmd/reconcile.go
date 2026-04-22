package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/agent"
	"github.com/shipyard-auto/shipyard/addons/crew/internal/crew/trigger"
)

// Exit codes for the `reconcile` subcommand (public contract — mirrored in
// task 28a). Kept distinct from the addon's general exit codes because the
// semantics differ: `reconcile` returns 1 for yaml problems and 2 for
// integration failures with shipyard cron / shipyard fairway.
const (
	ExitReconcileOK       = 0
	ExitReconcileNotFound = 1
	ExitReconcileError    = 2
)

type reconcileRequest struct {
	AgentName string
	AgentDir  string
	Home      string
	DryRun    bool
	JSON      bool
	Stdout    io.Writer
	Stderr    io.Writer
}

// runReconcileMode parses the `reconcile` subcommand flags and dispatches
// to the injectable handler. Separate from `run` because reconcile owns a
// distinct exit-code table.
func runReconcileMode(parent context.Context, deps runtimeDeps, args []string) int {
	fs := flag.NewFlagSet("shipyard-crew reconcile", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var (
		agentName string
		dryRun    bool
		jsonOut   bool
	)
	fs.StringVar(&agentName, "agent", "", "agent name (required)")
	fs.BoolVar(&dryRun, "dry-run", false, "compute diff without applying")
	fs.BoolVar(&jsonOut, "json", false, "emit JSON envelope instead of human output")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(deps.Stderr, "shipyard-crew reconcile: %s\n", err)
		return ExitReconcileError
	}

	if !agentNameRe.MatchString(agentName) {
		fmt.Fprintln(deps.Stderr, "shipyard-crew reconcile: invalid --agent: must match ^[a-z0-9][a-z0-9_-]{0,62}$")
		return ExitReconcileError
	}

	home := deps.Env("SHIPYARD_HOME")
	if home == "" {
		u, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(deps.Stderr, "shipyard-crew reconcile: %s\n", err)
			return ExitReconcileError
		}
		home = filepath.Join(u, ".shipyard")
	}

	req := reconcileRequest{
		AgentName: agentName,
		AgentDir:  filepath.Join(home, "crew", agentName),
		Home:      home,
		DryRun:    dryRun,
		JSON:      jsonOut,
		Stdout:    deps.Stdout,
		Stderr:    deps.Stderr,
	}

	sigCtx, cancel := deps.SignalCtx(parent)
	defer cancel()

	code, err := deps.RunReconcile(sigCtx, req)
	if err != nil {
		fmt.Fprintf(deps.Stderr, "shipyard-crew reconcile: %s\n", err)
	}
	return code
}

// reconcileEnvelope matches the JSON contract documented in task 28a:
//
//	{ "agent": "...", "cron": {added,removed,unchanged},
//	  "webhooks": {added,removed,unchanged} }
type reconcileEnvelope struct {
	Agent    string              `json:"agent"`
	DryRun   bool                `json:"dry_run,omitempty"`
	Cron     trigger.CronDiff    `json:"cron"`
	Webhooks trigger.WebhookDiff `json:"webhooks"`
}

// defaultRunReconcile is the production reconcile handler. It loads the
// agent, invokes the two reconcilers (cron + webhook), serialises the diff
// envelope, and maps errors to the public exit codes.
func defaultRunReconcile(ctx context.Context, req reconcileRequest) (int, error) {
	if _, err := os.Stat(req.AgentDir); err != nil {
		if os.IsNotExist(err) {
			return ExitReconcileNotFound, fmt.Errorf("agent %q not found at %s", req.AgentName, req.AgentDir)
		}
		return ExitReconcileNotFound, fmt.Errorf("stat %s: %w", req.AgentDir, err)
	}

	a, err := agent.Load(req.AgentDir)
	if err != nil {
		return ExitReconcileNotFound, fmt.Errorf("load agent: %w", err)
	}
	if a.Name != req.AgentName {
		return ExitReconcileNotFound, fmt.Errorf("agent name mismatch: agent.yaml=%q --agent=%q", a.Name, req.AgentName)
	}

	cronReconciler := trigger.NewCronReconciler(nil)
	webhookReconciler := trigger.NewWebhookReconciler(nil, req.Home)

	var (
		cronDiff trigger.CronDiff
		webDiff  trigger.WebhookDiff
	)
	if req.DryRun {
		cronDiff, err = cronReconciler.Plan(ctx, a)
		if err != nil {
			return ExitReconcileError, fmt.Errorf("cron plan: %w", err)
		}
		webDiff, err = webhookReconciler.Plan(ctx, a)
		if err != nil {
			return ExitReconcileError, fmt.Errorf("webhook plan: %w", err)
		}
	} else {
		cronDiff, err = cronReconciler.Reconcile(ctx, a)
		if err != nil {
			return ExitReconcileError, fmt.Errorf("cron reconcile: %w", err)
		}
		webDiff, err = webhookReconciler.Reconcile(ctx, a)
		if err != nil {
			return ExitReconcileError, fmt.Errorf("webhook reconcile: %w", err)
		}
	}

	env := reconcileEnvelope{
		Agent:    a.Name,
		DryRun:   req.DryRun,
		Cron:     cronDiff,
		Webhooks: webDiff,
	}
	if err := writeReconcileResult(req.Stdout, env, req.JSON); err != nil {
		return ExitReconcileError, fmt.Errorf("write result: %w", err)
	}
	return ExitReconcileOK, nil
}

// writeReconcileResult renders env either as the documented JSON envelope
// or as a terse human summary. Both shapes always end with a newline so
// shell pipelines behave predictably.
func writeReconcileResult(w io.Writer, env reconcileEnvelope, asJSON bool) error {
	if asJSON {
		data, err := json.MarshalIndent(env, "", "  ")
		if err != nil {
			return err
		}
		data = append(data, '\n')
		_, err = w.Write(data)
		return err
	}

	// Human-readable summary.
	label := "Reconciled"
	if env.DryRun {
		label = "Dry-run"
	}
	fmt.Fprintf(w, "%s %q:\n", label, env.Agent)
	fmt.Fprintf(w, "  cron:    +%d -%d =%d\n",
		len(env.Cron.Added), len(env.Cron.Removed), len(env.Cron.Unchanged))
	for _, c := range env.Cron.Added {
		fmt.Fprintf(w, "    + %s  schedule=%q", c.Name, c.Schedule)
		if c.ID != "" {
			fmt.Fprintf(w, "  id=%s", c.ID)
		}
		fmt.Fprintln(w)
	}
	for _, c := range env.Cron.Removed {
		fmt.Fprintf(w, "    - %s  id=%s\n", c.Name, c.ID)
	}
	fmt.Fprintf(w, "  webhook: +%d -%d =%d\n",
		len(env.Webhooks.Added), len(env.Webhooks.Removed), len(env.Webhooks.Unchanged))
	for _, c := range env.Webhooks.Added {
		fmt.Fprintf(w, "    + %s\n", c.Route)
	}
	for _, c := range env.Webhooks.Removed {
		fmt.Fprintf(w, "    - %s\n", c.Route)
	}
	return nil
}
