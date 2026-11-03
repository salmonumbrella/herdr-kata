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
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// UnsupportedRecord is a separate, durable observation intent, never a run journal.
// Body is saved once and replayed byte for byte, including its actor and times.
type UnsupportedRecord struct {
	Version                int
	Cause, Project, RunUID string
	Body                   []byte
	State, Error           string
	LastAttempt            time.Time
}

// Outbox shares the worker's already-held lifetime lock; it acquires no lock.
type Outbox struct{ Layout state.Layout }

func (o Outbox) save(r UnsupportedRecord) error {
	return state.WriteJSON(filepath.Join(o.Layout.Unsupported(), r.Cause+".json"), r)
}

func (o Outbox) All() ([]UnsupportedRecord, error) {
	entries, err := os.ReadDir(o.Layout.Unsupported())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []UnsupportedRecord
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(o.Layout.Unsupported(), entry.Name()))
		if err != nil {
			return nil, err
		}
		var r UnsupportedRecord
		if err = json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		var body kata.ObserveCronRunBody
		hash, hashErr := hex.DecodeString(r.Cause)
		if r.Version != 1 || hashErr != nil || len(hash) != sha256.Size || entry.Name() != r.Cause+".json" || r.Project == "" || r.RunUID == "" || !slices.Contains([]string{"pending", "done", "refused"}, r.State) || json.Unmarshal(r.Body, &body) != nil || body.JobUID == nil {
			return nil, fmt.Errorf("invalid unsupported record: %s", entry.Name())
		}
		records = append(records, r)
	}
	return records, nil
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
	path := filepath.Join(o.Layout.Unsupported(), cause+".json")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	details := make([]string, len(reasons))
	for i, reason := range reasons {
		details[i] = strings.Join(strings.Fields(reason), " ")
	}
	message := bounded("reason: unsupported\n" + strings.Join(details, ", "))
	r := journal.Run{RunUID: ulid.Make().String(), Actor: actor, Executor: executor, Plan: p, Occurrence: "unsupported:" + p.JobEventUID, StartedAt: new(now.UTC())}
	body, err := freeze(r, "failed", message, 0, new(now.UTC()))
	if err != nil {
		return err
	}
	return o.save(UnsupportedRecord{Version: 1, Cause: cause, Project: project, RunUID: r.RunUID, Body: body, State: "pending"})
}

// Deliver tries one pending intent, oldest attempt first, after ordinary work.
// Current definitions never decide whether a historical intent can be delivered.
func (o Outbox) Deliver(clients map[string]kata.Kata, now time.Time) error {
	records, err := o.All()
	if err != nil {
		return err
	}
	slices.SortFunc(records, func(a, b UnsupportedRecord) int {
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
		if err = o.save(r); err != nil {
			return err
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
		return errors.Join(sendErr, o.save(r))
	}
	return nil
}
