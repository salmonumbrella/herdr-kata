package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/lockfile"
	"github.com/salmonumbrella/herdr-kata/internal/statefs"
)

type NativeActivation struct {
	TargetKey     string `json:"target_key"`
	ProjectUID    string `json:"project_uid"`
	JobUID        string `json:"job_uid"`
	Enabled       bool   `json:"enabled"`
	ExecutorLabel string `json:"executor_label,omitempty"`
}
type nativeActivations struct {
	Version int                         `json:"version"`
	Entries map[string]NativeActivation `json:"entries"`
}

func (r *NativeRepository) activationIdentity(uid string) (NativeActivation, string, error) {
	if r == nil || r.Client == nil || r.StateDir == "" {
		return NativeActivation{}, "", ErrNativeUnconfigured
	}
	id, err := katacli.NormalizeUID(uid)
	if err != nil {
		return NativeActivation{}, "", err
	}
	a := NativeActivation{TargetKey: katacli.LocalTargetKey(r.Client.Target), ProjectUID: r.Binding.ProjectUID, JobUID: id, ExecutorLabel: r.Binding.ExecutorLabel}
	key, _ := json.Marshal([]string{a.TargetKey, a.ProjectUID, a.JobUID})
	return a, string(key), nil
}
func (r *NativeRepository) IsActivated(uid string) (bool, error) {
	a, key, err := r.activationIdentity(uid)
	if err != nil {
		return false, err
	}
	var file nativeActivations
	err = statefs.ReadJSON(filepath.Join(r.StateDir, "native-activation.json"), 262144, &file)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if file.Version != 1 {
		return false, errors.New("unsupported local activation version")
	}
	got, ok := file.Entries[key]
	if !ok {
		return false, nil
	}
	if got.TargetKey != a.TargetKey || got.ProjectUID != a.ProjectUID || got.JobUID != a.JobUID {
		return false, errors.New("local activation identity mismatch")
	}
	return got.Enabled, nil
}
func (r *NativeRepository) setActivated(uid string, enabled bool) error {
	a, key, err := r.activationIdentity(uid)
	if err != nil {
		return err
	}
	a.Enabled = enabled
	path := filepath.Join(r.StateDir, "native-activation.json")
	lock, err := lockfile.Acquire(path + ".lock")
	if err != nil {
		return err
	}
	defer lock.Release()
	file := nativeActivations{Version: 1, Entries: map[string]NativeActivation{}}
	err = statefs.ReadJSON(path, 262144, &file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if file.Version != 1 {
		return errors.New("unsupported local activation version")
	}
	if file.Entries == nil {
		file.Entries = map[string]NativeActivation{}
	}
	file.Entries[key] = a
	return statefs.WriteJSON(path, file, 262144)
}
func (s *Store) setNativeEnabled(ctx context.Context, id string, enabled bool) error {
	if enabled {
		j, err := s.nativeJob(ctx, id)
		if err != nil {
			return err
		}
		if j.NativeOffline {
			return errors.New("refresh current native definition before local activation")
		}
		var def struct {
			Action struct {
				Kind string `json:"kind"`
			} `json:"action"`
		}
		if err := katacli.Decode(j.NativeDefinition, &def); err != nil {
			return err
		}
		if def.Action.Kind == "notify" {
			return s.Native.setActivated(id, enabled)
		}
		if j.CWD == "" {
			return errors.New("map the definition checkout before local activation")
		}
		info, err := os.Stat(j.CWD)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return errors.New("local checkout is not a directory")
		}
		if j.Flow != "" {
			flow, err := s.Native.Client.Definition(ctx, "flow", j.Flow)
			if err != nil {
				return err
			}
			if err := ValidateNativeFlowPolicy(flow.Definition); err != nil {
				return err
			}
		}
	}
	return s.Native.setActivated(id, enabled)
}
