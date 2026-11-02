package main

import (
	"fmt"
	"io"
	"os"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout)) }

func run(args []string, out io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(out, "usage: herdr-kata <up|serve|stop|run|release|logs|report|exec-step|doctor|version>")
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(out, "herdr-kata v2")
		return 0
	case "up", "serve", "stop", "run", "release", "logs", "report", "exec-step", "doctor":
		fmt.Fprintln(out, "not implemented")
		return 2
	default:
		fmt.Fprintf(out, "unknown command: %s\n", args[0])
		return 2
	}
}
