package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type contextForm struct {
	inputs      []textinput.Model
	focus       int
	errorText   string
	submitted   bool
	suggestions []string
	suggestion  int
}

func newContextForm(current repositoryContext) contextForm {
	repo := textinput.New()
	repo.Prompt = ""
	repo.Placeholder = "~/backups/my-repository"
	repo.SetValue(current.repo)
	repo.Focus()

	passphrase := textinput.New()
	passphrase.Prompt = ""
	passphrase.Placeholder = "Repository passphrase"
	passphrase.EchoMode = textinput.EchoPassword
	passphrase.EchoCharacter = '*'
	passphrase.SetValue(current.passphrase)

	salt := textinput.New()
	salt.Prompt = ""
	salt.Placeholder = "At least 16 characters"
	salt.EchoMode = textinput.EchoPassword
	salt.EchoCharacter = '*'
	salt.SetValue(current.salt)

	return contextForm{inputs: []textinput.Model{repo, passphrase, salt}}
}

func (f contextForm) Init() tea.Cmd { return textinput.Blink }

func (f contextForm) Update(message tea.Msg) (contextForm, tea.Cmd) {
	if keyMessage, ok := message.(tea.KeyMsg); ok {
		switch keyMessage.String() {
		case "tab":
			if f.focus == 0 {
				matches, err := completePath(f.inputs[0].Value())
				if err != nil {
					f.errorText = err.Error()
					return f, nil
				}
				if len(matches) == 1 {
					f.inputs[0].SetValue(matches[0])
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
				f.inputs[0].SetValue(f.suggestions[f.suggestion])
				f.suggestions = nil
				f.suggestion = 0
				return f, nil
			}
			if f.focus < len(f.inputs)-1 {
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
	f.inputs[f.focus], cmd = f.inputs[f.focus].Update(message)
	return f, cmd
}

func (f *contextForm) moveFocus(delta int) {
	f.inputs[f.focus].Blur()
	f.focus = (f.focus + delta + len(f.inputs)) % len(f.inputs)
	f.inputs[f.focus].Focus()
	f.errorText = ""
	f.suggestions = nil
	f.suggestion = 0
}

func (f contextForm) validate() error {
	if strings.TrimSpace(f.inputs[0].Value()) == "" {
		return fmt.Errorf("repository directory is required")
	}
	if f.inputs[1].Value() == "" {
		return fmt.Errorf("passphrase is required")
	}
	if len(f.inputs[2].Value()) < 16 {
		return fmt.Errorf("salt must be at least 16 characters")
	}
	return nil
}

func (f contextForm) Value() repositoryContext {
	return repositoryContext{
		repo:       strings.TrimSpace(f.inputs[0].Value()),
		passphrase: f.inputs[1].Value(),
		salt:       f.inputs[2].Value(),
	}
}

func (f contextForm) View() string {
	labels := []string{"Repository directory", "Passphrase", "Salt"}
	lines := []string{
		sectionTitleStyle.Render("Connect repository"),
		mutedStyle.Render("Credentials stay in memory for this session."),
		"",
	}
	for index, input := range f.inputs {
		label := statusLabelStyle.Render(labels[index])
		if index == f.focus {
			label = accentStyle.Render(labels[index])
		}
		field := lipgloss.NewStyle().Width(54).Border(lipgloss.RoundedBorder()).BorderForeground(mutedColor).Padding(0, 1).Render(input.View())
		if index == f.focus {
			field = lipgloss.NewStyle().Width(54).Border(lipgloss.RoundedBorder()).BorderForeground(accentColor).Padding(0, 1).Render(input.View())
		}
		lines = append(lines, label, field, "")
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
	lines = append(lines, mutedStyle.Render("tab complete  up/down move  enter select/validate  esc cancel"))
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}
