package main

import (
	"context"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/store"
)

// Command parsing and unconfigured boundary tests use temporary local state.
// Configured native CRUD/CAS lives in native_test.go.
func jobCmdEnv(t *testing.T) context.Context {
	t.Helper()
	t.Setenv("HERDR_KATA_HOME", t.TempDir())
	return context.Background()
}

// storeForEnv opens the same database the commands just wrote to.
func storeForEnv(t *testing.T) *store.Store {
	t.Helper()
	s, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestJobAddRejectsIncompleteJobs(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"no id", []string{"--prompt", "hi"}, "--id is required"},
		{"no prompt and no workflow", []string{"--id", "x"}, "--prompt is required"},
		{"prompt and workflow together", []string{"--id", "x", "--prompt", "hi", "--workflow", "f"},
			"not both"},
		{"cron schedule without an expression",
			[]string{"--id", "x", "--prompt", "hi", "--schedule", "cron"}, "--cron"},
		{"interval schedule without an interval",
			[]string{"--id", "x", "--prompt", "hi", "--schedule", "interval"}, "--interval"},
		{"once schedule without a time",
			[]string{"--id", "x", "--prompt", "hi", "--schedule", "once"}, "--at"},
		{"unknown catchup policy",
			[]string{"--id", "x", "--prompt", "hi", "--catchup", "sometimes"}, "--catchup"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jobCmdEnv(t)
			err := jobAdd(tc.argv)
			if err == nil {
				t.Fatal("expected an error, the job was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestJobEnableAndRemoveNeedAnID(t *testing.T) {
	jobCmdEnv(t)
	if err := jobEnable(nil, false); err == nil {
		t.Error("pause with no id must fail rather than touch every job")
	}
	if err := jobRemove(nil); err == nil {
		t.Error("remove with no id must fail rather than delete anything")
	}
}
