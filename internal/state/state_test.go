package state

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

type record struct {
	Version int    `json:"version"`
	Text    string `json:"text"`
}

var errInjected = errors.New("errInjected failure")

type faultFS struct {
	OSFS
	syncFile, rename, syncDir bool
}
type faultFile struct{ File }

func (f faultFile) Sync() error { return errInjected }
func (f faultFS) CreateTemp(d, p string) (File, error) {
	file, err := f.OSFS.CreateTemp(d, p)
	if err == nil && f.syncFile {
		return faultFile{file}, nil
	}
	return file, err
}
func (f faultFS) Rename(a, b string) error {
	if f.rename {
		return errInjected
	}
	return f.OSFS.Rename(a, b)
}
func (f faultFS) SyncDir(p string) error {
	if f.syncDir {
		return errInjected
	}
	return f.OSFS.SyncDir(p)
}
func TestWriteJSONRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nested", "state.json")
	want := record{1, "hello"}
	if err := WriteJSON(OSFS{}, p, want); err != nil {
		t.Fatal(err)
	}
	var got record
	if err := ReadJSON(OSFS{}, p, 1024, &got); err != nil || got != want {
		t.Fatalf("read = %+v, %v", got, err)
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions = %v", info.Mode())
	}
}
func testWriteFailure(t *testing.T, f faultFS, want string) {
	t.Helper()
	d := t.TempDir()
	p := filepath.Join(d, "state.json")
	if err := os.WriteFile(p, []byte(`{"version":1,"text":"old"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(f, p, record{1, "new"}); !errors.Is(err, errInjected) {
		t.Fatalf("error = %v", err)
	}
	var got record
	if err := ReadJSON(OSFS{}, p, 1024, &got); err != nil || got.Text != want {
		t.Fatalf("read = %+v, %v", got, err)
	}
	entries, _ := os.ReadDir(d)
	if len(entries) != 1 {
		t.Fatalf("temporary files leaked: %v", entries)
	}
}
func TestWriteJSONSyncFailureLeavesOld(t *testing.T) {
	testWriteFailure(t, faultFS{syncFile: true}, "old")
}
func TestWriteJSONRenameFailureLeavesOld(t *testing.T) {
	testWriteFailure(t, faultFS{rename: true}, "old")
}
func TestWriteJSONDirSyncFailureReturnsError(t *testing.T) {
	testWriteFailure(t, faultFS{syncDir: true}, "new")
}
func TestReadJSONRejectsSymlink(t *testing.T) {
	d := t.TempDir()
	target := filepath.Join(d, "target")
	p := filepath.Join(d, "link")
	os.WriteFile(target, []byte(`{"version":1}`), 0600)
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	var got record
	var corrupt *CorruptError
	if err := ReadJSON(OSFS{}, p, 1024, &got); !errors.As(err, &corrupt) {
		t.Fatalf("error = %v", err)
	}
}
func TestReadJSONRejectsOversize(t *testing.T)      { testReadBad(t, `{"version":1}`, 4) }
func TestReadJSONRejectsUnknownField(t *testing.T)  { testReadBad(t, `{"extra":1}`, 1024) }
func TestReadJSONRejectsTrailingValue(t *testing.T) { testReadBad(t, `{} {}`, 1024) }
func TestReadJSONRejectsPartial(t *testing.T)       { testReadBad(t, `{"version":`, 1024) }
func testReadBad(t *testing.T, data string, max int64) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(p, []byte(data), 0600)
	var got record
	var corrupt *CorruptError
	if err := ReadJSON(OSFS{}, p, max, &got); !errors.As(err, &corrupt) {
		t.Fatalf("error = %v", err)
	}
}
func TestReadJSONMissing(t *testing.T) {
	var got record
	if err := ReadJSON(OSFS{}, filepath.Join(t.TempDir(), "missing"), 1024, &got); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error = %v", err)
	}
}
func TestQuarantineRunKeepsUID(t *testing.T) {
	l := Layout{Root: t.TempDir()}
	uid := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	p := l.RunFile(uid)
	os.MkdirAll(filepath.Dir(p), 0700)
	os.WriteFile(p, []byte("broken"), 0600)
	if err := QuarantineRun(OSFS{}, l, uid, p); err != nil {
		t.Fatal(err)
	}
	if !Quarantined(OSFS{}, l, uid) {
		t.Fatal("quarantine did not veto UID")
	}
	entries, err := os.ReadDir(l.QuarantineRunDir(uid))
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %v, %v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(l.QuarantineRunDir(uid), entries[0].Name()))
	if err != nil || string(data) != "broken" {
		t.Fatalf("data = %q, %v", data, err)
	}
	if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("source still exists: %v", err)
	}
}
func TestTryLockExclusiveAcrossProcesses(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lock")
	lock, ok, err := TryLock(p)
	if err != nil || !ok {
		t.Fatalf("lock = %v, %v", ok, err)
	}
	defer lock.Unlock()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelper$")
	cmd.Env = append(os.Environ(), "HK_LOCK_PATH="+p, "HK_LOCK_MODE=busy")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child = %v, %s", err, out)
	}
	held, err := Held(p)
	if err != nil || !held {
		t.Fatalf("held = %v, %v", held, err)
	}
}
func TestLockReleasedOnExit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lock")
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelper$")
	cmd.Env = append(os.Environ(), "HK_LOCK_PATH="+p, "HK_LOCK_MODE=exit")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child = %v, %s", err, out)
	}
	lock, ok, err := TryLock(p)
	if err != nil || !ok {
		t.Fatalf("lock = %v, %v", ok, err)
	}
	defer lock.Unlock()
}
func TestLockHelper(t *testing.T) {
	p := os.Getenv("HK_LOCK_PATH")
	if p == "" {
		return
	}
	lock, ok, err := TryLock(p)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("HK_LOCK_MODE") == "busy" {
		if ok {
			lock.Unlock()
			t.Fatal("second process acquired lock")
		}
		return
	}
	if !ok {
		t.Fatal("lock not acquired")
	}
	os.Exit(0)
}
func FuzzWriteJSONRoundTrip(f *testing.F) {
	f.Add("", 1)
	f.Add("quotes\"\n", 2)
	f.Fuzz(func(t *testing.T, text string, version int) {
		// JSON normalizes invalid UTF-8; test its round-trip domain explicitly.
		text = string([]rune(text))
		p := filepath.Join(t.TempDir(), "state.json")
		want := record{version, text}
		if err := WriteJSON(OSFS{}, p, want); err != nil {
			t.Fatal(err)
		}
		var got record
		if err := ReadJSON(OSFS{}, p, 1<<30, &got); err != nil || got != want {
			t.Fatalf("roundtrip = %+v, %v", got, err)
		}
	})
}

func TestWriteJSONInvalidUTF8Normalization(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	if err := WriteJSON(OSFS{}, p, record{1, "\xff"}); err != nil {
		t.Fatal(err)
	}
	var got record
	if err := ReadJSON(OSFS{}, p, 1024, &got); err != nil || got.Text != "�" {
		t.Fatalf("normalized = %q, %v", got.Text, err)
	}
}

// Directory entries for newly created parents must be synced as well as the
// final file's rename; otherwise a successful prepare can disappear on reboot.
type syncRecorder struct {
	OSFS
	dirs []string
}

func (f *syncRecorder) SyncDir(p string) error { f.dirs = append(f.dirs, p); return f.OSFS.SyncDir(p) }
func TestWriteJSONSyncsNewParentEntries(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "runs", "uid", "run.json")
	if err := WriteJSON(faultFS{syncDir: true}, p, record{1, "first"}); !errors.Is(err, errInjected) {
		t.Fatalf("first sync error = %v", err)
	}
	f := &syncRecorder{}
	if err := WriteJSON(f, p, record{1, "prepared"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{root, filepath.Join(root, "runs"), filepath.Join(root, "runs", "uid")} {
		found := false
		for _, got := range f.dirs {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("new directory entry not durable: missing sync of %s (got %v)", want, f.dirs)
		}
	}
}

func TestQuarantineRunSyncsNewParentEntries(t *testing.T) {
	root := t.TempDir()
	l := Layout{Root: root}
	p := l.RunFile("uid")
	os.MkdirAll(filepath.Dir(p), 0700)
	os.WriteFile(p, []byte("broken"), 0600)
	f := &syncRecorder{}
	if err := QuarantineRun(f, l, "uid", p); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{root, filepath.Join(root, "quarantine"), filepath.Join(root, "quarantine", "runs"), l.QuarantineRunDir("uid"), l.RunDir("uid")} {
		found := false
		for _, got := range f.dirs {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("quarantine entry not durable: missing sync of %s", want)
		}
	}
}

type unreadableQuarantine struct{ OSFS }

func (unreadableQuarantine) ReadDir(string) ([]fs.DirEntry, error) { return nil, fs.ErrPermission }
func TestQuarantinedFailsClosed(t *testing.T) {
	l := Layout{Root: t.TempDir()}
	if !Quarantined(unreadableQuarantine{}, l, "uid") {
		t.Fatal("unreadable quarantine permitted preparation")
	}
	if Quarantined(OSFS{}, l, "uid") {
		t.Fatal("missing directory vetoed preparation")
	}
	os.MkdirAll(l.QuarantineRunDir("uid"), 0700)
	if Quarantined(OSFS{}, l, "uid") {
		t.Fatal("confirmed empty directory vetoed preparation")
	}
}
func TestReadJSONSizeBoundary(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	data := []byte(`{"version":1}`)
	os.WriteFile(p, data, 0600)
	var got record
	if err := ReadJSON(OSFS{}, p, int64(len(data)), &got); err != nil || got.Version != 1 {
		t.Fatalf("boundary = %+v, %v", got, err)
	}
	testReadBad(t, string(data), int64(len(data)-1))
}
func TestLockSiblingAgreement(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lock")
	lock, ok, err := TryLock(p)
	if err != nil || !ok {
		t.Fatalf("lock = %v, %v", ok, err)
	}
	for range 2 {
		if err := lock.Unlock(); err != nil {
			t.Fatal(err)
		}
	}
	if held, err := Held(p); err != nil || held {
		t.Fatalf("unlocked Held = %v, %v", held, err)
	}
	again, ok, err := TryLock(p)
	if err != nil || !ok {
		t.Fatalf("reacquire = %v, %v", ok, err)
	}
	again.Unlock()
}

func TestDefaultRootPrecedence(t *testing.T) {
	t.Setenv("HOME", "/tmp/home")
	t.Setenv("HERDR_PLUGIN_STATE_DIR", "")
	t.Setenv("XDG_STATE_HOME", "")
	if got := DefaultRoot(); got != "/tmp/home/.local/state/herdr/plugins/kata" {
		t.Fatalf("fallback = %s", got)
	}
	t.Setenv("XDG_STATE_HOME", "/tmp/state")
	if got := DefaultRoot(); got != "/tmp/state/herdr/plugins/kata" {
		t.Fatalf("XDG = %s", got)
	}
	t.Setenv("HERDR_PLUGIN_STATE_DIR", "/tmp/plugin")
	if got := DefaultRoot(); got != "/tmp/plugin" {
		t.Fatalf("plugin override = %s", got)
	}
}

func TestReadJSONRejectsNull(t *testing.T) { testReadBad(t, "null", 1024) }
func TestWriteJSONRejectsNull(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	if err := WriteJSON(OSFS{}, p, nil); err == nil {
		t.Fatal("null state accepted")
	}
}
func TestDefaultRootWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("HERDR_PLUGIN_STATE_DIR", "")
	t.Setenv("XDG_STATE_HOME", "")
	if got := DefaultRoot(); !filepath.IsAbs(got) {
		t.Fatalf("HOME-less state root is relative: %s", got)
	}
}
