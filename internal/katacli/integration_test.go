package katacli

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in integration uses a branch-built binary solely as an isolated fixture.
// Product runtime resolves the installed/configured binary, never this path.
func TestBranchNativeCLIIntegration(t *testing.T) {
	bin := os.Getenv("KATA_NATIVE_TEST_BINARY")
	if bin == "" {
		t.Skip("requires explicit isolated branch binary")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	home := t.TempDir()
	workspace := t.TempDir()
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		integrationFailure(t, e)
	}
	address := listener.Addr().String()
	listener.Close()
	logFile, e := os.Create(filepath.Join(t.TempDir(), "daemon.log"))
	if e != nil {
		integrationFailure(t, e)
	}
	defer logFile.Close()
	daemonCtx, stop := context.WithCancel(ctx)
	daemon := exec.CommandContext(daemonCtx, bin, "daemon", "start", "--foreground", "--listen", address, "--no-auto-token")
	for _, key := range []string{"PATH", "HOME", "LANG", "LC_ALL", "GOPATH", "GOTOOLCHAIN", "TMPDIR", "TEMP", "SYSTEMROOT", "SystemRoot", "WINDIR", "COMSPEC"} {
		if v, ok := os.LookupEnv(key); ok {
			daemon.Env = append(daemon.Env, key+"="+v)
		}
	}
	daemon.Env = append(daemon.Env, "KATA_HOME="+home, "KATA_AUTH_TOKEN=fixture-token", "GOMAXPROCS=2")
	daemon.Stdout = logFile
	daemon.Stderr = logFile
	if e := daemon.Start(); e != nil {
		integrationFailure(t, e)
	}
	t.Cleanup(func() { stop(); daemon.Wait() })
	server := "http://" + address
	httpClient := &http.Client{Timeout: 200 * time.Millisecond, Transport: &http.Transport{Proxy: nil}}
	for {
		response, e := httpClient.Get(server + "/api/v1/ping")
		if e == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		select {
		case <-ctx.Done():
			raw, _ := os.ReadFile(logFile.Name())
			t.Fatalf("isolated daemon unavailable: %s", raw)
		case <-time.After(20 * time.Millisecond):
		}
	}
	c := &Client{Executable: bin, Target: Target{Server: server, Project: "spoke-project", Workspace: workspace, Actor: "worker", Teammate: "adapter", Home: home, Token: "fixture-token"}, Timeout: 5 * time.Second}
	if e := c.Call(ctx, []string{"init"}, nil, new(any)); e != nil {
		var commandError *CommandError
		if errors.As(e, &commandError) {
			t.Fatalf("init: %v stdout=%s stderr=%s", e, commandError.Stdout, commandError.Stderr)
		}
		integrationFailure(t, e)
	}
	capabilities, e := c.Capabilities(ctx)
	if e != nil {
		integrationFailure(t, e)
	}
	t.Logf("advertised cron_v1 empty-project capability project=%s", capabilities.ProjectUID)
	var created struct {
		Issue struct {
			UID string `json:"uid"`
		} `json:"issue"`
	}
	if e := c.Create(ctx, "Inspect ; --daemon other", "quotes ' ; $(echo inert)\nsecond line", "isolated-create", &created); e != nil {
		integrationFailure(t, e)
	}
	if created.Issue.UID == "" {
		t.Fatal("issue receipt missing UID")
	}
	var shown struct {
		Issue struct {
			Body string `json:"body"`
		} `json:"issue"`
	}
	if e := c.Show(ctx, created.Issue.UID, &shown); e != nil || shown.Issue.Body != "quotes ' ; $(echo inert)\nsecond line" {
		t.Fatalf("actual argv/body separation %+v %v", shown, e)
	}
	if e := c.Comment(ctx, created.Issue.UID, "Review\nexact text", "isolated-comment", new(any)); e != nil {
		integrationFailure(t, e)
	}
	if e := c.Comments(ctx, created.Issue.UID, new(any)); e != nil {
		integrationFailure(t, e)
	}
	if e := c.Notify(ctx, created.Issue.UID, "worker/adapter", "Inspect this issue", false, new(any)); e != nil {
		integrationFailure(t, e)
	}
	var inbox struct {
		Recipient string            `json:"recipient"`
		Requests  []json.RawMessage `json:"requests"`
	}
	if e := c.Inbox(ctx, "worker/adapter", &inbox); e != nil || inbox.Recipient != "worker/adapter" || len(inbox.Requests) != 1 {
		t.Fatalf("exact inbox %+v %v", inbox, e)
	}
	workflowUID, _ := NewUID()
	workflowDraft, _ := NewDraft("workflow", workflowUID, "Inspect", json.RawMessage(`{"version":1,"steps":[{"key":"inspect","kind":"command","command":"git status","options":{"limit":9007199254740993}}]}`), "")
	workflowDef, e := c.Save(ctx, workflowDraft)
	if e != nil {
		integrationFailure(t, e)
	}
	if !strings.Contains(string(workflowDef.Definition), "9007199254740993") {
		t.Fatal("native workflow numbers rounded")
	}
	if retry, e := c.Save(ctx, workflowDraft); e != nil || retry.DefinitionEventUID != workflowDef.DefinitionEventUID {
		t.Fatalf("accepted create retry changed identity %+v %v", retry, e)
	}
	jobUID, _ := NewUID()
	body := json.RawMessage(`{"version":1,"kind":"job","enabled":false,"trigger":{"kind":"manual"},"action":{"kind":"execute","workflow_uid":"` + workflowUID + `"},"issue":{"kind":"existing","uid":"` + created.Issue.UID + `"},"overlap":"forbid","catchup":"all","options":{"counter":9007199254740993}}`)
	jobDraft, _ := NewDraft("job", jobUID, "Inspect", body, "")
	job, e := c.Save(ctx, jobDraft)
	if e != nil {
		integrationFailure(t, e)
	}
	if job.UID != jobUID {
		t.Fatal("ordinary create returned another job")
	}
	jobs, e := c.Definitions(ctx, "job", false)
	if e != nil || len(jobs) != 1 {
		t.Fatalf("new peer/native definition visibility %+v %v", jobs, e)
	}
	// A second explicit project on the same real daemon cannot read this job.
	other := *c
	other.Target.Project = "other-project"
	other.Target.Workspace = t.TempDir()
	other.Target.Actor = "other-worker"
	other.Target.Teammate = "other-adapter"
	if e := other.Call(ctx, []string{"init"}, nil, new(any)); e != nil {
		integrationFailure(t, e)
	}
	otherCapabilities, e := other.Capabilities(ctx)
	if e != nil || otherCapabilities.ProjectUID == capabilities.ProjectUID {
		t.Fatalf("explicit project routing %+v %v", otherCapabilities, e)
	}
	foreign, e := other.Definitions(ctx, "job", false)
	if e != nil || len(foreign) != 0 {
		t.Fatalf("cross-project native job visibility %+v %v", foreign, e)
	}
	if _, e := other.Definition(ctx, "job", jobUID); e == nil {
		t.Fatal("foreign project resolved native job UID")
	}
	update, _ := NewDraft("workflow", workflowUID, "Updated", workflowDef.Definition, workflowDef.DefinitionEventUID)
	updated, e := c.Save(ctx, update)
	if e != nil {
		integrationFailure(t, e)
	}
	if _, e := c.Save(ctx, update); e == nil {
		t.Fatal("stale winner accepted")
	}
	if _, e := c.DefinitionAction(ctx, "workflow", "delete", workflowUID, updated.DefinitionEventUID); e != nil {
		integrationFailure(t, e)
	}
	t.Logf("isolated actual CLI: CRUD/CAS/create-readback, exact numbers, issue/comment/notify/inbox and explicit cross-project routing passed")
}

func integrationFailure(t *testing.T, e error) {
	t.Helper()
	var ce *CommandError
	if errors.As(e, &ce) {
		t.Fatalf("%v stdout=%s stderr=%s", e, ce.Stdout, ce.Stderr)
	}
	t.Fatal(e)
}
