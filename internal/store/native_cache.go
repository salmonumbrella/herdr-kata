package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"time"
)

func (r *NativeRepository) targetKey() string {
	if r.Client == nil {
		return ""
	}
	key, _ := json.Marshal(struct{ Server, Daemon, Home string }{r.Client.Target.Server, r.Client.Target.Daemon, r.Client.Target.Home})
	return string(key)
}
func (r *NativeRepository) check(ctx context.Context) error {
	if r == nil || r.Client == nil {
		return ErrNativeUnconfigured
	}
	a, e := r.Client.Capabilities(ctx)
	if e != nil {
		return e
	}
	if a.ProjectUID != r.Binding.ProjectUID {
		return errors.New("configured native project UID changed; select the intended binding explicitly")
	}
	return nil
}
func (r *NativeRepository) ActivationAllowed(ctx context.Context) error { return r.check(ctx) }
func (r *NativeRepository) Cached(ctx context.Context) (NativeSnapshot, error) {
	snapshot := NativeSnapshot{Offline: true, Problem: "not refreshed"}
	if r == nil || r.Client == nil {
		return snapshot, nil
	}
	rows, e := r.Store.db.QueryContext(ctx, `SELECT resource,document FROM native_definitions WHERE target_key=? AND project_uid=? ORDER BY resource,uid`, r.targetKey(), r.Binding.ProjectUID)
	if e != nil {
		return snapshot, e
	}
	defer rows.Close()
	for rows.Next() {
		var resource string
		var raw []byte
		if e := rows.Scan(&resource, &raw); e != nil {
			return snapshot, e
		}
		var def katacli.Definition
		if e := katacli.Decode(raw, &def); e != nil {
			return snapshot, e
		}
		if def.DeletedAt != nil {
			continue
		}
		if resource == "job" {
			snapshot.Jobs = append(snapshot.Jobs, def)
		} else {
			snapshot.Workflows = append(snapshot.Workflows, def)
		}
	}
	return snapshot, rows.Err()
}
func validateCachedDefinition(def katacli.Definition) error {
	for _, value := range []string{def.UID, def.DefinitionEventUID} {
		normalized, e := katacli.NormalizeUID(value)
		if e != nil || normalized != value {
			return errors.New("invalid native cache identity")
		}
	}
	if !json.Valid(def.Definition) || len(def.Definition) > 256*1024 {
		return errors.New("invalid native cache definition")
	}
	return nil
}
func (r *NativeRepository) Refresh(ctx context.Context) (NativeSnapshot, error) {
	snapshot := NativeSnapshot{}
	e := r.check(ctx)
	if e == nil {
		snapshot.Jobs, e = r.Client.Definitions(ctx, "job", true)
	}
	if e == nil {
		snapshot.Workflows, e = r.Client.Definitions(ctx, "workflow", true)
	}
	if e == nil {
		e = r.replace(ctx, snapshot)
	}
	if e != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return NativeSnapshot{}, ctx.Err()
		}
		cacheCtx := ctx
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			var cancel context.CancelFunc
			cacheCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 250*time.Millisecond)
			defer cancel()
		}
		cached, cacheErr := r.Cached(cacheCtx)
		cached.Offline = true
		cached.Problem = e.Error()
		return cached, cacheErr
	}
	var live NativeSnapshot
	for _, d := range snapshot.Jobs {
		if d.DeletedAt == nil {
			live.Jobs = append(live.Jobs, d)
		}
	}
	for _, d := range snapshot.Workflows {
		if d.DeletedAt == nil {
			live.Workflows = append(live.Workflows, d)
		}
	}
	return live, nil
}
func (r *NativeRepository) replace(ctx context.Context, snapshot NativeSnapshot) error {
	for _, defs := range [][]katacli.Definition{snapshot.Jobs, snapshot.Workflows} {
		for _, def := range defs {
			if e := validateCachedDefinition(def); e != nil {
				return e
			}
		}
	}
	tx, e := r.Store.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `DELETE FROM native_definitions WHERE target_key=? AND project_uid=?`, r.targetKey(), r.Binding.ProjectUID); e != nil {
		return e
	}
	for resource, defs := range map[string][]katacli.Definition{"job": snapshot.Jobs, "workflow": snapshot.Workflows} {
		for _, d := range defs {
			raw, e := json.Marshal(d)
			if e != nil {
				return e
			}
			if _, e := tx.ExecContext(ctx, `INSERT INTO native_definitions(target_key,project_uid,resource,uid,event_uid,document) VALUES(?,?,?,?,?,?)`, r.targetKey(), r.Binding.ProjectUID, resource, d.UID, d.DefinitionEventUID, raw); e != nil {
				return e
			}
		}
	}
	return tx.Commit()
}
func (r *NativeRepository) cache(ctx context.Context, resource string, def katacli.Definition) error {
	if e := validateCachedDefinition(def); e != nil {
		return e
	}
	raw, e := json.Marshal(def)
	if e != nil {
		return e
	}
	_, e = r.Store.db.ExecContext(ctx, `INSERT INTO native_definitions(target_key,project_uid,resource,uid,event_uid,document) VALUES(?,?,?,?,?,?) ON CONFLICT(target_key,project_uid,resource,uid) DO UPDATE SET event_uid=excluded.event_uid,document=excluded.document`, r.targetKey(), r.Binding.ProjectUID, resource, def.UID, def.DefinitionEventUID, raw)
	return e
}
func (r *NativeRepository) Save(ctx context.Context, draft katacli.Draft) (katacli.Definition, error) {
	if e := r.check(ctx); e != nil {
		return katacli.Definition{}, e
	}
	def, e := r.Client.Save(ctx, draft)
	if e != nil {
		return def, e
	}
	if e := r.cache(ctx, draft.Resource, def); e != nil {
		return def, fmt.Errorf("native save accepted but derived cache update failed: %w", e)
	}
	return def, nil
}
func (r *NativeRepository) Delete(ctx context.Context, resource string, def katacli.Definition) error {
	_, err := r.DefinitionAction(ctx, resource, "delete", def.UID, def.DefinitionEventUID)
	return err
}

// DefinitionAction caches the accepted lifecycle winner for offline views.
func (r *NativeRepository) DefinitionAction(ctx context.Context, resource, action, uid, expected string) (katacli.Definition, error) {
	if e := r.check(ctx); e != nil {
		return katacli.Definition{}, e
	}
	def, e := r.Client.DefinitionAction(ctx, resource, action, uid, expected)
	if e != nil {
		return def, e
	}
	if e := r.cache(ctx, resource, def); e != nil {
		return def, fmt.Errorf("native %s accepted but derived cache update failed: %w", action, e)
	}
	return def, nil
}
