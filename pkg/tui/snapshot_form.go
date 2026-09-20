package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tanjeetsarkar/backup-engine/pkg/pipeline"
)

type snapshotEditForm struct {
	snapshotID [32]byte
	fields     []actionField
	focus      int
	errorText  string
	submitted  bool
}

func newSnapshotEditForm(snapshot pipeline.SnapshotDetails) snapshotEditForm {
	makeField := func(label, description, placeholder, value string) actionField {
		input := textinput.New()
		input.Prompt = ""
		input.Placeholder = placeholder
		input.SetValue(value)
		return actionField{label: label, description: description, input: input}
	}
	retainUntil := ""
	if snapshot.Lifecycle.RetainUntil != nil {
		retainUntil = snapshot.Lifecycle.RetainUntil.Local().Format("2006-01-02 15:04 MST")
	}
	form := snapshotEditForm{
		snapshotID: snapshot.ID,
		fields: []actionField{
			makeField("Labels", "Comma-separated encrypted labels used for catalog search and organization. Labels do not change snapshot contents or its immutable ID.", "photos, important", strings.Join(snapshot.Lifecycle.Labels, ", ")),
			makeField("Note", "An encrypted private note for operators. Avoid placing passwords or recovery secrets here; it is metadata, not a credential store.", "Why this snapshot matters", snapshot.Lifecycle.Note),
			makeField("Retain until", "Optional local date and time through which GFS cleanup must retain this active snapshot. Use YYYY-MM-DD HH:MM, RFC3339, or leave blank to clear.", "2026-12-31 23:59", retainUntil),
		},
	}
	form.fields[0].input.Focus()
	return form
}

func (form snapshotEditForm) Init() tea.Cmd { return textinput.Blink }

func (form snapshotEditForm) Update(message tea.Msg) (snapshotEditForm, tea.Cmd) {
	if keyMessage, ok := message.(tea.KeyMsg); ok {
		switch keyMessage.String() {
		case "tab", "down":
			form.moveFocus(1)
			return form, textinput.Blink
		case "shift+tab", "up":
			form.moveFocus(-1)
			return form, textinput.Blink
		case "enter":
			if form.focus < len(form.fields)-1 {
				form.moveFocus(1)
				return form, textinput.Blink
			}
			if _, _, err := form.values(); err != nil {
				form.errorText = err.Error()
				return form, nil
			}
			form.submitted = true
			return form, nil
		}
	}
	var command tea.Cmd
	form.fields[form.focus].input, command = form.fields[form.focus].input.Update(message)
	return form, command
}

func (form *snapshotEditForm) moveFocus(delta int) {
	form.fields[form.focus].input.Blur()
	form.focus = (form.focus + delta + len(form.fields)) % len(form.fields)
	form.fields[form.focus].input.Focus()
	form.errorText = ""
}

func (form snapshotEditForm) values() (pipeline.SnapshotMetadataUpdate, *time.Time, error) {
	labels := splitTags(form.fields[0].input.Value())
	if strings.TrimSpace(form.fields[0].input.Value()) == "" {
		labels = nil
	}
	var retainUntil *time.Time
	rawTime := strings.TrimSpace(form.fields[2].input.Value())
	if rawTime != "" {
		parsed, err := parseUserTime(rawTime)
		if err != nil {
			return pipeline.SnapshotMetadataUpdate{}, nil, err
		}
		retainUntil = &parsed
	}
	return pipeline.SnapshotMetadataUpdate{Labels: labels, Note: form.fields[1].input.Value()}, retainUntil, nil
}

func parseUserTime(value string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04 MST", "2006-01-02 15:04", time.RFC3339} {
		var parsed time.Time
		var err error
		if layout == "2006-01-02 15:04" {
			parsed, err = time.ParseInLocation(layout, value, time.Local)
		} else {
			parsed, err = time.Parse(layout, value)
		}
		if err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("retain until must use YYYY-MM-DD HH:MM or RFC3339")
}

func (form snapshotEditForm) View() string {
	lines := []string{sectionTitleStyle.Render("Edit snapshot metadata"), mutedStyle.Render("Metadata is encrypted. Snapshot contents and immutable ID do not change."), ""}
	for index, field := range form.fields {
		style := statusLabelStyle
		marker := "  "
		if index == form.focus {
			style = accentStyle
			marker = "> "
		}
		lines = append(lines, style.Render(marker+field.label), "  "+field.input.View())
	}
	lines = append(lines, "", statusLabelStyle.Render("ABOUT THIS INPUT"), descriptionStyle.Render(form.fields[form.focus].description))
	if form.errorText != "" {
		lines = append(lines, "", errorStyle.Render(form.errorText))
	}
	lines = append(lines, "", mutedStyle.Render("up/down move  enter save  esc cancel"))
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}
