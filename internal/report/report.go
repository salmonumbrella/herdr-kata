// Package report journals replay-safe Kata writes and notification intent.
package report

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/salmonumbrella/herdr-kata/internal/journal"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

type timeKey struct{}

func WithNow(ctx context.Context, at time.Time) context.Context {
	return context.WithValue(ctx, timeKey{}, at)
}
func clock(ctx context.Context) time.Time {
	if at, ok := ctx.Value(timeKey{}).(time.Time); ok {
		return at.UTC()
	}
	return time.Now().UTC()
}
func bounded(s string) string {
	if len(s) > 4096 {
		s = s[:4096]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}
func freeze(r journal.Run, status, message string, expected int, ended *time.Time) ([]byte, error) {
	b := kata.ObserveCronRunBody{ExpectedRevision: int64(expected), Actor: new(r.Actor), ExecutorLabel: new(r.Executor), Teammate: new("herdr-kata"), Status: status, Summary: kata.Summary{Version: 1, Message: new(message)}, StartedAt: r.StartedAt, EndedAt: ended}
	if r.Plan.JobUID != "" {
		b.JobUID = new(r.Plan.JobUID)
		b.DefinitionEventUID = new(r.Plan.JobEventUID)
	}
	if r.Plan.WorkflowUID != "" {
		b.WorkflowUID = new(r.Plan.WorkflowUID)
		b.WorkflowDefinitionEventUID = new(r.Plan.WorkflowEventUID)
	}
	if r.Occurrence != "" {
		b.OccurrenceKey = new(r.Occurrence)
	}
	if r.IssueUID != "" {
		b.IssueUID = new(r.IssueUID)
	}
	return json.Marshal(b)
}
func refused(st *journal.Store, key string, err error, terminal bool) error {
	save := st.Update(key, func(r *journal.Run) error {
		r.Phase = "held"
		r.Hold = "report_refused"
		r.Detail = bounded(err.Error())
		r.ReportRefused = r.ReportRefused || terminal
		return nil
	})
	return errors.Join(err, save)
}
func observe(k kata.Kata, r journal.Run, b []byte, expected int) (int, error, bool) {
	rev, _, err := k.Observe(r.RunUID, b, expected)
	if err == nil {
		return rev, nil, false
	}
	switch kata.Classify(err) {
	case kata.Conflict:
		current, raw, readErr := k.ShowRun(r.RunUID)
		if readErr != nil {
			return 0, readErr, false
		}
		var wanted, actual kata.ObserveCronRunBody
		if json.Unmarshal(b, &wanted) != nil || json.Unmarshal(raw, &actual) != nil {
			return 0, err, false
		}
		wanted.ExpectedRevision = 0
		actual.ExpectedRevision = 0
		if reflect.DeepEqual(wanted, actual) {
			return current, nil, false
		}
		return 0, err, true
	case kata.Definitive:
		return 0, err, true
	default:
		return 0, err, false
	}
}

func Admit(ctx context.Context, k kata.Kata, st *journal.Store, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r, err := st.Load(key)
	if err != nil {
		return err
	}
	if r.Phase != "prepared" {
		return nil
	}
	if r.StartedAt == nil {
		return refused(st, key, errors.New("prepared run has no started_at"), false)
	}
	if r.IssueUID == "" {
		switch r.Plan.Issue.Kind {
		case "existing":
			r.IssueUID = r.Plan.Issue.UID
			r.IssueRef = r.IssueUID
		case "per-run":
			if !clock(ctx).Before(r.StartedAt.Add(6 * 24 * time.Hour)) {
				return st.Update(key, func(r *journal.Run) error { r.Phase = "held"; r.Hold = "admission_expired"; return nil })
			}
			title := strings.TrimSpace(r.Plan.Issue.Title + " " + r.Occurrence)
			r.IssueRef, r.IssueUID, err = k.CreateIssue(title, r.Plan.Issue.Body, "cron-worker:"+r.RunUID+":issue")
			if err != nil {
				if class := kata.Classify(err); class == kata.Definitive || class == kata.Conflict {
					return refused(st, key, err, false)
				}
				return err
			}
		default:
			return refused(st, key, errors.New("unsupported issue policy"), false)
		}
		if r.IssueUID == "" {
			return refused(st, key, errors.New("issue response omitted UID"), false)
		}
		if err = st.Update(key, func(saved *journal.Run) error { saved.IssueRef = r.IssueRef; saved.IssueUID = r.IssueUID; return nil }); err != nil {
			return err
		}
	}
	if offset := r.Plan.Issue.DeadlineOffsetSeconds; r.Plan.Issue.Kind == "per-run" && offset != nil && !r.DeadlineDone {
		if err = k.SetDeadline(r.IssueRef, r.StartedAt.Add(time.Duration(*offset)*time.Second)); err != nil {
			if class := kata.Classify(err); class == kata.Definitive || class == kata.Conflict {
				return refused(st, key, err, false)
			}
			return err
		}
		r.DeadlineDone = true
		if err = st.Update(key, func(saved *journal.Run) error { saved.DeadlineDone = true; return nil }); err != nil {
			return err
		}
	}
	if len(r.AdmissionBody) == 0 {
		r.AdmissionBody, err = freeze(r, "running", "running", 0, nil)
		if err != nil {
			return err
		}
		if err = st.Update(key, func(saved *journal.Run) error { saved.AdmissionBody = r.AdmissionBody; return nil }); err != nil {
			return err
		}
	}
	rev, err, permanent := observe(k, r, r.AdmissionBody, 0)
	if err != nil {
		if permanent {
			return refused(st, key, err, true)
		}
		return err
	}
	return st.Update(key, func(r *journal.Run) error { r.ObsRev = rev; r.Phase = "running"; return nil })
}

func NotifyOnce(ctx context.Context, k kata.Kata, st *journal.Store, key, id, msg string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r, err := st.Load(key)
	if err != nil {
		return err
	}
	if r.Plan.Issue.Kind == "existing" || r.IssueRef == "" || slices.Contains(r.Notified, id) {
		return nil
	}
	if err = st.Update(key, func(r *journal.Run) error { r.Notified = append(r.Notified, id); return nil }); err != nil {
		return err
	}
	if err = k.Notify(r.IssueRef, r.Actor, msg); err == nil {
		return nil
	}
	if kata.Classify(err) == kata.Unavailable {
		save := st.Update(key, func(r *journal.Run) error {
			r.Notified = slices.DeleteFunc(r.Notified, func(s string) bool { return s == id })
			return nil
		})
		return errors.Join(err, save)
	}
	if kata.Classify(err) == kata.Definitive {
		return refused(st, key, err, false)
	}
	return err // Uncertain notifications keep their saved intent and are not retried.
}
func publish(ctx context.Context, k kata.Kata, st *journal.Store, key string, holding bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r, err := st.Load(key)
	if err != nil {
		return err
	}
	if r.ReportDone || r.ReportRefused {
		return nil
	}
	if len(r.ReportBody) == 0 {
		status := r.Outcome
		if status == "" {
			status = "unknown"
		}
		message := status
		if holding {
			if status != "failed" {
				status = "unknown"
			}
			message = "reason: " + r.Hold
		} else if status != "succeeded" {
			message = "reason: " + status
			if status == "unknown" {
				for _, a := range r.Attempts {
					if a.Status == "needs-human" {
						message = "reason: needs-human"
						break
					}
				}
			}
		}
		if r.Detail != "" {
			message += "; " + bounded(r.Detail)
		}
		if len(r.Attempts) > 0 && r.Attempts[len(r.Attempts)-1].Note != "" {
			message += "; " + r.Attempts[len(r.Attempts)-1].Note
		}
		if r.EndedAt == nil {
			r.EndedAt = new(clock(ctx))
		}
		r.ReportBody, err = freeze(r, status, message, r.ObsRev, r.EndedAt)
		if err != nil {
			return err
		}
		if err = st.Update(key, func(saved *journal.Run) error { saved.ReportBody = r.ReportBody; saved.EndedAt = r.EndedAt; return nil }); err != nil {
			return err
		}
	}
	rev, err, permanent := observe(k, r, r.ReportBody, r.ObsRev)
	if err != nil {
		if permanent {
			return refused(st, key, err, true)
		}
		return err
	}
	return st.Update(key, func(r *journal.Run) error {
		r.ObsRev = rev
		r.ReportDone = true
		if !holding {
			r.Phase = "reported"
		}
		return nil
	})
}
func Hold(ctx context.Context, k kata.Kata, st *journal.Store, key string) error {
	if err := publish(ctx, k, st, key, true); err != nil {
		return err
	}
	r, err := st.Load(key)
	if err != nil {
		return err
	}
	address := ""
	if len(r.Attempts) > 0 {
		address = r.Attempts[len(r.Attempts)-1].Address
	}
	return NotifyOnce(ctx, k, st, key, "held", "held: "+r.Hold+"; hc "+address)
}
func Final(ctx context.Context, k kata.Kata, st *journal.Store, key string) error {
	return publish(ctx, k, st, key, false)
}
