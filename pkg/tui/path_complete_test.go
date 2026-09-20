package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCompletePathMatchesPrefixAndFuzzyQuery(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"documents", "downloads", "pictures"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	prefixMatches, err := completePath(filepath.Join(root, "doc"))
	if err != nil {
		t.Fatal(err)
	}
	if len(prefixMatches) != 1 || prefixMatches[0] != filepath.Join(root, "documents")+string(os.PathSeparator) {
		t.Fatalf("prefix matches = %v", prefixMatches)
	}

	fuzzyMatches, err := completePath(filepath.Join(root, "dwn"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fuzzyMatches) != 1 || fuzzyMatches[0] != filepath.Join(root, "downloads")+string(os.PathSeparator) {
		t.Fatalf("fuzzy matches = %v", fuzzyMatches)
	}
}

func TestCompletePathReportsUnreadableParent(t *testing.T) {
	_, err := completePath(filepath.Join(t.TempDir(), "missing", "entry"))
	if err == nil {
		t.Fatal("expected missing parent error")
	}
}

func TestFuzzyMatch(t *testing.T) {
	tests := []struct {
		candidate string
		query     string
		want      bool
	}{
		{candidate: "portfolio", query: "port", want: true},
		{candidate: "portfolio", query: "ptfo", want: true},
		{candidate: "portfolio", query: "xyz", want: false},
	}
	for _, test := range tests {
		if got := fuzzyMatch(test.candidate, test.query); got != test.want {
			t.Errorf("fuzzyMatch(%q, %q) = %v, want %v", test.candidate, test.query, got, test.want)
		}
	}
}

func TestActionFormTabHighlightsFirstPathMatch(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"documents", "documentation"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	form := newActionForm(actionBackup)
	form.fields[0].input.SetValue(filepath.Join(root, "doc"))

	form, _ = form.Update(tea.KeyMsg{Type: tea.KeyTab})
	if len(form.suggestions) != 2 {
		t.Fatalf("suggestions = %v", form.suggestions)
	}
	if form.suggestion != 0 {
		t.Fatalf("selected suggestion = %d, want 0", form.suggestion)
	}
}
