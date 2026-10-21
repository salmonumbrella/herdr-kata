package runner

import (
	"syscall"
	"testing"
)

func TestGroupGoneAfterKernelMemberRemoval(t *testing.T) {
	// A retained group object can outlive its final process. A failed signal
	// lookup alone cannot distinguish that state from surviving descendants.
	const pgid = 123
	count := 1
	queried := false
	gone := groupGone(pgid, func(pid int, signal syscall.Signal) error {
		if pid != -pgid || signal != 0 {
			t.Fatalf("signal pid=%d signal=%v", pid, signal)
		}
		count = 0 // Final member removed while the kernel still retains the group.
		return syscall.EPERM
	}, func(group int) (int, error) {
		queried = true
		if group != pgid {
			t.Fatalf("queried group=%d", group)
		}
		return count, nil
	})
	if !gone || !queried {
		t.Fatalf("empty group rejected: gone=%v queried=%v", gone, queried)
	}
}

func TestGroupGoneRefusesUnprovenAbsence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		signal error
		count  int
		query  error
	}{
		{name: "live member", count: 1},
		{name: "permission denied with member", signal: syscall.EPERM, count: 1},
		{name: "membership denied", signal: syscall.EPERM, query: syscall.EPERM},
		{name: "membership unsupported", signal: syscall.EPERM, query: syscall.ENOTSUP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if groupGone(123, func(int, syscall.Signal) error { return tc.signal }, func(int) (int, error) { return tc.count, tc.query }) {
				t.Fatal("unproven group absence allowed retry")
			}
		})
	}
}

func TestGroupGoneUsesSignalAbsenceProof(t *testing.T) {
	if !groupGone(123, func(int, syscall.Signal) error { return syscall.ESRCH }, func(int) (int, error) { t.Fatal("queried after ESRCH"); return 0, nil }) {
		t.Fatal("ESRCH did not prove absence")
	}
	for _, pgid := range []int{0, -1} {
		if groupGone(pgid, func(int, syscall.Signal) error { t.Fatal("signaled invalid group"); return nil }, func(int) (int, error) { t.Fatal("queried invalid group"); return 0, nil }) {
			t.Fatal("invalid group proved absent")
		}
	}
}
