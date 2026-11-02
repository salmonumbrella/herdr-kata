package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/salmonumbrella/herdr-kata/internal/prompt"
)

func report(args []string, out io.Writer) int {
	f := flag.NewFlagSet("report", flag.ContinueOnError)
	f.SetOutput(out)
	path := f.String("result", "", "result path")
	status := f.String("status", "", "succeeded, failed or needs-human")
	summary := f.String("summary", "", "summary")
	note := f.String("note", "", "one line")
	if err := f.Parse(args); err != nil {
		return 2
	}
	n, _ := strconv.Atoi(os.Getenv("KATA_CRON_ATTEMPT"))
	if !filepath.IsAbs(*path) || f.NArg() != 0 {
		fmt.Fprintln(out, "report requires an absolute --result path")
		return 2
	}
	r := prompt.Result{RunUID: os.Getenv("KATA_CRON_RUN"), Step: os.Getenv("KATA_CRON_STEP"), Attempt: n, Status: *status, Summary: *summary, Note: *note}
	if err := prompt.WriteResult(*path, r); err != nil {
		fmt.Fprintln(out, err)
		return 2
	}
	return 0
}
