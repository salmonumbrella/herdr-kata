package main

import (
	"bytes"
	"context"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/doctor"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type runsKata struct {
	uid   string
	job   string
	limit int
}

func (k *runsKata) ListJobs(context.Context) ([]kata.Job, error) {
	return []kata.Job{{UID: "job-uid", Name: "named-job"}}, nil
}
func (k *runsKata) ShowJob(_ context.Context, ref string) (kata.Job, error) {
	return kata.Job{UID: "job-uid"}, nil
}
func (k *runsKata) ListRuns(_ context.Context, job, before string, limit int) ([]kata.Run, string, error) {
	k.job = job
	k.limit = limit
	uid := k.uid
	if uid == "" {
		uid = "run-uid"
	}
	return []kata.Run{{UID: uid, JobUID: "job-uid", Status: "succeeded", Summary: kata.Summary{Version: 1, Message: "Verified command workflow"}}}, "", nil
}
func (k *runsKata) Capabilities(context.Context) (kata.Capabilities, error) {
	return kata.Capabilities{ProjectUID: "project"}, nil
}
func (k *runsKata) FederationIdentity(context.Context) (kata.Identity, error) {
	return kata.Identity{InstanceUID: "instance"}, nil
}
func TestRunsListAndFilter(t *testing.T) {
	k := &runsKata{}
	var out bytes.Buffer
	if e := listRuns(t.Context(), k, "named-job", 12, &out); e != nil {
		t.Fatal(e)
	}
	if k.job != "job-uid" || k.limit != 12 || !strings.Contains(out.String(), "run-uid") || !strings.Contains(out.String(), "succeeded") {
		t.Fatal(k, out.String())
	}
}
func TestRunsArguments(t *testing.T) {
	for _, args := range [][]string{{"runs", "extra"}, {"runs", "--limit", "-1"}, {"runs", "--bad"}} {
		var out, err bytes.Buffer
		if code := run(args, &out, &err); code != 2 {
			t.Fatal(args, code, err.String())
		}
	}
}

type projectRunsKata struct {
	identity kata.Identity
	name     string
	missing  bool
	listed   bool
	limit    int
	rows     []kata.Run
}

func (k *projectRunsKata) Capabilities(context.Context) (kata.Capabilities, error) {
	return kata.Capabilities{ProjectUID: k.identity.ProjectUID}, nil
}
func (k *projectRunsKata) FederationIdentity(context.Context) (kata.Identity, error) {
	return k.identity, nil
}

func (k *projectRunsKata) ShowJob(context.Context, string) (kata.Job, error) {
	if k.missing {
		return kata.Job{}, &kata.Error{Code: "not_found"}
	}
	return kata.Job{UID: "job-" + k.name}, nil
}
func (k *projectRunsKata) ListJobs(context.Context) ([]kata.Job, error) {
	if k.missing {
		return []kata.Job{}, nil
	}
	return []kata.Job{{UID: "job-" + k.name}}, nil
}
func (k *projectRunsKata) ListRuns(_ context.Context, job, before string, limit int) ([]kata.Run, string, error) {
	k.listed = true
	k.limit = limit
	if k.rows != nil {
		return k.rows[:min(len(k.rows), limit)], "", nil
	}
	return []kata.Run{{UID: "run-" + k.name, JobUID: job, Status: "succeeded"}}, "", nil
}
func TestRunsFilterFindsJobInLaterProject(t *testing.T) {
	cfg := config.Config{Projects: []config.Project{{Name: "project-a", Actor: "worker"}, {Name: "project-b", Actor: "worker"}}}
	a, b := &projectRunsKata{name: "a", missing: true}, &projectRunsKata{name: "b"}
	var out bytes.Buffer
	e := listRunsProjects(t.Context(), cfg, "", "job-b", 100, &out, func(view config.Config) runsReader {
		if view.Kata.Project == "project-a" {
			return a
		}
		return b
	})
	if e != nil || a.listed || !b.listed || !strings.Contains(out.String(), "project-b\trun-b") {
		t.Fatal("history in later project was not listed", out.String(), e)
	}
}

func TestRunsLimitAppliesAcrossProjects(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	cfg := config.Config{Projects: []config.Project{{Name: "project-a", Actor: "worker"}, {Name: "project-b", Actor: "worker"}}}
	a, b := &projectRunsKata{name: "a"}, &projectRunsKata{name: "b"}
	var out bytes.Buffer
	e := listRunsProjects(t.Context(), cfg, "", "", 1, &out, func(view config.Config) runsReader {
		if view.Kata.Project == "project-a" {
			return a
		}
		return b
	})
	if e != nil || out.String() != "project-a\trun-a\tsucceeded\t\t\t\n" || b.listed {
		t.Fatal("run limit exceeded across projects", out.String(), b.listed, e)
	}
}

func TestRunsLaterProjectReceivesRemainingLimit(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
	cfg := config.Config{Projects: []config.Project{{Name: "project-a", Actor: "worker"}, {Name: "project-b", Actor: "worker"}}}
	a := &projectRunsKata{name: "a"}
	b := &projectRunsKata{rows: []kata.Run{{UID: "run-b1", Status: "failed"}, {UID: "run-b2", Status: "succeeded"}}}
	var out bytes.Buffer
	e := listRunsProjects(t.Context(), cfg, "", "", 2, &out, func(view config.Config) runsReader {
		if view.Kata.Project == "project-a" {
			return a
		}
		return b
	})
	if e != nil || a.limit != 2 || b.limit != 1 || out.String() != "project-a\trun-a\tsucceeded\t\t\t\nproject-b\trun-b1\tfailed\t\t\t\n" {
		t.Fatal("remaining run budget was not applied", out.String(), a.limit, b.limit, e)
	}
}

func TestRunsShowLocalAttempts(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	l := state.Layout{Root: root}
	uid := "01K00000000000000000000000"
	if e := state.WriteJSON(state.OSFS{}, l.RunFile(uid), runner.Run{Version: 1, UID: uid, PromptCap: 12, PromptsSent: 5, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project", InstanceUID: "instance"}}, Steps: []runner.StepState{{Key: "make", Attempt: 2, Status: "succeeded"}, {Key: "check", Attempt: 3, Status: "failed"}, {Key: "after", Attempt: 1}}}); e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	if e := listRuns(t.Context(), &runsKata{uid: uid}, "", 12, &out); e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"make · attempt 2 · succeeded", "check · attempt 3 · failed", "prompts 5/12", "after · attempt 1 · not started\n"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
}

func TestRunsLocalEvidenceMatchesRemoteProjectAndInstance(t *testing.T) {
	for _, tc := range []struct {
		name, project, instance string
		steps                   bool
	}{
		{"matching identity", "project-a", "instance-a", true},
		{"another project", "project-b", "instance-a", false},
		{"another instance", "project-a", "instance-b", false},
		{"missing project", "", "instance-a", false},
		{"missing instance", "project-a", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
			uid := "01K00000000000000000000000"
			local := runner.Run{Version: 1, UID: uid, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project-a", InstanceUID: "instance-a"}}, Steps: []runner.StepState{{Key: "local-step", Attempt: 2, Status: "succeeded"}}}
			if err := state.WriteJSON(state.OSFS{}, (state.Layout{Root: root}).RunFile(uid), local); err != nil {
				t.Fatal(err)
			}
			k := &projectRunsKata{identity: kata.Identity{ProjectUID: tc.project, InstanceUID: tc.instance}, rows: []kata.Run{{UID: uid, Status: "succeeded"}}}
			cfg := config.Config{Projects: []config.Project{{Name: "example-project", Actor: "worker"}}}
			var out bytes.Buffer
			if err := listRunsProjects(t.Context(), cfg, "example-project", "", 10, &out, func(config.Config) runsReader { return k }); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "example-project\t"+uid+"\tsucceeded") || strings.Contains(out.String(), "local-step · attempt 2 · succeeded") != tc.steps {
				t.Fatal("history mixed local evidence across identities", out.String())
			}
		})
	}
}

func TestRunsDisplaysParkedStateOnlyForMatchingLocalJournal(t *testing.T) {
	for _, tc := range []struct{ name, instance, want string }{{"same installation", "instance", "parked"}, {"other installation", "elsewhere", "failed"}} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
			uid := "01K00000000000000000000000"
			local := runner.Run{Version: 1, UID: uid, Phase: runner.Held, Outcome: "failed", Reason: "step_failed", Target: runner.Target{Identity: kata.Identity{ProjectUID: "project", InstanceUID: "instance"}}}
			if err := state.WriteJSON(state.OSFS{}, (state.Layout{Root: root}).RunFile(uid), local); err != nil {
				t.Fatal(err)
			}
			k := &projectRunsKata{identity: kata.Identity{ProjectUID: "project", InstanceUID: tc.instance}, rows: []kata.Run{{UID: uid, Status: "failed"}}}
			var out bytes.Buffer
			if err := listRuns(t.Context(), k, "", 10, &out); err != nil {
				t.Fatal(err)
			}
			if out.String() != uid+"\t"+tc.want+"\t\t\t\n" {
				t.Fatal(out.String())
			}
		})
	}
}

func TestRunsDistinguishesActiveAndUnstartedSteps(t *testing.T) {
	for _, tc := range []struct {
		name, kind, status, want string
		phase                    runner.Phase
		submission               runner.Submission
	}{
		{"active command", "command", "", "running", runner.Running, runner.Sending},
		{"recorded outcome", "command", "succeeded", "succeeded", runner.Running, runner.Sending},
		{"blocked prompt", "prompt", "", "blocked", runner.Blocked, runner.Accepted},
		{"parked prompt", "prompt", "", "parked", runner.Held, runner.Accepted},
		{"handed off prompt", "prompt", "", "needs-human", runner.HandedOff, runner.Accepted},
		{"admitted prompt", "prompt", "", "running", runner.Admitted, runner.Accepted},
		{"unsent prompt", "prompt", "", "not started", runner.Running, runner.None},
		{"uncertain submitted command", "command", "", "unknown", runner.Settled, runner.Accepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := state.Layout{Root: t.TempDir()}
			t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
			t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
			const uid = "01K00000000000000000000000"
			local := runner.Run{
				Version: 1, UID: uid, Phase: tc.phase, ActiveStep: "first",
				Target: runner.Target{Identity: kata.Identity{ProjectUID: "project", InstanceUID: "instance"}},
				Steps: []runner.StepState{
					{Key: "first", Kind: tc.kind, Attempt: 1, Submission: tc.submission, Status: tc.status},
					{Key: "after", Kind: "command", Attempt: 1, Submission: runner.None},
				},
			}
			if err := state.WriteJSON(state.OSFS{}, l.RunFile(uid), local); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := listRuns(t.Context(), &runsKata{uid: uid}, "", 10, &out); err != nil {
				t.Fatal(err)
			}
			want := "  first · attempt 1 · " + tc.want + "\n  after · attempt 1 · not started\n"
			if !strings.HasSuffix(out.String(), want) {
				t.Fatalf("step labels: %s; want suffix %q", out.String(), want)
			}
		})
	}
}

func TestRunsListsLocalRequestJournals(t *testing.T) {
	for _, tc := range []struct {
		name, project, instance, job string
		limit, want                  int
	}{
		{"matching", "project", "instance", "", 10, 1},
		{"foreign project", "other", "instance", "", 10, 0},
		{"foreign instance", "project", "other", "", 10, 0},
		{"job filter", "project", "instance", "job-uid", 10, 0},
		{"shared limit", "project", "instance", "", 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
			const uid = "01K00000000000000000000001"
			local := runner.Run{Version: 2, UID: uid, Source: "issue-request:child", Phase: runner.Held, Outcome: "unknown", Reason: "command_no_receipt", Target: runner.Target{Identity: kata.Identity{ProjectUID: tc.project, InstanceUID: tc.instance}}, Steps: []runner.StepState{{Key: "check", Attempt: 1, Status: "unknown"}}}
			if err := state.WriteJSON(state.OSFS{}, (state.Layout{Root: root}).RunFile(uid), local); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			n, err := listRunsProject(t.Context(), &runsKata{}, tc.job, tc.limit, &out, "example-project")
			if err != nil || n != 1+tc.want || strings.Contains(out.String(), uid+"\tparked") != (tc.want == 1) {
				t.Fatalf("rows=%d out=%q err=%v", n, out.String(), err)
			}
			if tc.want == 1 && !strings.Contains(out.String(), "check · attempt 1 · unknown") {
				t.Fatal(out.String())
			}
		})
	}
}

// A request run Kata does not list yet prints the table's word, not its phase.
func TestRunsLocalRequestUsesStatusWord(t *testing.T) {
	for phase, want := range map[runner.Phase]string{runner.Prepared: "queued", runner.Admitted: "queued", runner.HandedOff: "needs-human"} {
		t.Run(string(phase), func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
			const uid = "01K00000000000000000000001"
			local := runner.Run{Version: 2, UID: uid, Source: "issue-request:child", Phase: phase, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project", InstanceUID: "instance"}}}
			if err := state.WriteJSON(state.OSFS{}, (state.Layout{Root: root}).RunFile(uid), local); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if _, err := listRunsProject(t.Context(), &runsKata{}, "", 10, &out, "example-project"); err != nil || !strings.Contains(out.String(), uid+"\t"+want+"\t") {
				t.Fatalf("out=%q err=%v, want status %s", out.String(), err, want)
			}
		})
	}
}

func TestRunsAcceptsVersionTwoLocalAttempts(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	const uid = "01K00000000000000000000000"
	local := runner.Run{Version: 2, UID: uid, Source: "issue-request:child", Phase: runner.Held, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project", InstanceUID: "instance"}}, Steps: []runner.StepState{{Key: "check", Attempt: 1, Status: "failed"}}}
	if err := state.WriteJSON(state.OSFS{}, (state.Layout{Root: root}).RunFile(uid), local); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := listRuns(t.Context(), &runsKata{uid: uid}, "", 10, &out); err != nil || strings.Count(out.String(), uid+"\t") != 1 || !strings.Contains(out.String(), "check · attempt 1 · failed") {
		t.Fatalf("out=%q err=%v", out.String(), err)
	}
}

func TestRunsIncludesRecentRequestWithFullCronPage(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", root)
	const uid = "01K00000000000000000000001"
	at := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	local := runner.Run{Version: 2, UID: uid, Source: "issue-request:child", Phase: runner.Held, StartedAt: &at, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project", InstanceUID: "instance"}}}
	if err := state.WriteJSON(state.OSFS{}, (state.Layout{Root: root}).RunFile(uid), local); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	n, err := listRunsProject(t.Context(), &runsKata{}, "", 1, &out, "example-project")
	if err != nil || n != 1 || !strings.Contains(out.String(), uid+"\tparked") {
		t.Fatalf("rows=%d out=%q err=%v", n, out.String(), err)
	}
}

// A damaged retained journal cannot suppress usable history, including when
// that journal would otherwise decorate a row in the remote page.
func TestRunsSkipsUnreadableLocalJournals(t *testing.T) {
	for _, kind := range []string{"corrupt", "permission"} {
		for _, location := range []string{"retained", "remote row"} {
			t.Run(kind+"/"+location, func(t *testing.T) {
				if kind == "permission" && os.Geteuid() == 0 {
					t.Skip("root can read permission-denied fixtures")
				}
				l := state.Layout{Root: t.TempDir()}
				t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
				const remoteUID = "01K00000000000000000000000"
				const requestUID = "01K00000000000000000000001"
				badUID := "01K00000000000000000000002"
				if location == "remote row" {
					badUID = remoteUID
				}
				path := l.RunFile(badUID)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				body := []byte(`broken json`)
				mode := os.FileMode(0600)
				if kind == "permission" {
					body = []byte(`{"Version":1,"UID":"` + badUID + `","Phase":"settled"}`)
					mode = 0
				}
				if err := os.WriteFile(path, body, mode); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(path, 0600) })
				local := runner.Run{Version: 2, UID: requestUID, Source: "issue-request:child", Phase: runner.Held, Target: runner.Target{Identity: kata.Identity{ProjectUID: "project", InstanceUID: "instance"}}, Steps: []runner.StepState{{Key: "check", Attempt: 1, Status: "failed"}}}
				if err := state.WriteJSON(state.OSFS{}, l.RunFile(requestUID), local); err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				n, err := listRunsProject(t.Context(), &runsKata{uid: remoteUID}, "", 10, &out, "example-project")
				if err != nil || n != 2 {
					t.Fatalf("history aborted: rows=%d out=%q err=%v", n, out.String(), err)
				}
				for _, want := range []string{remoteUID + "\tsucceeded", requestUID + "\tparked", "check · attempt 1 · failed", "inspect herdr-kata doctor"} {
					if !strings.Contains(out.String(), want) {
						t.Fatalf("missing %q in %q", want, out.String())
					}
				}
				if strings.Contains(out.String(), l.Root) || strings.Contains(out.String(), path) {
					t.Fatal("raw state path in history", out.String())
				}
				checks, err := doctor.LocalChecks(state.OSFS{}, l)
				if err != nil {
					t.Fatal(err)
				}
				diagnosed := false
				for _, check := range checks {
					if check.Name == "local run "+badUID && !check.Passed && strings.Contains(check.Detail, "inspect") {
						diagnosed = true
					}
				}
				if !diagnosed {
					t.Fatal("doctor did not report damaged journal", checks)
				}
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != mode {
					t.Fatal("diagnostics changed journal permissions", info, err)
				}
				if err := os.Chmod(path, 0600); err != nil {
					t.Fatal(err)
				}
				saved, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(saved, body) {
					t.Fatal("diagnostics changed journal", string(saved), err)
				}
			})
		}
	}
}
