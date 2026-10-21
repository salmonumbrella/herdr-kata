//go:build kata || kata_replies

package kata

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/ident"
	"github.com/salmonumbrella/herdr-kata/internal/testenv"
)

func newKata(t *testing.T) *CLI {
	t.Helper()
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_KATA_BIN")
	home := t.TempDir()
	c := New(config.Kata{Bin: bin, Project: "adapter-test", Actor: "worker", Env: map[string]string{"KATA_HOME": home}}, home, nil, nil)
	// Start and stop only the daemon belonging to this test's isolated home.
	rawKata(t, c, "daemon", "start")
	t.Cleanup(func() { rawKata(t, c, "daemon", "stop") })
	rawKata(t, c, "projects", "create", "adapter-test")
	return c
}

func rawKata(t *testing.T, c *CLI, argv ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.cfg.Bin, append(argv, "--json", "--as", c.cfg.Actor)...)
	cmd.Env, cmd.Dir = append(c.Env(), "GOMAXPROCS=2"), c.stateDir
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("kata %q: %v %s", argv, err, exitStderr(err))
	}
	return output
}

func exitStderr(err error) []byte {
	if e, ok := err.(*exec.ExitError); ok {
		return e.Stderr
	}
	return nil
}

func fixtureJob(t *testing.T, c *CLI) Job {
	t.Helper()
	path := filepath.Join(c.stateDir, "job.json")
	body := `{"name":"Adapter test","definition":{"version":1,"kind":"job","enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"Inspect the task"},"issue":{"kind":"per-run","title":"Adapter task"},"overlap":"forbid","catchup":"skip"}}`
	requireNoError(t, os.WriteFile(path, []byte(body), 0600))
	var reply struct {
		Job Job `json:"job"`
	}
	requireNoError(t, json.Unmarshal(rawKata(t, c, "cron", "job", "create", "--project", c.cfg.Project, "--file", path), &reply))
	return reply.Job
}

func fixtureIssue(t *testing.T, c *CLI) IssueView {
	t.Helper()
	i, err := c.CreateIssue(t.Context(), CreateReq{Title: "Fix adapter integration", Body: "Validate adapter integration behavior", IdempotencyKey: "fixture"})
	requireNoError(t, err)
	return i
}

func observation(j Job) Observation {
	return Observation{JobUID: j.UID, DefinitionEventUID: j.DefinitionEventUID, OccurrenceKey: "test", Actor: "worker", ExecutorLabel: "installation", Status: "running", Summary: Summary{Version: 1, Message: "admitted"}}
}

func TestIntegCapabilities(t *testing.T) {
	c := newKata(t)
	x, err := c.Capabilities(t.Context())
	if err != nil || x.ProjectUID == "" || len(x.EventFeatures) == 0 || x.EventFeatures[0] != "cron_v1" {
		t.Fatal(x, err)
	}
}

func TestIntegIdentityAndDiagnose(t *testing.T) {
	c := newKata(t)
	caps, err := c.Capabilities(t.Context())
	requireNoError(t, err)
	i, err := c.FederationIdentity(t.Context())
	if err != nil || i.InstanceUID == "" || i.Actor != "worker" {
		t.Fatal(i, err)
	}
	d, err := c.DaemonDiagnose(t.Context(), caps.ProjectUID)
	if err != nil || d.State != "ready" || !d.Authoritative || d.Endpoint == "" {
		t.Fatal(d, err)
	}
	route := c.ResolveRoute()
	if route.Endpoint != d.Endpoint || route.DiagnoseState != d.State || route.Env["KATA_HOME"] != c.stateDir {
		t.Fatal(route)
	}
	d, err = c.DaemonDiagnose(t.Context(), ident.RunUID(time.Now(), "wrong-project"))
	if err != nil || d.State == "ready" || d.Authoritative {
		t.Fatal(d, err)
	}
}

func TestIntegJobCRUD(t *testing.T) {
	c := newKata(t)
	j := fixtureJob(t, c)
	got, err := c.ShowJob(t.Context(), j.UID)
	if err != nil || got.UID != j.UID || len(got.Definition) == 0 {
		t.Fatal(got, err)
	}
	jobs, err := c.ListJobs(t.Context())
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	wfpath := filepath.Join(c.stateDir, "workflow.json")
	requireNoError(t, os.WriteFile(wfpath, []byte(`{"name":"Check","definition":{"version":1,"steps":[{"key":"check","kind":"command","command":"true"}]}}`), 0600))
	var wr struct {
		Workflow Workflow `json:"workflow"`
	}
	requireNoError(t, json.Unmarshal(rawKata(t, c, "cron", "workflow", "create", "--project", c.cfg.Project, "--file", wfpath), &wr))
	wf, err := c.ShowWorkflow(t.Context(), wr.Workflow.UID)
	if err != nil || wf.UID != wr.Workflow.UID {
		t.Fatal(wf, err)
	}
	wfs, err := c.ListWorkflows(t.Context())
	if err != nil || len(wfs) != 1 {
		t.Fatal(wfs, err)
	}
	rawKata(t, c, "cron", "job", "delete", j.UID, "--expected-event-uid", j.DefinitionEventUID, "--project", c.cfg.Project)
	jobs, err = c.ListJobs(t.Context())
	if err != nil || len(jobs) != 0 {
		t.Fatal(jobs, err)
	}
}

func TestIntegObserveCreateReplayConflict(t *testing.T) {
	c := newKata(t)
	j := fixtureJob(t, c)
	body := observation(j)
	uid := ident.RunUID(time.Now(), "run")
	r, replay, err := c.Observe(t.Context(), uid, body)
	if err != nil || replay || r.Revision != 1 || r.Teammate == nil || *r.Teammate != "herdr-kata" {
		t.Fatal(r, replay, err)
	}
	_, replay, err = c.Observe(t.Context(), uid, body)
	if err != nil || !replay {
		t.Fatal(replay, err)
	}
	body.ExecutorLabel = "other"
	_, _, err = c.Observe(t.Context(), uid, body)
	if Class(err) != Conflict {
		t.Fatal(err)
	}
	r, err = c.ShowRun(t.Context(), uid)
	if err != nil || r.ExecutorLabel != "installation" {
		t.Fatal(r, err)
	}
}

func TestIntegObserveSummaryTokens(t *testing.T) {
	c := newKata(t)
	body := observation(fixtureJob(t, c))
	body.Summary.InputTokens, body.Summary.OutputTokens = 1200, 45
	uid := ident.RunUID(time.Now(), "tokens")
	if _, _, err := c.Observe(t.Context(), uid, body); err != nil {
		t.Fatal(err)
	}
	got, err := c.ShowRun(t.Context(), uid)
	if err != nil || got.Summary != body.Summary {
		t.Fatal(got.Summary, err)
	}
	if _, replay, err := c.Observe(t.Context(), uid, body); err != nil || !replay {
		t.Fatal("identical token observation did not replay", replay, err)
	}
}

func TestIntegCreateIdempotentLookAlikeCandidates(t *testing.T) {
	c := newKata(t)
	i := fixtureIssue(t, c)
	req := CreateReq{Title: i.Title, Body: i.Body, IdempotencyKey: "fixture"}
	again, err := c.CreateIssue(t.Context(), req)
	if err != nil || again.UID != i.UID {
		t.Fatal(again, err)
	}
	req.Body = "Different body"
	_, err = c.CreateIssue(t.Context(), req)
	assertCandidate(t, err, i.UID)
	req.Body, req.IdempotencyKey = i.Body, "second"
	_, err = c.CreateIssue(t.Context(), req)
	assertCandidate(t, err, i.UID)
	req.ForceNew = true
	fresh, err := c.CreateIssue(t.Context(), req)
	if err != nil || fresh.UID == i.UID {
		t.Fatal(fresh, err)
	}
}

func assertCandidate(t *testing.T, err error, uid string) {
	t.Helper()
	e, ok := err.(*Error)
	if !ok || Class(err) != Conflict || len(e.CandidateUIDs) != 1 || e.CandidateUIDs[0] != uid {
		t.Fatal(err)
	}
}

func TestIntegCloseValidation(t *testing.T) {
	c := newKata(t)
	i := fixtureIssue(t, c)
	req := CloseReq{Ref: i.UID, Reason: "done", Message: "short", IfMatch: strconv.FormatInt(i.Revision, 10), IdempotencyKey: "close"}
	if err := c.Close(t.Context(), req); err == nil || Class(err) != Fatal {
		t.Fatal(err)
	}
	req.Message, req.Evidence = "Verified the adapter integration against the real daemon successfully.", []string{"test:go test ./internal/kata"}
	requireNoError(t, c.Close(t.Context(), req))
	requireNoError(t, c.Close(t.Context(), req))
	i, err := c.ShowIssue(t.Context(), i.UID)
	if err != nil || i.Status != "closed" {
		t.Fatal(i, err)
	}
}

func TestIntegFlagLikeOperands(t *testing.T) {
	c := newKata(t)
	i, err := c.CreateIssue(t.Context(), CreateReq{Title: "-adapter", Body: "Flag-like operands are literal text"})
	requireNoError(t, err)
	if _, err = c.MetaSet(t.Context(), i.UID, "number", "-1", Cond{JSONValue: true}); err != nil {
		t.Fatal(err)
	}
	requireNoError(t, c.LabelAdd(t.Context(), i.UID, "-review"))
	i, err = c.ShowIssue(t.Context(), i.UID)
	if err != nil || string(i.Metadata["number"]) != "-1" || len(i.Labels) != 1 || i.Labels[0] != "-review" {
		t.Fatal(i, err)
	}
}

func TestIntegMetaConditional(t *testing.T) {
	c := newKata(t)
	i := fixtureIssue(t, c)
	cond := Cond{IfMatch: strconv.FormatInt(i.Revision, 10), IfAbsent: true}
	rev, err := c.MetaSet(t.Context(), i.UID, "work.attention", "ok", cond)
	if err != nil || rev <= i.Revision {
		t.Fatal(rev, err)
	}
	if _, err = c.MetaSet(t.Context(), i.UID, "work.attention", "stuck", cond); Class(err) != Conflict {
		t.Fatal(err)
	}
	value := `"ok"`
	rev, err = c.MetaUnset(t.Context(), i.UID, "work.attention", Cond{IfMatch: strconv.FormatInt(rev, 10), IfValue: &value, JSONValue: true})
	requireNoError(t, err)
	requireNoError(t, c.Deadline(t.Context(), i.UID, "2030-01-01T00:00:00Z", Cond{IfMatch: strconv.FormatInt(rev, 10)}))
	comment, err := c.Comment(t.Context(), CommentReq{Ref: i.UID, Body: "Verified metadata guards", IdempotencyKey: "comment"})
	requireNoError(t, err)
	if comment.UID == "" {
		t.Fatal("missing comment UID")
	}
	replay, err := c.Comment(t.Context(), CommentReq{Ref: i.UID, Body: "Verified metadata guards", IdempotencyKey: "comment"})
	requireNoError(t, err)
	if replay.UID != comment.UID {
		t.Fatal("keyed replay changed UID", replay, comment)
	}
	requireNoError(t, c.LabelAdd(t.Context(), i.UID, "needs-review"))
	i, err = c.ShowIssue(t.Context(), i.UID)
	if err != nil || len(i.Comments) != 1 || i.Comments[0].UID != comment.UID || len(i.Labels) != 1 || string(i.Metadata["deadline_on"]) != `"2030-01-01T00:00:00Z"` {
		t.Fatal(i, err)
	}
}

func TestIntegClaimIfUnowned(t *testing.T) {
	c := newKata(t)
	i := fixtureIssue(t, c)
	got, err := c.Claim(t.Context(), i.UID, true)
	if err != nil || got.Owner != "worker" {
		t.Fatal(got, err)
	}
	other := New(config.Kata{Bin: c.cfg.Bin, Project: c.cfg.Project, Actor: "other", Env: c.cfg.Env}, c.stateDir, nil, nil)
	if _, err = other.Claim(t.Context(), i.UID, true); Class(err) != Conflict {
		t.Fatal(err)
	}
}

func TestIntegReadyInbox(t *testing.T) {
	c := newKata(t)
	i := fixtureIssue(t, c)
	ready, err := c.Ready(t.Context())
	if err != nil || len(ready) != 1 || ready[0].UID != i.UID {
		t.Fatal(ready, err)
	}
	requireNoError(t, c.Notify(t.Context(), i.UID, "worker/runtime", "Please inspect"))
	i, err = c.ShowIssue(t.Context(), i.UID)
	requireNoError(t, err)
	var request struct {
		From     string `json:"from"`
		Teammate string `json:"teammate"`
		Message  string `json:"message"`
	}
	// Pin the recipient key and sender shape used by uncertain-notify read-back.
	requireNoError(t, json.Unmarshal(i.Metadata["notify.d29ya2VyL3J1bnRpbWU"], &request))
	if request.From != "worker" || request.Teammate != "herdr-kata" || request.Message != "Please inspect" {
		t.Fatalf("notify metadata contract: %+v", request)
	}
	inbox, err := c.Inbox(t.Context(), "worker/runtime")
	if err != nil || len(inbox) != 1 || inbox[0].Message != "Please inspect" {
		t.Fatal(inbox, err)
	}
}

func TestIntegListRunsPaginates150(t *testing.T) {
	c := newKata(t)
	j := fixtureJob(t, c)
	for n := range 150 {
		_, _, err := c.Observe(t.Context(), ident.RunUID(time.Now(), fmt.Sprint(n)), observation(j))
		requireNoError(t, err)
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		runs, next, err := c.ListRuns(t.Context(), j.UID, cursor, 100)
		requireNoError(t, err)
		for _, r := range runs {
			if seen[r.UID] {
				t.Fatal("duplicate", r.UID)
			}
			seen[r.UID] = true
		}
		if next == "" {
			break
		}
		if next == cursor {
			t.Fatal("cursor stalled")
		}
		cursor = next
	}
	if len(seen) != 150 {
		t.Fatal(len(seen))
	}
}

// #493 deliberately answers the same 404 for a missing resource and lost
// project access. The adapter must keep that answer uncertain.
func TestIntegTeamAccessRemovedMidRun(t *testing.T) {
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_KATA_BIN")
	home := t.TempDir()
	const ownerToken = "isolated-owner-token"
	t.Setenv("KATA_AUTH_TOKEN", ownerToken)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[auth]\ntoken = \"isolated-owner-token\"\nrequire_token_identity = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Kata{Bin: bin, Project: "adapter-test", Actor: "worker", CredentialEnv: "KATA_AUTH_TOKEN", Env: map[string]string{"KATA_HOME": home}}
	c := New(cfg, home, nil, nil)
	rawKata(t, c, "daemon", "start")
	t.Cleanup(func() { t.Setenv("KATA_AUTH_TOKEN", ownerToken); rawKata(t, c, "daemon", "stop") })

	var token struct {
		Token string `json:"plaintext"`
	}
	body := rawKata(t, c, "tokens", "create", "--actor", "worker")
	if err := json.Unmarshal(body, &token); err != nil || token.Token == "" {
		t.Fatal("member token unavailable", err)
	}
	t.Setenv("KATA_AUTH_TOKEN", token.Token)
	rawKata(t, c, "projects", "create", "adapter-test")
	t.Setenv("KATA_AUTH_TOKEN", ownerToken)
	rawKata(t, c, "teams", "create", "reviewers")
	rawKata(t, c, "teams", "members", "add", "reviewers", "--actor", "worker")
	rawKata(t, c, "projects", "access", "set", "adapter-test", "--visibility", "teams", "--team", "reviewers")
	t.Setenv("KATA_AUTH_TOKEN", token.Token)
	j := fixtureJob(t, c)
	issue := fixtureIssue(t, c)
	o := observation(j)
	o.IssueUID = issue.UID
	uid := ident.RunUID(time.Now().UTC(), "access-test")
	if _, _, err := c.Observe(t.Context(), uid, o); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KATA_AUTH_TOKEN", ownerToken)
	rawKata(t, c, "teams", "members", "remove", "reviewers", "--actor", "worker")
	t.Setenv("KATA_AUTH_TOKEN", token.Token)
	if _, err := c.ShowJob(t.Context(), j.UID); !NotFound(err) {
		t.Fatal("access loss did not withhold job", err)
	}
	if _, err := c.ListJobs(t.Context()); err == nil {
		t.Fatal("lost access supplied false absence proof")
	}
	if _, err := c.Comment(t.Context(), CommentReq{Ref: issue.UID, Body: "Run finished; report is pending access repair.", IdempotencyKey: "access-report"}); !Ambiguous(err) {
		t.Fatal("write access loss became a definitive refusal", err)
	}
	t.Setenv("KATA_AUTH_TOKEN", ownerToken)
	rawKata(t, c, "teams", "members", "add", "reviewers", "--actor", "worker")
	t.Setenv("KATA_AUTH_TOKEN", token.Token)
	if _, err := c.Comment(t.Context(), CommentReq{Ref: issue.UID, Body: "Run finished; report is pending access repair.", IdempotencyKey: "access-report"}); err != nil {
		t.Fatal("identical report did not recover", err)
	}
	if got, err := c.ShowRun(t.Context(), uid); err != nil || got.Status != "running" {
		t.Fatal("access loss changed evidence", got, err)
	}
}

func TestIntegRunParentClosedReplayAndLabelFilter(t *testing.T) {
	c := newKata(t)
	parent, err := c.CreateIssue(t.Context(), CreateReq{Title: "Run history", Body: "Groups the command runs", IdempotencyKey: "parent"})
	requireNoError(t, err)
	req := CreateReq{Title: "Command run", Body: "Validate the command output", Parent: parent.UID, Labels: []string{"herdr-kata:run"}, IdempotencyKey: "run-child"}
	child, err := c.CreateIssue(t.Context(), req)
	requireNoError(t, err)
	shown, err := c.ShowIssue(t.Context(), child.UID)
	requireNoError(t, err)
	if shown.ParentUID != parent.UID {
		t.Fatal("child has no parent", shown)
	}
	closeReq := CloseReq{Ref: parent.UID, Reason: "done", Message: "Verified the run history and completed all command checks.", Evidence: []string{"test:go test ./internal/kata"}, IdempotencyKey: "close-parent"}
	if err := c.Close(t.Context(), closeReq); err == nil {
		t.Fatal("closed parent with open child")
	}
	var ready struct {
		Issues []IssueView `json:"issues"`
	}
	requireNoError(t, json.Unmarshal(rawKata(t, c, "ready", "--project", c.cfg.Project, "--limit", "0", "--no-label", "herdr-kata:run"), &ready))
	if len(ready.Issues) != 1 || ready.Issues[0].UID != parent.UID {
		t.Fatal("run filter", ready)
	}
	closeChild := closeReq
	closeChild.Ref = child.UID
	closeChild.IdempotencyKey = "close-child"
	requireNoError(t, c.Close(t.Context(), closeChild))
	requireNoError(t, c.Close(t.Context(), closeReq))
	replay, err := c.CreateIssue(t.Context(), req)
	requireNoError(t, err)
	if replay.UID != child.UID {
		t.Fatal("closed parent changed replay", replay)
	}
	freshReq := req
	freshReq.Title = "Collect build artifacts"
	freshReq.IdempotencyKey = "next-run-child"
	fresh, err := c.CreateIssue(t.Context(), freshReq)
	requireNoError(t, err)
	shown, err = c.ShowIssue(t.Context(), fresh.UID)
	requireNoError(t, err)
	if shown.ParentUID != parent.UID || fresh.UID == child.UID {
		t.Fatal("closed parent fresh create", shown)
	}
}

func TestIntegRunParentInvisiblePreventsKeyedReplay(t *testing.T) {
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_KATA_BIN")
	home := t.TempDir()
	const ownerToken = "isolated-owner-token"
	t.Setenv("KATA_AUTH_TOKEN", ownerToken)
	requireNoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte("[auth]\ntoken = \"isolated-owner-token\"\nrequire_token_identity = true\n"), 0600))
	cfg := config.Kata{Bin: bin, Project: "adapter-test", Actor: "worker", CredentialEnv: "KATA_AUTH_TOKEN", Env: map[string]string{"KATA_HOME": home}}
	c := New(cfg, home, nil, nil)
	rawKata(t, c, "daemon", "start")
	t.Cleanup(func() { t.Setenv("KATA_AUTH_TOKEN", ownerToken); rawKata(t, c, "daemon", "stop") })
	var token struct {
		Token string `json:"plaintext"`
	}
	requireNoError(t, json.Unmarshal(rawKata(t, c, "tokens", "create", "--actor", "worker"), &token))
	if token.Token == "" {
		t.Fatal("member token unavailable")
	}
	t.Setenv("KATA_AUTH_TOKEN", token.Token)
	rawKata(t, c, "projects", "create", "adapter-test")
	rawKata(t, c, "projects", "create", "private-project")
	t.Setenv("KATA_AUTH_TOKEN", ownerToken)
	rawKata(t, c, "teams", "create", "operators")
	rawKata(t, c, "projects", "access", "set", "private-project", "--visibility", "teams", "--team", "operators")
	t.Setenv("KATA_AUTH_TOKEN", token.Token)
	parent, err := c.CreateIssue(t.Context(), CreateReq{Title: "Run history", IdempotencyKey: "parent"})
	requireNoError(t, err)
	req := CreateReq{Title: "Command run", Body: "Validate the command output", Parent: parent.UID, Labels: []string{"herdr-kata:run"}, IdempotencyKey: "run-child"}
	child, err := c.CreateIssue(t.Context(), req)
	requireNoError(t, err)
	t.Setenv("KATA_AUTH_TOKEN", ownerToken)
	rawKata(t, c, "teams", "members", "add", "operators", "--actor", "worker")
	t.Setenv("KATA_AUTH_TOKEN", token.Token)
	rawKata(t, c, "move", parent.UID, "private-project", "--project", cfg.Project)
	t.Setenv("KATA_AUTH_TOKEN", ownerToken)
	rawKata(t, c, "teams", "members", "remove", "operators", "--actor", "worker")
	t.Setenv("KATA_AUTH_TOKEN", token.Token)
	// Project and child remain accessible. Only the parent became invisible.
	if _, err := c.Capabilities(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ShowIssue(t.Context(), child.UID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIssue(t.Context(), req); !NotFound(err) || Class(err) != Retry {
		t.Fatal("invisible parent replay was not uncertain", err)
	}
	t.Setenv("KATA_AUTH_TOKEN", ownerToken)
	rawKata(t, c, "teams", "members", "add", "operators", "--actor", "worker")
	t.Setenv("KATA_AUTH_TOKEN", token.Token)
	rawKata(t, c, "move", parent.UID, cfg.Project, "--project", "private-project")
	t.Setenv("KATA_AUTH_TOKEN", token.Token)
	replay, err := c.CreateIssue(t.Context(), req)
	requireNoError(t, err)
	if replay.UID != child.UID {
		t.Fatal("restored parent duplicated child", replay)
	}
}

// The scheduled-request wake relies on Kata resolving the instant and on
// ready including the issue once that instant passes.
func TestIntegPlanningDatesAndReadyAtScheduledInstant(t *testing.T) {
	c := newKata(t)
	i := fixtureIssue(t, c)
	at := time.Now().UTC().Add(3 * time.Second).Truncate(time.Second)
	rawKata(t, c, "schedule", "--project", c.cfg.Project, i.UID, at.Format(time.RFC3339))
	p, err := c.PlanningDates(t.Context(), i.UID)
	if err != nil || p.IssueUID != i.UID || p.Scheduled == nil || p.Scheduled.Value != at.Format(time.RFC3339) || !p.Scheduled.Instant.Equal(at) {
		t.Fatalf("%+v %v", p, err)
	}
	ready, err := c.Ready(t.Context())
	if err != nil || len(ready) != 0 {
		t.Fatal("scheduled issue ready before its instant", ready, err)
	}
	// The real daemon's clock decides readiness, so wait in real time.
	select {
	case <-time.After(time.Until(at.Add(time.Second))):
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	ready, err = c.Ready(t.Context())
	if err != nil || len(ready) != 1 || ready[0].UID != i.UID {
		t.Fatal("scheduled issue not ready one second after its instant", ready, err)
	}
}
