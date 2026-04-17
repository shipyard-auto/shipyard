package components

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/shipyard-auto/shipyard/internal/ui/tui/theme"
)

type Input struct {
	theme       theme.Theme
	model       textinput.Model
	label       string
	hint        string
	err         string
	maxLength   int
	validator   func(string) error
	width       int
	placeholder string
}

func NewInput(th theme.Theme, label, placeholder string, validator func(string) error) Input {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.Focus()
	return Input{
		theme:       th,
		model:       ti,
		label:       label,
		validator:   validator,
		placeholder: placeholder,
	}
}

func (i Input) Init() tea.Cmd { return textinput.Blink }

func (i *Input) Resize(width, _ int) {
	i.width = width
	i.model.Width = max(20, width-8)
}

func (i *Input) SetHint(h string) { i.hint = h }

func (i *Input) SetMaxLength(n int) {
	i.maxLength = n
	i.model.CharLimit = n
}

func (i *Input) SetValue(v string) {
	i.model.SetValue(v)
	i.validate()
}

func (i Input) Value() string { return i.model.Value() }

func (i Input) Error() string { return i.err }

func (i *Input) Focus() { i.model.Focus() }

func (i *Input) Blur() { i.model.Blur() }

func (i *Input) Update(msg tea.Msg) (tea.Cmd, bool) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		i.Resize(msg.Width, msg.Height)
	case tea.KeyMsg:
		if msg.Type == tea.KeyEnter {
			i.validate()
			return nil, i.err == ""
		}
	}
	i.model, cmd = i.model.Update(msg)
	if i.maxLength > 0 && len(i.model.Value()) > i.maxLength {
		i.model.SetValue(i.model.Value()[:i.maxLength])
	}
	i.validate()
	return cmd, false
}

func (i *Input) validate() {
	value := i.model.Value()
	if i.validator == nil {
		i.err = ""
		return
	}
	if err := i.validator(value); err != nil {
		i.err = err.Error()
		return
	}
	i.err = ""
}

func (i Input) View() string {
	style := i.theme.InputStyle
	if i.model.Focused() {
		style = i.theme.InputFocusedStyle
	}
	body := []string{
		i.theme.LabelStyle.Render(i.label),
		style.Render(i.model.View()),
	}
	if i.hint != "" {
		body = append(body, i.theme.RenderHint(i.hint))
	}
	if i.err != "" {
		body = append(body, i.theme.RenderError(i.err))
	}
	return strings.Join(body, "\n")
}
