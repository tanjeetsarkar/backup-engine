package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestBackupAndRestorePreservesSymlinksAndXAttrs(t *testing.T) {
	repo := t.TempDir()
	sourceDir := t.TempDir()
	restoreDir := t.TempDir()

	targetPath := filepath.Join(sourceDir, "target.txt")
	if err := os.WriteFile(targetPath, []byte("symlink target content"), 0640); err != nil {
		t.Fatalf("WriteFile target: %v", err)
	}
	linkPath := filepath.Join(sourceDir, "link.txt")
	if err := os.Symlink("target.txt", linkPath); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	xattrSupported := true
	if err := unix.Setxattr(targetPath, "user.backup_test", []byte("attribute-value"), 0); err != nil {
		if isXattrUnsupported(err) {
			xattrSupported = false
		} else {
			t.Fatalf("Setxattr: %v", err)
		}
	}

	engine, err := Open(EngineConfig{RepoDir: repo, Passphrase: []byte("passphrase"), Salt: []byte("0123456789abcdef")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer engine.Close()

	snapshotID, err := engine.BackupPath(context.Background(), sourceDir, nil, []string{"DAILY"})
	if err != nil {
		t.Fatalf("BackupPath: %v", err)
	}
	if err := engine.RestoreSnapshot(context.Background(), snapshotID, restoreDir); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}

	restoredLink := filepath.Join(restoreDir, "link.txt")
	linkInfo, err := os.Lstat(restoredLink)
	if err != nil {
		t.Fatalf("Lstat restored link: %v", err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected restored link.txt to remain a symlink, got mode %v", linkInfo.Mode())
	}
	dest, err := os.Readlink(restoredLink)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if dest != "target.txt" {
		t.Fatalf("expected symlink target %q, got %q", "target.txt", dest)
	}

	restoredTarget := filepath.Join(restoreDir, "target.txt")
	content, err := os.ReadFile(restoredTarget)
	if err != nil {
		t.Fatalf("ReadFile restored target: %v", err)
	}
	if string(content) != "symlink target content" {
		t.Fatalf("unexpected restored target content: %q", content)
	}

	if xattrSupported {
		size, err := unix.Getxattr(restoredTarget, "user.backup_test", nil)
		if err != nil {
			t.Fatalf("Getxattr size: %v", err)
		}
		buf := make([]byte, size)
		if _, err := unix.Getxattr(restoredTarget, "user.backup_test", buf); err != nil {
			t.Fatalf("Getxattr: %v", err)
		}
		if string(buf) != "attribute-value" {
			t.Fatalf("expected restored xattr value %q, got %q", "attribute-value", buf)
		}
	}
}
