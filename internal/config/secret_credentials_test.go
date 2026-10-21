package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Secret aliases must not bypass the credential fence when secret_refs uses an
// unrelated variable name, including credentials belonging to another lane.
func TestLoadRejectsSecretCredentialAliases(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	credential := filepath.Join(root, "token")
	if err := os.WriteFile(credential, []byte("credential-value"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Link(credential, alias); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, source string }{
		{"auth env", `env="KATA_AUTH_TOKEN"`},
		{"other lane env", `env="OTHER_TOKEN"`},
		{"credential file", fmt.Sprintf("file=%q", credential)},
		{"clean file", fmt.Sprintf("file=%q", root+"/unused/../token")},
		{"home file", `file="~/token"`},
		{"hard link", fmt.Sprintf("file=%q", alias)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := fmt.Sprintf(`session="work"
executor="worker"
[kata]
bin="/bin/kata"
[[projects]]
name="example-project"
actor="worker"
[[projects]]
name="other-project"
actor="worker"
credential_env="OTHER_TOKEN"
credential_file=%q
[profiles.default]
kind="codex"
[secrets.alias]
%s
`, credential, tc.source)
			if _, err := loadText(t, text); err == nil || !strings.Contains(err.Error(), `secret "alias"`) || !strings.Contains(err.Error(), "Kata credential") {
				t.Fatalf("credential alias accepted or misdiagnosed: %v", err)
			}
		})
	}
}

func TestLoadRejectsLegacyCredentialAlias(t *testing.T) {
	text := strings.Replace(fixture(t), `actor = "runner"`, "actor = \"runner\"\ncredential_env=\"LANE_TOKEN\"", 1)
	if _, err := loadText(t, text+"\n[secrets.alias]\nenv=\"LANE_TOKEN\"\n"); err == nil || !strings.Contains(err.Error(), "Kata credential") {
		t.Fatal(err)
	}
}

func TestLoadAllowsSeparateSecretSource(t *testing.T) {
	root := t.TempDir()
	credential := filepath.Join(root, "credential")
	service := filepath.Join(root, "service")
	for _, path := range []string{credential, service} {
		if err := os.WriteFile(path, []byte("fixture-value"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	text := strings.Replace(fixture(t), `actor = "runner"`, fmt.Sprintf("actor = \"runner\"\ncredential_env=\"LANE_TOKEN\"\ncredential_file=%q", credential), 1)
	for _, source := range []string{`env="SERVICE_TOKEN"`, fmt.Sprintf("file=%q", service)} {
		if _, err := loadText(t, text+"\n[secrets.alias]\n"+source+"\n"); err != nil {
			t.Fatal(err)
		}
	}
}
