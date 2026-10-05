package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExecutionPolicyAdHocPreservesNonNativeRuntimeEnvironment(t *testing.T) {
	for _, configured := range []bool{false, true} {
		name := "unconfigured"
		if configured {
			name = "configured"
		}
		t.Run(name, func(t *testing.T) {
			s, _, herdrDir := policyProduct(t)
			if !configured {
				if err := os.Remove(filepath.Join(stateDir(), "native.json")); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HTTP_PROXY", "http://proxy.invalid:8181")
			t.Setenv("NO_PROXY", "example.invalid")
			t.Setenv("PORT", "31337")
			if _, err := captureStdout(t, func() error {
				return runOnce([]string{"--id", "inspect", "--prompt", "Inspect workspace", "--kind", "codex", "--cwd", s.Native.Binding.Checkouts["primary"], "--timeout", "1s"})
			}); err != nil {
				t.Fatal(err)
			}
			state := policyHerdr(t, herdrDir)
			for key, want := range map[string]string{"HTTP_PROXY": "http://proxy.invalid:8181", "NO_PROXY": "example.invalid", "PORT": "31337"} {
				if configured {
					want = ""
				}
				if state.Env[key] != want {
					t.Errorf("%s did not preserve applicable ordinary/native environment policy", key)
				}
			}
		})
	}
}
