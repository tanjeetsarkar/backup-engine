package tui

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Run launches the full-screen terminal interface.
func Run(ctx context.Context) error {
	_, err := tea.NewProgram(newModel(ctx), tea.WithAltScreen()).Run()
	return err
}

func splitTags(raw string) []string {
	parts := strings.Split(raw, ",")
	tags := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			tags = append(tags, trimmed)
		}
	}
	if len(tags) == 0 {
		return []string{"DAILY"}
	}
	return tags
}

func parseHexID(hexID string) ([32]byte, error) {
	var snapshotID [32]byte
	raw, err := hex.DecodeString(hexID)
	if err != nil {
		return snapshotID, fmt.Errorf("invalid snapshot id hex: %w", err)
	}
	if len(raw) != len(snapshotID) {
		return snapshotID, fmt.Errorf("invalid snapshot id length: expected 32 bytes")
	}
	copy(snapshotID[:], raw)
	return snapshotID, nil
}
