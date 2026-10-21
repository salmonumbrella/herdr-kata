package runner

import "syscall"

// GroupGone requires proof that a command process group has no members.
func GroupGone(pgid int) bool {
	return groupGone(pgid, syscall.Kill, commandGroupMembers)
}

func groupGone(pgid int, signal func(int, syscall.Signal) error, members func(int) (int, error)) bool {
	if pgid <= 0 {
		return false
	}
	if signal(-pgid, 0) == syscall.ESRCH {
		return true
	}
	// A failed signal check does not prove that members remain. Some kernels
	// retain an empty group object; only a successful empty membership query
	// provides an alternate absence proof.
	count, err := members(pgid)
	return err == nil && count == 0
}
