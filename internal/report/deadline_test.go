package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

// The test binary stands in for the Kata executable without reading any daemon
// or account data. TestMain intercepts only explicitly marked fixture children.
func TestMain(m *testing.M) {
	if dir := os.Getenv("HK_OUTBOX_CLI_FIXTURE"); dir != "" {
		outboxCLIChild(dir)
		return
	}
	os.Exit(m.Run())
}

type outboxCLICall struct {
	Args []string
	Body []byte
}

func outboxCLIChild(dir string) {
	args := os.Args[1:]
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(3)
	}
	encoded, err := json.Marshal(outboxCLICall{Args: args, Body: b})
	if err != nil {
		os.Exit(3)
	}
	f, err := os.OpenFile(filepath.Join(dir, "calls.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(3)
	}
	if _, err = f.Write(append(encoded, '\n')); err != nil {
		os.Exit(3)
	}
	if err = f.Close(); err != nil {
		os.Exit(3)
	}
	if len(args) < 13 {
		os.Exit(3)
	}
	switch args[11] {
	case "observe":
		// Consume most of the shared budget, then force conflict readback.
		<-time.NewTimer(kata.CallTimeout - 3*time.Second).C
		fmt.Fprintln(os.Stderr, "conflict")
		os.Exit(5)
	case "show":
		// The concrete client must kill this child when the original budget expires.
		<-time.NewTimer(2 * kata.CallTimeout).C
		os.Exit(7)
	default:
		os.Exit(3)
	}
}

func TestOutboxCLIDeliverySharesDeadline(t *testing.T) {
	o, p, _ := outboxFixture(t)
	discover(t, o, p, []string{"missing"})
	p.JobUID = "second-job"
	discover(t, o, p, []string{"missing"})
	initial := records(t, o)
	frozen := map[string][]byte{}
	for _, r := range initial {
		frozen[r.RunUID] = bytes.Clone(r.Body)
	}
	dir := t.TempDir()
	t.Setenv("HK_OUTBOX_CLI_FIXTURE", dir)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	k := kata.New(bin, "fixture", map[string]string{"KATA_HOME": dir}, "example-project", "worker")
	started := time.Now()
	err = deliver(t, o, k, at)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("conflict readback should time out")
	}
	if elapsed > kata.CallTimeout+5*time.Second {
		t.Fatalf("delivery exceeded one shared deadline: %s", elapsed)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("calls=%d, want one observe and one readback", len(lines))
	}
	var calls [2]outboxCLICall
	for i, line := range lines {
		if err := json.Unmarshal([]byte(line), &calls[i]); err != nil {
			t.Fatal(err)
		}
	}
	if len(calls[0].Args) != 15 {
		t.Fatalf("observe argv %v", calls[0].Args)
	}
	uid := calls[0].Args[12]
	expectedPrefix := []string{"--project", "example-project", "--daemon", "fixture", "--as", "worker", "--teammate", "herdr-kata", "--json", "cron", "run"}
	if !slices.Equal(calls[0].Args, append(slices.Clone(expectedPrefix), "observe", uid, "--json-input", "-")) || !slices.Equal(calls[1].Args, append(slices.Clone(expectedPrefix), "show", uid)) || !bytes.Equal(calls[0].Body, frozen[uid]) {
		t.Fatalf("CLI calls %+v", calls)
	}
	attempted := 0
	for _, r := range records(t, o) {
		if r.State != "pending" || !bytes.Equal(r.Body, frozen[r.RunUID]) {
			t.Fatal("timeout changed frozen pending intent")
		}
		if !r.LastAttempt.IsZero() {
			attempted++
		}
	}
	if attempted != 1 {
		t.Fatalf("attempted %d records in one delivery, want 1", attempted)
	}
}
