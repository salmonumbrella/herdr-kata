package doctor

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestDoctorPinMismatchNamesFieldsWithoutEnvironmentValues(t *testing.T) {
	c, k, h, l := doctorFixture(t)
	i, route, err := kata.ResolveIdentity(t.Context(), k)
	if err != nil {
		t.Fatal(err)
	}
	route.Env = map[string]string{"KATA_SERVER": "PINNED_PRIVATE_VALUE"}
	pin := kata.Installation{Version: 1, CreatedAt: time.Now(), Executor: "old-worker", Actor: i.Actor, InstanceUID: i.InstanceUID, ProjectUID: i.ProjectUID, Route: route}
	if err := state.WriteJSON(state.OSFS{}, l.Project(i.ProjectUID).StateFile(), pin); err != nil {
		t.Fatal(err)
	}
	if err := state.InitializeLayout(state.OSFS{}, l); err != nil {
		t.Fatal(err)
	}
	r := Check(t.Context(), c, k, h, l)
	found := false
	for _, check := range r.Checks {
		if check.Name == "identity and route" {
			found = true
			if check.Passed || !strings.Contains(check.Detail, `executor (pinned="old-worker", current="test")`) || !strings.Contains(check.Detail, "kata.env.KATA_SERVER (values withheld)") || !strings.Contains(check.Detail, "herdr-kata doctor --json") {
				t.Fatalf("unhelpful pin diagnostic: %+v", check)
			}
		}
	}
	raw, err := json.Marshal(r)
	if err != nil || !found || r.OK() || strings.Contains(string(raw), "PINNED_PRIVATE_VALUE") {
		t.Fatalf("doctor mismatch report: %s, error: %v", raw, err)
	}
}

func TestDoctorPinGatesLaneChecks(t *testing.T) {
	for _, tc := range []struct {
		name       string
		wantPassed bool
		wantDetail string
		wantLost   string
	}{
		{"absent", true, "not pinned yet; instance / project / actor", "installation not pinned yet"},
		{"matched", true, "matches pinned installation", "no lost runs in scanned pages"},
		{"mismatched", false, `executor (pinned="other", current="test")`, ""},
		{"corrupt", false, "corrupt state", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, k, _, l := doctorFixture(t)
			i, route, err := kata.ResolveIdentity(t.Context(), k)
			if err != nil {
				t.Fatal(err)
			}
			pl := l.Project(i.ProjectUID)
			if tc.name != "absent" {
				pin := kata.Installation{Version: 1, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Executor: c.Executor, Actor: i.Actor, InstanceUID: i.InstanceUID, ProjectUID: i.ProjectUID, Route: route}
				if tc.name == "mismatched" {
					pin.Executor = "other"
				}
				if err := state.WriteJSON(state.OSFS{}, pl.StateFile(), pin); err != nil {
					t.Fatal(err)
				}
				if tc.name == "corrupt" {
					if err := os.WriteFile(pl.StateFile(), []byte("{"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			r, uid := checkKata(t.Context(), c, k, l)
			if uid != "project" {
				t.Fatalf("resolved project = %q", uid)
			}
			wantNames := []string{"kata version", "kata commands", "capabilities", "route", "identity and route"}
			if tc.wantPassed {
				wantNames = append(wantNames, "lane lease", "lost-runs")
			}
			if len(r.Checks) != len(wantNames) {
				t.Fatalf("pin check gating: %+v", r.Checks)
			}
			for n, name := range wantNames {
				if r.Checks[n].Name != name {
					t.Fatalf("check order: %+v", r.Checks)
				}
			}
			pinCheck := r.Checks[4]
			if pinCheck.Passed != tc.wantPassed || !strings.Contains(pinCheck.Detail, tc.wantDetail) {
				t.Fatalf("pin diagnostic: %+v", pinCheck)
			}
			if tc.wantPassed && (!r.Checks[5].Passed || !r.Checks[6].Passed || r.Checks[6].Detail != tc.wantLost) {
				t.Fatalf("downstream checks: %+v", r.Checks[5:])
			}
		})
	}
}
