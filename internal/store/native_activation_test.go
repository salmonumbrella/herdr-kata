package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/katacli"
)

func FuzzNativeActivationStaysInConfiguredProject(f *testing.F) {
	f.Add("spoke-project")
	f.Add("project λ")
	f.Fuzz(func(t *testing.T, project string) {
		project = "project:" + strings.ToValidUTF8(project, "\ufffd")
		r := &NativeRepository{StateDir: t.TempDir(), Client: &katacli.Client{Target: katacli.Target{Server: "http://127.0.0.1:7777", Project: project}}, Binding: NativeBinding{ProjectUID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}}
		uid := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
		a, key, err := r.activationIdentity(uid)
		if err != nil {
			t.Fatal(err)
		}
		a.Enabled = true
		// Apply the pinned whole-file limit to materialized escaped JSON, not to
		// the generator's Unicode domain.
		encoded, _ := json.Marshal(nativeActivations{Version: 1, Entries: map[string]NativeActivation{key: a}})
		if len(encoded) > 262144 {
			if err := r.setActivated(uid, true); err == nil {
				t.Fatal("oversized activation file accepted")
			}
			return
		}
		if enabled, err := r.IsActivated(uid); err != nil || enabled {
			t.Fatalf("missing local intent activated: %v %v", enabled, err)
		}
		if err := r.setActivated(uid, true); err != nil {
			t.Fatal(err)
		}
		if enabled, err := r.IsActivated(uid); err != nil || !enabled {
			t.Fatalf("explicit local intent lost: %v %v", enabled, err)
		}
		r.Client.Target.Project = "other:" + project
		if enabled, err := r.IsActivated(uid); err != nil || enabled {
			t.Fatalf("activation leaked to another configured project: %v %v", enabled, err)
		}
	})
}
