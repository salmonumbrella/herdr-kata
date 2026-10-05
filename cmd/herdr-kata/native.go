package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"io"
	"os"
	"path/filepath"
	"time"
)

func nativeCmd(argv []string) error {
	if len(argv) == 0 {
		return errors.New("usage: herdr-kata native <configure|import|checkout|secret|activate|deactivate|refresh|tui|job|flow>")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	switch argv[0] {
	case "import":
		return nativeImport(ctx, argv[1:])
	case "configure":
		fs := flag.NewFlagSet("native configure", flag.ContinueOnError)
		file := fs.String("file", "", "explicit local JSON mapping")
		if e := fs.Parse(argv[1:]); e != nil {
			return e
		}
		if *file == "" {
			return errors.New("native configure requires --file with explicit Kata routing and project mapping")
		}
		f, e := os.Open(*file)
		if e != nil {
			return e
		}
		defer f.Close()
		var cfg store.NativeConfig
		decoder := json.NewDecoder(io.LimitReader(f, 1024*1024))
		decoder.DisallowUnknownFields()
		if e := decoder.Decode(&cfg); e != nil {
			return e
		}
		if e := cfg.Client.Target.Validate(); e != nil {
			return e
		}
		authority, e := cfg.Client.Capabilities(ctx)
		if e != nil {
			return e
		}
		if cfg.Binding.ProjectUID != "" && cfg.Binding.ProjectUID != authority.ProjectUID {
			return errors.New("native project UID does not match configured mapping")
		}
		cfg.Binding.ProjectUID = authority.ProjectUID
		if e := store.WriteNativeConfig(stateDir(), cfg); e != nil {
			return e
		}
		fmt.Printf("native Kata project %s configured\n", cfg.Binding.ProjectUID)
		return nil
	case "checkout", "secret":
		if len(argv) != 3 {
			return errors.New("usage: native checkout <key> <directory> or native secret <reference> <value-file>")
		}
		cfg, e := store.LoadNativeConfig(stateDir())
		if e != nil {
			return e
		}
		if argv[0] == "checkout" {
			path, e := filepath.Abs(argv[2])
			if e != nil {
				return e
			}
			if cfg.Binding.Checkouts == nil {
				cfg.Binding.Checkouts = map[string]string{}
			}
			cfg.Binding.Checkouts[argv[1]] = path
		} else {
			raw, e := os.ReadFile(argv[2])
			if e != nil {
				return e
			}
			if len(raw) > 64*1024 {
				return errors.New("local secret exceeds64KiB")
			}
			if cfg.Binding.Secrets == nil {
				cfg.Binding.Secrets = map[string]string{}
			}
			cfg.Binding.Secrets[argv[1]] = string(raw)
		}
		return store.WriteNativeConfig(stateDir(), cfg)
	case "activate", "deactivate":
		if len(argv) != 2 {
			return errors.New("usage: native activate|deactivate <job-uid>")
		}
		s, e := openStore()
		if e != nil {
			return e
		}
		defer s.Close()
		return s.SetEnabled(ctx, argv[1], argv[0] == "activate")
	case "refresh":
		s, e := openStore()
		if e != nil {
			return e
		}
		defer s.Close()
		snapshot, e := s.Native.Refresh(ctx)
		if e != nil {
			return e
		}
		fmt.Printf("%s · %d jobs · %d flows\n", snapshot.Label(), len(snapshot.Jobs), len(snapshot.Flows))
		if snapshot.Offline {
			return errors.New(snapshot.Problem)
		}
		return nil
	case "tui":
		if len(argv) > 2 {
			return errors.New("usage: native tui [issue-ref]")
		}
		cfg, e := store.LoadNativeConfig(stateDir())
		if e != nil {
			return e
		}
		issue := ""
		if len(argv) == 2 {
			issue = argv[1]
		}
		cmd, e := cfg.Client.TUICommand(context.Background(), issue)
		if e != nil {
			return e
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	case "job", "flow":
		return nativeDefinitionCmd(ctx, argv[0], argv[1:])
	}
	return errors.New("unknown native command")
}
func nativeDefinitionCmd(ctx context.Context, resource string, argv []string) error {
	if len(argv) == 0 {
		return errors.New("usage: native job|flow <save|list|show|delete|restore>")
	}
	s, e := openStore()
	if e != nil {
		return e
	}
	defer s.Close()
	if s.Native.Client == nil {
		return store.ErrNativeUnconfigured
	}
	switch argv[0] {
	case "save":
		fs := flag.NewFlagSet("native definition save", flag.ContinueOnError)
		uid := fs.String("uid", "", "retained definition ULID required")
		name := fs.String("name", "", "native label")
		expected := fs.String("expected-event-uid", "", "last observed winner for updates")
		file := fs.String("file", "", "portable definition JSON file (not local mappings)")
		if e := fs.Parse(argv[1:]); e != nil {
			return e
		}
		if *uid == "" || *file == "" {
			return errors.New("retain --uid and provide --file before saving; updates also require --expected-event-uid")
		}
		raw, e := os.ReadFile(*file)
		if e != nil {
			return e
		}
		draft, e := katacli.NewDraft(resource, *uid, *name, raw, *expected)
		if e != nil {
			return e
		}
		def, e := s.Native.Save(ctx, draft)
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(def)
	case "list":
		snapshot, e := s.Native.Refresh(ctx)
		if e != nil {
			return e
		}
		fmt.Fprintln(os.Stderr, snapshot.Label())
		defs := snapshot.Jobs
		if resource == "flow" {
			defs = snapshot.Flows
		}
		return json.NewEncoder(os.Stdout).Encode(defs)
	case "show":
		if len(argv) != 2 {
			return errors.New("show requires a UID")
		}
		def, e := s.Native.Client.Definition(ctx, resource, argv[1])
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(def)
	case "delete", "restore":
		if len(argv) != 3 {
			return errors.New("delete/restore requires UID and expected event UID")
		}
		def, e := s.Native.DefinitionAction(ctx, resource, argv[0], argv[1], argv[2])
		if e != nil {
			return e
		}
		return json.NewEncoder(os.Stdout).Encode(def)
	}
	return errors.New("unknown native definition command")
}
