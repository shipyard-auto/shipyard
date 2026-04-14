package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/shipyard-auto/shipyard/internal/cron"
	"github.com/shipyard-auto/shipyard/internal/ui"
)

func newCronCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cron",
		Short: "Manage Shipyard-owned cron jobs",
		Long: strings.Join([]string{
			"Create and manage Shipyard-owned cron jobs stored in ~/.shipyard/crons.json",
			"and synchronized to the current user's crontab.",
			"",
			"Shipyard only manages jobs it created itself. External cron entries are",
			"preserved and never imported automatically.",
		}, "\n"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(newCronListCmd())
	cmd.AddCommand(newCronShowCmd())
	cmd.AddCommand(newCronAddCmd())
	cmd.AddCommand(newCronUpdateCmd())
	cmd.AddCommand(newCronDeleteCmd())

	return cmd
}

func newCronListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List Shipyard cron jobs",
		Long:  "Render a table with the Shipyard-managed cron jobs known in ~/.shipyard/crons.json.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCronList(cmd)
		},
	}
}

func newCronShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show one Shipyard cron job",
		Long:  "Display the full metadata for a single Shipyard-managed cron job.",
		Example: strings.Join([]string{
			"shipyard cron show AB12CD",
		}, "\n"),
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

			service, err := cron.NewService()
			if err != nil {
				return err
			}

			job, err := service.Get(args[0])
			if err != nil {
				return humanizeCronError(err, args[0])
			}

			renderCronDetails(cmd.OutOrStdout(), job)
			return nil
		},
	}
}

func newCronAddCmd() *cobra.Command {
	var (
		name        string
		description string
		schedule    string
		command     string
		enabled     bool
		filePath    string
	)

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Create a Shipyard cron job",
		Long: strings.Join([]string{
			"Create a Shipyard-managed cron job and sync it to the current user's crontab.",
			"",
			"Provide fields with flags for quick usage or pass --file to load a JSON",
			"definition from disk.",
		}, "\n"),
		Example: strings.Join([]string{
			"shipyard cron add --name \"Backup\" --schedule \"0 * * * *\" --command \"/usr/local/bin/backup-home\"",
			"shipyard cron add --file ./backup-cron.json",
		}, "\n"),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if wantsInlineHelp(args) {
				return cmd.Help()
			}
			if len(args) > 0 {
				return fmt.Errorf("unexpected argument: %s", args[0])
			}

			service, err := cron.NewService()
			if err != nil {
				return err
			}

			input, err := buildCronInput(cmd, filePath, cron.JobInput{
				Name:        flagString(cmd, "name", name),
				Description: flagString(cmd, "description", description),
				Schedule:    flagString(cmd, "schedule", schedule),
				Command:     flagString(cmd, "command", command),
				Enabled:     flagBool(cmd, "enabled", enabled),
			})
			if err != nil {
				return err
			}

			job, err := service.Add(input)
			if err != nil {
				return humanizeCronError(err, "")
			}

			ui.Printf(cmd.OutOrStdout(), "%s\n", ui.Emphasis("Shipyard cron job created."))
			renderCronDetails(cmd.OutOrStdout(), job)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Human-readable job name")
	cmd.Flags().StringVar(&description, "description", "", "Optional job description")
	cmd.Flags().StringVar(&schedule, "schedule", "", "Cron schedule expression")
	cmd.Flags().StringVar(&command, "command", "", "Command to execute")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "Enable the job immediately")
	cmd.Flags().StringVar(&filePath, "file", "", "Load the job definition from a JSON file")

	return cmd
}

func newCronUpdateCmd() *cobra.Command {
	var (
		name        string
		description string
		schedule    string
		command     string
		enabled     bool
		filePath    string
	)

	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a Shipyard cron job",
		Long: strings.Join([]string{
			"Update one Shipyard-managed cron job and rewrite the managed cron block",
			"in the current user's crontab.",
		}, "\n"),
		Example: strings.Join([]string{
			"shipyard cron update AB12CD --schedule \"*/15 * * * *\"",
			"shipyard cron update AB12CD --enabled=false",
			"shipyard cron update AB12CD --file ./patch.json",
		}, "\n"),
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

			service, err := cron.NewService()
			if err != nil {
				return err
			}

			input, err := buildCronInput(cmd, filePath, cron.JobInput{
				Name:        flagString(cmd, "name", name),
				Description: flagString(cmd, "description", description),
				Schedule:    flagString(cmd, "schedule", schedule),
				Command:     flagString(cmd, "command", command),
				Enabled:     flagBool(cmd, "enabled", enabled),
			})
			if err != nil {
				return err
			}

			job, err := service.Update(args[0], input)
			if err != nil {
				return humanizeCronError(err, args[0])
			}

			ui.Printf(cmd.OutOrStdout(), "%s\n", ui.Emphasis("Shipyard cron job updated."))
			renderCronDetails(cmd.OutOrStdout(), job)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Updated human-readable job name")
	cmd.Flags().StringVar(&description, "description", "", "Updated job description")
	cmd.Flags().StringVar(&schedule, "schedule", "", "Updated cron schedule expression")
	cmd.Flags().StringVar(&command, "command", "", "Updated command to execute")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "Enable the job")
	cmd.Flags().StringVar(&filePath, "file", "", "Load patch values from a JSON file")

	return cmd
}

func newCronDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <id>",
		Short:   "Delete a Shipyard cron job",
		Long:    "Remove a Shipyard-managed cron job from the local store and from the current user's crontab.",
		Example: "shipyard cron delete AB12CD",
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

			service, err := cron.NewService()
			if err != nil {
				return err
			}

			if err := service.Delete(args[0]); err != nil {
				return humanizeCronError(err, args[0])
			}

			ui.Printf(cmd.OutOrStdout(), "%s %s\n", ui.Emphasis("Deleted Shipyard cron job"), ui.Highlight(strings.ToUpper(args[0])))
			return nil
		},
	}
}

func runCronList(cmd *cobra.Command) error {
	service, err := cron.NewService()
	if err != nil {
		return err
	}

	jobs, err := service.List()
	if err != nil {
		return humanizeCronError(err, "")
	}

	ui.Printf(cmd.OutOrStdout(), "%s\n", ui.SectionTitle("Shipyard Cron Jobs"))
	ui.Printf(cmd.OutOrStdout(), "%s\n\n", ui.Muted("Managed jobs stored in ~/.shipyard/crons.json and synced to your user crontab."))

	if len(jobs) == 0 {
		ui.Printf(cmd.OutOrStdout(), "%s\n", ui.Emphasis("No Shipyard cron jobs found."))
		ui.Printf(cmd.OutOrStdout(), "%s\n", ui.Muted("Create one with `shipyard cron add --name ... --schedule ... --command ...`."))
		return nil
	}

	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tName\tSchedule\tEnabled\tCommand")
	for _, job := range jobs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", job.ID, job.Name, job.Schedule, enabledLabel(job.Enabled), job.Command)
	}
	tw.Flush()
	return nil
}

func renderCronDetails(w io.Writer, job cron.Job) {
	ui.Printf(w, "\n%s %s\n", ui.Highlight("ID:"), job.ID)
	ui.Printf(w, "%s %s\n", ui.Highlight("Name:"), job.Name)
	if job.Description != "" {
		ui.Printf(w, "%s %s\n", ui.Highlight("Description:"), job.Description)
	}
	ui.Printf(w, "%s %s\n", ui.Highlight("Schedule:"), job.Schedule)
	ui.Printf(w, "%s %s\n", ui.Highlight("Command:"), job.Command)
	ui.Printf(w, "%s %s\n", ui.Highlight("Enabled:"), enabledLabel(job.Enabled))
	ui.Printf(w, "%s %s\n", ui.Highlight("Created:"), job.CreatedAt.Format("2006-01-02 15:04:05 MST"))
	ui.Printf(w, "%s %s\n", ui.Highlight("Updated:"), job.UpdatedAt.Format("2006-01-02 15:04:05 MST"))
}

func buildCronInput(cmd *cobra.Command, filePath string, input cron.JobInput) (cron.JobInput, error) {
	service, err := cron.NewService()
	if err != nil {
		return cron.JobInput{}, err
	}

	if filePath == "" {
		return input, nil
	}

	fileInput, err := service.LoadInputFile(filePath)
	if err != nil {
		return cron.JobInput{}, err
	}

	return mergeCronInputs(fileInput, input), nil
}

func mergeCronInputs(base, override cron.JobInput) cron.JobInput {
	if override.Name != nil {
		base.Name = override.Name
	}
	if override.Description != nil {
		base.Description = override.Description
	}
	if override.Schedule != nil {
		base.Schedule = override.Schedule
	}
	if override.Command != nil {
		base.Command = override.Command
	}
	if override.Enabled != nil {
		base.Enabled = override.Enabled
	}
	return base
}

func flagString(cmd *cobra.Command, name, value string) *string {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	v := value
	return &v
}

func flagBool(cmd *cobra.Command, name string, value bool) *bool {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	v := value
	return &v
}

func enabledLabel(enabled bool) string {
	if enabled {
		return "yes"
	}
	return "no"
}

func humanizeCronError(err error, id string) error {
	if errors.Is(err, cron.ErrJobNotFound) {
		if id == "" {
			return errors.New("Shipyard cron job not found")
		}
		return fmt.Errorf("Shipyard cron job %s was not found", strings.ToUpper(id))
	}
	return err
}

func wantsInlineHelp(args []string) bool {
	return len(args) == 1 && strings.EqualFold(args[0], "help")
}
