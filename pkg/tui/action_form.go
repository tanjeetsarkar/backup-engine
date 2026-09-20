package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type actionKind int

const (
	actionBackup actionKind = iota
	actionRestore
	actionGC
	actionInit
)

type actionField struct {
	label string
	input textinput.Model
	path  bool
}

type actionForm struct {
	kind        actionKind
	title       string
	fields      []actionField
	focus       int
	errorText   string
	submitted   bool
	suggestions []string
	suggestion  int
}

func actionForSection(current section) actionKind {
	switch current {
	case sectionRestore:
		return actionRestore
	case sectionRetention:
		return actionGC
	case sectionSetup:
		return actionInit
	default:
		return actionBackup
	}
}

func newActionForm(kind actionKind) actionForm {
	makeField := func(label, placeholder, value string, path bool) actionField {
		input := textinput.New()
		input.Prompt = ""
		input.Placeholder = placeholder
		input.SetValue(value)
		return actionField{label: label, input: input, path: path}
	}

	form := actionForm{kind: kind}
	switch kind {
	case actionBackup:
		form.title = "Create backup"
		form.fields = []actionField{
			makeField("Source path", "~/Documents", "", true),
			makeField("Retention tags", "DAILY,IMPORTANT", "DAILY", false),
		}
	case actionRestore:
		form.title = "Restore snapshot"
		form.fields = []actionField{
			makeField("Snapshot ID", "64 character hexadecimal ID", "", false),
			makeField("Destination path", "~/restore", "", true),
		}
	case actionGC:
		form.title = "Retention and garbage collection"
		form.fields = []actionField{
			makeField("Keep daily", "7", "7", false),
			makeField("Keep weekly", "4", "4", false),
			makeField("Keep monthly", "12", "12", false),
			makeField("Keep yearly", "3", "3", false),
			makeField("Grace duration", "24h", "24h", false),
		}
	case actionInit:
		form.title = "Initialize repository"
		form.fields = []actionField{makeField("Bind existing data", "no", "no", false)}
	}
	form.fields[0].input.Focus()
	return form
}

func (f actionForm) Init() tea.Cmd { return textinput.Blink }

func (f actionForm) Update(message tea.Msg) (actionForm, tea.Cmd) {
	if keyMessage, ok := message.(tea.KeyMsg); ok {
		switch keyMessage.String() {
		case "tab":
			if f.fields[f.focus].path {
				matches, err := completePath(f.fields[f.focus].input.Value())
				if err != nil {
					f.errorText = err.Error()
					return f, nil
				}
				if len(matches) == 1 {
					f.fields[f.focus].input.SetValue(matches[0])
					f.suggestions = nil
				} else if len(matches) > 1 {
					if len(f.suggestions) > 0 {
						f.suggestion = (f.suggestion + 1) % len(matches)
					} else {
						f.suggestion = 0
					}
					f.suggestions = matches
				}
				return f, nil
			}
			f.moveFocus(1)
			return f, textinput.Blink
		case "down":
			f.moveFocus(1)
			return f, textinput.Blink
		case "shift+tab", "up":
			f.moveFocus(-1)
			return f, textinput.Blink
		case "enter":
			if len(f.suggestions) > 0 {
				f.fields[f.focus].input.SetValue(f.suggestions[f.suggestion])
				f.suggestions = nil
				f.suggestion = 0
				return f, nil
			}
			if f.focus < len(f.fields)-1 {
				f.moveFocus(1)
				return f, textinput.Blink
			}
			if err := f.validate(); err != nil {
				f.errorText = err.Error()
				return f, nil
			}
			f.submitted = true
			return f, nil
		}
	}

	var cmd tea.Cmd
	f.fields[f.focus].input, cmd = f.fields[f.focus].input.Update(message)
	return f, cmd
}

func (f *actionForm) moveFocus(delta int) {
	f.fields[f.focus].input.Blur()
	f.focus = (f.focus + delta + len(f.fields)) % len(f.fields)
	f.fields[f.focus].input.Focus()
	f.errorText = ""
	f.suggestions = nil
	f.suggestion = 0
}

func (f actionForm) validate() error {
	for _, field := range f.fields {
		if strings.TrimSpace(field.input.Value()) == "" {
			return fmt.Errorf("%s is required", strings.ToLower(field.label))
		}
	}
	switch f.kind {
	case actionRestore:
		if _, err := parseHexID(f.fields[0].input.Value()); err != nil {
			return err
		}
	case actionGC:
		for _, field := range f.fields[:4] {
			value, err := strconv.Atoi(field.input.Value())
			if err != nil || value < 0 {
				return fmt.Errorf("%s must be a non-negative number", strings.ToLower(field.label))
			}
		}
		if _, err := time.ParseDuration(f.fields[4].input.Value()); err != nil {
			return fmt.Errorf("invalid grace duration: %w", err)
		}
	case actionInit:
		value := strings.ToLower(f.fields[0].input.Value())
		if value != "yes" && value != "no" && value != "y" && value != "n" {
			return fmt.Errorf("bind existing data must be yes or no")
		}
	}
	return nil
}

func (f actionForm) values() []string {
	values := make([]string, len(f.fields))
	for index := range f.fields {
		values[index] = strings.TrimSpace(f.fields[index].input.Value())
	}
	return values
}

func (f actionForm) View() string {
	lines := []string{sectionTitleStyle.Render(f.title), mutedStyle.Render("Review inputs before starting the operation."), ""}
	for index, field := range f.fields {
		labelStyle := statusLabelStyle
		fieldStyle := lipgloss.NewStyle().Width(58).Border(lipgloss.RoundedBorder()).BorderForeground(mutedColor).Padding(0, 1)
		if index == f.focus {
			labelStyle = accentStyle
			fieldStyle = fieldStyle.BorderForeground(accentColor)
		}
		lines = append(lines,
			labelStyle.Render(field.label),
			fieldStyle.Render(field.input.View()),
			"",
		)
	}
	if len(f.suggestions) > 0 {
		lines = append(lines, statusLabelStyle.Render("PATH MATCHES"))
		for index, candidate := range f.suggestions {
			if index >= 6 {
				lines = append(lines, mutedStyle.Render(fmt.Sprintf("  + %d more", len(f.suggestions)-index)))
				break
			}
			style := mutedStyle
			marker := "  "
			if index == f.suggestion {
				style = accentStyle
				marker = "> "
			}
			lines = append(lines, style.Render(marker+candidate))
		}
		lines = append(lines, "")
	}
	if f.errorText != "" {
		lines = append(lines, errorStyle.Render(f.errorText), "")
	}
	lines = append(lines, mutedStyle.Render("tab complete  up/down move  enter select/start  esc cancel"))
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}
