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
	if phrase, _ := confirmationFor(safetyFast, form); phrase != "GC" {
		t.Fatalf("fast phrase = %q, want GC", phrase)
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

func TestRecoverConfirmationByProfile(t *testing.T) {
	form := newActionForm(actionRecover)

	if phrase, _ := confirmationFor(safetyStrict, form); phrase != "RECOVER" {
		t.Fatalf("strict phrase = %q, want RECOVER", phrase)
	}
	if phrase, _ := confirmationFor(safetyStandard, form); phrase != "yes" {
		t.Fatalf("standard phrase = %q, want yes", phrase)
	}
}

func TestReplicateAndScrubHaveNoConfirmation(t *testing.T) {
	form := newActionForm(actionReplicate)
	if phrase, _ := confirmationFor(safetyStrict, form); phrase != "" {
		t.Fatalf("replicate strict phrase = %q, want none (non-destructive)", phrase)
	}
}

func TestRemoteFieldValidation(t *testing.T) {
	form := newActionForm(actionReplicate)
	// Required remote fields are blank by default except backend and use-tls.
	if err := form.validate(); err == nil {
		t.Fatal("expected validation error for missing required remote fields")
	}

	form.fields[1].input.SetValue("localhost:9000")
	form.fields[2].input.SetValue("bucket")
	form.fields[4].input.SetValue("access")
	form.fields[5].input.SetValue("secret")
	if err := form.validate(); err != nil {
		t.Fatalf("expected valid form, got %v", err)
	}

	form.fields[0].input.SetValue("not-a-backend")
	if err := form.validate(); err == nil {
		t.Fatal("expected error for invalid remote backend")
	}
	form.fields[0].input.SetValue("minio")

	form.fields[6].input.SetValue("maybe")
	if err := form.validate(); err == nil {
		t.Fatal("expected error for invalid use-tls value")
	}
}
