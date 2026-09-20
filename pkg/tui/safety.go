package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type safetyProfile int

const confirmationInputDescription = "This phrase prevents an accidental destructive action. The comparison is case-insensitive; no repository credential is requested here."

const (
	safetyStrict safetyProfile = iota
	safetyStandard
	safetyFast
)

func (profile safetyProfile) String() string {
	switch profile {
	case safetyStrict:
		return "strict"
	case safetyFast:
		return "fast"
	default:
		return "standard"
	}
}

func (profile safetyProfile) next() safetyProfile {
	return (profile + 1) % 3
}

func confirmationFor(profile safetyProfile, form actionForm) (string, string) {
	switch form.kind {
	case actionGC:
		if profile == safetyFast {
			return "", ""
		}
		if profile == safetyStrict {
			return "GC", "Garbage collection can permanently remove unreferenced chunks."
		}
		return "yes", "Review the retention values before garbage collection."
	case actionRestore:
		values := form.values()
		if len(values) < 2 {
			return "", ""
		}
		if _, err := os.Stat(expandHome(values[1])); err != nil {
			return "", ""
		}
		if profile == safetyStrict {
			return "RESTORE", "The destination exists and may contain files with matching names."
		}
		return "yes", "The destination exists and may contain files with matching names."
	}
	return "", ""
}

type confirmForm struct {
	reason    string
	phrase    string
	input     textinput.Model
	errorText string
	confirmed bool
}

func newConfirmForm(reason, phrase string) confirmForm {
	input := textinput.New()
	input.Prompt = ""
	input.Placeholder = phrase
	input.Focus()
	return confirmForm{reason: reason, phrase: phrase, input: input}
}

func (form confirmForm) Init() tea.Cmd { return textinput.Blink }

func (form confirmForm) Update(message tea.Msg) (confirmForm, tea.Cmd) {
	if keyMessage, ok := message.(tea.KeyMsg); ok && keyMessage.String() == "enter" {
		if !strings.EqualFold(strings.TrimSpace(form.input.Value()), form.phrase) {
			form.errorText = fmt.Sprintf("Type %q exactly to continue.", form.phrase)
			return form, nil
		}
		form.confirmed = true
		return form, nil
	}
	var command tea.Cmd
	form.input, command = form.input.Update(message)
	return form, command
}

func (form confirmForm) View() string {
	lines := []string{
		errorStyle.Render("Confirmation required"),
		form.reason,
		"",
		fmt.Sprintf("Type %s to continue:", accentStyle.Render(form.phrase)),
		descriptionStyle.Render(confirmationInputDescription),
		lipgloss.NewStyle().Width(32).Border(lipgloss.RoundedBorder()).BorderForeground(warningColor).Padding(0, 1).Render(form.input.View()),
	}
	if form.errorText != "" {
		lines = append(lines, "", errorStyle.Render(form.errorText))
	}
	lines = append(lines, "", mutedStyle.Render("enter confirm  esc go back"))
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}
