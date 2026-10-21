package runner

import (
	"context"
	"errors"
	"github.com/salmonumbrella/herdr-kata/internal/herdr"
	"github.com/salmonumbrella/herdr-kata/internal/state"
	"time"
)

type workspaceRef struct {
	WorkspaceID string
	ServerID    herdr.ServerID
}

func (m *Manager) ensureWorkspace(ctx context.Context) (workspaceRef, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	m.workspaceMu.Lock()
	defer m.workspaceMu.Unlock()
	if m.h == nil {
		return workspaceRef{}, errors.New("herdr client unavailable")
	}
	id, idErr := herdr.ReadServerID(m.h.Path)
	var saved workspaceRef
	err := state.ReadJSON(m.store.fsys, m.store.layout.Workspace(), 64<<10, &saved)
	if idErr == nil && err == nil && saved.ServerID == id && saved.WorkspaceID != "" {
		w, e := m.h.WorkspaceGet(ctx, saved.WorkspaceID)
		if e == nil && w.WorkspaceID == saved.WorkspaceID {
			return saved, nil
		}
		if e != nil && !herdrCode(e, "workspace_not_found") {
			return workspaceRef{}, e
		}
	}
	w, err := m.h.WorkspaceCreate(ctx, "Kata", m.store.layout.Root)
	if err != nil {
		return workspaceRef{}, err
	}
	if w.WorkspaceID == "" {
		return workspaceRef{}, errors.New("workspace identity missing")
	}
	saved = workspaceRef{WorkspaceID: w.WorkspaceID, ServerID: id}
	// An unreadable generation is saved as zero; it can never authorize adoption.
	if idErr != nil {
		saved.ServerID = herdr.ServerID{}
	}
	if err = state.WriteJSON(m.store.fsys, m.store.layout.Workspace(), saved); err != nil {
		return workspaceRef{}, err
	}
	return saved, nil
}
func herdrCode(err error, code string) bool {
	var e *herdr.Error
	return errors.As(err, &e) && e.Code == code
}
func (m *Manager) pause(ctx context.Context, duration time.Duration) error {
	timer := m.clock.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C():
		return nil
	}
}
