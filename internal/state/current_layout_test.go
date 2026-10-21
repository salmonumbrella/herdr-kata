package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fresh installations have one layout and never reinterpret an unknown marker.
func TestFreshLayoutAndFutureRefusal(t *testing.T) {
	l := Layout{Root: t.TempDir()}
	f := OSFS{}
	if err := InitializeLayout(f, l); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(l.StateFile())
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != `{"version":1}` {
		t.Fatalf("fresh marker: %s", data)
	}
	if err := InitializeLayout(f, l); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(f, l.Project("project-a").StateFile(), map[string]any{"version": 1, "project_uid": "project-a"}); err != nil {
		t.Fatal(err)
	}
	layouts, err := ProjectLayouts(f, l)
	if err != nil || len(layouts) != 1 || layouts[0] != l.Project("project-a") {
		t.Fatal(layouts, err)
	}
	future := []byte(`{"version":99}`)
	if err := os.WriteFile(l.StateFile(), future, 0600); err != nil {
		t.Fatal(err)
	}
	for _, read := range []func() error{func() error { return InitializeLayout(f, l) }, func() error { _, err := ProjectLayouts(f, l); return err }} {
		err := read()
		if err == nil || !strings.Contains(err.Error(), l.StateFile()) || !strings.Contains(err.Error(), "move the state directory aside and run herdr-kata up again") {
			t.Fatalf("missing repair: %v", err)
		}
	}
	after, err := os.ReadFile(l.StateFile())
	if err != nil || string(after) != string(future) {
		t.Fatal(string(after), err)
	}
}

func TestLayoutPresentValidatesMarker(t *testing.T) {
	for _, tc := range []struct {
		name, marker     string
		present, wantErr bool
	}{
		{name: "absent"},
		{name: "current", marker: `{"version":1}`, present: true},
		{name: "future", marker: `{"version":99}`, wantErr: true},
		{name: "malformed", marker: `{`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := Layout{Root: t.TempDir()}
			if tc.marker != "" {
				if err := os.WriteFile(l.StateFile(), []byte(tc.marker), 0600); err != nil {
					t.Fatal(err)
				}
			}
			present, err := LayoutPresent(OSFS{}, l)
			if present != tc.present || (err != nil) != tc.wantErr {
				t.Fatal(present, err)
			}
		})
	}
}

func TestCorruptLayoutPreservesOccupantsAndArtifactPaths(t *testing.T) {
	l := Layout{Root: t.TempDir()}
	if err := os.WriteFile(l.StateFile(), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, read := range []func() error{
		func() error { return InitializeLayout(OSFS{}, l) },
		func() error { _, err := LayoutPresent(OSFS{}, l); return err },
		func() error { _, err := ProjectLayouts(OSFS{}, l); return err },
	} {
		err := read()
		if err == nil {
			t.Fatal("corrupt layout accepted")
		}
		if strings.Contains(err.Error(), "move the state directory aside") || !strings.Contains(err.Error(), "herdr-kata stop") || !strings.Contains(err.Error(), "preserve original artifact paths") || !strings.Contains(err.Error(), "restore damaged state from trusted evidence") || !strings.Contains(err.Error(), "resolve existing occupants") {
			t.Fatal("unsafe or missing corruption remedy", err)
		}
	}
	after, err := os.ReadFile(l.StateFile())
	if err != nil || string(after) != "broken" {
		t.Fatal("changed corrupt marker", string(after), err)
	}
}

func TestLayoutFixturesKeepRecoveryAdviceDistinct(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		incompatible, corrupt bool
	}{
		{name: "current"},
		{name: "legacy-empty-project", incompatible: true},
		{name: "legacy-project", incompatible: true},
		{name: "version2", incompatible: true},
		{name: "version2-migration", incompatible: true},
		{name: "version3", incompatible: true},
		{name: "damaged-current", corrupt: true},
		{name: "damaged-legacy", incompatible: true},
		{name: "missing-version", corrupt: true},
		{name: "invalid-version", corrupt: true},
		{name: "undecodable", corrupt: true},
		{name: "trailing-data", corrupt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, err := os.ReadFile(filepath.Join("testdata", "layouts", tc.name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			l := Layout{Root: t.TempDir()}
			if err := os.WriteFile(l.StateFile(), fixture, 0600); err != nil {
				t.Fatal(err)
			}
			for _, read := range []func() error{
				func() error { return InitializeLayout(OSFS{}, l) },
				func() error {
					present, err := LayoutPresent(OSFS{}, l)
					if present != (tc.name == "current") {
						t.Errorf("present=%t", present)
					}
					return err
				},
				func() error { _, err := ProjectLayouts(OSFS{}, l); return err },
			} {
				err := read()
				if tc.name == "current" {
					if err != nil {
						t.Fatal(err)
					}
					continue
				}
				if err == nil {
					t.Fatal("unsupported or damaged layout accepted")
				}
				_, corrupt := errors.AsType[*CorruptError](err)
				if corrupt != tc.corrupt || strings.Contains(err.Error(), "move the state directory aside and run herdr-kata up again") != tc.incompatible || strings.Contains(err.Error(), "preserve original artifact paths") != tc.corrupt {
					t.Fatal("wrong recovery advice", err)
				}
			}
			after, err := os.ReadFile(l.StateFile())
			if err != nil || string(after) != string(fixture) {
				t.Fatal("classification changed state", err)
			}
		})
	}
}
