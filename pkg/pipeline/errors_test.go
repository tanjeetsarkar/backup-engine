package pipeline

import (
	"context"
	"fmt"
	"testing"
)

func TestPresentErrorHandlesWrappedKnownFailures(t *testing.T) {
	tests := []struct {
		err     error
		summary string
	}{
		{err: fmt.Errorf("open: %w", ErrRepositoryKeyMismatch), summary: "Repository credentials do not match"},
		{err: fmt.Errorf("prepare: %w", ErrRepositoryNeedsInit), summary: "Repository key-check metadata is missing"},
		{err: fmt.Errorf("backup: %w", context.Canceled), summary: "Operation cancelled"},
	}
	for _, test := range tests {
		presentation := PresentError(test.err)
		if presentation.Summary != test.summary || presentation.Cause == "" || presentation.Hint == "" {
			t.Errorf("PresentError(%v) = %+v", test.err, presentation)
		}
	}
}
