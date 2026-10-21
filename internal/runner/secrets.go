package runner

import (
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"sort"
)

// resolveSecrets is step-local. Neither resolved values nor the returned map
// may be included in a journal, diagnostic, or Kata request.
func resolveSecrets(cfg config.Config, def defs.JobDef) (map[string]string, error) {
	keys := make([]string, 0, len(def.SecretRefs))
	for key := range def.SecretRefs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make(map[string]string, len(keys))
	for _, key := range keys {
		name := def.SecretRefs[key]
		ref, ok := cfg.Secrets[name]
		if !ok {
			return nil, fmt.Errorf("secret %s: unmapped", name)
		}
		if !config.ChildVariable(key) || cfg.ReservedVariable(key) {
			return nil, fmt.Errorf("secret %s: invalid or reserved variable %s", name, key)
		}
		if ref.Value != "" || ref.IsZero() {
			return nil, fmt.Errorf("secret %s: env or file source required", name)
		}
		secret, err := ref.Resolve()
		if err != nil {
			return nil, fmt.Errorf("secret %s: %s: %w", name, config.SecretSource(ref), err)
		}
		env[key] = secret.Value
	}
	return env, nil
}

func (m *Manager) holdSecret(uid string, index int, err error) error {
	_, e := m.store.Transition(uid, func(r *Run) error {
		r.Phase = Held
		r.Outcome = "unknown"
		r.Reason = "secret_unavailable"
		r.ActiveStep = r.Steps[index].Key
		r.Steps[index].Reason = err.Error()
		return nil
	})
	return e
}
