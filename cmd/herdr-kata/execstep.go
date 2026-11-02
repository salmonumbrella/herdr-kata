package main

import "github.com/salmonumbrella/herdr-kata/internal/stepexec"

func execStep(args []string) int {
	if len(args) < 3 || args[1] != "--" {
		return 2
	}
	return stepexec.Run(args[0], args[2:])
}
