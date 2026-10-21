package state

import (
	"path/filepath"
	"testing"
)

func TestInheritedLockRetainsOwnership(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lock")
	l, ok, err := TryLock(p)
	if err != nil || !ok {
		t.Fatal(err)
	}
	fd, err := l.Inherit()
	if err != nil {
		t.Fatal(err)
	}
	if err := l.file.Close(); err != nil {
		t.Fatal(err)
	}
	l.file = nil
	heldBefore, err := Held(p)
	if err != nil || !heldBefore {
		t.Fatal("lock gap before adoption")
	}
	adopted, err := AdoptLock(p, fd)
	if err != nil {
		t.Fatal(err)
	}
	defer adopted.Unlock()
	held, err := Held(p)
	if err != nil || !held {
		t.Fatal("inherited ownership lost")
	}
}
func TestInheritedLockRejectsOtherFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lock")
	l, _, _ := TryLock(p)
	defer l.Unlock()
	fd, err := l.Inherit()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AdoptLock(filepath.Join(t.TempDir(), "other"), fd); err == nil {
		t.Fatal("foreign file adopted")
	}
}
