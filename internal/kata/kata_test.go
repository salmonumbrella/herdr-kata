package kata

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"hegel.dev/go/hegel"
)

func stub(t *testing.T, fixture, code string) (Kata, func() []string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "kata")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$HK_TEST_ARGS"
printf '%s\n%s\n' "$KATA_HOME" "${KATA_AUTH_TOKEN-unset}" > "$HK_TEST_ENV"
cat > "$HK_TEST_INPUT"
if [ "$HK_TEST_CODE" = killed ]; then kill -TERM $$; fi
if [ "$HK_TEST_CODE" = 0 ]; then cat "$HK_TEST_REPLY"; else cat "$HK_TEST_REPLY" >&2; fi
exit "$HK_TEST_CODE"
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	reply, err := filepath.Abs(filepath.Join("testdata", fixture+".json"))
	if err != nil {
		t.Fatal(err)
	}
	for key, val := range map[string]string{"HK_TEST_ARGS": filepath.Join(dir, "args"), "HK_TEST_ENV": filepath.Join(dir, "env"), "HK_TEST_INPUT": filepath.Join(dir, "input"), "HK_TEST_REPLY": reply, "HK_TEST_CODE": code} {
		t.Setenv(key, val)
	}
	return New(bin, "fixture", map[string]string{"KATA_HOME": "/srv/kata-home"}, "example-project", "worker"), func() []string {
		b, err := os.ReadFile(filepath.Join(dir, "args"))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	}
}

var prefix = []string{"--project", "example-project", "--daemon", "fixture", "--as", "worker", "--teammate", "herdr-kata", "--json"}

func assertArgs(t *testing.T, got []string, tail ...string) {
	t.Helper()
	want := append(append([]string{}, prefix...), tail...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv %q != %q", got, want)
	}
}

func TestCreateAlwaysForceNewAndKey(t *testing.T) {
	k, args := stub(t, "create", "0")
	ref, uid, err := k.CreateIssue("Example scheduled work", "Fixture body", "cron-worker:fixture:issue", "")
	if err != nil || ref != "wp51" || uid != "01M6E4T4BKT5ZHTF2CHFZVWP51" {
		t.Fatalf("create: %q %q %v", ref, uid, err)
	}
	assertArgs(t, args(), "create", "Example scheduled work", "--body", "Fixture body", "--force-new", "--idempotency-key", "cron-worker:fixture:issue")
}

func TestCreateUnderRunParent(t *testing.T) {
	k, args := stub(t, "create", "0")
	if _, _, err := k.CreateIssue("Example scheduled work", "Fixture body", "cron-worker:fixture:issue", "01J00000000000000000000020"); err != nil {
		t.Fatal(err)
	}
	assertArgs(t, args(), "create", "Example scheduled work", "--body", "Fixture body", "--force-new", "--idempotency-key", "cron-worker:fixture:issue", "--parent", "01J00000000000000000000020")
}

func TestClassifyExitCodes(t *testing.T) {
	for _, tt := range []struct {
		code string
		want Class
	}{{"1", Uncertain}, {"2", Definitive}, {"3", Definitive}, {"4", Definitive}, {"5", Conflict}, {"6", Definitive}, {"7", Unavailable}, {"8", Uncertain}, {"killed", Uncertain}} {
		t.Run(tt.code, func(t *testing.T) {
			fixture := "exit" + tt.code
			if tt.code == "killed" {
				fixture = "exit1"
			}
			k, _ := stub(t, fixture, tt.code)
			err := k.Whoami()
			if err == nil || Classify(err) != tt.want {
				t.Fatalf("class %v for %v", Classify(err), err)
			}
		})
	}
}

func TestClassifyUnknownMutationExitSeven(t *testing.T) {
	k, _ := stub(t, "exit7", "7")
	path := filepath.Join(t.TempDir(), "reply.json")
	if err := os.WriteFile(path, []byte(`{"error":{"message":"daemon connection lost; mutation result may be unknown"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HK_TEST_REPLY", path)
	// Construct again so the child receives the replacement response path.
	k = New(k.(*cli).bin, "fixture", nil, "example-project", "worker")
	if err := k.Notify("abcd", "worker", "held"); err == nil || Classify(err) != Uncertain {
		t.Fatalf("unknown write is retryable: %v (%s)", err, Classify(err))
	}
}

func TestDeadlineReadsBackUnknownMutation(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "kata")
	flag := filepath.Join(dir, "written")
	calls := filepath.Join(dir, "calls")
	script := `#!/bin/sh
case " $* " in
 *" show "*)
  if [ -f "$FIXTURE_WRITTEN" ]; then printf '%s\n' '{"revision":3,"deadline_on":{"instant":"2026-11-03T09:00:00Z"}}'; else printf '%s\n' '{"revision":2,"deadline_on":null}'; fi;;
 *" deadline "*)
  case " $* " in *" --if-match 2 "*) ;; *) exit 3;; esac
  printf 'write\n' >> "$FIXTURE_CALLS"
  touch "$FIXTURE_WRITTEN"
  printf '%s\n' 'mutation result may be unknown' >&2; exit 7;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FIXTURE_WRITTEN", flag)
	t.Setenv("FIXTURE_CALLS", calls)
	k := New(bin, "fixture", nil, "example-project", "worker")
	at := time.Date(2026, 11, 3, 9, 0, 0, 123456789, time.UTC)
	for range 2 {
		if err := k.SetDeadline("abcd", at); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(calls)
	if err != nil || string(b) != "write\n" {
		t.Fatalf("deadline was retried: %q %v", b, err)
	}
}

func TestObserveRevisionMismatchRefused(t *testing.T) {
	k, _ := stub(t, "observe", "0")
	if _, _, err := k.Observe("01J00000000000000000000001", []byte(`{"expected_revision":0}`), 1); err == nil || Classify(err) != Definitive {
		t.Fatalf("mismatch: %v", err)
	}
}

func TestIssueTitle(t *testing.T) {
	k, args := stub(t, "create", "0")
	got, err := k.IssueTitle("01J00000000000000000000020")
	if err != nil || got != "Example scheduled work" {
		t.Fatalf("title %q %v", got, err)
	}
	assertArgs(t, args(), "show", "01J00000000000000000000020")
}

func TestObserveReplay(t *testing.T) {
	k, args := stub(t, "observe-replay", "0")
	body := []byte("{\n  \"expected_revision\":0,\"status\":\"running\",\"summary\":{\"version\":1}\n}\n")
	rev, replayed, err := k.Observe("01J00000000000000000000001", body, 0)
	if err != nil || rev != 1 || !replayed {
		t.Fatalf("observe: %d %v %v", rev, replayed, err)
	}
	assertArgs(t, args(), "cron", "run", "observe", "01J00000000000000000000001", "--json-input", "-")
	got, err := os.ReadFile(os.Getenv("HK_TEST_INPUT"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("mutated observation: %q", got)
	}
}

func TestReadyParse(t *testing.T) {
	k, args := stub(t, "ready", "0")
	got, err := k.Ready()
	if err != nil || !reflect.DeepEqual(got, map[string]bool{"01M6E4T4BKT5ZHTF2CHFZVWP51": true}) {
		t.Fatalf("ready: %v %v", got, err)
	}
	assertArgs(t, args(), "ready", "--limit", "0")
}

func TestPlanningDatesParse(t *testing.T) {
	k, args := stub(t, "dates", "0")
	got, err := k.PlanningDates("01M6E4T4BKT5ZHTF2CHFZVWP51")
	if err != nil || got == nil || got.Value != "2026-11-03T09:00:00Z" || got.Timezone != "UTC" || !got.Instant.Equal(time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("date: %+v %v", got, err)
	}
	assertArgs(t, args(), "show", "01M6E4T4BKT5ZHTF2CHFZVWP51", "--planning-dates")
}

func TestDefinitionsParse(t *testing.T) {
	t.Run("jobs", func(t *testing.T) {
		k, args := stub(t, "jobs", "0")
		got, err := k.Jobs()
		if err != nil || len(got) != 1 || got[0].UID != "01J00000000000000000000010" || got[0].Definition.Action.Kind != "execute" {
			t.Fatalf("jobs: %+v %v", got, err)
		}
		assertArgs(t, args(), "cron", "job", "list")
	})
	t.Run("workflows", func(t *testing.T) {
		k, args := stub(t, "workflows", "0")
		got, err := k.Workflows()
		if err != nil || len(got) != 1 || got[0].UID != "01J00000000000000000000011" || got[0].Definition.Steps[0].Key != "main" {
			t.Fatalf("workflows: %+v %v", got, err)
		}
		assertArgs(t, args(), "cron", "workflow", "list")
	})
}

func TestWriteAndProbeArgv(t *testing.T) {
	for _, method := range []string{"deadline", "notify", "whoami", "capabilities", "show"} {
		t.Run(method, func(t *testing.T) {
			fixture := "run-show"
			if method == "deadline" {
				fixture = "dates"
			}
			k, args := stub(t, fixture, "0")
			var err error
			switch method {
			case "deadline":
				err = k.SetDeadline("wp51", time.Date(2026, 11, 3, 9, 0, 0, 0, time.FixedZone("offset", 3600)))
				assertArgs(t, args(), "deadline", "wp51", "2026-11-03T08:00:00Z", "--if-match", "2")
			case "notify":
				err = k.Notify("wp51", "worker", "held: timeout")
				assertArgs(t, args(), "notify", "wp51", "--to", "worker", "--message", "held: timeout")
			case "whoami":
				err = k.Whoami()
				assertArgs(t, args(), "whoami")
			case "capabilities":
				err = k.Capabilities()
				assertArgs(t, args(), "cron", "capabilities")
			case "show":
				var rev int
				var b []byte
				rev, b, err = k.ShowRun("01J00000000000000000000001")
				if rev != 1 || !bytes.Contains(b, []byte(`"status":"running"`)) {
					t.Fatalf("show: %d %s", rev, b)
				}
				assertArgs(t, args(), "cron", "run", "show", "01J00000000000000000000001")
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestKataChildEnvironment(t *testing.T) {
	t.Setenv("KATA_AUTH_TOKEN", "poisoned")
	k, _ := stub(t, "whoami", "0")
	if err := k.Whoami(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(os.Getenv("HK_TEST_ENV"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "/srv/kata-home\nunset\n" {
		t.Fatalf("env: %q", b)
	}
	if os.Getenv("KATA_AUTH_TOKEN") != "poisoned" {
		t.Fatal("mutated parent")
	}
}

// Raw observation whitespace and arbitrary summaries survive the CLI boundary.
func TestObserveBytesStable(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		text := hegel.Draw(ht, hegel.Text())
		k, _ := stub(t, "observe-replay", "0")
		payload, err := json.Marshal(map[string]any{"expected_revision": 0, "status": "running", "summary": map[string]any{"version": 1, "message": text}})
		if err != nil {
			ht.Fatal(err)
		}
		body := append([]byte("\n "), payload...)
		body = append(body, '\n')
		if _, _, err := k.Observe("01J00000000000000000000001", body, 0); err != nil {
			ht.Fatal(err)
		}
		got, err := os.ReadFile(os.Getenv("HK_TEST_INPUT"))
		if err != nil {
			ht.Fatal(err)
		}
		if !bytes.Equal(got, body) {
			ht.Fatal("observation bytes changed")
		}
	})
}

// A multi-call report shares one deadline, including conflict readback.
func TestClientContextBoundsFollowupCalls(t *testing.T) {
	k, _ := stub(t, "whoami", "0")
	ctx, cancel := context.WithCancel(t.Context())
	bounded := WithContext(k, ctx)
	if err := bounded.Whoami(); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := bounded.Whoami(); err == nil {
		t.Fatal("followup ignored exhausted call budget")
	}
	if err := k.Whoami(); err != nil {
		t.Fatalf("budget leaked to ordinary client: %v", err)
	}
}

func TestRunsPerJobBoundedPage(t *testing.T) {
	k, args := stub(t, "runs", "0")
	runs, err := k.Runs("01J00000000000000000000010", 100)
	if err != nil || len(runs) != 1 || runs[0].UID != "01J00000000000000000000001" || runs[0].CreatedAt.Format(time.RFC3339) != "2026-11-02T09:00:00Z" || runs[0].ExecutorLabel == nil || *runs[0].ExecutorLabel != "other-worker" || runs[0].Summary.Message == nil || *runs[0].Summary.Message != "completed" {
		t.Fatalf("runs %+v err %v", runs, err)
	}
	assertArgs(t, args(), "cron", "run", "list", "--job-uid", "01J00000000000000000000010", "--limit", "100")
}
