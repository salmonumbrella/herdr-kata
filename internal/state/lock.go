package state

import (
	"errors"
	"os"
	"syscall"
)

// Lock retains an open descriptor; lock files must never be removed while in use.
type Lock struct{ file *os.File }

func TryLock(path string) (*Lock, bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, false, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		if err == nil {
			err = errors.New("lock is not a regular file")
		}
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &Lock{file: f}, true, nil
}
func (l *Lock) Unlock() error {
	if l.file == nil {
		return nil
	}
	f := l.file
	l.file = nil
	return errors.Join(syscall.Flock(int(f.Fd()), syscall.LOCK_UN), f.Close())
}
func Held(path string) (bool, error) {
	l, ok, err := TryLock(path)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}
	return false, l.Unlock()
}
