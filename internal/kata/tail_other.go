//go:build !linux && !darwin

package kata

import "os/exec"

func configureTailProcess(*exec.Cmd) (func(), error) { return func() {}, nil }
