package runner

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/lockfile"
	"github.com/salmonumbrella/herdr-kata/internal/statefs"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

// NativeExecutionContext is immutable local intent. Raw definitions and paths
// never enter a shared observation; credentials are reloaded for this target.
type NativeExecutionContext struct {
	Version         int                 `json:"version"`
	RunUID          string              `json:"run_uid"`
	Target          katacli.Target      `json:"target"`
	ProjectUID      string              `json:"project_uid"`
	ExecutorLabel   string              `json:"executor_label,omitempty"`
	Job             *katacli.Definition `json:"job,omitempty"`
	Workflow        *katacli.Definition `json:"workflow,omitempty"`
	Runtime         store.Job           `json:"runtime"`
	Occurrence      string              `json:"occurrence,omitempty"`
	IssueUID        string              `json:"issue_uid,omitempty"`
	IssuePreparedAt time.Time           `json:"issue_prepared_at,omitempty"`
}

func (c NativeExecutionContext) Save(dir string) error {
	if c.Target.Token != "" {
		return errors.New("credentials cannot enter local execution snapshot")
	}
	path := filepath.Join(dir, "native-context.json")
	lock, err := lockfile.Acquire(path + ".lock")
	if err != nil {
		return err
	}
	defer lock.Release()
	if _, err := os.Stat(path); err == nil {
		return errors.New("immutable local execution context already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return statefs.WriteJSON(path, c, 8<<20)
}
func LoadNativeContext(dir string) (NativeExecutionContext, error) {
	var c NativeExecutionContext
	err := statefs.ReadJSON(filepath.Join(dir, "native-context.json"), 8<<20, &c)
	if err != nil {
		return c, err
	}
	if c.Version != 1 || c.Target.Token != "" {
		return c, errors.New("unsupported local execution context; authority-era journals require operator inspection")
	}
	if _, err = katacli.NormalizeUID(c.RunUID); err != nil {
		return c, err
	}
	err = c.loadIssueBinding(dir)
	return c, err
}
func (c NativeExecutionContext) Check(repo *store.NativeRepository) error {
	if repo == nil || repo.Client == nil {
		return store.ErrNativeUnconfigured
	}
	t := repo.Client.Target
	t.Token = ""
	if c.Target != t || c.ProjectUID != repo.Binding.ProjectUID {
		return errors.New("saved execution actor, target or project changed; select the recorded routing explicitly")
	}
	return nil
}

// Environment removes ambient routing/credentials and applies only the current
// credentials for the immutable recorded target. No grant is consulted.
func (c NativeExecutionContext) Environment(repo *store.NativeRepository, env []string) []string {
	out := make([]string, 0, len(env)+16)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "KATA_") || upper == "HTTP_PROXY" || upper == "HTTPS_PROXY" || upper == "ALL_PROXY" || upper == "NO_PROXY" || upper == "PORT" {
			continue
		}
		out = append(out, entry)
	}
	t := repo.Client.Target
	inbox := t.Actor
	if t.Teammate != "" {
		inbox += "/" + t.Teammate
	}
	values := map[string]string{"KATA_AUTHOR": t.Actor, "KATA_TEAMMATE": t.Teammate, "KATA_INBOX_USER": inbox, "KATA_REF": c.IssueUID, "KATA_SERVER": t.Server, "KATA_HOME": t.Home, "KATA_DAEMON": t.Daemon, "KATA_AUTH_TOKEN": t.Token, "KATA_TRUST_PRIVATE_NETWORK": ""}
	if t.TrustPrivateNetwork {
		values["KATA_TRUST_PRIVATE_NETWORK"] = "1"
	}
	for key, value := range values {
		out = append(out, key+"="+value)
	}
	return out
}
