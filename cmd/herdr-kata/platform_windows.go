//go:build windows

package main

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// terminatePID stops a process. Windows has no SIGTERM to hand an unrelated
// process, so this is a hard terminate rather than the graceful wind-down the
// Unix build gets. A process that is already gone is success, not failure.
func terminatePID(pid int) error {
	if !pidAlive(pid) {
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return nil // already gone
	}
	defer p.Release()
	if err := p.Kill(); err != nil {
		if !pidAlive(pid) {
			return nil // raced with its own exit
		}
		return err
	}
	return nil
}

// waitTimeout is what WaitForSingleObject returns when the process handle is not
// signaled — i.e. the process is still running.
const waitTimeout = uint32(0x00000102) // WAIT_TIMEOUT

// pidAlive reports whether a pid still names a live process. It opens the
// process for SYNCHRONIZE and asks whether the handle has signaled; a handle
// that times out (has not signaled) belongs to a process still running. This
// avoids the STILL_ACTIVE(259) ambiguity a plain exit-code check has.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		// ERROR_INVALID_PARAMETER is the "no such pid" answer. Any other
		// failure — ERROR_ACCESS_DENIED for a process we may not open (a
		// higher-integrity peer), say — means the process exists, so report it
		// alive rather than silently treating it as gone. That keeps
		// terminatePID from reporting success without signalling anything.
		return !errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer windows.CloseHandle(h)
	s, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return false
	}
	return s == waitTimeout
}

// createNoWindow is CREATE_NO_WINDOW: the child gets a console that is never
// shown. DETACHED_PROCESS gave it none at all, which looks equivalent until the
// daemon runs a console program — herdr, every tick — and Windows allocates a
// fresh visible console for each one, flashing a window every five seconds.
// A hidden console is inherited by those children instead.
const createNoWindow = 0x08000000

// detachSysProcAttr puts a spawned child in its own process group with a
// hidden console, so it outlives whatever started it — the Windows counterpart
// of a new session.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | createNoWindow,
	}
}
