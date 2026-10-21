package doctor

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestEvalDoctorCredentialRemedy(t *testing.T) {
	for _, source := range []string{"unset env", "missing file", "public file"} {
		t.Run(source, func(t *testing.T) {
			c := config.Config{Kata: config.Kata{Project: "example-project", CredentialEnv: "KATA_TEST_DOCTOR_TOKEN"}}
			t.Setenv("KATA_TEST_DOCTOR_TOKEN", "")
			want := "credential_env"
			if source != "unset env" {
				want = "credential_file"
				c.Kata.CredentialFile = filepath.Join(t.TempDir(), "credential")
			}
			if source == "public file" {
				if err := os.WriteFile(c.Kata.CredentialFile, []byte("test-value"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			k := kata.New(c.Kata, t.TempDir(), func(context.Context, []string, []string, string, []byte) ([]byte, []byte, int, error) {
				t.Fatal("invalid credential reached Kata")
				return nil, nil, 1, nil
			}, nil)
			report, _ := checkKata(t.Context(), c, k, state.Layout{Root: t.TempDir()})
			found := false
			for _, check := range report.Checks {
				if !check.Passed && strings.Contains(check.Detail, want) {
					found = true
					if !strings.Contains(check.Detail, "fix "+want) || !strings.Contains(check.Detail, "example-project") || strings.Contains(check.Detail, "daemon diagnose") {
						t.Error(check)
					}
				}
			}
			if !found {
				t.Fatal(report)
			}
		})
	}
}

func TestEvalDoctorHealthRemedy(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		exit             int
	}{
		{"unknown daemon", `{"error":{"kind":"validation","message":"named daemon not found: missing","exit_code":3}}`, `fix daemon for project "example-project"`, 3},
		{"non-Kata binary", "", "set kata.bin", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config.Config{Kata: config.Kata{Daemon: "missing", Project: "example-project"}}
			k := kata.New(c.Kata, t.TempDir(), func(_ context.Context, args, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
				if args[len(args)-1] != "health" {
					t.Fatal(args)
				}
				return []byte(tc.body), nil, tc.exit, nil
			}, nil)
			report, _ := checkKata(t.Context(), c, k, state.Layout{Root: t.TempDir()})
			if len(report.Checks) != 1 || report.Checks[0].Passed || !strings.Contains(report.Checks[0].Detail, tc.want) || strings.Contains(report.Checks[0].Detail, "daemon diagnose") {
				t.Fatal(report)
			}
		})
	}
}

func TestEvalDoctorCorruptStateRemedies(t *testing.T) {
	for _, which := range []string{"layout", "pin"} {
		t.Run(which, func(t *testing.T) {
			c, k, h, l := doctorFixture(t)
			if err := state.InitializeLayout(state.OSFS{}, l); err != nil {
				t.Fatal(err)
			}
			path := l.StateFile()
			if which == "pin" {
				path = l.Project("project").StateFile()
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
				t.Fatal(err)
			}
			report := Check(t.Context(), c, k, h, l)
			found := false
			for _, check := range report.Checks {
				if !check.Passed && strings.Contains(check.Detail, path) {
					found = true
					if strings.Contains(check.Detail, "move the state directory aside") || strings.Count(check.Detail, "preserve original artifact paths") != 1 || !strings.Contains(check.Detail, "herdr-kata stop") || !strings.Contains(check.Detail, "preserve original artifact paths") || !strings.Contains(check.Detail, "restore damaged state from trusted evidence") || !strings.Contains(check.Detail, "resolve existing occupants") {
						t.Error(check)
					}
				}
			}
			if !found {
				t.Fatal(report)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "broken" {
				t.Fatal("doctor changed damaged state", string(data), err)
			}
		})
	}
}

type unreadableRuns struct {
	state.OSFS
	path string
}

func (f unreadableRuns) ReadDir(path string) ([]fs.DirEntry, error) {
	if path == f.path {
		return nil, &os.PathError{Op: "open", Path: path, Err: fs.ErrPermission}
	}
	return f.OSFS.ReadDir(path)
}
func TestEvalDoctorUnreadableRunsRemedy(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	_, err := LocalChecks(unreadableRuns{path: filepath.Join(l.Root, "runs")}, l)
	if !errors.Is(err, fs.ErrPermission) || !strings.Contains(err.Error(), "restore read and search permissions") {
		t.Fatal(err)
	}
}

func TestEvalDoctorHoldWithoutIssue(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	const uid = "01K00000000000000000000000"
	r := runner.Run{Version: 1, UID: uid, Phase: runner.Held, Reason: "create_rejected", Inspect: "create refused"}
	if err := state.WriteJSON(state.OSFS{}, l.RunFile(uid), r); err != nil {
		t.Fatal(err)
	}
	checks, err := LocalChecks(state.OSFS{}, l)
	if err != nil || len(checks) != 1 || strings.Contains(checks[0].Detail, "issue  ()") || !strings.Contains(checks[0].Detail, "no issue created") {
		t.Fatal(checks, err)
	}
}

func TestEvalDoctorHerdrConnectionRemedy(t *testing.T) {
	c, k, _, l := doctorFixture(t)
	dir, err := os.MkdirTemp("/tmp", "hkd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	h := &herdr.Client{Path: filepath.Join(dir, "missing.sock")}
	report := Check(t.Context(), c, k, h, l)
	for _, check := range report.Checks {
		if check.Name == "herdr" {
			if check.Passed || !strings.Contains(check.Detail, "fix session") || !strings.Contains(check.Detail, "herdr --session") {
				t.Fatal(check)
			}
			return
		}
	}
	t.Fatal("missing Herdr check", report)
}

func TestDoctorLayoutFixturesChooseOneRecoveryPath(t *testing.T) {
	for _, tc := range []struct {
		name         string
		incompatible bool
	}{
		{"legacy-empty-project", true}, {"legacy-project", true}, {"damaged-legacy", true}, {"version2", true}, {"version2-migration", true}, {"version3", true}, {"damaged-current", false}, {"undecodable", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, k, h, l := doctorFixture(t)
			fixture, err := os.ReadFile(filepath.Join("..", "state", "testdata", "layouts", tc.name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(l.StateFile(), fixture, 0600); err != nil {
				t.Fatal(err)
			}
			report := Check(t.Context(), c, k, h, l)
			found := false
			for _, check := range report.Checks {
				if check.Name != "state layout" {
					continue
				}
				found = true
				if check.Passed || strings.Contains(check.Detail, "move the state directory aside and run herdr-kata up again") != tc.incompatible || strings.Contains(check.Detail, "preserve original artifact paths") == tc.incompatible {
					t.Fatal("wrong doctor remedy", check)
				}
			}
			if !found {
				t.Fatal("missing layout check", report)
			}
			after, err := os.ReadFile(l.StateFile())
			if err != nil || string(after) != string(fixture) {
				t.Fatal("doctor changed state", err)
			}
		})
	}
}

func TestDoctorRejectedCredentialRemedy(t *testing.T) {
	for _, source := range []string{"env", "file"} {
		t.Run(source, func(t *testing.T) {
			c := config.Config{Kata: config.Kata{Project: "example-project", CredentialEnv: "LANE_TOKEN"}}
			t.Setenv("LANE_TOKEN", "rejected-token")
			want := "credential_env"
			if source == "file" {
				want = "credential_file"
				c.Kata.CredentialFile = filepath.Join(t.TempDir(), "token")
				if err := os.WriteFile(c.Kata.CredentialFile, []byte("rejected-token"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			k := kata.New(c.Kata, t.TempDir(), func(_ context.Context, args, _ []string, _ string, _ []byte) ([]byte, []byte, int, error) {
				if args[len(args)-1] != "health" {
					t.Fatal(args)
				}
				return []byte(`{"error":{"kind":"token_invalid","message":"token invalid","exit_code":1}}`), nil, 1, nil
			}, nil)
			report, _ := checkKata(t.Context(), c, k, state.Layout{Root: t.TempDir()})
			if len(report.Checks) != 1 || report.Checks[0].Passed {
				t.Fatal(report)
			}
			detail := report.Checks[0].Detail
			if !strings.Contains(detail, "replace") || !strings.Contains(detail, want) || !strings.Contains(detail, "example-project") || strings.Contains(detail, "tunnel") || strings.Contains(detail, "rejected-token") {
				t.Fatal(detail)
			}
		})
	}
}

func TestDoctorReportsUnreadableControlResultsWithoutMutation(t *testing.T) {
	for _, body := range []string{`{"version":1,"run_uid":"run","future_field":true}`, `broken json`} {
		t.Run(body, func(t *testing.T) {
			l := state.Layout{Root: t.TempDir()}
			path := runner.RequestResultPath(l, "request")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			checks, err := LocalChecks(state.OSFS{}, l)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, check := range checks {
				if check.Name == "control result request" && !check.Passed && strings.Contains(check.Detail, "current build") && strings.Contains(check.Detail, "preserve") {
					found = true
				}
			}
			if !found {
				t.Fatal(checks)
			}
			saved, err := os.ReadFile(path)
			if err != nil || string(saved) != body {
				t.Fatal("doctor changed control result", string(saved), err)
			}
		})
	}
}

type unreadableControlResult struct {
	state.OSFS
	path string
}

func (f unreadableControlResult) Lstat(path string) (fs.FileInfo, error) {
	if path == f.path {
		return nil, &os.PathError{Op: "lstat", Path: path, Err: fs.ErrPermission}
	}
	return f.OSFS.Lstat(path)
}

func TestDoctorControlResultPermissionRemedy(t *testing.T) {
	l := state.Layout{Root: t.TempDir()}
	path := runner.RequestResultPath(l, "request")
	if err := state.WriteJSON(state.OSFS{}, path, runner.RequestResult{Version: 1, RunUID: "run"}); err != nil {
		t.Fatal(err)
	}
	checks, err := LocalChecks(unreadableControlResult{path: path}, l)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, check := range checks {
		if check.Name == "control result request" && !check.Passed {
			found = true
			if !strings.Contains(check.Detail, "permissions") || strings.Contains(check.Detail, "newer build") {
				t.Fatal(check.Detail)
			}
		}
	}
	if !found {
		t.Fatal(checks)
	}
}
