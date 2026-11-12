package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/salmonumbrella/herdr-kata/internal/clock"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

func hasInputs(job kata.CronJob, wfs map[string]kata.CronWorkflow) bool {
	for _, key := range []string{"inputs", "input_values"} {
		if _, ok := job.Definition.Options[key]; ok {
			return true
		}
	}
	if uid := job.Definition.Action.WorkflowUID; uid != nil {
		_, ok := wfs[*uid].Definition.Options["inputs"]
		return ok
	}
	return false
}

func supportedVersion(version string, minor int) (bool, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return false, nil
	}
	if suffix := strings.IndexAny(version, "-+"); suffix >= 0 {
		version = version[:suffix]
	}
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false, fmt.Errorf("invalid daemon API version %q", version)
	}
	n := [3]int{}
	for i, part := range parts {
		v, err := strconv.Atoi(part)
		if err != nil || v < 0 {
			return false, fmt.Errorf("invalid daemon API version %q", version)
		}
		n[i] = v
	}
	return n[0] > 0 || n[1] >= minor, nil
}

func inputValues(result kata.InputResolution) (json.RawMessage, error) {
	values := make(map[string]any)
	for _, input := range result.Inputs {
		if input.Source == "unset" {
			continue
		}
		if len(input.Value) == 0 || string(input.Value) == "null" {
			return nil, fmt.Errorf("resolved input %q has no value", input.Name)
		}
		switch input.Type {
		case "string", "enum":
			var v string
			if err := json.Unmarshal(input.Value, &v); err != nil {
				return nil, err
			}
			values[input.Name] = v
		case "bool":
			var v bool
			if err := json.Unmarshal(input.Value, &v); err != nil {
				return nil, err
			}
			values[input.Name] = v
		case "int":
			var v int64
			if err := json.Unmarshal(input.Value, &v); err != nil {
				return nil, err
			}
			values[input.Name] = v
		default:
			return nil, fmt.Errorf("unknown resolved input type %q", input.Type)
		}
	}
	return json.Marshal(values)
}

func (w *Worker) prepareInputs(ctx context.Context, k kata.Kata, project config.Project, job kata.CronJob, wfs map[string]kata.CronWorkflow, p defs.Plan, key, occurrence, requestID, remoteUID string, now time.Time, deadline ...time.Time) error {
	if !hasInputs(job, wfs) && remoteUID == "" {
		return w.prepare(ctx, k, project, p, key, occurrence, requestID, now, deadline...)
	}
	refuse := func(reason, detail string) error {
		if len(deadline) > 0 && !clock.Current(ctx, now).Before(deadline[0]) {
			return nil
		}
		r := journal.Run{Version: 1, Key: key, RunUID: ulid.Make().String(), Project: project.Name, Actor: project.Actor, Executor: w.Config.Executor, JobUID: p.JobUID, Occurrence: occurrence, RequestID: requestID, Phase: "settled", Outcome: "cancelled", Hold: reason, Detail: detail, Plan: p, StartedAt: new(now.UTC()), EndedAt: new(now.UTC())}
		err := w.Store.Create(r)
		if errors.Is(err, journal.ErrExists) {
			return nil
		}
		return err
	}
	version, err := k.HealthVersion()
	if err != nil {
		return err
	}
	floor := 48
	if remoteUID != "" {
		floor = 49
	}
	ok, err := supportedVersion(version, floor)
	if err != nil {
		return err
	}
	if !ok {
		version = strings.TrimSpace(version)
		if version == "" {
			version = "absent"
		}
		return refuse(journal.HoldInputsUnavailable, "daemon API "+version)
	}
	firstMismatch := ""
	for attempt := 0; attempt < 2; attempt++ {
		result, err := k.JobInputs(job.UID, remoteUID)
		if err != nil {
			return err
		}
		if result.JobUID == p.JobUID && result.DefinitionEventUID == p.JobEventUID && result.WorkflowUID == p.WorkflowUID && result.WorkflowDefinitionEventUID == p.WorkflowEventUID {
			if len(result.Errors) > 0 {
				b, _ := json.Marshal(result.Errors)
				return refuse(journal.HoldInvalidInputs, string(b))
			}
			p.InputValues, err = inputValues(result)
			if err != nil {
				return err
			}
			return w.prepare(ctx, k, project, p, key, occurrence, requestID, now, deadline...)
		}
		mismatch := fmt.Sprintf("plan %s/%s, resolution %s/%s", p.JobEventUID, p.WorkflowEventUID, result.DefinitionEventUID, result.WorkflowDefinitionEventUID)
		if attempt == 1 {
			return fmt.Errorf("input definition mismatch: first %s; second %s", firstMismatch, mismatch)
		}
		firstMismatch = mismatch
		jobs, err := k.Jobs()
		if err != nil {
			return err
		}
		if jobs.Paused() {
			return errors.New("project paused during input resolution")
		}
		workflows, err := k.Workflows()
		if err != nil {
			return err
		}
		fresh := make(map[string]kata.CronWorkflow)
		for _, wf := range workflows {
			fresh[wf.UID] = wf
		}
		found := false
		for _, candidate := range jobs.Jobs {
			if candidate.UID == job.UID && candidate.DefinitionEventUID == job.DefinitionEventUID && candidate.DeletedAt == nil {
				job = candidate
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("job definition changed during input resolution")
		}
		input := p.Input
		var reasons []string
		p, reasons = defs.Freeze(job, fresh, w.Config, project.Name, exec.LookPath)
		p.Input = input
		if len(reasons) > 0 {
			return fmt.Errorf("refreshed definition unsupported: %s", strings.Join(reasons, ", "))
		}
	}
	return errors.New("input resolution did not settle")
}
