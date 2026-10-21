package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type repeatedFlag []string

func (e *repeatedFlag) String() string     { return strings.Join(*e, ", ") }
func (e *repeatedFlag) Set(v string) error { *e = append(*e, v); return nil }

func reportCommand(args []string, stdout, stderr io.Writer) int {
	f := flag.NewFlagSet("report", flag.ContinueOnError)
	f.SetOutput(stderr)
	var path string
	r := runner.Result{Version: 1, Run: os.Getenv("HERDR_KATA_RUN"), Evidence: []string{}}
	var evidence repeatedFlag
	f.StringVar(&path, "result", "", "absolute attempt result.json path")
	f.StringVar(&r.Status, "status", "", "succeeded, failed or needs-human")
	f.StringVar(&r.Summary, "summary", "", "what was done and how it was verified")
	f.StringVar(&r.Note, "note", "", "one line for the next step (at most 280 bytes)")
	f.StringVar(&r.Assert, "assert", "", "confirm or refute the saved maker claim (requires --target)")
	f.StringVar(&r.Target, "target", "", "full comment ULID, optionally prefixed c: (requires --assert)")
	f.StringVar(&r.CloseReason, "close-reason", "", "done or audit-no-change")
	f.Var(&evidence, "evidence", "type:value (repeatable)")
	if err := f.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if f.NArg() != 0 || strings.TrimSpace(r.Summary) == "" {
		fmt.Fprintln(stderr, "report requires --result, --status and --summary, with no positional arguments")
		return 2
	}
	if err := runner.NormalizeAssertion(&r); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	step, attempt, err := reportIdentity(os.Getenv("HERDR_KATA_RUN_DIR"), path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	r.Step, r.Attempt = step, attempt
	if len(evidence) > 0 {
		r.Evidence = evidence
	}
	if err := runner.WriteResult(state.OSFS{}, path, &r); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, path)
	return 0
}

// The worker creates the attempt directory before launching. Check both lexical
// and resolved containment so an in-tree symlink cannot write outside the run.
// Identity comes from the run environment and steps/<key>/<attempt>/result.json.
func reportIdentity(dir, path string) (string, int, error) {
	bad := func() (string, int, error) {
		return "", 0, fmt.Errorf("result must be inside $HERDR_KATA_RUN_DIR at steps/<key>/<attempt>/result.json")
	}
	if !filepath.IsAbs(dir) || !filepath.IsAbs(path) {
		return bad()
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return bad()
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 4 || parts[0] != "steps" || parts[3] != "result.json" {
		return bad()
	}
	n, err := strconv.Atoi(parts[2])
	if err != nil || n < 1 || strconv.Itoa(n) != parts[2] {
		return bad()
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return bad()
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return bad()
	}
	resolved, err := filepath.Rel(root, parent)
	if err != nil || resolved != filepath.Join(parts[:3]...) {
		return bad()
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return bad()
		}
	} else if !os.IsNotExist(err) {
		return "", 0, err
	}
	return parts[1], n, nil
}
