package runner

import (
	"context"
	"path/filepath"

	"github.com/salmonumbrella/herdr-kata/internal/usage"
)

func (m *Manager) collectRunUsage(ctx context.Context, uid string) error {
	if ctx.Err() != nil {
		return nil
	}
	r, err := m.store.LoadContext(ctx, uid)
	if err != nil {
		return err
	}
	if r.EndedAt != nil {
		return nil
	}
	for i := range r.Steps {
		if err = m.collectStepUsage(ctx, uid, i); err != nil {
			return err
		}
	}
	return nil
}
func (m *Manager) collectStepUsage(ctx context.Context, uid string, index int) error {
	if ctx.Err() != nil {
		return nil
	}
	r, err := m.store.LoadContext(ctx, uid)
	if err != nil {
		return err
	}
	if index < 0 || index >= len(r.Steps) {
		return nil
	}
	if r.Phase == HandedOff {
		return nil
	}
	step := r.Steps[index]
	if step.Kind != "prompt" || step.SubmittedAt == nil || step.Usage != nil {
		return nil
	}
	ref := r.Herdr
	if step.Herdr != nil {
		ref = *step.Herdr
	}
	prompt := filepath.Join(m.store.layout.AttemptDir(uid, step.Key, step.Attempt), "prompt.md")
	_, profile := StepProfile(r, index)
	data := usage.Collect(ctx, usage.Request{Session: ref.Session, Agent: profile.Kind, Cwd: r.Cwd, PromptPath: prompt, LaunchedAt: ref.LaunchedAt, StartedAt: *step.SubmittedAt, EndedAt: m.clock.Now(), Env: m.agentEnv()})
	if ctx.Err() != nil {
		return nil
	}
	_, err = m.store.TransitionContext(ctx, uid, func(r *Run) error {
		if r.Phase == HandedOff {
			return nil
		}
		s := &r.Steps[index]
		if s.Attempt != step.Attempt || s.Key != step.Key || s.Usage != nil {
			return nil
		}
		s.Usage = &data
		partial := r.Usage != nil && r.Usage.Found != data.Found
		r.Usage = usage.Add(r.Usage, data)
		// A run total missing one attempt's data is not reported to Kata.
		r.Usage.Partial = r.Usage.Partial || partial
		return nil
	})
	return err
}
