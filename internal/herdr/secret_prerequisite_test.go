//go:build herdr

package herdr_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/testenv"
	"time"
)

// Characterize the pinned server before enabling secrets on the socket API.
func TestRealHerdrTabEnvironmentExposure(t *testing.T) {
	bin := testenv.RequiredBinary(t, "HERDR_KATA_TEST_HERDR_BIN")
	s := newRealHerdr(t, bin)
	s.start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pong, err := s.client.Ping(ctx)
	if err != nil || pong.Version != "0.9.5" {
		t.Fatalf("pinned Herdr: %+v %v", pong, err)
	}
	ws, err := s.client.WorkspaceCreate(ctx, "Fixture", s.home)
	if err != nil {
		t.Fatal(err)
	}
	_, pane, err := s.client.TabCreate(ctx, ws.WorkspaceID, "Fixture", s.home, map[string]string{"EXAMPLE_SECRET": "fixture-secret-only"})
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(s.home, "env-received")
	if err := s.client.Call(ctx, "pane.send_input", map[string]any{"pane_id": pane.PaneID, "text": `test -n "$EXAMPLE_SECRET" && : > '` + marker + `'`, "keys": []string{"enter"}}, nil); err != nil {
		t.Fatal(err)
	}
	waitRealHerdr(t, ctx, func() error { _, err := os.Stat(marker); return err })
	var raw json.RawMessage
	if err := s.client.Call(ctx, "pane.get", map[string]string{"pane_id": pane.PaneID}, &raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "fixture-secret-only") {
		t.Fatal("pane.get exposes tab environment: prompt secrets must be refused")
	}
	var snapshot json.RawMessage
	if err := s.client.Call(ctx, "session.snapshot", nil, &snapshot); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(snapshot), pane.PaneID) {
		t.Fatal("snapshot did not contain fixture pane")
	}
	if strings.Contains(string(snapshot), "fixture-secret-only") {
		t.Fatal("session.snapshot exposes tab environment")
	}
	s.stop(t)
	saved := false
	err = filepath.WalkDir(s.home, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if filepath.Base(path) == "session.json" {
			saved = true
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		if strings.Contains(string(b), "fixture-secret-only") {
			t.Errorf("tab environment persisted in %s", filepath.Base(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !saved {
		t.Fatal("server did not persist a session snapshot")
	}
}
