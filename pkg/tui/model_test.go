package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/tanjeetsarkar/backup-engine/pkg/pipeline"
)

func TestModelNavigationOpensRepositoryForm(t *testing.T) {
	current := newModel(context.Background())

	updated, _ := current.Update(tea.KeyMsg{Type: tea.KeyDown})
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
