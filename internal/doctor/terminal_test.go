package doctor

import (
	"bytes"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

func TestDoctorEscapesInvalidTriggerControlCharacters(t *testing.T) {
	c, l, h := fixture(t)
	job := ownedJob("job")
	job.Definition.Trigger.Kind = "unknown\x1b[2J\n\u202e"
	var out bytes.Buffer
	_ = Check(t.Context(), c, l, h, map[string]kata.Kata{"example-project": fakeKata{jobs: []kata.CronJob{job}}}, &out)
	want := `invalid trigger job: unsupported trigger: unknown\x1b[2J\n\u202e`
	if !strings.Contains(out.String(), want) || strings.ContainsAny(out.String(), "\x1b\u202e") {
		t.Fatalf("unsafe or missing trigger diagnostic: %q", out.String())
	}
}
