package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/tanjeetsarkar/backup-engine/pkg/pipeline"
)

func TestModelNavigationOpensRepositoryForm(t *testing.T) {
	current := newModel(context.Background())

	updated, _ := current.Update(tea.KeyMsg{Type: tea.KeyDown})
	current = updated.(model)
	updated, _ = current.Update(tea.KeyMsg{Type: tea.KeyDown})
	current = updated.(model)
	if current.active != sectionContext {
		t.Fatalf("active section = %v, want repository", current.active)
	}

	updated, _ = current.Update(tea.KeyMsg{Type: tea.KeyEnter})
	current = updated.(model)
	if current.mode != modeContextForm {
		t.Fatalf("mode = %v, want context form", current.mode)
	}
}

func TestModelRecordsProgressAndShowsActionableFailure(t *testing.T) {
	current := newModel(context.Background())
	current.mode = modeBusy
	events := make(chan tea.Msg)
	current.progressStream = events

	for index := 0; index < 205; index++ {
		updated, _ := current.Update(progressEventMsg{event: pipeline.ProgressEvent{
			Operation: pipeline.OperationBackup,
			Phase:     pipeline.PhaseProcessing,
			Message:   "Processing files",
			Time:      time.Unix(int64(index), 0),
		}})
		current = updated.(model)
	}
	if len(current.activity) != 200 {
		t.Fatalf("activity length = %d, want 200", len(current.activity))
	}
	if current.progress.Operation != pipeline.OperationBackup {
		t.Fatalf("current progress = %+v", current.progress)
	}

	updated, _ := current.Update(taskResultMsg{title: "Backup transaction", err: pipeline.ErrRepositoryKeyMismatch})
	current = updated.(model)
	if current.mode != modeResult || current.result.next == "" || current.result.detail == "" {
		t.Fatalf("failure result = %+v mode=%v", current.result, current.mode)
	}
}

func TestControlCCancelsActiveOperation(t *testing.T) {
	current := newModel(context.Background())
	operationContext, cancel := context.WithCancel(context.Background())
	current.mode = modeBusy
	current.operationCancel = cancel

	updated, command := current.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	current = updated.(model)
	if command != nil {
		t.Fatal("ctrl+c returned a quit command during active work")
	}
	if operationContext.Err() != context.Canceled {
		t.Fatalf("operation context error = %v, want canceled", operationContext.Err())
	}
	if current.mode != modeBusy || current.status != "Cancelling operation..." {
		t.Fatalf("unexpected cancellation state: mode=%v status=%q", current.mode, current.status)
	}
}

func TestModelCyclesSafetyProfile(t *testing.T) {
	current := newModel(context.Background())
	if current.safety != safetyStandard {
		t.Fatalf("initial safety = %v, want standard", current.safety)
	}

	updated, _ := current.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	current = updated.(model)
	if current.safety != safetyFast {
		t.Fatalf("cycled safety = %v, want fast", current.safety)
	}
}

func TestContextFormRejectsShortSalt(t *testing.T) {
	form := newContextForm(repositoryContext{})
	form.inputs[0].SetValue("/tmp/repo")
	form.inputs[1].SetValue("passphrase")
	form.inputs[2].SetValue("short")

	if err := form.validate(); err == nil {
		t.Fatal("expected short salt validation error")
	}
}

func TestEveryInputHasUserGuidance(t *testing.T) {
	contextForm := newContextForm(repositoryContext{})
	if len(contextFieldDescriptions) != len(contextForm.inputs) {
		t.Fatalf("context descriptions = %d, inputs = %d", len(contextFieldDescriptions), len(contextForm.inputs))
	}
	for index, description := range contextFieldDescriptions {
		if strings.TrimSpace(description) == "" {
			t.Errorf("context input %d has no description", index)
		}
	}

	for _, kind := range []actionKind{actionBackup, actionRestore, actionGC, actionInit} {
		form := newActionForm(kind)
		for _, field := range form.fields {
			if strings.TrimSpace(field.description) == "" {
				t.Errorf("%q field %q has no description", form.title, field.label)
			}
		}
	}

	if strings.TrimSpace(confirmationInputDescription) == "" {
		t.Fatal("confirmation input does not explain its purpose")
	}

	if strings.TrimSpace(snapshotFilterDescription) == "" {
		t.Fatal("snapshot filter does not explain its effect")
	}
}

func TestDescribedFormsFitStandardContentHeight(t *testing.T) {
	forms := map[string]string{
		"repository": newContextForm(repositoryContext{}).View(),
		"retention":  newActionForm(actionGC).View(),
	}
	for name, view := range forms {
		if lines := strings.Count(view, "\n") + 1; lines > 25 {
			t.Errorf("%s form uses %d lines, want at most 25", name, lines)
		}
	}
}

func TestSnapshotFilterAndRestoreHandoff(t *testing.T) {
	current := newModel(context.Background())
	current.mode = modeSnapshots
	current.snapshotFilter = textinput.New()
	current.snapshotFilter.SetValue("auth")
	current.snapshots = []pipeline.SnapshotStatus{
		{ID: [32]byte{1}, Readable: true, StatusText: "ok"},
		{ID: [32]byte{2}, Readable: false, StatusText: "auth-failed"},
	}

	visible := current.visibleSnapshots()
	if len(visible) != 1 || visible[0].ID[0] != 2 {
		t.Fatalf("visible snapshots = %#v", visible)
	}

	updated, _ := current.Update(tea.KeyMsg{Type: tea.KeyEnter})
	current = updated.(model)
	if current.mode != modeActionForm || current.action.kind != actionRestore {
		t.Fatalf("restore handoff mode=%v kind=%v", current.mode, current.action.kind)
	}
	if value := current.action.fields[0].input.Value(); !strings.HasPrefix(value, "02") || len(value) != 64 {
		t.Fatalf("prefilled snapshot ID = %q", value)
	}
}
