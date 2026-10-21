package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/version"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("herdr-kata", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: herdr-kata <up|stop|status|doctor|run|resume|release|abandon|runs|logs|tui|open-kata|report|version>")
	}
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	args = flags.Args()
	if len(args) == 0 {
		flags.Usage()
		return 2
	}
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") && (args[0] == "open-kata" || args[0] == "version" || args[0] == "env-names") {
		synopsis := args[0]
		if args[0] == "env-names" {
			synopsis += " <path>"
		}
		fmt.Fprintf(stderr, "usage: herdr-kata %s\n", synopsis)
		return 0
	}
	switch args[0] {
	case "env-names":
		if len(args) != 2 {
			return 2
		}
		f, err := os.OpenFile(args[1], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return 1
		}
		err = herdr.WriteEnvironmentNames(f, os.Environ())
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			return 1
		}
		return 0
	case "runs":
		return runsCommand(args[1:], stdout, stderr)
	case "logs":
		return logsCommand(args[1:], stdout, stderr)
	case "tui", "board", "board-wide":
		return tuiCommand(args, stdout, stderr)
	case "open-kata":
		return openKataCommand(args[1:], stdout, stderr)
	case "run":
		return runCommand(args[1:], stdout, stderr)
	case "resume":
		return resumeCommand(args[1:], stdout, stderr)
	case "release":
		return releaseCommand(args[1:], stdout, stderr)
	case "abandon":
		return abandonCommand(args[1:], stdout, stderr)

	case "report":
		return reportCommand(args[1:], stdout, stderr)
	case "up", "serve", "worker", "stop", "status", "doctor":
		return runtimeCommand(args, stdout, stderr)
	case "version":
		if len(args) != 1 {
			fmt.Fprintln(stderr, "usage: herdr-kata version")
			return 2
		}
		fmt.Fprintf(stdout, "herdr-kata %s\n", version.String())
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		return 2
	}
}
