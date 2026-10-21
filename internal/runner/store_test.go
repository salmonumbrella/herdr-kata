package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	// Usage tests and worker fakes must not contact the operator’s local daemon.
	t.Setenv("AGENTSVIEW_DATA_DIR", t.TempDir())
	return NewStore(state.OSFS{}, state.Layout{Root: t.TempDir()})
}

// Existing journals and older binaries use Deadline for the run expiry.
func TestStorePreservesLegacyRunExpiry(t *testing.T) {
	s := testStore(t)
	uid := seed(t, s)
	legacy := []byte(`{"Version":1,"UID":"01K00000000000000000000000","Phase":"prepared","Deadline":"2030-01-02T03:04:05Z"}`)
	if err := os.WriteFile(s.layout.RunFile(uid), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transition(uid, func(r *Run) error { r.Reason = "inspected"; return nil }); err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Version  int
		Deadline string
		Reason   string
	}
	data, err := os.ReadFile(s.layout.RunFile(uid))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Version != 1 || saved.Deadline != "2030-01-02T03:04:05Z" || saved.Reason != "inspected" {
		t.Fatalf("legacy expiry lost during transition: %+v", saved)
	}
}

type unreadableFS struct{ state.FS }

func (f unreadableFS) ReadDir(string) ([]fs.DirEntry, error) { return nil, fs.ErrPermission }
func TestStoreActiveScanFailure(t *testing.T) {
	s := NewStore(unreadableFS{state.OSFS{}}, state.Layout{Root: t.TempDir()})
	if _, err := s.ScanActive(); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("scan failure hidden: %v", err)
	}
	defer func() {
		if recover() == nil {
			t.Error("Active silently treated unreadable state as zero gates")
		}
	}()
	s.Active()
}

type journalReadFaultOnce struct {
	state.FS
	path   string
	failed bool
}

func (f *journalReadFaultOnce) Open(path string) (state.File, error) {
	if path == f.path && !f.failed {
		f.failed = true
		return nil, os.ErrPermission
	}
	return f.FS.Open(path)
}

func TestProjectScanPropagatesMembershipReadFailure(t *testing.T) {
	s := testStore(t)
	r := &Run{Version: 1, UID: "held", Phase: Held}
	r.Target.Identity.ProjectUID = "project"
	if err := s.Create(r); err != nil {
		t.Fatal(err)
	}
	s.fsys = &journalReadFaultOnce{FS: s.fsys, path: s.layout.RunFile("held")}
	if ids, err := s.scanActive(context.Background(), "project"); !errors.Is(err, os.ErrPermission) || len(ids) != 0 {
		t.Fatal("membership I/O failure supplied a passing scan", ids, err)
	}
	if saved := loaded(t, s, "held"); saved.Phase != Held || saved.SlotReleased || saved.Target.Identity.ProjectUID != "project" {
		t.Fatal("read failure changed the occupied journal", saved)
	}
}

func TestStoreEncodedSizeIncludesNewline(t *testing.T) {
	s := testStore(t)
	r := &Run{Version: 1, UID: "run", Phase: Prepared}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	r.Reason = strings.Repeat("x", maxRunBytes-len(b))
	if err := s.Create(r); err == nil {
		t.Fatal("created immediately unreadable oversized journal")
	}
}

func TestStoreRejectsInvalidPayloadUTF8(t *testing.T) {
	s := testStore(t)
	err := s.Create(&Run{Version: 1, UID: "run", Phase: Prepared, Ops: []Op{{ID: "a", Kind: "comment", Payload: []byte("{\"Body\":\"\xff\"}")}}})
	if err == nil {
		t.Fatal("invalid payload accepted and silently replaced during delivery")
	}
}

func seed(t *testing.T, s *Store, ops ...Op) string {
	t.Helper()
	uid := "01K00000000000000000000000"
	if err := s.Create(&Run{Version: 1, UID: uid, Phase: Prepared, Steps: []StepState{{Key: "main"}}, Ops: ops}); err != nil {
		t.Fatal(err)
	}
	return uid
}

func TestTransitionSerializes(t *testing.T) {
	s := testStore(t)
	uid := seed(t, s)
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			_, err := s.Transition(uid, func(r *Run) error { r.Steps[0].Attempt++; return nil })
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	r, err := s.Load(uid)
	if err != nil || r.Steps[0].Attempt != 100 {
		t.Fatalf("run=%+v err=%v", r, err)
	}
}

func TestStoreCreateAndFailedTransition(t *testing.T) {
	s := testStore(t)
	uid := seed(t, s)
	if err := s.Create(&Run{Version: 1, UID: uid, Phase: Prepared}); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("duplicate: %v", err)
	}
	_, err := s.Transition(uid, func(r *Run) error { r.Phase = Settled; return errors.New("abort") })
	if err == nil {
		t.Fatal("abort accepted")
	}
	r, err := s.Load(uid)
	if err != nil || r.Phase != Prepared {
		t.Fatalf("aborted state persisted: %+v %v", r, err)
	}
	if _, err := s.Load("../escape"); err == nil {
		t.Fatal("unsafe path accepted")
	}
}

func TestStoreCorruptQuarantineVetoesCreate(t *testing.T) {
	s := testStore(t)
	uid := seed(t, s)
	if err := os.WriteFile(s.layout.RunFile(uid), []byte(`{"version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(uid); err == nil {
		t.Fatal("corrupt run accepted")
	}
	if !state.Quarantined(s.fsys, s.layout, uid) {
		t.Fatal("missing quarantine")
	}
	if err := s.Create(&Run{Version: 1, UID: uid, Phase: Prepared}); err == nil {
		t.Fatal("quarantined UID reused")
	}
}

func TestStoreActiveIncludesPendingReports(t *testing.T) {
	s := testStore(t)
	uid := seed(t, s, Op{ID: "report", Kind: "comment", Payload: []byte(`{}`)})
	_, err := s.Transition(uid, func(r *Run) error { r.Phase = Settled; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Active(); len(got) != 1 || got[0] != uid {
		t.Fatalf("pending report missing: %v", got)
	}
	_, err = s.Transition(uid, func(r *Run) error { r.Ops[0].Done = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Active(); len(got) != 0 {
		t.Fatalf("fully settled active: %v", got)
	}
}

func FuzzTransitionRoundTrip(f *testing.F) {
	f.Add("hello", uint8(3))
	f.Add("\xff", uint8(0))
	f.Fuzz(func(t *testing.T, reason string, n uint8) {
		s := testStore(t)
		uid := seed(t, s)
		_, err := s.Transition(uid, func(r *Run) error { r.Reason = reason; r.Steps[0].Attempt = int(n); return nil })
		if !utf8.ValidString(reason) {
			if err == nil {
				t.Fatal("invalid UTF-8 persisted with silent replacement")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		r, err := s.Load(uid)
		if err != nil || r.Reason != reason || r.Steps[0].Attempt != int(n) {
			t.Fatalf("round trip: %+v %v", r, err)
		}
	})
}
