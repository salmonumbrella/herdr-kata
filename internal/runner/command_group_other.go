//go:build !darwin

package runner

import "syscall"

func commandGroupMembers(int) (int, error) {
	return 0, syscall.ENOTSUP
}
