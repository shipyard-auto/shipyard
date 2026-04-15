package components

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type FormCompletedMsg struct {
	Values map[string]any
}

type FormCancelledMsg struct{}

type FormStep struct {
	ID        string
	Label     string
	Field     *Input
	Validator func(value string) error
}

type Form struct {
	theme  theme.Theme
	steps  []FormStep
	index  int
	values map[string]any
	width  int
}

func NewForm(th theme.Theme, steps []FormStep) Form {
	return Form{theme: th, steps: steps, values: map[string]any{}}
}

func (f Form) Init() tea.Cmd {
	if len(f.steps) == 0 || f.steps[0].Field == nil {
		return nil
	}
	return f.steps[0].Field.Init()
}

func (f *Form) Resize(width, height int) {
	f.width = width
	for i := range f.steps {
		if f.steps[i].Field != nil {
			f.steps[i].Field.Resize(width, height)
		}
	}
}

func (f Form) Update(msg tea.Msg) (Form, tea.Cmd) {
	if len(f.steps) == 0 {
		return f, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		f.Resize(msg.Width, msg.Height)
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			return f, func() tea.Msg { return FormCancelledMsg{} }
		}
	}
	step := f.steps[f.index]
	if step.Field == nil {
		return f, nil
	}
	cmd, submitted := step.Field.Update(msg)
	if submitted {
		f.values[step.ID] = step.Field.Value()
		if f.index == len(f.steps)-1 {
			values := map[string]any{}
			for k, v := range f.values {
				values[k] = v
			}
			return f, func() tea.Msg { return FormCompletedMsg{Values: values} }
		}
		f.index++
		if next := f.steps[f.index].Field; next != nil {
			next.Focus()
			return f, next.Init()
		}
	}
	return f, cmd
}

func (f Form) View() string {
	if len(f.steps) == 0 {
		return ""
	}
	step := f.steps[f.index]
	progress := f.theme.SubtitleStyle.Render(fmt.Sprintf("Step %d of %d", f.index+1, len(f.steps)))
	return strings.Join([]string{
		progress,
		step.Field.View(),
	}, "\n\n")
}
