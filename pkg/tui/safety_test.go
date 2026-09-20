package tui

import (
	"os"
	"testing"
)

func TestGarbageCollectionConfirmationByProfile(t *testing.T) {
	form := newActionForm(actionGC)

	if phrase, _ := confirmationFor(safetyStrict, form); phrase != "GC" {
		t.Fatalf("strict phrase = %q, want GC", phrase)
	}
	if phrase, _ := confirmationFor(safetyStandard, form); phrase != "yes" {
		t.Fatalf("standard phrase = %q, want yes", phrase)
	}
	if phrase, _ := confirmationFor(safetyFast, form); phrase != "" {
		t.Fatalf("fast phrase = %q, want none", phrase)
	}
}

func TestRestoreExistingDestinationRequiresConfirmation(t *testing.T) {
	destination := t.TempDir()
	form := newActionForm(actionRestore)
	form.fields[1].input.SetValue(destination)

	phrase, _ := confirmationFor(safetyStrict, form)
	if phrase != "RESTORE" {
		t.Fatalf("strict phrase = %q, want RESTORE", phrase)
	}

	missing := destination + string(os.PathSeparator) + "missing"
	form.fields[1].input.SetValue(missing)
	phrase, _ = confirmationFor(safetyStrict, form)
	if phrase != "" {
		t.Fatalf("missing destination phrase = %q, want none", phrase)
	}
}
