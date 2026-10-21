package runner

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
)

func TestBuildPromptGolden(t *testing.T) {
	for _, name := range []string{"job", "workflow", "checker", "notes"} {
		t.Run(name, func(t *testing.T) {
			r := &Run{UID: "run-1", Occurrence: "2026-01-02T03:04:05Z", IssueRef: "project#abcd", Job: kata.Job{Name: "Parser maintenance"}, JobDef: defs.JobDef{Action: defs.Action{Prompt: "Update the parser and run its tests."}, Issue: &defs.IssuePolicy{Title: "Maintain parser", Body: "Keep the public API stable."}}}
			step := StepState{Key: "main", Kind: "prompt", Attempt: 1}
			if name != "job" {
				r.WFDef = &defs.WorkflowDef{About: "Review the parser changes.", Input: "Compare with the previous API.", Steps: []defs.Step{{Key: "review", Kind: "prompt", Prompt: "Audit the parser; report any changes."}}}
				r.Workflow = &kata.Workflow{Name: "Parser review"}
				step.Key = "review"
				step.Attempt = 2
			}
			if name == "checker" || name == "notes" {
				r.WFDef.Steps = []defs.Step{{Key: "make", Kind: "command", Command: "true"}, {Key: "review", Kind: "prompt", Prompt: "Audit the parser; report any changes.", After: []string{"make"}, Options: json.RawMessage(`{"on_fail":{"goto":"make","max_loops":1}}`)}}
				step.FindingContext = &FindingContext{RootUID: "root", HeadUID: "head", MakerUID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Text: "Saved maker claim."}
			}
			if name == "notes" {
				step.FindingContext.Notes = "c:01ARZ3NDEKTSV4RRFFQ69G5FAW answers c:head\nKeep the old flag accepted."
				step.FindingContext.NotesTruncated = true
			}
			before, _ := json.Marshal(r)
			got := BuildPrompt(r, step, "/state/runs/run-1/steps/"+step.Key+"/"+map[string]string{"job": "1", "workflow": "2", "checker": "2", "notes": "2"}[name]+"/result.json", "/state/bin/report's binary", true)
			want, err := os.ReadFile("testdata/prompt-" + name + ".golden")
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Fatalf("prompt differs from golden:\n%s", got)
			}
			after, _ := json.Marshal(r)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("BuildPrompt mutated run")
			}
		})
	}
}
