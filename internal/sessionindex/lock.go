package sessionindex

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

var ErrLockBusy = errors.New("session index lock busy")

type FileLock struct {
	file *os.File
}

func LockPath(indexPath string) string {
	return indexPath + ".lock"
}

func lockFile(indexPath string, nonblocking bool) (*FileLock, error) {
	if err := os.MkdirAll(filepath.Dir(indexPath), 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(LockPath(indexPath), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	op := syscall.LOCK_EX
	if nonblocking {
		op |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), op); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLockBusy
		}
		return nil, err
	}
	return &FileLock{file: f}, nil
}

func Lock(indexPath string) (*FileLock, error) {
	return lockFile(indexPath, false)
}

func TryLock(indexPath string) (*FileLock, error) {
	return lockFile(indexPath, true)
}

func (l *FileLock) Unlock() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return err
	}
	return closeErr
}

func WithLockedIndex(indexPath string, fn func(raws []json.RawMessage, sessions []Session) ([]json.RawMessage, bool, error)) (bool, error) {
	return withLockedIndex(indexPath, false, fn)
}

func TryWithLockedIndex(indexPath string, fn func(raws []json.RawMessage, sessions []Session) ([]json.RawMessage, bool, error)) (bool, error) {
	return withLockedIndex(indexPath, true, fn)
}

func withLockedIndex(indexPath string, nonblocking bool, fn func(raws []json.RawMessage, sessions []Session) ([]json.RawMessage, bool, error)) (bool, error) {
	var l *FileLock
	var err error
	if nonblocking {
		l, err = TryLock(indexPath)
	} else {
		l, err = Lock(indexPath)
	}
	if err != nil {
		return false, err
	}
	defer l.Unlock()

	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		raws, changed, err := fn(nil, nil)
		if err != nil || !changed {
			return false, err
		}
		return true, WriteAllBytes(indexPath, raws)
	}

	raws, sessions, err := ReadAllBytes(indexPath)
	if err != nil {
		return false, err
	}
	next, changed, err := fn(raws, sessions)
	if err != nil || !changed {
		return false, err
	}
	return true, WriteAllBytes(indexPath, next)
}
