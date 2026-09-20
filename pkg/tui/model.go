package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tanjeetsarkar/backup-engine/pkg/pipeline"
)

type section int

const (
	sectionOverview section = iota
	sectionActivity
	sectionContext
	sectionBackup
	sectionRestore
	sectionSnapshots
	sectionHealth
	sectionRetention
	sectionSetup
)

var sectionNames = []string{
	"Overview",
	"Activity",
	"Repository",
	"Backup",
	"Restore",
	"Snapshots",
	"Health",
	"Retention",
	"Setup",
}

const snapshotFilterDescription = "Matches the loaded catalog by any part of a snapshot ID or status such as ok or auth-failed. Filtering does not change repository data."
const formContentWidth = 46

type keyMap struct {
	Up     key.Binding
	Down   key.Binding
	Open   key.Binding
	Help   key.Binding
	Safety key.Binding
	Quit   key.Binding
}

func defaultKeyMap() keyMap {
	return keyMap{
		Up:     key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("up/k", "move")),
		Down:   key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("down/j", "move")),
		Open:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		Help:   key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "more")),
		Safety: key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "safety")),
		Quit:   key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Open, k.Safety, k.Help, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Up, k.Down, k.Open}, {k.Safety, k.Help, k.Quit}}
}

type model struct {
	ctx             context.Context
	active          section
	mode            viewMode
	width           int
	height          int
	help            help.Model
	keys            keyMap
	showHelp        bool
	context         repositoryContext
	form            contextForm
	action          actionForm
	confirm         confirmForm
	spinner         spinner.Model
	snapshots       []pipeline.SnapshotStatus
	snapshotCursor  int
	snapshotFilter  textinput.Model
	filtering       bool
	status          string
	statusOK        bool
	safety          safetyProfile
	progress        pipeline.ProgressEvent
	activity        []pipeline.ProgressEvent
	progressStream  <-chan tea.Msg
	operationStart  time.Time
	operationCancel context.CancelFunc
	result          taskResultMsg
}

type viewMode int

const (
	modeNavigate viewMode = iota
	modeContextForm
	modeActionForm
	modeConfirm
	modeBusy
	modeSnapshots
	modeResult
)

type repositoryContext struct {
	repo       string
	passphrase string
	salt       string
}

type contextValidatedMsg struct{ err error }

type taskResultMsg struct {
	title     string
	detail    string
	summary   []string
	next      string
	warning   bool
	snapshots []pipeline.SnapshotStatus
	err       error
}

func newModel(ctx context.Context) model {
	activity := spinner.New()
	activity.Spinner = spinner.Dot
	activity.Style = accentStyle
	return model{ctx: ctx, help: help.New(), keys: defaultKeyMap(), spinner: activity, safety: safetyStandard}
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		m.height = message.Height
		m.help.Width = message.Width
	case tea.KeyMsg:
		if message.String() == "ctrl+c" {
			if m.mode == modeBusy && m.operationCancel != nil {
				m.operationCancel()
				m.operationCancel = nil
				m.status = "Cancelling operation..."
				m.progress.Message = m.status
				return m, nil
			}
			return m, tea.Quit
		}
		if m.mode == modeContextForm {
			if message.String() == "esc" {
				m.mode = modeNavigate
				return m, nil
			}
			var cmd tea.Cmd
			m.form, cmd = m.form.Update(message)
			if m.form.submitted {
				m.context = m.form.Value()
				m.beginOperation("Validating repository key", nil)
				return m, validateRepository(m.context)
			}
			return m, cmd
		}
		if m.mode == modeActionForm {
			if message.String() == "esc" {
				m.mode = modeNavigate
				return m, nil
			}
			var cmd tea.Cmd
			m.action, cmd = m.action.Update(message)
			if m.action.submitted {
				if phrase, reason := confirmationFor(m.safety, m.action); phrase != "" {
					m.confirm = newConfirmForm(reason, phrase)
					m.mode = modeConfirm
					return m, m.confirm.Init()
				}
				return m.startActionOperation()
			}
			return m, cmd
		}
		if m.mode == modeConfirm {
			if message.String() == "esc" {
				m.mode = modeActionForm
				m.action.submitted = false
				return m, nil
			}
			var cmd tea.Cmd
			m.confirm, cmd = m.confirm.Update(message)
			if m.confirm.confirmed {
				return m.startActionOperation()
			}
			return m, cmd
		}
		if m.mode == modeSnapshots {
			if m.filtering {
				switch message.String() {
				case "esc", "enter":
					m.filtering = false
					m.snapshotFilter.Blur()
					return m, nil
				}
				var cmd tea.Cmd
				m.snapshotFilter, cmd = m.snapshotFilter.Update(message)
				m.snapshotCursor = 0
				return m, cmd
			}
			visible := m.visibleSnapshots()
			switch message.String() {
			case "esc", "q":
				m.mode = modeNavigate
			case "/":
				m.filtering = true
				m.snapshotFilter.Focus()
				return m, textinput.Blink
			case "up", "k":
				if m.snapshotCursor > 0 {
					m.snapshotCursor--
				}
			case "down", "j":
				if m.snapshotCursor < len(visible)-1 {
					m.snapshotCursor++
				}
			case "enter":
				if len(visible) > 0 {
					m.active = sectionRestore
					m.action = newActionForm(actionRestore)
					m.action.fields[0].input.SetValue(fmt.Sprintf("%x", visible[m.snapshotCursor].ID))
					m.mode = modeActionForm
					return m, m.action.Init()
				}
			}
			return m, nil
		}
		if m.mode == modeBusy {
			return m, nil
		}
		if m.mode == modeResult {
			if message.String() == "esc" || message.String() == "enter" {
				m.mode = modeNavigate
			}
			return m, nil
		}
		switch {
		case key.Matches(message, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(message, m.keys.Up):
			if m.active > sectionOverview {
				m.active--
			}
		case key.Matches(message, m.keys.Down):
			if int(m.active) < len(sectionNames)-1 {
				m.active++
			}
		case key.Matches(message, m.keys.Help):
			m.showHelp = !m.showHelp
			m.help.ShowAll = m.showHelp
		case key.Matches(message, m.keys.Safety):
			m.safety = m.safety.next()
			m.statusOK = true
			m.status = "Safety profile changed to " + m.safety.String() + "."
		case key.Matches(message, m.keys.Open):
			switch m.active {
			case sectionContext:
				m.form = newContextForm(m.context)
				m.mode = modeContextForm
				return m, m.form.Init()
			case sectionBackup, sectionRestore, sectionRetention, sectionSetup:
				if m.context.repo == "" {
					m.statusOK = false
					m.status = "Connect a repository first."
					m.active = sectionContext
					return m, nil
				}
				m.action = newActionForm(actionForSection(m.active))
				m.mode = modeActionForm
				return m, m.action.Init()
			case sectionSnapshots:
				return m.startTask("Loading snapshots...", loadSnapshots(m.context))
			case sectionHealth:
				if m.context.repo == "" {
					m.active = sectionContext
					m.statusOK = false
					m.status = "Connect a repository first."
					return m, nil
				}
				events := make(chan tea.Msg, 64)
				operationContext, cancel := context.WithCancel(m.ctx)
				m.operationCancel = cancel
				m.beginOperation("Preparing repository health checks", events)
				return m, tea.Batch(m.spinner.Tick, waitForProgress(events), runHealth(operationContext, m.context, events))
			case sectionOverview:
				m.active = sectionContext
				m.form = newContextForm(m.context)
				m.mode = modeContextForm
				return m, m.form.Init()
			}
		}
	case spinner.TickMsg:
		if m.mode == modeBusy {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(message)
			return m, cmd
		}
	case progressEventMsg:
		m.progress = message.event
		m.status = message.event.Message
		m.activity = append(m.activity, message.event)
		if len(m.activity) > 200 {
			m.activity = append([]pipeline.ProgressEvent(nil), m.activity[len(m.activity)-200:]...)
		}
		if m.mode == modeBusy && m.progressStream != nil {
			return m, waitForProgress(m.progressStream)
		}
	case progressStreamClosedMsg:
		m.progressStream = nil
	case contextValidatedMsg:
		m.mode = modeNavigate
		m.active = sectionContext
		m.statusOK = message.err == nil
		switch {
		case message.err == nil:
			m.status = "Connected. Repository key verified."
		case errors.Is(message.err, pipeline.ErrRepositoryNeedsInit):
			m.status = "Repository needs initialization. Use Setup to bind existing data."
		case errors.Is(message.err, pipeline.ErrRepositoryKeyMismatch):
			m.status = "Key mismatch. Check the passphrase and salt."
		default:
			m.status = message.err.Error()
		}
	case taskResultMsg:
		if m.operationCancel != nil {
			m.operationCancel()
			m.operationCancel = nil
		}
		m.statusOK = message.err == nil && !message.warning
		m.result = message
		if message.err != nil {
			presentation := pipeline.PresentError(message.err)
			m.result.title = presentation.Summary
			m.result.detail = presentation.Cause
			m.result.next = presentation.Hint
			m.status = presentation.Summary
			m.mode = modeResult
			return m, nil
		}
		m.status = message.detail
		if message.snapshots != nil {
			m.snapshots = message.snapshots
			m.snapshotCursor = 0
			m.snapshotFilter = textinput.New()
			m.snapshotFilter.Prompt = "/ "
			m.snapshotFilter.Placeholder = "filter by ID or status"
			m.mode = modeSnapshots
		} else {
			m.mode = modeResult
		}
		if m.progressStream != nil {
			return m, waitForProgress(m.progressStream)
		}
	}
	return m, nil
}

func (m model) startActionOperation() (tea.Model, tea.Cmd) {
	events := make(chan tea.Msg, 64)
	operationContext, cancel := context.WithCancel(m.ctx)
	m.operationCancel = cancel
	m.beginOperation("Preparing operation", events)
	return m, tea.Batch(m.spinner.Tick, waitForProgress(events), runAction(operationContext, m.context, m.action, events))
}

func (m *model) beginOperation(status string, events <-chan tea.Msg) {
	m.mode = modeBusy
	m.status = status
	m.progress = pipeline.ProgressEvent{Message: status, Phase: pipeline.PhasePreparing, Time: time.Now()}
	m.progressStream = events
	m.operationStart = time.Now()
	m.result = taskResultMsg{}
}

func (m model) startTask(label string, command tea.Cmd) (tea.Model, tea.Cmd) {
	if m.context.repo == "" {
		m.active = sectionContext
		m.statusOK = false
		m.status = "Connect a repository first."
		return m, nil
	}
	m.beginOperation(label, nil)
	return m, tea.Batch(m.spinner.Tick, command)
}

func validateRepository(repository repositoryContext) tea.Cmd {
	return func() tea.Msg {
		engine, err := pipeline.Open(pipeline.EngineConfig{
			RepoDir:    repository.repo,
			Passphrase: []byte(repository.passphrase),
			Salt:       []byte(repository.salt),
		})
		if err == nil {
			err = engine.Close()
		}
		return contextValidatedMsg{err: err}
	}
}

func (m model) View() string {
	if m.width == 0 {
		return "Starting Backup Engine..."
	}

	contentHeight := max(8, m.height-5)
	navWidth := 22
	if m.width < 72 {
		navWidth = 16
	}
	mainWidth := max(24, m.width-navWidth-5)

	header := titleStyle.Render("BACKUP ENGINE") + "  " + mutedStyle.Render("encrypted repository control plane") + "  " + statusLabelStyle.Render("SAFETY "+strings.ToUpper(m.safety.String()))
	navigation := m.navigationView(navWidth, contentHeight)
	detail := panelStyle.Width(mainWidth).Height(contentHeight).Render(m.detailView())
	body := lipgloss.JoinHorizontal(lipgloss.Top, navigation, detail)
	footer := m.help.View(m.keys)

	return lipgloss.JoinVertical(lipgloss.Left, headerStyle.Width(m.width).Render(header), body, footerStyle.Width(m.width).Render(footer))
}

func (m model) navigationView(width, height int) string {
	rows := make([]string, 0, len(sectionNames)+3)
	rows = append(rows, eyebrowStyle.Render("WORKSPACE"), "")
	for index, name := range sectionNames {
		marker := "  "
		style := navItemStyle
		if section(index) == m.active {
			marker = "> "
			style = activeNavStyle
		}
		rows = append(rows, style.Width(width-4).Render(marker+name))
	}
	return navStyle.Width(width).Height(height).Render(strings.Join(rows, "\n"))
}

func (m model) detailView() string {
	if m.mode == modeContextForm {
		return m.form.View()
	}
	if m.mode == modeActionForm {
		return m.action.View()
	}
	if m.mode == modeConfirm {
		return m.confirm.View()
	}
	if m.mode == modeBusy {
		return m.busyView()
	}
	if m.mode == modeSnapshots {
		return m.snapshotsView()
	}
	if m.mode == modeResult {
		return m.resultView()
	}

	title := sectionNames[m.active]
	description := map[section]string{
		sectionOverview:  "Repository status and the next useful action at a glance.",
		sectionActivity:  "Review progress and results from this TUI session.",
		sectionContext:   "Connect to a repository and validate its encryption key.",
		sectionBackup:    "Create a deduplicated, encrypted snapshot.",
		sectionRestore:   "Recover a snapshot into a chosen destination.",
		sectionSnapshots: "Browse, filter, and inspect available snapshots.",
		sectionHealth:    "Verify stored data and diagnose index consistency.",
		sectionRetention: "Apply retention policy and compact unreferenced data.",
		sectionSetup:     "Initialize a new repository or bind key-check metadata.",
	}[m.active]
	if m.active == sectionActivity {
		return m.activityView()
	}

	contextStatus := warningStyle.Render("Not configured")
	if m.context.repo != "" {
		contextStatus = m.context.repo
	}
	if m.status != "" {
		style := errorStyle
		if m.statusOK {
			style = successStyle
		}
		contextStatus += "\n" + style.Render(m.status)
	}

	return lipgloss.JoinVertical(
		lipgloss.Left,
		sectionTitleStyle.Render(title),
		mutedStyle.Render(description),
		"",
		statusLabelStyle.Render("REPOSITORY"),
		contextStatus,
		"",
		fmt.Sprintf("Select %s and press Enter to connect.", accentStyle.Render("Repository")),
	)
}

func (m model) busyView() string {
	event := m.progress
	elapsed := time.Since(m.operationStart).Round(time.Second)
	operationName := string(event.Operation)
	if operationName == "" {
		operationName = "Operation"
	} else {
		operationName = strings.ToUpper(operationName[:1]) + operationName[1:]
	}
	lines := []string{
		sectionTitleStyle.Render(operationName + " in progress"),
		mutedStyle.Render(fmt.Sprintf("Phase: %s  Elapsed: %s", event.Phase, elapsed)),
		"",
		accentStyle.Render(m.spinner.View() + " " + event.Message),
	}
	if event.Total > 0 {
		percent := float64(event.Completed) / float64(event.Total) * 100
		lines = append(lines, fmt.Sprintf("%d / %d %s  (%.0f%%)", event.Completed, event.Total, event.Unit, percent))
	}
	lines = append(lines, "", statusLabelStyle.Render("ACTIVITY"))
	lines = append(lines, m.activityLines(8)...)
	lines = append(lines, "", mutedStyle.Render("Progress is session-only. ctrl+c requests cancellation."))
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func (m model) resultView() string {
	style := successStyle
	status := "COMPLETED"
	if m.result.err != nil {
		style = errorStyle
		status = "FAILED"
	} else if m.result.warning {
		style = warningStyle
		status = "COMPLETED WITH WARNINGS"
	}
	lines := []string{sectionTitleStyle.Render(m.result.title), style.Render(status), "", m.result.detail}
	if len(m.result.summary) > 0 {
		lines = append(lines, "", statusLabelStyle.Render("SUMMARY"))
		lines = append(lines, m.result.summary...)
	}
	if m.result.next != "" {
		lines = append(lines, "", statusLabelStyle.Render("NEXT STEP"), m.result.next)
	}
	lines = append(lines, "", mutedStyle.Render("enter/esc return to dashboard"))
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func (m model) activityView() string {
	lines := []string{sectionTitleStyle.Render("Session Activity"), mutedStyle.Render("Recent operation phases and outcomes. Nothing is written to repository metadata."), ""}
	if len(m.activity) == 0 {
		return lipgloss.JoinVertical(lipgloss.Left, append(lines, "No operations have run in this session.")...)
	}
	lines = append(lines, m.activityLines(max(6, m.height-12))...)
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func (m model) activityLines(limit int) []string {
	start := max(0, len(m.activity)-limit)
	lines := make([]string, 0, len(m.activity)-start)
	for _, event := range m.activity[start:] {
		line := fmt.Sprintf("%s  %-11s %s", event.Time.Format("15:04:05"), event.Phase, event.Message)
		style := mutedStyle
		if event.Level == pipeline.EventSuccess {
			style = successStyle
		} else if event.Level == pipeline.EventWarning {
			style = warningStyle
		}
		lines = append(lines, style.Render(line))
	}
	return lines
}

func (m model) snapshotsView() string {
	lines := []string{sectionTitleStyle.Render("Snapshot Catalog"), mutedStyle.Render("up/down move  / filter  enter restore  esc back"), ""}
	if m.filtering || m.snapshotFilter.Value() != "" {
		lines = append(lines, statusLabelStyle.Render("FILTER"), descriptionStyle.Render(snapshotFilterDescription), m.snapshotFilter.View(), "")
	}
	visible := m.visibleSnapshots()
	if len(visible) == 0 {
		return lipgloss.JoinVertical(lipgloss.Left, append(lines, "No snapshots found.")...)
	}
	for index, snapshot := range visible {
		marker := "  "
		style := mutedStyle
		if index == m.snapshotCursor {
			marker = "> "
			style = accentStyle
		}
		status := successStyle.Render(snapshot.StatusText)
		if !snapshot.Readable {
			status = errorStyle.Render(snapshot.StatusText)
		}
		lines = append(lines, style.Render(fmt.Sprintf("%s%x", marker, snapshot.ID))+"  "+status)
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func (m model) visibleSnapshots() []pipeline.SnapshotStatus {
	query := strings.ToLower(strings.TrimSpace(m.snapshotFilter.Value()))
	if query == "" {
		return m.snapshots
	}
	visible := make([]pipeline.SnapshotStatus, 0, len(m.snapshots))
	for _, snapshot := range m.snapshots {
		if strings.Contains(strings.ToLower(fmt.Sprintf("%x", snapshot.ID)), query) || strings.Contains(strings.ToLower(snapshot.StatusText), query) {
			visible = append(visible, snapshot)
		}
	}
	return visible
}

var (
	accentColor  = lipgloss.Color("39")
	successColor = lipgloss.Color("42")
	warningColor = lipgloss.Color("214")
	mutedColor   = lipgloss.AdaptiveColor{Light: "#59636E", Dark: "#7D8996"}

	titleStyle        = lipgloss.NewStyle().Bold(true).Foreground(accentColor)
	headerStyle       = lipgloss.NewStyle().Padding(0, 1)
	eyebrowStyle      = lipgloss.NewStyle().Bold(true).Foreground(mutedColor)
	navStyle          = lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, true, false, false).BorderForeground(lipgloss.Color("238")).Padding(1)
	navItemStyle      = lipgloss.NewStyle().Foreground(mutedColor).Padding(0, 1)
	activeNavStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(accentColor).Padding(0, 1)
	panelStyle        = lipgloss.NewStyle().Padding(1, 2)
	sectionTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#17212B", Dark: "#F4F7FA"})
	mutedStyle        = lipgloss.NewStyle().Foreground(mutedColor)
	descriptionStyle  = lipgloss.NewStyle().Foreground(mutedColor).Width(formContentWidth)
	statusLabelStyle  = lipgloss.NewStyle().Bold(true).Foreground(mutedColor)
	warningStyle      = lipgloss.NewStyle().Bold(true).Foreground(warningColor)
	successStyle      = lipgloss.NewStyle().Bold(true).Foreground(successColor)
	errorStyle        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
	accentStyle       = lipgloss.NewStyle().Bold(true).Foreground(accentColor)
	footerStyle       = lipgloss.NewStyle().Padding(0, 1).Foreground(mutedColor)
)
