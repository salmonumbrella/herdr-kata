package doctor

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestQuarantineHasRemedyAndAcknowledgementClearsFailure(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "readable", true: "corrupt"}[corrupt], func(t *testing.T) {
			l := state.Layout{Root: t.TempDir()}
			uid := "01K00000000000000000000000"
			var data any = runner.Run{Version: 1, UID: uid, Phase: runner.Held, Reason: "admission_rejected"}
			if corrupt {
				data = map[string]any{"unknown_future_field": true}
			}
			path := filepath.Join(l.QuarantineRunDir(uid), "saved-run.json")
			if err := state.WriteJSON(state.OSFS{}, path, data); err != nil {
				t.Fatal(err)
			}
			checks, err := LocalChecks(state.OSFS{}, l)
			if err != nil || len(checks) != 1 || checks[0].Passed || !strings.Contains(checks[0].Detail, "herdr-kata abandon "+uid+" --local") {
				t.Fatal("quarantine has no remedy", checks, err)
			}
			if err := runner.AbandonLocal(t.Context(), state.OSFS{}, l, uid, nil); err != nil {
				t.Fatal("cannot acknowledge preserved quarantine", err)
			}
			checks, err = LocalChecks(state.OSFS{}, l)
			if err != nil {
				t.Fatal(err)
			}
			for _, check := range checks {
				if !check.Passed {
					t.Fatal("resolved quarantine still fails doctor", check)
				}
			}
			if !state.Quarantined(state.OSFS{}, l, uid) {
				t.Fatal("acknowledgement removed admission veto")
			}
		})
	}
}
