package herdr

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
)

func TestEnvironmentNamesNeverWritesValues(t *testing.T) {
	var out bytes.Buffer
	if err := WriteEnvironmentNames(&out, []string{"KATA_DAEMON=fixture-route-only", "EXAMPLE_CREDENTIAL=fixture-secret-only", "PATH=/bin"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "fixture-") || !strings.Contains(out.String(), "KATA_DAEMON") || !strings.Contains(out.String(), "EXAMPLE_CREDENTIAL") {
		t.Fatal(out.String())
	}
}

// Session logs live under the agent's own home directories, which come from
// the Herdr server and shell startup files rather than the worker. Only those
// absolute paths cross back with the names.
func TestEnvironmentNamesReportAgentHomes(t *testing.T) {
	var out bytes.Buffer
	if err := WriteEnvironmentNames(&out, []string{"CODEX_HOME=/srv/projects/example/codex-home", "CLAUDE_CONFIG_DIR=relative/dir", "HOME=/srv/home/worker", "EXAMPLE_CREDENTIAL=/fixture-secret-only"}); err != nil {
		t.Fatal(err)
	}
	var entries []string
	if err := json.Unmarshal(out.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	names, homes := parseEnvironmentNames(entries)
	want := map[string]string{"CODEX_HOME": "/srv/projects/example/codex-home", "HOME": "/srv/home/worker"}
	if !maps.Equal(homes, want) || strings.Contains(out.String(), "fixture-secret") || strings.Contains(out.String(), "relative/dir") || !slices.Contains(names, "CLAUDE_CONFIG_DIR") || !slices.Contains(names, "EXAMPLE_CREDENTIAL") {
		t.Fatalf("names %v homes %v output %s", names, homes, out.String())
	}
	if slices.ContainsFunc(names, func(n string) bool { return strings.Contains(n, "=") }) {
		t.Fatalf("a value stayed among the names: %v", names)
	}
}
