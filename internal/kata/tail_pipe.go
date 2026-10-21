//go:build linux || darwin

package kata

import (
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// The worker holds the only write end. EOF wakes the watchdog even when the
// worker was killed before the wrapper started or the event stream is idle.
// Disabling job control keeps the wrapper, watchdog and tail in one isolated
// group. The watchdog belongs to that group, so its group ID cannot be reused
// before kill(0) stops every member.
const tailPipeScript = `set +m
( IFS= read -r ignored <&3; kill -KILL 0 ) >/dev/null 2>&1 &
watchdog=$!
trap 'kill -KILL "$watchdog" 2>/dev/null; wait "$watchdog" 2>/dev/null' EXIT
"$@" 3<&- &
wait "$!"
`

func configureTailPipe(cmd *exec.Cmd) (func(), error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stop := sync.OnceFunc(func() { _ = writer.Close(); _ = reader.Close() })
	argv := append([]string{cmd.Path}, cmd.Args[1:]...)
	cmd.Path = "/bin/sh"
	cmd.Args = append([]string{"sh", "-c", tailPipeScript, "herdr-kata-tail"}, argv...)
	cmd.ExtraFiles = []*os.File{reader}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Cancellation must wake the watchdog, rather than killing only its shell.
	cmd.Cancel = func() error { stop(); return nil }
	return stop, nil
}
