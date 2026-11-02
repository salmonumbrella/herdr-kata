package main

import (
	"flag"
	"fmt"
	"github.com/oklog/ulid/v2"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"io"
)

func postRequest(args []string, st *journal.Store, out io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(out, "target required")
		return 2
	}
	r := journal.Request{ID: ulid.Make().String(), Kind: args[0]}
	if r.Kind == "run" {
		if _, err := ulid.ParseStrict(args[1]); err != nil {
			fmt.Fprintln(out, "job must be a full UID")
			return 2
		}
		r.JobUID = args[1]
		f := flag.NewFlagSet("run", flag.ContinueOnError)
		f.SetOutput(out)
		f.StringVar(&r.Input, "input", "", "input text")
		if err := f.Parse(args[2:]); err != nil {
			return 2
		}
		if f.NArg() != 0 {
			return 2
		}
		f.Visit(func(flag *flag.Flag) {
			if flag.Name == "input" {
				r.InputSet = true
			}
		})
	} else {
		if len(args) != 2 {
			return 2
		}
		run, err := st.Find(args[1])
		if err != nil {
			fmt.Fprintln(out, err)
			return 2
		}
		if run.Phase != "held" {
			fmt.Fprintln(out, "release requires a held run")
			return 2
		}
		r.Run = run.RunUID
	}
	if err := (journal.Requests{Dir: st.Layout.Requests()}).Write(r); err != nil {
		fmt.Fprintln(out, err)
		return 1
	}
	fmt.Fprintln(out, "queued", r.ID)
	return 0
}
