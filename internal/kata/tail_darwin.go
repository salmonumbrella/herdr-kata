package kata

import "os/exec"

func configureTailProcess(cmd *exec.Cmd) (func(), error) {
	return configureTailPipe(cmd)
}
