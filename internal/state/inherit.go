package state

import (
	"fmt"
	"os"
	"syscall"
)

// Inherit duplicates the locked open file description without FD_CLOEXEC.
// The caller closes the duplicate if exec fails; no LOCK_UN is sent before exec.
func (l *Lock) Inherit() (int, error) {
	if l.file == nil {
		return -1, fmt.Errorf("lock is closed")
	}
	return syscall.Dup(int(l.file.Fd()))
}

// AdoptLock validates an inherited descriptor against the current lock inode,
// then restores CLOEXEC so ordinary child processes cannot retain this lock.
func AdoptLock(path string, fd int) (*Lock, error) {
	if fd < 3 {
		return nil, fmt.Errorf("invalid inherited lock descriptor")
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		return nil, fmt.Errorf("invalid inherited lock descriptor")
	}
	failed := true
	defer func() {
		if failed {
			f.Close()
		}
	}()
	got, err := f.Stat()
	if err != nil {
		return nil, err
	}
	want, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !got.Mode().IsRegular() || !os.SameFile(got, want) {
		return nil, fmt.Errorf("inherited descriptor differs from lock file")
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	failed = false
	return &Lock{file: f}, nil
}
