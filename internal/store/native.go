package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"os"
	"path/filepath"
	"strings"
)

var ErrNativeUnconfigured = errors.New("configure a native Kata target with `herdr-kata native configure` before saving; draft remains unsaved")

// NativeBinding is installation-local. It is never serialized into definitions.
type NativeBinding struct {
	ProjectUID    string
	ExecutorLabel string
	Checkouts     map[string]string
	Secrets       map[string]string
}
type NativeConfig struct {
	Client  katacli.Client
	Binding NativeBinding
}
type NativeRepository struct {
	StateDir string
	Store    *Store
	Client   *katacli.Client
	Binding  NativeBinding
}
type NativeSnapshot struct {
	Jobs, Flows []katacli.Definition
	Offline     bool
	Problem     string
}

func (s NativeSnapshot) Label() string {
	if s.Offline {
		return "offline native cache · " + s.Problem
	}
	return "native Kata"
}
func LoadNativeConfig(dir string) (NativeConfig, error) {
	raw, e := os.ReadFile(filepath.Join(dir, "native.json"))
	if errors.Is(e, os.ErrNotExist) {
		return NativeConfig{}, ErrNativeUnconfigured
	}
	var cfg NativeConfig
	if e != nil {
		return cfg, e
	}
	e = json.Unmarshal(raw, &cfg)
	return cfg, e
}

func WriteNativeConfig(dir string, cfg NativeConfig) error {
	if e := cfg.Client.Target.Validate(); e != nil {
		return e
	}
	if _, e := katacli.NormalizeUID(cfg.Binding.ProjectUID); e != nil {
		return e
	}
	for key, path := range cfg.Binding.Checkouts {
		if key == "" || !filepath.IsAbs(path) {
			return errors.New("checkout mappings require a key and absolute local path")
		}
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	raw, e := json.MarshalIndent(cfg, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, "native-*.json")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(raw)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, filepath.Join(dir, "native.json"))
}

// AttachNative makes product job writes require the native service. Legacy rows
// remain accessible only to explicit lowlevel migration readers, never fallback.
func (s *Store) AttachNative(dir string) error {
	s.Native = &NativeRepository{Store: s, StateDir: dir}
	cfg, e := LoadNativeConfig(dir)
	if errors.Is(e, ErrNativeUnconfigured) {
		return nil
	}
	if e != nil {
		return e
	}
	if e := cfg.Client.Target.Validate(); e != nil {
		return e
	}
	s.Native.Client = &cfg.Client
	s.Native.Binding = cfg.Binding
	return nil
}

// ProjectJobs isolates executor-specific projection problems from raw native data.
func (r *NativeRepository) ProjectJobs(snapshot NativeSnapshot) ([]Job, []error) {
	var jobs []Job
	var problems []error
	for _, def := range snapshot.Jobs {
		j, e := r.JobFrom(def, snapshot.Offline)
		if e != nil {
			problems = append(problems, fmt.Errorf("native job %s cannot be projected: %w", def.UID, e))
			continue
		}
		if r.StateDir != "" && r.Client != nil {
			j.Enabled, e = r.IsActivated(j.ID)
			if e != nil {
				problems = append(problems, fmt.Errorf("native job %s local activation: %w", def.UID, e))
				continue
			}
		}
		jobs = append(jobs, j)
	}
	return jobs, problems
}
func (s *Store) nativeJobs(ctx context.Context) ([]Job, error) {
	snapshot, e := s.Native.Refresh(ctx)
	if e != nil {
		return nil, e
	}
	jobs, problems := s.Native.ProjectJobs(snapshot)
	return jobs, errors.Join(problems...)
}
func (s *Store) nativeJob(ctx context.Context, id string) (*Job, error) {
	snapshot, e := s.Native.Refresh(ctx)
	if e != nil {
		return nil, e
	}
	for _, def := range snapshot.Jobs {
		if strings.EqualFold(def.UID, id) {
			job, e := s.Native.JobFrom(def, snapshot.Offline)
			if e != nil {
				return nil, fmt.Errorf("native job %s cannot be projected: %w", def.UID, e)
			}
			job.Enabled, e = s.Native.IsActivated(job.ID)
			if e != nil {
				return nil, e
			}
			return &job, nil
		}
	}
	if s.Native.Client == nil {
		return nil, ErrNativeUnconfigured
	}
	return nil, ErrNotFound
}

// DeleteJobAt uses the caller's displayed winner, including board/prune actions.
func (s *Store) DeleteJobAt(ctx context.Context, j Job) error {
	if s.Native != nil {
		return s.Native.Delete(ctx, "job", katacli.Definition{UID: j.ID, DefinitionEventUID: j.NativeEventUID})
	}
	return s.DeleteJob(ctx, j.ID)
}
