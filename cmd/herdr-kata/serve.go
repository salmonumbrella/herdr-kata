package main

import (
	"context"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"github.com/salmonumbrella/herdr-kata/internal/worker"
	"io"
	"os"
	"time"
)

func serve(ctx context.Context, c config.Config, st *journal.Store, h hc.HC, clients map[string]kata.Kata, bin string, out io.Writer) error {
	startedAt := time.Now().UTC()
	release, err := journal.Lock(st.Layout.Lock())
	if err != nil {
		return err
	}
	defer release()
	if err = state.WriteFile(st.Layout.PID(), []byte(fmt.Sprint(os.Getpid())+"\n")); err != nil {
		return err
	}
	defer func() { _ = os.Remove(st.Layout.PID()) }()
	w := worker.New(c, st, h, clients, bin)
	w.StartedAt = startedAt
	advance := func() {
		if err := w.Tick(ctx, time.Now().UTC()); err != nil {
			fmt.Fprintln(out, err)
		}
	}
	advance()
	ticks := time.NewTicker(30 * time.Second)
	defer ticks.Stop()
	requests := time.NewTicker(2 * time.Second)
	defer requests.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticks.C:
			advance()
		case <-requests.C:
			pollNewRequests(journal.Requests{Dir: st.Layout.Requests()}, w.SeenRequests, advance, out)
		}
	}
}

func pollNewRequests(q journal.Requests, seen map[string]bool, advance func(), out io.Writer) {
	pending, err := q.Pending()
	if err != nil {
		fmt.Fprintln(out, err)
	}
	fresh := false
	for _, req := range pending {
		if !seen[req.ID] {
			fresh = true
			seen[req.ID] = true
		}
	}
	if fresh {
		advance()
	}
}
