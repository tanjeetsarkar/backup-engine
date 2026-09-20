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
	label       string
	description string
	input       textinput.Model
	path        bool
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
	makeField := func(label, description, placeholder, value string, path bool) actionField {
		input := textinput.New()
		input.Prompt = ""
		input.Placeholder = placeholder
		input.SetValue(value)
		return actionField{label: label, description: description, input: input, path: path}
	}

	form := actionForm{kind: kind}
	switch kind {
	case actionBackup:
		form.title = "Create backup"
		form.fields = []actionField{
			makeField("Source path", "File or directory to snapshot. Directories are scanned recursively; the source itself is never modified. Tab completes paths.", "~/Documents", "", true),
			makeField("Retention tags", "Comma-separated labels stored with the snapshot, such as DAILY or IMPORTANT. These labels describe the snapshot; current GFS cleanup uses snapshot dates.", "DAILY,IMPORTANT", "DAILY", false),
		}
	case actionRestore:
		form.title = "Restore snapshot"
		form.fields = []actionField{
			makeField("Snapshot ID", "The snapshot's 64-character hexadecimal identifier. Select a snapshot in the catalog to prefill this value automatically.", "64 character hexadecimal ID", "", false),
			makeField("Destination path", "Directory where restored files will be written. It is created when needed; matching files inside an existing directory may be replaced.", "~/restore", "", true),
		}
	case actionGC:
		form.title = "Retention and garbage collection"
		form.fields = []actionField{
			makeField("Keep daily", "Number of distinct recent calendar days represented by one retained snapshot. Use 0 to disable the daily tier.", "7", "7", false),
			makeField("Keep weekly", "Number of distinct recent ISO weeks represented by one retained snapshot. Use 0 to disable the weekly tier.", "4", "4", false),
			makeField("Keep monthly", "Number of distinct recent calendar months represented by one retained snapshot. Use 0 to disable the monthly tier.", "12", "12", false),
			makeField("Keep yearly", "Number of distinct recent calendar years represented by one retained snapshot. Use 0 to disable the yearly tier.", "3", "3", false),
			makeField("Grace duration", "Minimum age of an unreferenced chunk before it may be removed, protecting recently uploaded data. Use a Go duration such as 24h or 168h.", "24h", "24h", false),
		}
	case actionInit:
		form.title = "Initialize repository"
		form.fields = []actionField{makeField("Bind existing data", "Enter yes only for a repository that already contains data but lacks key-check metadata. Confirm the original credentials first; binding does not prove all old data is readable.", "no", "no", false)}
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
		marker := "  "
		fieldStyle := lipgloss.NewStyle().Width(formContentWidth - 2).Foreground(mutedColor)
		if index == f.focus {
			labelStyle = accentStyle
			fieldStyle = fieldStyle.Foreground(accentColor)
			marker = "> "
		}
		lines = append(lines,
			labelStyle.Render(marker+field.label),
			fieldStyle.Render("  "+field.input.View()),
		)
	}
	lines = append(lines,
		statusLabelStyle.Render("ABOUT THIS INPUT"),
		descriptionStyle.Render(f.fields[f.focus].description),
		"",
	)
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
	lines = append(lines, statusLabelStyle.Render("PLANNED TRANSACTION"))
	lines = append(lines, f.preflightLines()...)
	lines = append(lines, "")
	lines = append(lines, mutedStyle.Render("tab complete  up/down move  enter select/start  esc cancel"))
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func (f actionForm) preflightLines() []string {
	values := f.values()
	switch f.kind {
	case actionBackup:
		return []string{
			"Action       Create encrypted snapshot",
			"Source       " + values[0],
			"Retention    " + values[1],
			"Data policy  Existing chunks will be reused",
		}
	case actionRestore:
		return []string{
			"Action       Restore snapshot files",
			"Snapshot     " + shortened(values[0], 24),
			"Destination  " + values[1],
			"Write policy Matching files may be replaced",
		}
	case actionGC:
		return []string{
			fmt.Sprintf("Retention    daily %s / weekly %s / monthly %s / yearly %s", values[0], values[1], values[2], values[3]),
			"Grace period " + values[4],
			"Effect       Unreferenced chunks may be permanently removed",
		}
	case actionInit:
		return []string{
			"Action       Write repository key-check metadata",
			"Bind data    " + values[0],
		}
	default:
		return nil
	}
}

func shortened(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}
