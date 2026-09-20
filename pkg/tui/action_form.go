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
	actionReplicate
	actionRecover
)

type actionField struct {
	label       string
	description string
	input       textinput.Model
	path        bool
	optional    bool
}

type actionForm struct {
	kind        actionKind
	title       string
	subtitle    string
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
	case sectionReplicate:
		return actionReplicate
	case sectionRecover:
		return actionRecover
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
	makeOptionalField := func(label, description, placeholder, value string) actionField {
		field := makeField(label, description, placeholder, value, false)
		field.optional = true
		return field
	}
	remoteFields := func() []actionField {
		return []actionField{
			makeField("Remote backend", "Storage backend for the offsite target. Currently minio (any S3-compatible endpoint).", "minio", "minio", false),
			makeField("Remote endpoint", "Offsite host:port the S3-compatible API listens on.", "offsite-host:9000", "", false),
			makeField("Remote bucket", "Bucket on the offsite endpoint that holds packs, manifests, and the CID directory.", "backup-engine-offsite", "", false),
			makeOptionalField("Remote prefix", "Optional object key prefix within the bucket. Leave blank to use the bucket root.", "", ""),
			makeField("Remote access key", "Access key for the offsite endpoint.", "access key", "", false),
			makeField("Remote secret key", "Secret key for the offsite endpoint. Not stored beyond this session.", "secret key", "", false),
			makeField("Use TLS", "Whether to connect to the offsite endpoint over TLS. Enter yes unless the endpoint is a local/testing instance without TLS.", "yes", "yes", false),
		}
	}

	form := actionForm{kind: kind}
	switch kind {
	case actionBackup:
		form.title = "Create backup"
		form.subtitle = "Reads the source path and writes new encrypted data; it never modifies or deletes the source."
		form.fields = []actionField{
			makeField("Source path", "File or directory to snapshot. Directories are scanned recursively; the source itself is never modified. Tab completes paths.", "~/Documents", "", true),
			makeField("Retention tags", "Comma-separated labels stored with the snapshot, such as DAILY or IMPORTANT. These labels describe the snapshot; current GFS cleanup uses snapshot dates.", "DAILY,IMPORTANT", "DAILY", false),
			makeOptionalField("Permission policy", "How to handle unreadable files: 'fail' aborts the backup, 'skip' omits them and continues. Default: fail.", "fail", "fail"),
			makeOptionalField("Workers", "Number of worker goroutines for file preparation. 0 = automatic (bounded by CPU cores).", "0", "0"),
		}
	case actionRestore:
		form.title = "Restore snapshot"
		form.subtitle = "Writes decrypted files to the destination; existing files with matching names may be overwritten."
		form.fields = []actionField{
			makeField("Snapshot ID", "The snapshot's 64-character hexadecimal identifier. Select a snapshot in the catalog to prefill this value automatically.", "64 character hexadecimal ID", "", false),
			makeField("Destination path", "Directory where restored files will be written. It is created when needed; matching files inside an existing directory may be replaced.", "~/restore", "", true),
		}
	case actionGC:
		form.title = "Retention and garbage collection"
		form.subtitle = "Permanently removes chunks no retained snapshot references; this cannot be undone."
		form.fields = []actionField{
			makeField("Keep daily", "Number of distinct recent calendar days represented by one retained snapshot. Use 0 to disable the daily tier.", "7", "7", false),
			makeField("Keep weekly", "Number of distinct recent ISO weeks represented by one retained snapshot. Use 0 to disable the weekly tier.", "4", "4", false),
			makeField("Keep monthly", "Number of distinct recent calendar months represented by one retained snapshot. Use 0 to disable the monthly tier.", "12", "12", false),
			makeField("Keep yearly", "Number of distinct recent calendar years represented by one retained snapshot. Use 0 to disable the yearly tier.", "3", "3", false),
			makeField("Grace duration", "Minimum age of an unreferenced chunk before it may be removed, protecting recently uploaded data. Use a Go duration such as 24h or 168h.", "24h", "24h", false),
		}
	case actionInit:
		form.title = "Initialize repository"
		form.subtitle = "Writes key-check metadata to the connected path; it does not touch unrelated data."
		form.fields = []actionField{makeField("Bind existing data", "Enter yes only for a repository that already contains data but lacks key-check metadata. Confirm the original credentials first; binding does not prove all old data is readable.", "no", "no", false)}
	case actionReplicate:
		form.title = "Replicate to offsite storage"
		form.subtitle = "Copies packs, manifests, and the encrypted CID directory to the remote; it never deletes data at either end and is safe to repeat."
		form.fields = remoteFields()
	case actionRecover:
		form.title = "Recover from offsite storage"
		form.subtitle = "Rebuilds the connected repository path entirely from the remote. That path must be empty; content is only readable again if the remote has a replicated CID directory."
		form.fields = remoteFields()
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
		if field.optional {
			continue
		}
		if strings.TrimSpace(field.input.Value()) == "" {
			return fmt.Errorf("%s is required", strings.ToLower(field.label))
		}
	}
	switch f.kind {
	case actionBackup:
		policy := strings.ToLower(f.fields[2].input.Value())
		if policy != "fail" && policy != "skip" {
			return fmt.Errorf("permission policy must be 'fail' or 'skip'")
		}
		workersStr := strings.TrimSpace(f.fields[3].input.Value())
		if workersStr != "" {
			if _, err := strconv.Atoi(workersStr); err != nil {
				return fmt.Errorf("workers must be a non-negative integer")
			}
		}
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
	case actionReplicate, actionRecover:
		backend := strings.ToLower(f.fields[0].input.Value())
		if backend != "minio" && backend != "s3" {
			return fmt.Errorf("remote backend must be minio (or s3)")
		}
		useTLS := strings.ToLower(f.fields[6].input.Value())
		if useTLS != "yes" && useTLS != "no" && useTLS != "y" && useTLS != "n" {
			return fmt.Errorf("use tls must be yes or no")
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
	subtitle := f.subtitle
	if subtitle == "" {
		subtitle = "Review inputs before starting the operation."
	}
	lines := []string{sectionTitleStyle.Render(f.title), mutedStyle.Render(subtitle), ""}
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
		policy := "fail"
		if len(values) > 2 && values[2] != "" {
			policy = values[2]
		}
		workers := "auto"
		if len(values) > 3 && values[3] != "" {
			workers = values[3]
		}
		return []string{
			"Action       Create encrypted snapshot",
			"Source       " + values[0],
			"Retention    " + values[1],
			"Permission   " + policy,
			"Workers      " + workers,
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
	case actionReplicate:
		return []string{
			"Action       Copy packs, manifests, and CID directory to remote",
			"Remote       " + values[0] + " @ " + values[1] + "/" + values[2],
			"Data policy  Never deletes local or remote data; safe to repeat",
		}
	case actionRecover:
		return []string{
			"Action       Rebuild connected repository path from remote",
			"Remote       " + values[0] + " @ " + values[1] + "/" + values[2],
			"Requirement  Connected path must be empty or uninitialized",
			"Data policy  Full content recovery needs a replicated CID directory",
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
