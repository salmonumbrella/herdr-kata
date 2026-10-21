package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/salmonumbrella/herdr-kata/internal/state"
)

// Only the post-quarantine source probe fails. Journal preservation and the
// acknowledgement otherwise use real files, so a retry has observable effects.
type quarantinedJournalProbe struct {
	state.OSFS
	journal string
	probe   error
	moved   bool
	probes  int
}

func (f *quarantinedJournalProbe) Rename(from, to string) error {
	if err := f.OSFS.Rename(from, to); err != nil {
		return err
	}
	if from == f.journal {
		f.moved = true
	}
	return nil
}

func (f *quarantinedJournalProbe) Lstat(path string) (fs.FileInfo, error) {
	if path == f.journal && f.moved {
		f.probes++
		return nil, f.probe
	}
	return f.OSFS.Lstat(path)
}

func TestAbandonLocalCorruptJournalProbe(t *testing.T) {
	for _, tc := range []struct {
		name   string
		probe  error
		code   int
		probes int
	}{
		{"permission", fs.ErrPermission, 1, 1},
		{"wrapped-missing", fmt.Errorf("source probe: %w", fs.ErrNotExist), 0, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := state.Layout{Root: t.TempDir()}
			t.Setenv("HERDR_PLUGIN_STATE_DIR", l.Root)
			t.Setenv("HERDR_PLUGIN_CONFIG_DIR", t.TempDir())
			const uid = "01K00000000000000000000000"
			if err := os.MkdirAll(l.RunDir(uid), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(l.RunFile(uid), []byte("broken journal"), 0600); err != nil {
				t.Fatal(err)
			}
			fsys := &quarantinedJournalProbe{journal: l.RunFile(uid), probe: tc.probe}
			var out, stderr bytes.Buffer
			code := abandonWithFS([]string{uid, "--local"}, &out, &stderr, fsys)
			if code != tc.code || !fsys.moved || fsys.probes != tc.probes {
				t.Fatalf("code=%d probes=%d moved=%t stdout=%q stderr=%q", code, fsys.probes, fsys.moved, out.String(), stderr.String())
			}
			ack := filepath.Join(l.QuarantineRunDir(uid), "acknowledged.json")
			if tc.code != 0 {
				if out.Len() != 0 || !strings.Contains(stderr.String(), "corrupt state") {
					t.Fatal("source probe failure hidden", out.String(), stderr.String())
				}
				if _, err := os.Stat(ack); !os.IsNotExist(err) {
					t.Fatal("unproven move was acknowledged", err)
				}
				return
			}
			var saved struct {
				Acknowledged bool `json:"acknowledged"`
			}
			if err := state.ReadJSON(state.OSFS{}, ack, 4096, &saved); err != nil || !saved.Acknowledged {
				t.Fatal("proven journal move was not acknowledged", saved, err)
			}
			if !strings.Contains(out.String(), "quarantined (local resolution)") || stderr.Len() != 0 {
				t.Fatal(out.String(), stderr.String())
			}
		})
	}
}
