package defs

import (
	"go.kenn.io/kit/secretref"
	"strings"
	"testing"
)

func TestSecretClassification(t *testing.T) {
	for _, tc := range []struct{ variable, secret, reason string }{
		{"GITHUB_TOKEN", "review", ""}, {"GITHUB_TOKEN", "missing", "unmapped secret"},
		{"KATA_DAEMON", "review", "reserved"}, {"KATA_SERVER", "review", "reserved"},
		{"KATA_AUTH_TOKEN", "review", "reserved"}, {"EXAMPLE_CREDENTIAL", "review", "reserved"},
		{"OTHER_CREDENTIAL", "review", "reserved"},
		{"HERDR_KATA_RUN", "review", "reserved"}, {"invalid-name", "review", "invalid"},
	} {
		t.Run(tc.variable+tc.secret, func(t *testing.T) {
			c := cfg()
			c.Kata.CredentialEnv = "EXAMPLE_CREDENTIAL"
			c.Kata.CredentialNames = []string{"EXAMPLE_CREDENTIAL", "OTHER_CREDENTIAL"}
			c.Secrets = map[string]secretref.Ref{"review": {Env: "EXAMPLE_SOURCE"}}
			j := fixture(t, "job")
			j.SecretRefs = map[string]string{tc.variable: tc.secret}
			_, s := Classify(j, nil, c)
			if tc.reason == "" {
				if !s.Supported {
					t.Fatal(s)
				}
			} else if s.Supported || !strings.Contains(strings.Join(s.Reasons, " "), tc.reason) {
				t.Fatal(s)
			}
		})
	}
}
