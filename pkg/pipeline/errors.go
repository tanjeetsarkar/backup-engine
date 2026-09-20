package pipeline

import (
	"context"
	"errors"
	"os"
	"strings"
)

// ErrorPresentation is a safe, actionable explanation for an operation failure.
type ErrorPresentation struct {
	Summary string
	Cause   string
	Hint    string
}

// PresentError maps wrapped technical failures to user-facing recovery guidance.
func PresentError(err error) ErrorPresentation {
	if err == nil {
		return ErrorPresentation{}
	}
	presentation := ErrorPresentation{Summary: "Operation failed", Cause: err.Error(), Hint: "Review the operation inputs and run it again."}
	switch {
	case errors.Is(err, context.Canceled):
		presentation.Summary = "Operation cancelled"
		presentation.Hint = "No further work was started. Review partial output before retrying."
	case errors.Is(err, ErrRepositoryKeyMismatch):
		presentation.Summary = "Repository credentials do not match"
		presentation.Hint = "Reconnect using the passphrase and salt originally used for this repository."
	case errors.Is(err, ErrRepositoryNeedsInit):
		presentation.Summary = "Repository key-check metadata is missing"
		presentation.Hint = "Open Setup and bind the existing repository only after confirming its credentials."
	case errors.Is(err, os.ErrPermission):
		presentation.Summary = "Permission denied"
		presentation.Hint = "Check read/write permissions for the source, destination, and repository paths."
	case strings.Contains(strings.ToLower(err.Error()), "snapshot not found"):
		presentation.Summary = "Snapshot was not found"
		presentation.Hint = "Refresh the snapshot catalog and select an existing snapshot."
	case strings.Contains(strings.ToLower(err.Error()), "authentication failed"):
		presentation.Summary = "Encrypted data could not be authenticated"
		presentation.Hint = "Verify repository credentials. If they are correct, run Doctor and restore damaged data from another copy."
	case strings.Contains(strings.ToLower(err.Error()), "hash mismatch") || strings.Contains(strings.ToLower(err.Error()), "decode error"):
		presentation.Summary = "Repository data appears corrupted"
		presentation.Hint = "Run Doctor, avoid garbage collection, and restore affected repository files from another copy."
	}
	return presentation
}
