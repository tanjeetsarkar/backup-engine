package index

import (
	"testing"
	"time"
)

func TestTransactionHistoryOrderingAndLimit(t *testing.T) {
	idx := openTestDB(t)
	defer idx.Close()

	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for i, label := range []string{"first", "second", "third"} {
		when := base.Add(time.Duration(i) * time.Minute)
		if err := idx.PutTransactionLog(when, []byte(label)); err != nil {
			t.Fatalf("PutTransactionLog(%s): %v", label, err)
		}
	}

	all, err := idx.ListTransactionLogs(0)
	if err != nil {
		t.Fatalf("ListTransactionLogs: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(all))
	}
	// Newest first.
	if string(all[0]) != "third" || string(all[1]) != "second" || string(all[2]) != "first" {
		t.Fatalf("unexpected order: %q %q %q", all[0], all[1], all[2])
	}

	limited, err := idx.ListTransactionLogs(2)
	if err != nil {
		t.Fatalf("ListTransactionLogs(2): %v", err)
	}
	if len(limited) != 2 || string(limited[0]) != "third" || string(limited[1]) != "second" {
		t.Fatalf("unexpected limited result: %v", limited)
	}
}

func TestTransactionHistorySameTimestampOrderedBySequence(t *testing.T) {
	idx := openTestDB(t)
	defer idx.Close()

	when := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	if err := idx.PutTransactionLog(when, []byte("a")); err != nil {
		t.Fatalf("PutTransactionLog a: %v", err)
	}
	if err := idx.PutTransactionLog(when, []byte("b")); err != nil {
		t.Fatalf("PutTransactionLog b: %v", err)
	}

	all, err := idx.ListTransactionLogs(0)
	if err != nil {
		t.Fatalf("ListTransactionLogs: %v", err)
	}
	if len(all) != 2 || string(all[0]) != "b" || string(all[1]) != "a" {
		t.Fatalf("expected insertion-ordered entries newest first, got %v", all)
	}
}

func TestDeleteTransactionLogsBeforeCutoff(t *testing.T) {
	idx := openTestDB(t)
	defer idx.Close()

	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	old := base
	recent := base.Add(time.Hour)
	if err := idx.PutTransactionLog(old, []byte("old")); err != nil {
		t.Fatalf("PutTransactionLog old: %v", err)
	}
	if err := idx.PutTransactionLog(recent, []byte("recent")); err != nil {
		t.Fatalf("PutTransactionLog recent: %v", err)
	}

	removed, err := idx.DeleteTransactionLogsBefore(base.Add(30 * time.Minute))
	if err != nil {
		t.Fatalf("DeleteTransactionLogsBefore: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected 1 removed entry, got %d", removed)
	}

	remaining, err := idx.ListTransactionLogs(0)
	if err != nil {
		t.Fatalf("ListTransactionLogs: %v", err)
	}
	if len(remaining) != 1 || string(remaining[0]) != "recent" {
		t.Fatalf("expected only the recent entry to remain, got %v", remaining)
	}
}
