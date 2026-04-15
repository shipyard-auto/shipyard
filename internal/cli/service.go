package cli

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"text/tabwriter"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	svcpkg "github.com/shipyard-auto/shipyard/internal/service"
	"github.com/shipyard-auto/shipyard/internal/ui"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/servicewiz"
	tuitype "github.com/shipyard-auto/shipyard/internal/ui/tui/tty"
)

func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Manage Shipyard-owned system services",
		Long: strings.Join([]string{
			"Create and manage Shipyard-owned user services stored in ~/.shipyard/services.json",
			"and projected into user-scoped system services on Linux and macOS.",
			"",
			"Shipyard only manages services it created itself. External units and agents are",
			"preserved and never imported automatically.",
		}, "\n"),
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}

	cmd.AddCommand(newServiceListCmd())
	cmd.AddCommand(newServiceShowCmd())
	cmd.AddCommand(newServiceAddCmd())
	cmd.AddCommand(newServiceUpdateCmd())
	cmd.AddCommand(newServiceDeleteCmd())
	cmd.AddCommand(newServiceEnableCmd())
	cmd.AddCommand(newServiceDisableCmd())
	cmd.AddCommand(newServiceStartCmd())
	cmd.AddCommand(newServiceStopCmd())
	cmd.AddCommand(newServiceRestartCmd())
	cmd.AddCommand(newServiceStatusCmd())
	cmd.AddCommand(newServiceConfigCmd())
	return cmd
}

func newServiceConfigCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Interactive service control panel",
		Long: strings.Join([]string{
			"Open the interactive Shipyard service control panel.",
			"",
			"Use this wizard for keyboard-driven service management. For scripting and CI,",
			"keep using the flag-based commands such as `shipyard service add`.",
		}, "\n"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := tuitype.RequireTTY(cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
				return err
			}
			service, err := svcpkg.NewService()
			if err != nil {
				return err
			}
			model, err := tea.NewProgram(servicewiz.NewRoot(service), tea.WithAltScreen()).Run()
			if err != nil {
				return err
			}
			if finished, ok := model.(*servicewiz.Root); ok && strings.TrimSpace(finished.Summary()) != "" {
				ui.Printf(cmd.OutOrStdout(), "%s\n", ui.Emphasis(finished.Summary()))
			}
			return nil
		},
	}
}

func newServiceListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List Shipyard services",
		Long:  "Render a table with Shipyard-managed services from ~/.shipyard/services.json and current runtime state.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			service, err := svcpkg.NewService()
			if err != nil {
				return err
			}
			records, err := service.List()
			if err != nil {
				return humanizeServiceError(err, "")
			}
			ui.Printf(cmd.OutOrStdout(), "%s\n", ui.SectionTitle("Shipyard Services"))
			ui.Printf(cmd.OutOrStdout(), "%s\n\n", ui.Muted("Managed services stored in ~/.shipyard/services.json and projected into your user service manager."))
			if len(records) == 0 {
				ui.Printf(cmd.OutOrStdout(), "%s\n", ui.Emphasis("No Shipyard services found."))
				ui.Printf(cmd.OutOrStdout(), "%s\n", ui.Muted("Create one with `shipyard service add --name ... --command ...`."))
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tSTATE\tENABLED\tCOMMAND")
			for _, record := range records {
				_, status, _ := service.Status(record.ID)
				state := "unknown"
				if status.State != "" {
					state = status.State
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", record.ID, record.Name, state, enabledLabel(record.Enabled), record.Command)
			}
			tw.Flush()
			return nil
		},
	}
}

func newServiceShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show one Shipyard service",
		Long:  "Display the full metadata and runtime status for a single Shipyard-managed service.",
		Args: func(cmd *cobra.Command, args []string) error {
			if wantsInlineHelp(args) {
				return nil
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if wantsInlineHelp(args) {
				return cmd.Help()
			}
			service, err := svcpkg.NewService()
			if err != nil {
				return err
			}
			record, status, err := service.Status(args[0])
			if err != nil {
				return humanizeServiceError(err, args[0])
			}
			renderServiceDetails(cmd.OutOrStdout(), record, status)
			return nil
		},
	}
}

func newServiceStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <id>",
		Short: "Show runtime status for a Shipyard service",
		Long:  "Display runtime status, PID, last exit code, and related metadata for a Shipyard-managed service.",
		Args: func(cmd *cobra.Command, args []string) error {
			if wantsInlineHelp(args) {
				return nil
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if wantsInlineHelp(args) {
				return cmd.Help()
			}
			service, err := svcpkg.NewService()
			if err != nil {
				return err
			}
			record, status, err := service.Status(args[0])
			if err != nil {
				return humanizeServiceError(err, args[0])
			}
			renderServiceDetails(cmd.OutOrStdout(), record, status)
			return nil
		},
	}
}

func newServiceAddCmd() *cobra.Command {
	var (
		name, description, command, workingDir, filePath string
		env                                                []string
		autoRestart, enabled                               bool
	)
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Create a Shipyard service",
		Long: strings.Join([]string{
			"Create a Shipyard-managed user service and sync it to the current platform service manager.",
			"",
			"Provide fields with flags for quick usage or pass --file to load a JSON definition from disk.",
		}, "\n"),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if wantsInlineHelp(args) {
				return cmd.Help()
			}
			if len(args) > 0 {
				return fmt.Errorf("unexpected argument: %s", args[0])
			}
			service, err := svcpkg.NewService()
			if err != nil {
				return err
			}
			input, err := buildServiceInput(cmd, filePath, svcpkg.ServiceInput{
				Name:        flagString(cmd, "name", name),
				Description: flagString(cmd, "description", description),
				Command:     flagString(cmd, "command", command),
				WorkingDir:  flagString(cmd, "working-dir", workingDir),
				Environment: flagEnv(cmd, "env", env),
				AutoRestart: flagBool(cmd, "auto-restart", autoRestart),
				Enabled:     flagBool(cmd, "enabled", enabled),
			})
			if err != nil {
				return err
			}
			record, err := service.Add(input)
			if err != nil {
				return humanizeServiceError(err, "")
			}
			ui.Printf(cmd.OutOrStdout(), "%s\n", ui.Emphasis("Shipyard service created."))
			renderServiceDetails(cmd.OutOrStdout(), record, svcpkg.RuntimeStatus{})
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Human-readable service name")
	cmd.Flags().StringVar(&description, "description", "", "Optional service description")
	cmd.Flags().StringVar(&command, "command", "", "Command to execute")
	cmd.Flags().StringVar(&workingDir, "working-dir", "", "Working directory for the service")
	cmd.Flags().StringArrayVar(&env, "env", nil, "Environment variables as KEY=VALUE")
	cmd.Flags().BoolVar(&autoRestart, "auto-restart", false, "Restart the service on failure")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "Enable the service at login")
	cmd.Flags().StringVar(&filePath, "file", "", "Load the service definition from a JSON file")
	return cmd
}

func newServiceUpdateCmd() *cobra.Command {
	var (
		name, description, command, workingDir, filePath string
		env                                                []string
		autoRestart, enabled                               bool
	)
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a Shipyard service",
		Long:  "Update a Shipyard-managed service and rewrite its managed system unit or launch agent.",
		Args: func(cmd *cobra.Command, args []string) error {
			if wantsInlineHelp(args) {
				return nil
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if wantsInlineHelp(args) {
				return cmd.Help()
			}
			service, err := svcpkg.NewService()
			if err != nil {
				return err
			}
			input, err := buildServiceInput(cmd, filePath, svcpkg.ServiceInput{
				Name:        flagString(cmd, "name", name),
				Description: flagString(cmd, "description", description),
				Command:     flagString(cmd, "command", command),
				WorkingDir:  flagString(cmd, "working-dir", workingDir),
				Environment: flagEnv(cmd, "env", env),
				AutoRestart: flagBool(cmd, "auto-restart", autoRestart),
				Enabled:     flagBool(cmd, "enabled", enabled),
			})
			if err != nil {
				return err
			}
			record, err := service.Update(args[0], input)
			if err != nil {
				return humanizeServiceError(err, args[0])
			}
			ui.Printf(cmd.OutOrStdout(), "%s\n", ui.Emphasis("Shipyard service updated."))
			renderServiceDetails(cmd.OutOrStdout(), record, svcpkg.RuntimeStatus{})
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Updated service name")
	cmd.Flags().StringVar(&description, "description", "", "Updated service description")
	cmd.Flags().StringVar(&command, "command", "", "Updated command to execute")
	cmd.Flags().StringVar(&workingDir, "working-dir", "", "Updated working directory")
	cmd.Flags().StringArrayVar(&env, "env", nil, "Updated environment variables as KEY=VALUE")
	cmd.Flags().BoolVar(&autoRestart, "auto-restart", false, "Restart the service on failure")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "Enable the service at login")
	cmd.Flags().StringVar(&filePath, "file", "", "Load patch values from a JSON file")
	return cmd
}

func newServiceDeleteCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a Shipyard service",
		Long:  "Remove a Shipyard-managed service from local state and from the current user's service manager.",
		Args: func(cmd *cobra.Command, args []string) error {
			if wantsInlineHelp(args) {
				return nil
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if wantsInlineHelp(args) {
				return cmd.Help()
			}
			service, err := svcpkg.NewService()
			if err != nil {
				return err
			}
			record, err := service.Get(args[0])
			if err != nil {
				return humanizeServiceError(err, args[0])
			}
			if !yes {
				renderServiceDetails(cmd.OutOrStdout(), record, svcpkg.RuntimeStatus{})
				return errors.New("re-run with --yes to confirm")
			}
			if err := service.Delete(args[0]); err != nil {
				return humanizeServiceError(err, args[0])
			}
			ui.Printf(cmd.OutOrStdout(), "%s %s\n", ui.Emphasis("Deleted Shipyard service"), ui.Highlight(strings.ToUpper(args[0])))
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm deletion")
	return cmd
}

func newServiceEnableCmd() *cobra.Command  { return newServiceLifecycleCmd("enable", "Enable a Shipyard service", "Enable a Shipyard-managed service at login.", func(s svcpkg.Service, id string) (svcpkg.ServiceRecord, error) { return s.Enable(id) }) }
func newServiceDisableCmd() *cobra.Command { return newServiceLifecycleCmd("disable", "Disable a Shipyard service", "Disable a Shipyard-managed service at login.", func(s svcpkg.Service, id string) (svcpkg.ServiceRecord, error) { return s.Disable(id) }) }
func newServiceStartCmd() *cobra.Command   { return newServiceLifecycleCmd("start", "Start a Shipyard service", "Start a Shipyard-managed service immediately.", func(s svcpkg.Service, id string) (svcpkg.ServiceRecord, error) { return s.Start(id) }) }
func newServiceStopCmd() *cobra.Command    { return newServiceLifecycleCmd("stop", "Stop a Shipyard service", "Stop a Shipyard-managed service immediately.", func(s svcpkg.Service, id string) (svcpkg.ServiceRecord, error) { return s.Stop(id) }) }
func newServiceRestartCmd() *cobra.Command { return newServiceLifecycleCmd("restart", "Restart a Shipyard service", "Restart a Shipyard-managed service immediately.", func(s svcpkg.Service, id string) (svcpkg.ServiceRecord, error) { return s.Restart(id) }) }

func newServiceLifecycleCmd(use, short, long string, action func(s svcpkg.Service, id string) (svcpkg.ServiceRecord, error)) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <id>",
		Short: short,
		Long:  long,
		Args: func(cmd *cobra.Command, args []string) error {
			if wantsInlineHelp(args) {
				return nil
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if wantsInlineHelp(args) {
				return cmd.Help()
			}
			service, err := svcpkg.NewService()
			if err != nil {
				return err
			}
			record, err := action(service, args[0])
			if err != nil {
				return humanizeServiceError(err, args[0])
			}
			ui.Printf(cmd.OutOrStdout(), "%s %s\n", ui.Emphasis(lifecyclePastTense(use)+" Shipyard service"), ui.Highlight(record.ID))
			return nil
		},
	}
}

func lifecyclePastTense(use string) string {
	switch use {
	case "start":
		return "Started"
	case "stop":
		return "Stopped"
	case "restart":
		return "Restarted"
	case "enable":
		return "Enabled"
	case "disable":
		return "Disabled"
	default:
		return "Updated"
	}
}

func buildServiceInput(cmd *cobra.Command, filePath string, input svcpkg.ServiceInput) (svcpkg.ServiceInput, error) {
	service, err := svcpkg.NewService()
	if err != nil {
		return svcpkg.ServiceInput{}, err
	}
	if filePath == "" {
		return input, nil
	}
	fileInput, err := service.LoadInputFile(filePath)
	if err != nil {
		return svcpkg.ServiceInput{}, err
	}
	return mergeServiceInputs(fileInput, input), nil
}

func mergeServiceInputs(base, override svcpkg.ServiceInput) svcpkg.ServiceInput {
	if override.Name != nil {
		base.Name = override.Name
	}
	if override.Description != nil {
		base.Description = override.Description
	}
	if override.Command != nil {
		base.Command = override.Command
	}
	if override.WorkingDir != nil {
		base.WorkingDir = override.WorkingDir
	}
	if override.Environment != nil {
		base.Environment = override.Environment
	}
	if override.AutoRestart != nil {
		base.AutoRestart = override.AutoRestart
	}
	if override.Enabled != nil {
		base.Enabled = override.Enabled
	}
	return base
}

func flagEnv(cmd *cobra.Command, name string, values []string) *map[string]string {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	out := make(map[string]string, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		key, item, ok := strings.Cut(value, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(key)] = strings.TrimSpace(item)
	}
	return &out
}

func renderServiceDetails(w io.Writer, record svcpkg.ServiceRecord, status svcpkg.RuntimeStatus) {
	ui.Printf(w, "\n%s %s\n", ui.Highlight("ID:"), record.ID)
	ui.Printf(w, "%s %s\n", ui.Highlight("Name:"), record.Name)
	if record.Description != "" {
		ui.Printf(w, "%s %s\n", ui.Highlight("Description:"), record.Description)
	}
	ui.Printf(w, "%s %s\n", ui.Highlight("Command:"), record.Command)
	if record.WorkingDir != "" {
		ui.Printf(w, "%s %s\n", ui.Highlight("Working Dir:"), record.WorkingDir)
	}
	if len(record.Environment) > 0 {
		keys := slices.Sorted(maps.Keys(record.Environment))
		items := make([]string, 0, len(keys))
		for _, key := range keys {
			items = append(items, key+"="+record.Environment[key])
		}
		ui.Printf(w, "%s %s\n", ui.Highlight("Environment:"), strings.Join(items, ", "))
	}
	ui.Printf(w, "%s %s\n", ui.Highlight("Auto Restart:"), enabledLabel(record.AutoRestart))
	ui.Printf(w, "%s %s\n", ui.Highlight("Enabled At Login:"), enabledLabel(record.Enabled))
	ui.Printf(w, "%s %s\n", ui.Highlight("Created:"), record.CreatedAt.Format("2006-01-02 15:04:05 MST"))
	ui.Printf(w, "%s %s\n", ui.Highlight("Updated:"), record.UpdatedAt.Format("2006-01-02 15:04:05 MST"))
	if status.State != "" || status.Raw != "" {
		ui.Printf(w, "%s %s\n", ui.Highlight("State:"), fallback(status.State, "unknown"))
		if status.SubState != "" {
			ui.Printf(w, "%s %s\n", ui.Highlight("SubState:"), status.SubState)
		}
		if status.PID != 0 {
			ui.Printf(w, "%s %d\n", ui.Highlight("PID:"), status.PID)
		}
		if status.EnabledAt != "" {
			ui.Printf(w, "%s %s\n", ui.Highlight("EnabledAt:"), status.EnabledAt)
		}
		if status.SinceHint != "" {
			ui.Printf(w, "%s %s\n", ui.Highlight("Since:"), status.SinceHint)
		}
		if status.LastExit != 0 {
			ui.Printf(w, "%s %d\n", ui.Highlight("Last Exit:"), status.LastExit)
		}
	}
}

func fallback(value, alt string) string {
	if strings.TrimSpace(value) == "" {
		return alt
	}
	return value
}

func humanizeServiceError(err error, id string) error {
	if errors.Is(err, svcpkg.ErrServiceNotFound) {
		if id == "" {
			return errors.New("Shipyard service not found")
		}
		return fmt.Errorf("Shipyard service %s was not found", strings.ToUpper(id))
	}
	return err
}
