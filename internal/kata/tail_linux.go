package kata

import (
	"os/exec"
	"syscall"
)

func configureTailProcess(cmd *exec.Cmd) (func(), error) {
	// Context cancellation cannot clean up after a killed worker. The stream
	// may be idle indefinitely, so a broken output pipe is not enough either.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	return func() {}, nil
}
