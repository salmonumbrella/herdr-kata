package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/herdrcli"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/lockfile"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/store"
	"github.com/salmonumbrella/herdr-kata/internal/workflow"
)

type nativeFireTime struct{}

func executeNative(ctx context.Context, s *store.Store, j store.Job, trigger string) (*runner.Run, error) {
	if s.Native.Client == nil {
		return nil, store.ErrNativeUnconfigured
	}
	if j.NativeOffline {
		return nil, errors.New("local freshness policy requires current definitions for new work; saved runs may resume offline")
	}
	if _, err := s.Native.JobFrom(katacli.Definition{Definition: j.NativeDefinition}, false); err != nil {
		return nil, err
	}
	var notification nativeDateDefinition
	if err := katacli.Decode(j.NativeDefinition, &notification); err != nil {
		return nil, err
	}
	if notification.Action.Kind == "notify" {
		return executeNativeNotification(ctx, s, j, trigger, notification)
	}
	if j.CheckoutKey == "" || j.CWD == "" || s.Native.Binding.Checkouts[j.CheckoutKey] != j.CWD {
		return nil, errors.New("map this native checkout before execution; no current-directory fallback")
	}
	info, err := os.Stat(j.CWD)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("native checkout is not a directory")
	}
	uid, err := katacli.NewUID()
	if err != nil {
		return nil, err
	}
	target := s.Native.Client.Target
	target.Token = ""
	c := runner.NativeExecutionContext{Version: 1, RunUID: uid, Target: target, ProjectUID: s.Native.Binding.ProjectUID, ExecutorLabel: s.Native.Binding.ExecutorLabel, Runtime: j}
	if _, err = katacli.NormalizeUID(j.ID); err != nil {
		return nil, err
	}
	c.Job = &katacli.Definition{UID: j.ID, Name: j.Name, DefinitionEventUID: j.NativeEventUID, Definition: append(json.RawMessage(nil), j.NativeDefinition...)}
	if j.Workflow != "" {
		fd, e := s.Native.Client.Definition(ctx, "workflow", j.Workflow)
		if e != nil {
			return nil, e
		}
		c.Workflow = &fd
	}
	if trigger != "manual" {
		if source, ok := ctx.Value(nativeSourceKey{}).(string); ok && source != "" {
			// An ordinary reread invalidates queued work after a moved/cleared
			// source; it supplies no execution permission or reservation.
			_, current, e := nativeDateJob(ctx, s, j)
			if e != nil {
				return nil, e
			}
			if current != source {
				return nil, errors.New("native planning-date source changed before local execution")
			}
			c.Occurrence = source
		}
		if fire, ok := ctx.Value(nativeFireTime{}).(time.Time); ok && !fire.IsZero() {
			if c.Occurrence == "" {
				c.Occurrence = fire.UTC().Format(time.RFC3339Nano)
			}
		} else if j.RunAt != nil {
			if c.Occurrence == "" {
				c.Occurrence = j.RunAt.UTC().Format(time.RFC3339Nano)
			}
		}
	}
	policy, err := nativeRunIssuePolicy(c)
	if err != nil {
		return nil, err
	}
	switch policy.Kind {
	case "existing":
		c.IssueUID = policy.UID
	case "per-run":
		c.IssuePreparedAt = time.Now().UTC()
		c.Runtime.Ref = ""
	default:
		return nil, errors.New("execute requires an existing or per-run issue policy")
	}
	if c.IssueUID != "" {
		if err = nativeIssueReady(ctx, s, c.IssueUID); err != nil {
			return nil, err
		}
	}
	// An actual linked issue becomes the runtime/local-row reference. Without
	// one, retain the raw local ref; shared evidence still has no issue UID.
	if c.IssueUID != "" {
		c.Runtime.Ref = c.IssueUID
	}
	var action struct{ Action struct{ Kind string } }
	if katacli.Decode(j.NativeDefinition, &action) != nil || action.Action.Kind != "execute" {
		return nil, errors.New("this runtime executes ordinary execute actions; notify delivery uses the ordinary bridge")
	}
	if err = c.Save(runDirFor(uid)); err != nil {
		return nil, err
	}
	return runNativeContext(ctx, s, c, store.Run{ID: uid, JobID: j.ID, Trigger: trigger, RunDir: runDirFor(uid), Workflow: j.Workflow, Input: j.Input, Ref: c.Runtime.Ref}, workflowOpts{})
}

func nativeIssueReady(ctx context.Context, s *store.Store, uid string) error {
	canonical, err := katacli.NormalizeUID(uid)
	if err != nil || canonical != uid {
		return errors.New("linked issue identity must be canonical")
	}
	var out struct {
		Issue struct {
			UID        string     `json:"uid"`
			ProjectUID string     `json:"project_uid"`
			Status     string     `json:"status"`
			DeletedAt  *time.Time `json:"deleted_at"`
		} `json:"issue"`
	}
	if err := s.Native.Client.Show(ctx, uid, &out); err != nil {
		return err
	}
	if out.Issue.UID != uid || (out.Issue.ProjectUID != "" && out.Issue.ProjectUID != s.Native.Binding.ProjectUID) {
		return errors.New("Kata returned another linked issue identity")
	}
	if out.Issue.Status == "closed" || out.Issue.DeletedAt != nil {
		return errors.New("linked issue is closed or deleted under local readiness policy")
	}
	var ready struct {
		Issues []struct {
			UID        string `json:"uid"`
			ProjectUID string `json:"project_uid"`
		} `json:"issues"`
	}
	if err := s.Native.Client.Ready(ctx, &ready); err != nil {
		return fmt.Errorf("ordinary issue readiness: %w", err)
	}
	for _, issue := range ready.Issues {
		if issue.UID == uid && (issue.ProjectUID == "" || issue.ProjectUID == s.Native.Binding.ProjectUID) {
			return nil
		}
	}
	return errors.New("linked issue is not in the selected project's ordinary ready list under local readiness policy")
}

func runNativeContext(ctx context.Context, s *store.Store, c runner.NativeExecutionContext, rec store.Run, opts workflowOpts) (*runner.Run, error) {
	if rec.ID != c.RunUID {
		return nil, errors.New("saved local run identity does not match requested run")
	}
	if err := c.Check(s.Native); err != nil {
		return nil, err
	}
	// Serialize only this local saved UID across initial execution and resume.
	// Independent run UIDs, including identical occurrences, use separate locks.
	execution, err := lockfile.Acquire(filepath.Join(runDirFor(c.RunUID), "execution.lock"))
	if err != nil {
		return nil, err
	}
	defer execution.Release()
	if opts.OnlyIssuePreparation {
		fresh, err := s.Run(ctx, c.RunUID)
		if err != nil {
			return nil, err
		}
		c, err = runner.LoadNativeContext(runDirFor(c.RunUID))
		if err != nil {
			return nil, err
		}
		if err := c.Check(s.Native); err != nil {
			return nil, err
		}
		if c.RunUID != fresh.ID || !nativeIssuePreparationPending(c, *fresh) {
			return nil, errors.New("run is not awaiting per-run issue preparation")
		}
		rec = *fresh
	}
	if c.Job != nil {
		if _, err := s.Native.JobFrom(*c.Job, false); err != nil {
			return nil, err
		}
	}
	if c.Workflow != nil {
		if _, err := workflow.FromNative(*c.Workflow); err != nil {
			return nil, err
		}
	}
	secrets, err := nativeSecrets(c, s.Native)
	if err != nil {
		return nil, err
	}
	if err := prepareNativeRunIssue(ctx, s, &c); err != nil {
		// A refusal must preserve historical evidence, including modern contexts
		// whose bound issue no longer passes readiness after execution.
		if c.IssuePreparedAt.IsZero() || (!rec.StartedAt.IsZero() && !nativeIssuePreparationPending(c, rec)) {
			return nil, fmt.Errorf("prepare issue for local run %s: %w", c.RunUID, err)
		}
		// Retain a resumable local row even when ordinary creation was accepted
		// but its reply was lost. No child or running observation has started.
		rec.Ref = c.IssueUID
		rec.Outcome, rec.ParkReason = "parked", "blocked"
		rec.Note = nativeIssuePreparationNote
		rec.StartedAt = c.IssuePreparedAt
		ended := time.Now()
		rec.EndedAt = &ended
		if rec.RunDir == "" {
			rec.RunDir = runDirFor(c.RunUID)
		}
		return nil, errors.Join(fmt.Errorf("prepare issue for local run %s (inspect with herdr-kata run show %s): %w", c.RunUID, c.RunUID, err), s.PutRun(ctx, rec))
	}
	if c.IssueUID != "" {
		rec.Ref = c.IssueUID
	}
	j := c.Runtime
	var provenance *store.Run
	if j.Persistent && c.Workflow == nil {
		var err error
		provenance, err = nativePersistentProvenance(ctx, s, c)
		if err != nil {
			return nil, err
		}
	}
	if rec.RunDir == "" {
		rec.RunDir = runDirFor(c.RunUID)
	}
	rec.StartedAt = time.Now()
	rec.Outcome = "running"
	rec.EndedAt = nil
	if err := s.PutRun(ctx, rec); err != nil {
		return nil, err
	}
	if err := cancelNativeAttention(s, "run-job:"+rec.JobID); err != nil {
		fmt.Fprintln(os.Stderr, "herdr-kata: superseded attention:", err)
	}
	delivery := NativeDelivery{Version: 1, TargetKey: nativeTargetKey(c.Target), ProjectUID: c.ProjectUID, Actor: c.Target.Actor, Teammate: c.Target.Teammate, RunUID: c.RunUID}
	initial := nativeObservation(c, "running", rec.StartedAt, time.Time{})
	if err := queueNativeObservation(ctx, nativeDeliveryPath(rec.RunDir), delivery, initial); err != nil {
		fmt.Fprintln(os.Stderr, "herdr-kata: local observation buffer:", err)
	}
	env := nativeRunnerEnvironment(c, s.Native)
	for name, value := range secrets {
		env[name] = value
	}
	r := &runner.Runner{Herdr: herdrcli.New(), StateDir: stateDir(), Store: s, Env: env}
	if j.Persistent && c.Workflow == nil {
		r.BeforeReuse = func(ctx context.Context, _ runner.Job, ag *herdrcli.Agent, how string) error {
			if len(secrets) != 0 && (how == "kept" || how == "adopted") {
				return errors.New("mapped secrets require a fresh agent process; inspect the existing live conversation before restarting it")
			}
			return checkNativeConversation(ctx, s, c, provenance, ag, how)
		}
	}
	var run *runner.Run
	var execErr error
	var space *runner.WorkflowSpace
	var savedWorkflow workflow.Workflow
	var workflowRun *runner.WorkflowRun
	if c.Workflow == nil {
		job := runner.FromStore(j)
		job.Prompt += nativePromptInstructions(c.Target)
		run, execErr = r.ExecuteIn(ctx, job, c.RunUID, rec.RunDir)
	} else {
		def, err := workflow.FromNative(*c.Workflow)
		if err != nil {
			return nil, err
		}
		if err = s.SeedRunSteps(ctx, rec.ID, def.Steps); err != nil {
			return nil, err
		}
		savedWorkflow = def
		space = openWorkflowSpace(ctx, s, def, &rec, j.CWD)
		if err = s.PutRun(ctx, rec); err != nil {
			return nil, err
		}
		w := runner.Workflow{Space: space, Launch: func(ctx context.Context, job runner.Job, uid, dir string) (*runner.Run, error) {
			job.Prompt += nativePromptInstructions(c.Target)
			return r.ExecuteIn(ctx, job, uid, dir)
		}, ProcessEnv: func(v []string) []string { return nativeEnvironmentWithSecrets(c.Environment(s.Native, v), secrets) }, Report: func(sr runner.StepRun) { persistStep(ctx, s, rec.ID, sr) }, ResetLoops: opts.ResetLoops}
		wr, e := w.Execute(ctx, j, def, rec.Input, rec.ID, rec.RunDir)
		workflowRun = wr
		execErr = e
		status := "ok"
		if wr.Outcome != runner.OutcomeDone {
			status = "error"
		}
		run = &runner.Run{JobID: j.ID, RunID: c.RunUID, RunDir: rec.RunDir, Outcome: wr.Outcome, ParkReason: wr.ParkReason, StartedAt: wr.StartedAt, EndedAt: wr.EndedAt, Result: &runner.Result{Status: status, Note: wr.Note()}, Err: e}
	}
	if run == nil {
		return nil, execErr
	}
	rec.Outcome, rec.ParkReason, rec.Note = string(run.Outcome), string(run.ParkReason), run.Note()
	rec.TabID, rec.AgentName = run.TabID, run.AgentName
	rec.Context, rec.ContextSession, rec.ContextNote = run.Context, run.ContextSession, run.ContextNote
	rec.EndedAt = &run.EndedAt
	if err := s.PutRun(ctx, rec); err != nil {
		if c.Workflow != nil {
			closeWorkflowSpace(ctx, s, space, savedWorkflow, rec, workflowRun)
		}
		return run, err
	}
	if c.Workflow != nil {
		closeWorkflowSpace(ctx, s, space, savedWorkflow, rec, workflowRun)
	}
	if err := queueRunAttention(s, c, rec); err != nil {
		fmt.Fprintln(os.Stderr, "herdr-kata: attention buffer:", err)
	}
	if err := queueHistoricalResult(s, c, rec); err != nil {
		fmt.Fprintln(os.Stderr, "herdr-kata: historical result buffer:", err)
	}
	status := "unknown"
	switch run.Outcome {
	case runner.OutcomeDone:
		status = "succeeded"
	case runner.OutcomeFailed:
		status = "failed"
	}
	if err := queueNativeObservation(ctx, nativeDeliveryPath(rec.RunDir), delivery, nativeObservation(c, status, rec.StartedAt, run.EndedAt)); err != nil {
		fmt.Fprintln(os.Stderr, "herdr-kata: local observation buffer:", err)
	} else {
		client := *s.Native.Client
		path := nativeDeliveryPath(rec.RunDir)
		s.StartBackground(ctx, func(ctx context.Context) { deliverNativeObservations(ctx, client, path) })
	}
	return run, execErr
}

func nativeObservation(c runner.NativeExecutionContext, status string, start, end time.Time) katacli.RunObservation {
	dto := katacli.RunObservation{OccurrenceKey: c.Occurrence, IssueUID: c.IssueUID, Teammate: c.Target.Teammate, ExecutorLabel: c.ExecutorLabel, Status: status, Summary: json.RawMessage(`{"version":1,"message":"Local execution reported"}`), StartedAt: start.UTC().Format(time.RFC3339Nano)}
	if c.Job != nil {
		dto.JobUID = c.Job.UID
		dto.DefinitionEventUID = c.Job.DefinitionEventUID
	}
	if c.Workflow != nil {
		dto.WorkflowUID = c.Workflow.UID
		dto.WorkflowDefinitionEventUID = c.Workflow.DefinitionEventUID
	}
	if !end.IsZero() {
		dto.EndedAt = end.UTC().Format(time.RFC3339Nano)
	}
	return dto
}
func nativeTargetKey(t katacli.Target) string { return katacli.LocalTargetKey(t) }

func nativeRunnerEnvironment(c runner.NativeExecutionContext, repo *store.NativeRepository) map[string]string {
	env := map[string]string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "KATA_") || upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" || upper == "NO_PROXY" || upper == "PORT" {
			env[key] = ""
		}
	}
	for _, key := range []string{"KATA_DB", "KATA_CONFIG", "KATA_SESSION_ID", "KATA_ALLOW_INSECURE"} {
		env[key] = ""
	}
	if repo != nil && repo.Client != nil {
		for _, entry := range c.Environment(repo, nil) {
			key, value, _ := strings.Cut(entry, "=")
			env[key] = value
		}
	}
	return env
}
func nativePromptInstructions(t katacli.Target) string {
	prefix, _ := json.Marshal([]string{"kata", "--project", t.Project, "--workspace", t.Workspace, "--as", t.Actor, "--teammate", t.Teammate})
	return "\nFor Kata operations use this explicit argv prefix for this run: " + string(prefix) + ". Routing credentials are supplied locally."
}
