package servicewiz

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	svcpkg "github.com/shipyard-auto/shipyard/internal/service"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/components"
	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type ServiceAPI interface {
	List() ([]svcpkg.ServiceRecord, error)
	Get(id string) (svcpkg.ServiceRecord, error)
	Add(input svcpkg.ServiceInput) (svcpkg.ServiceRecord, error)
	Update(id string, patch svcpkg.ServiceInput) (svcpkg.ServiceRecord, error)
	Delete(id string) error
	Enable(id string) (svcpkg.ServiceRecord, error)
	Disable(id string) (svcpkg.ServiceRecord, error)
	Start(id string) (svcpkg.ServiceRecord, error)
	Stop(id string) (svcpkg.ServiceRecord, error)
	Restart(id string) (svcpkg.ServiceRecord, error)
	Status(id string) (svcpkg.ServiceRecord, svcpkg.RuntimeStatus, error)
}

type Screen interface {
	Init() tea.Cmd
	Update(msg tea.Msg) (Screen, tea.Cmd)
	View() string
	Title() string
	Breadcrumb() []string
	Footer() []components.KeyHint
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

func validateWorkingDir(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if strings.Contains(value, "\n") {
		return errors.New("working dir must be a single line")
	}
	if !strings.HasPrefix(value, "/") {
		return errors.New("working dir must be an absolute path")
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

func compactCommand(command string) string {
	command = strings.TrimSpace(command)
	if len(command) <= 40 {
		return command
	}
	return command[:37] + "..."
}

func recordsToMenuItems(records []svcpkg.ServiceRecord) []components.MenuItem {
	items := make([]components.MenuItem, 0, len(records))
	for _, record := range records {
		items = append(items, components.MenuItem{
			Title:       fmt.Sprintf("%s  %s", record.ID, record.Name),
			Description: compactCommand(record.Command),
			Key:         record.ID,
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

func blankOrValue(v string) string {
	if strings.TrimSpace(v) == "" {
		return "(empty)"
	}
	return v
}

func boolLabel(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func envSummary(env map[string]string) string {
	if len(env) == 0 {
		return "(empty)"
	}
	keys := slices.Sorted(maps.Keys(env))
	items := make([]string, 0, len(keys))
	for _, key := range keys {
		items = append(items, key+"="+env[key])
	}
	return strings.Join(items, ", ")
}

func fallback(value, alt string) string {
	if strings.TrimSpace(value) == "" {
		return alt
	}
	return value
}

func parseEnvCSV(value string) (map[string]string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	out := map[string]string{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, item, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("invalid env entry: %s", part)
		}
		out[strings.TrimSpace(key)] = strings.TrimSpace(item)
	}
	return out, nil
}

func strptr(v string) *string {
	v = strings.TrimSpace(v)
	return &v
}

func boolptr(v bool) *bool { return &v }

func envptr(v map[string]string) *map[string]string {
	return &v
}
