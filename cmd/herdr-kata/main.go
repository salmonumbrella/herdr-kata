package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout)) }

func run(args []string, out io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(out, "usage: herdr-kata <up|serve|stop|run|release|logs|report|exec-step|doctor|service|version>")
		return 2
	}
	configPath, statePath := "", ""
	for len(args) > 0 && strings.HasPrefix(args[0], "--") {
		if len(args) < 2 || (args[0] != "--config" && args[0] != "--state") {
			fmt.Fprintln(out, "expected --config <path> or --state <path>")
			return 2
		}
		if args[0] == "--config" {
			configPath = args[1]
		} else {
			statePath = args[1]
		}
		args = args[2:]
	}
	if len(args) == 0 {
		fmt.Fprintln(out, "command required")
		return 2
	}
	switch args[0] {
	case "service":
		if len(args) < 2 || (args[1] != "install" && args[1] != "uninstall") || (len(args) != 2 && !(len(args) == 3 && args[1] == "install" && args[2] == "--print")) {
			fmt.Fprintln(out, "usage: herdr-kata [--config <path>] [--state <path>] service <install [--print]|uninstall>")
			return 2
		}
		return operatorCommand(args, configPath, statePath, out)
	case "report":
		return report(args[1:], out)
	case "exec-step":
		return execStep(args[1:])
	case "version":
		fmt.Fprintln(out, "herdr-kata 0.3.0")
		return 0
	case "up", "serve", "stop", "run", "release", "logs", "doctor":
		return operatorCommand(args, configPath, statePath, out)
	default:
		fmt.Fprintf(out, "unknown command: %s\n", args[0])
		return 2
	}
}
