package main

import (
	"context"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/attempt"
	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"io"
	"os"
	"path/filepath"
)

func logs(ctx context.Context, args []string, st *journal.Store, h hc.HC, out io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(out, "logs requires a run UID or key")
		return 2
	}
	r, err := st.Find(args[1])
	if err != nil {
		fmt.Fprintln(out, err)
		return 1
	}
	if len(r.Attempts) == 0 {
		fmt.Fprintf(out, "no attempts; phase %s; reason %s\n", r.Phase, r.Hold)
		return 1
	}
	a := r.Attempts[len(r.Attempts)-1]
	path := filepath.Join(st.Layout.Attempt(r.Key, a.Step, a.N), "output.log")
	f, err := os.Open(path)
	if err == nil {
		defer func() { _ = f.Close() }()
		_, err = io.Copy(out, f)
	} else if os.IsNotExist(err) && a.Address != "" {
		if err = attempt.Guard(ctx, h, a); err == nil {
			var text string
			var complete bool
			text, complete, err = h.Read(ctx, a.Address, 200)
			if err == nil {
				fmt.Fprintln(out, text)
				if !complete {
					fmt.Fprintln(out, "[terminal output incomplete]")
				}
			}
		}
	}
	if err != nil {
		fmt.Fprintln(out, err)
		return 1
	}
	return 0
}
