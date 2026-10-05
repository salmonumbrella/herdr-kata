package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/salmonumbrella/herdr-kata/internal/katacli"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/store"
)

// Resolve the frozen reference names against current installation-local values.
// Values are used only in child environments, never in snapshots or observations.
func nativeSecrets(c runner.NativeExecutionContext, repo *store.NativeRepository) (map[string]string, error) {
	values := map[string]string{}
	if c.Job == nil {
		return values, nil
	}
	var body struct {
		Refs map[string]string `json:"secret_refs"`
	}
	if err := katacli.Decode(c.Job.Definition, &body); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(body.Refs))
	for name := range body.Refs {
		names = append(names, name)
	}
	sort.Strings(names)
	seen := map[string]bool{}
	for _, name := range names {
		upper := strings.ToUpper(name)
		if !nativeSecretName(name) || nativeProtectedEnvironment(upper) || seen[upper] {
			return nil, fmt.Errorf("unsupported secret environment name %q", name)
		}
		seen[upper] = true
		ref := body.Refs[name]
		value, ok := repo.Binding.Secrets[ref]
		if !ok || ref == "" {
			return nil, fmt.Errorf("local secret reference %q is not configured", ref)
		}
		if strings.ContainsRune(value, 0) {
			return nil, errors.New("local secret value cannot contain NUL")
		}
		values[name] = value
	}
	return values, nil
}
func nativeSecretName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}
func nativeProtectedEnvironment(name string) bool {
	for _, prefix := range []string{"KATA_", "HERDR_", "LD_", "DYLD_", "GIT_"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	switch name {
	case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "PORT", "PATH", "HOME", "USER", "LOGNAME", "SHELL", "ENV", "BASH_ENV", "ZDOTDIR", "CDPATH", "TMPDIR", "TMP", "TEMP", "GOENV", "GOTOOLCHAIN", "GOROOT", "GOPATH":
		return true
	}
	return false
}
func nativeEnvironmentWithSecrets(env []string, values map[string]string) []string {
	out := make([]string, 0, len(env)+len(values))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		replace := false
		for name := range values {
			if strings.EqualFold(key, name) {
				replace = true
				break
			}
		}
		if !replace {
			out = append(out, entry)
		}
	}
	keys := make([]string, 0, len(values))
	for name := range values {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		out = append(out, name+"="+values[name])
	}
	return out
}
