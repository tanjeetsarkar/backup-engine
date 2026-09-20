package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~/"))
}

func completePath(input string) ([]string, error) {
	expanded := expandHome(strings.TrimSpace(input))
	if expanded == "" {
		expanded = "."
	}

	directory := expanded
	prefix := ""
	if !strings.HasSuffix(expanded, string(os.PathSeparator)) {
		directory = filepath.Dir(expanded)
		prefix = filepath.Base(expanded)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("cannot browse %s: %w", directory, err)
	}

	matches := make([]string, 0)
	for _, entry := range entries {
		if !fuzzyMatch(strings.ToLower(entry.Name()), strings.ToLower(prefix)) {
			continue
		}
		candidate := filepath.Join(directory, entry.Name())
		if entry.IsDir() {
			candidate += string(os.PathSeparator)
		}
		matches = append(matches, candidate)
	}
	sort.Strings(matches)
	return matches, nil
}

func fuzzyMatch(candidate, query string) bool {
	if query == "" || strings.HasPrefix(candidate, query) {
		return true
	}
	queryRunes := []rune(query)
	queryIndex := 0
	for _, character := range candidate {
		if queryIndex < len(queryRunes) && character == queryRunes[queryIndex] {
			queryIndex++
		}
	}
	return queryIndex == len(queryRunes)
}
