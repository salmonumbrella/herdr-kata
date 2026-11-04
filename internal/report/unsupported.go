package report

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/sched"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// Record is a separate, durable observation intent, never a run journal.
// Body is saved once and replayed byte for byte, including its actor and times.
type Record struct {
	Checkpoint             *sched.Checkpoint `json:",omitempty"`
	Version                int
	Cause, Project, RunUID string
	Body                   json.RawMessage
	State, Error           string
	LastAttempt            time.Time
}

// UnmarshalJSON also accepts the base64 Body encoding used by earlier Version 1 files.
func (r *Record) UnmarshalJSON(b []byte) error {
	type stored Record
	var decoded stored
	if err := json.Unmarshal(b, &decoded); err != nil {
		return err
	}
	if len(decoded.Body) > 0 && decoded.Body[0] == '"' {
		var legacy []byte
		if err := json.Unmarshal(decoded.Body, &legacy); err != nil {
			return err
		}
		decoded.Body = legacy
	}
	*r = Record(decoded)
	return nil
}

// Outbox shares the worker's already-held lifetime lock; it acquires no lock.
type Outbox struct {
	Layout state.Layout
	Dir    string
}

func (o Outbox) dir() string {
	if o.Dir != "" {
		return o.Dir
	}
	return o.Layout.Unsupported()
}

func (o Outbox) save(r Record) error {
	return state.WriteJSON(filepath.Join(o.dir(), r.Cause+".json"), r)
}

func (o Outbox) All() ([]Record, error) {
	entries, err := os.ReadDir(o.dir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []Record
	var errs []error
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(o.dir(), entry.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("outbox record %s: %w", path, err))
			continue
		}
		var r Record
		if err = json.Unmarshal(b, &r); err != nil {
			errs = append(errs, fmt.Errorf("outbox record %s: %w", path, err))
			continue
		}
		var body kata.ObserveCronRunBody
		hash, hashErr := hex.DecodeString(r.Cause)
		if r.Version != 1 || hashErr != nil || len(hash) != sha256.Size || entry.Name() != r.Cause+".json" || r.Project == "" || r.RunUID == "" || !slices.Contains([]string{"pending", "done", "refused"}, r.State) || json.Unmarshal(r.Body, &body) != nil || body.JobUID == nil {
			errs = append(errs, fmt.Errorf("invalid outbox record: %s", path))
			continue
		}
		if c := r.Checkpoint; o.Dir == o.Layout.Dropped() && (c == nil || c.Version != 1 || c.JobUID != *body.JobUID || body.DefinitionEventUID == nil || c.EventUID != *body.DefinitionEventUID || c.Through.IsZero()) {
			errs = append(errs, fmt.Errorf("invalid dropped checkpoint: %s", path))
			continue
		}
		records = append(records, r)
	}
	return records, errors.Join(errs...)
}

// Discover only writes a new intent; no network call or admission occurs here.
func (o Outbox) Discover(p defs.Plan, project, actor, executor string, reasons []string, now time.Time) error {
	workflowEvent := ""
	if p.WorkflowUID != "" {
		workflowEvent = p.WorkflowEventUID
	}
	tuple, err := json.Marshal([]any{p.JobUID, p.JobEventUID, workflowEvent, reasons})
	if err != nil {
		return err
	}
	sum := sha256.Sum256(tuple)
	cause := hex.EncodeToString(sum[:])
	details := make([]string, len(reasons))
	lineBreaks := strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ")
	for i, reason := range reasons {
		details[i] = lineBreaks.Replace(reason)
	}
	return o.intent(p, project, actor, executor, cause, "unsupported:"+p.JobEventUID, "failed", "reason: unsupported\n"+strings.Join(details, ", "), nil, now)
}

// Drop freezes the decision and its checkpoint before any acknowledgement.
func (o Outbox) Drop(p defs.Plan, project, actor, executor, code, detail, firstKey string, checkpoint sched.Checkpoint, now time.Time) error {
	tuple, err := json.Marshal([]string{p.JobUID, p.JobEventUID, firstKey})
	if err != nil {
		return err
	}
	sum := sha256.Sum256(tuple)
	return o.intent(p, project, actor, executor, hex.EncodeToString(sum[:]), firstKey, "cancelled", "reason: "+code+"\n"+detail, &checkpoint, now)
}
func (o Outbox) intent(p defs.Plan, project, actor, executor, cause, occurrence, status, message string, checkpoint *sched.Checkpoint, now time.Time) error {
	path := filepath.Join(o.dir(), cause+".json")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	r := journal.Run{RunUID: ulid.Make().String(), Actor: actor, Executor: executor, Plan: p, Occurrence: occurrence, StartedAt: new(now.UTC())}
	body, err := freeze(r, status, bounded(message), 0, new(now.UTC()))
	if err != nil {
		return err
	}
	return o.save(Record{Checkpoint: checkpoint, Version: 1, Cause: cause, Project: project, RunUID: r.RunUID, Body: body, State: "pending"})
}

// Deliver tries one pending intent, oldest attempt first, after ordinary work.
// Current definitions never decide whether a historical intent can be delivered.
func (o Outbox) Deliver(clients map[string]kata.Kata, now time.Time) error {
	records, scanErr := o.All()
	slices.SortFunc(records, func(a, b Record) int {
		if n := a.LastAttempt.Compare(b.LastAttempt); n != 0 {
			return n
		}
		return strings.Compare(a.Cause, b.Cause)
	})
	for _, r := range records {
		k := clients[r.Project]
		if r.State != "pending" || k == nil {
			continue
		}
		r.LastAttempt = now.UTC()
		if err := o.save(r); err != nil {
			return errors.Join(scanErr, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), kata.CallTimeout)
		_, sendErr, permanent := observe(kata.WithContext(k, ctx), journal.Run{RunUID: r.RunUID}, r.Body, 0)
		cancel()
		r.Error = ""
		if sendErr == nil {
			r.State = "done"
		} else {
			r.Error = bounded(sendErr.Error())
			if permanent {
				r.State = "refused"
			}
		}
		return errors.Join(scanErr, sendErr, o.save(r))
	}
	return scanErr
}
