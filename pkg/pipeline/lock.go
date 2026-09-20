package pipeline

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var ErrRepositoryBusy = errors.New("repository is busy with another mutation")

type mutationLock struct {
	file *os.File
}

func acquireMutationLock(repoDir string) (*mutationLock, error) {
	if err := os.MkdirAll(repoDir, 0750); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(repoDir, ".mutation.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrRepositoryBusy
		}
		return nil, fmt.Errorf("lock repository: %w", err)
	}
	return &mutationLock{file: file}, nil
}

func (lock *mutationLock) release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
	closeErr := lock.file.Close()
	return errors.Join(unlockErr, closeErr)
}
