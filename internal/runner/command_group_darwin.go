package runner

import "golang.org/x/sys/unix"

func commandGroupMembers(pgid int) (int, error) {
	// Unlike signal-zero's group lookup, this enumerates process members,
	// including zombies. A retained empty group is not a surviving command.
	members, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
	return len(members), err
}
