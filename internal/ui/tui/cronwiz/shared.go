package cronwiz

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/cron"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type CronService interface {
	List() ([]cron.Job, error)
	Get(id string) (cron.Job, error)
	Add(input cron.JobInput) (cron.Job, error)
	Update(id string, patch cron.JobInput) (cron.Job, error)
	Enable(id string) (cron.Job, error)
	Disable(id string) (cron.Job, error)
	Run(id string) (cron.Job, string, error)
	Delete(id string) error
}

type Screen interface {
	Init() tea.Cmd
	Update(msg tea.Msg) (Screen, tea.Cmd)
	View() string
	Title() string
	Breadcrumb() []string
	Footer() []components.KeyHint
}

type schedulePreset struct {
	Title      string
	Expression string
	Summary    string
}

var schedulePresets = []schedulePreset{
	{Title: "Every minute", Expression: "* * * * *", Summary: "Runs every minute"},
	{Title: "Every 5 minutes", Expression: "*/5 * * * *", Summary: "Runs every 5 minutes"},
	{Title: "Every hour", Expression: "0 * * * *", Summary: "Runs every hour"},
	{Title: "Every day at 00:00", Expression: "0 0 * * *", Summary: "Runs every day at 00:00"},
	{Title: "Every Monday at 09:00", Expression: "0 9 * * 1", Summary: "Runs every Monday at 09:00"},
}

func scheduleSummary(expr string) string {
	expr = strings.TrimSpace(expr)
	for _, preset := range schedulePresets {
		if preset.Expression == expr {
			return preset.Summary
		}
	}
	if expr == "" {
		return "No schedule selected"
	}
	return expr + " (custom)"
}

func validateSingleLineRequired(field, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if strings.Contains(value, "\n") {
		return fmt.Errorf("%s must be a single line", field)
	}
	return nil
}

func validateOptionalSingleLine(_ string, value string) error {
	if strings.Contains(value, "\n") {
		return errors.New("value must be a single line")
	}
	return nil
}

func validateSchedule(expr string) error {
	expr = strings.TrimSpace(expr)
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return errors.New("schedule must have exactly 5 fields")
	}
	return nil
}

func dangerousCommandWarning(command string) string {
	command = strings.TrimSpace(command)
	if strings.HasPrefix(command, "sudo ") || strings.Contains(command, "rm -rf ") {
		return "WARNING: this command uses sudo or rm -rf. Review it carefully."
	}
	return ""
}

func strptr(v string) *string {
	v = strings.TrimSpace(v)
	return &v
}

func boolptr(v bool) *bool { return &v }

func compactCommand(command string) string {
	command = strings.TrimSpace(command)
	if len(command) <= 40 {
		return command
	}
	return command[:37] + "..."
}

func jobsToMenuItems(jobs []cron.Job) []components.MenuItem {
	items := make([]components.MenuItem, 0, len(jobs))
	for _, job := range jobs {
		items = append(items, components.MenuItem{
			Title:       fmt.Sprintf("%s  %s", job.ID, job.Name),
			Description: fmt.Sprintf("%s  %s", job.Schedule, compactCommand(job.Command)),
			Key:         job.ID,
		})
	}
	return items
}

func renderReview(th theme.Theme, title string, lines [][2]string, changed map[string]bool) string {
	out := []string{th.ValueStyle.Render(title)}
	for _, line := range lines {
		label := th.LabelStyle.Render(line[0] + ":")
		valueStyle := th.ValueStyle
		if changed != nil && !changed[line[0]] {
			valueStyle = th.SubtitleStyle
		}
		out = append(out, label+" "+valueStyle.Render(line[1]))
	}
	return strings.Join(out, "\n")
}
