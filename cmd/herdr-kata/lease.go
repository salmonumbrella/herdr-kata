package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

// leaseCmd deliberately requires an explicit coordinator scope and holder.
// It never discovers identities or resource scope by contacting Herdr.
func leaseCmd(argv []string) error {
	if len(argv) == 0 {
		return errors.New("usage: herdr-kata lease <claim|renew|release|list>")
	}
	action := argv[0]
	fs := flag.NewFlagSet("lease "+action, flag.ContinueOnError)
	scope := fs.String("scope", "", "explicit local/shared resource coordinator scope")
	as := fs.String("as", "", "lease holder identity")
	run := fs.String("run", "", "execution run identity")
	job := fs.String("job", "", "execution job identity")
	pid := fs.String("holder-id", "", "interactive holder discriminator")
	ttl := fs.Duration("ttl", 0, "lease duration; omitted or zero means no expiry")
	wait := fs.Duration("wait", 0, "claim wait limit")
	why := fs.String("why", "", "reason for holding the resource")
	jsonOut := fs.Bool("json", false, "emit JSON")
	usageLine := "usage: herdr-kata lease " + action
	if action == "list" {
		usageLine += " [--scope <scope>] [--json]"
	} else {
		usageLine += " <resource> --scope <scope> --as <holder>"
		if action == "claim" || action == "renew" {
			usageLine += " --ttl <duration>"
		}
		usageLine += " [--job <job>] [--run <run>] [--holder-id <id>]"
	}
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), usageLine)
		fs.PrintDefaults()
	}
	args := argv[1:]
	resource := ""
	switch action {
	case "claim", "renew", "release":
		if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
			return fs.Parse(args)
		}
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			return errors.New(usageLine)
		}
		resource = args[0]
		args = args[1:]
	case "list":
	default:
		return fmt.Errorf("unknown lease subcommand %q", action)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected lease arguments")
	}
	if *wait < 0 {
		return errors.New("wait cannot be negative")
	}
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()
	ctx := context.Background()
	if action == "list" {
		ls, err := s.ListLeases(ctx, *scope, time.Time{})
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(ls)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "SCOPE\tRESOURCE\tHOLDER\tRUN\tEXPIRES\tWHY")
		for _, l := range ls {
			expiry := "no expiry"
			if l.ExpiresAt != nil {
				expiry = l.ExpiresAt.Format(time.RFC3339Nano)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", l.Scope, l.Resource, l.Holder.Short(), l.Holder.RunID, expiry, l.Why)
		}
		return w.Flush()
	}
	req := store.LeaseRequest{Scope: *scope, Resource: resource, By: store.Identity{Name: *as, JobID: *job, RunID: *run, PID: *pid}, TTL: *ttl, Why: *why}
	var l store.Lease
	switch action {
	case "claim":
		l, err = s.AcquireLeaseWait(ctx, req, *wait)
	case "renew":
		l, err = s.RenewLease(ctx, req)
	case "release":
		l, err = s.ReleaseLease(ctx, req.Scope, req.Resource, req.By, time.Time{})
	}
	if err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(l)
	}
	fmt.Printf("%s %s/%s as %s\n", action, l.Scope, l.Resource, l.Holder)
	return nil
}
