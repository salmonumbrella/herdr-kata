package tui

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type metadataFS struct {
	state.OSFS
	t        *testing.T
	attempts int
	denied   string
}

func (f *metadataFS) Open(string) (state.File, error) {
	f.t.Fatal("artifact content read")
	return nil, errors.New("content read")
}
func (f *metadataFS) ReadDir(p string) ([]fs.DirEntry, error) {
	if strings.Contains(p, string(filepath.Separator)+"steps"+string(filepath.Separator)) && len(strings.Split(filepath.ToSlash(p), "/")) > 0 {
		if name := filepath.Base(p); name == "1" {
			f.attempts++
		}
	}
	if p == f.denied {
		return nil, fs.ErrPermission
	}
	return f.OSFS.ReadDir(p)
}
func artifactFile(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("not read"), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestU1ArtifactsPaths(t *testing.T) {
	root := t.TempDir()
	l := state.Layout{Root: root}
	paths := []string{l.RunFile("run"), filepath.Join(l.AttemptDir("run", "build", 1), "stderr.log"), filepath.Join(l.AttemptDir("run", "build", 2), "result.json"), l.LogFile()}
	for _, p := range paths {
		artifactFile(t, p)
	}
	if err := os.Symlink(l.RunFile("run"), filepath.Join(l.RunDir("run"), "symlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(l.AttemptDir("run", "build", 1), filepath.Join(l.RunDir("run"), "steps", "linked")); err != nil {
		t.Fatal(err)
	}
	f := &metadataFS{t: t}
	got := scanArtifacts(f, root, "run")
	if got.Err != nil || got.Missing || !reflect.DeepEqual(got.Paths, paths) {
		t.Fatal(got)
	}
	for _, uid := range []string{"../run", ".", "/run", "a/b", "a\\b", ""} {
		if got := scanArtifacts(f, root, uid); got.Err == nil || len(got.Paths) > 0 {
			t.Fatal(uid, got)
		}
	}
}
func TestU1ArtifactsBounds(t *testing.T) {
	root := t.TempDir()
	l := state.Layout{Root: root}
	for i := range 65 {
		artifactFile(t, filepath.Join(l.AttemptDir("run", fmt.Sprintf("step-%02d", i), 1), "output"))
	}
	f := &metadataFS{t: t}
	got := scanArtifacts(f, root, "run")
	if len(got.Paths) != 64 || got.More != 1 || f.attempts != 64 {
		t.Fatal(len(got.Paths), got.More, f.attempts)
	}
	root = t.TempDir()
	l.Root = root
	for i := range 514 {
		artifactFile(t, filepath.Join(l.RunDir("run"), fmt.Sprintf("file-%03d", i)))
	}
	got = scanArtifacts(&metadataFS{t: t}, root, "run")
	if len(got.Paths) != 512 || got.More != 2 {
		t.Fatal(len(got.Paths), got.More)
	}
}
func TestU1ArtifactsErrors(t *testing.T) {
	root := t.TempDir()
	got := scanArtifacts(state.OSFS{}, root, "missing")
	if !got.Missing || got.Err != nil {
		t.Fatal(got)
	}
	l := state.Layout{Root: root}
	artifactFile(t, l.RunFile("run"))
	artifactFile(t, filepath.Join(l.AttemptDir("run", "build", 1), "output"))
	f := &metadataFS{t: t, denied: l.AttemptDir("run", "build", 1)}
	got = scanArtifacts(f, root, "run")
	if !errors.Is(got.Err, fs.ErrPermission) || len(got.Paths) != 1 || got.Paths[0] != l.RunFile("run") {
		t.Fatal(got)
	}
}
func TestU1ArtifactsMissingRunKeepsLog(t *testing.T) {
	root := t.TempDir()
	l := state.Layout{Root: root}
	artifactFile(t, l.LogFile())
	got := scanArtifacts(state.OSFS{}, root, "removed")
	if !got.Missing || len(got.Paths) != 1 || got.Paths[0] != l.LogFile() {
		t.Fatal(got)
	}
}

func artifactModel(t *testing.T) (*model, *int) {
	t.Helper()
	m, _ := fixture(t)
	count := new(int)
	m.artifactScanner = func(state.FS, string, string) artifactListing {
		*count++
		return artifactListing{Paths: []string{fmt.Sprintf("/state/file-%d", *count)}}
	}
	send(m, "3")
	send(m, "enter")
	return m, count
}
func TestU1ArtifactTriggers(t *testing.T) {
	m, count := artifactModel(t)
	if *count != 1 {
		t.Fatal("entry", *count)
	}
	for range 4 {
		_, cmd := m.Update(dataMsg{data: m.data})
		applyTestCommand(m, cmd)
	}
	if *count != 1 {
		t.Fatal("early periodic scan", *count)
	}
	_, cmd := m.Update(dataMsg{data: m.data})
	applyTestCommand(m, cmd)
	if *count != 2 {
		t.Fatal("fifth poll", *count)
	}
	m.data.Runs[0].Steps[0].Note = "fresh note"
	_, cmd = m.Update(dataMsg{data: m.data})
	applyTestCommand(m, cmd)
	if *count != 2 || !strings.Contains(uncachedView(m), "fresh note") {
		t.Fatal("note freshness", *count)
	}
	m.data.Runs[0].Steps[0].Attempt++
	_, cmd = m.Update(dataMsg{data: m.data})
	applyTestCommand(m, cmd)
	if *count != 3 {
		t.Fatal("attempt change", *count)
	}
	for range 10 {
		m.Update(marqueeTickMsg{})
	}
	if *count != 3 || m.polling {
		t.Fatal("animation did IO", *count)
	}
}
func TestU1ArtifactSequence(t *testing.T) {
	m, _ := artifactModel(t)
	older := m.requestArtifacts()
	newer := m.requestArtifacts()
	m.Update(newer())
	latest := strings.Join(m.artifactListing.Paths, ",")
	m.Update(older())
	if strings.Join(m.artifactListing.Paths, ",") != latest {
		t.Fatal("old sequence accepted")
	}
	oldVisit := m.requestArtifacts()
	send(m, "h")
	send(m, "enter")
	latest = strings.Join(m.artifactListing.Paths, ",")
	m.Update(oldVisit())
	if strings.Join(m.artifactListing.Paths, ",") != latest {
		t.Fatal("old visit accepted")
	}
}
func TestU1ArtifactRefreshGeneration(t *testing.T) {
	m, count := artifactModel(t)
	m.polling = true
	m.pollGeneration = 1
	for range 5 {
		send(m, "r")
	}
	_, cmd := m.Update(dataMsg{data: m.data})
	if *count != 1 || cmd == nil || !m.polling {
		t.Fatal("old poll satisfied refresh", *count)
	}
	_, cmd = m.Update(dataMsg{data: m.data, err: errors.New("snapshot unavailable")})
	applyTestCommand(m, cmd)
	if *count != 2 || m.polling {
		t.Fatal("follow-up did not satisfy refresh", *count)
	}
	if m.requestPoll() == nil {
		t.Fatal("error wedged poll")
	}
}

func TestU1ArtifactsRejectSymlinkRoot(t *testing.T) {
	target := t.TempDir()
	artifactFile(t, state.Layout{Root: target}.LogFile())
	root := filepath.Join(t.TempDir(), "linked-state")
	if err := os.Symlink(target, root); err != nil {
		t.Fatal(err)
	}
	listing := scanArtifacts(&metadataFS{t: t}, root, "run")
	if len(listing.Paths) != 0 {
		t.Fatal("followed rejected state root", listing.Paths)
	}
}

func TestU1ArtifactsMissingRoot(t *testing.T) {
	listing := scanArtifacts(state.OSFS{}, filepath.Join(t.TempDir(), "removed"), "run")
	if !listing.Missing || listing.Err != nil || len(listing.Paths) != 0 {
		t.Fatal("missing state root must report the removed run directory", listing)
	}
}

func TestRunScrollSurvivesArtifactReload(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}} {
		for _, back := range []string{"h", "l"} {
			t.Run(fmt.Sprintf("%dx%d/%s", size[0], size[1], back), func(t *testing.T) {
				m, _ := fixture(t)
				for i := range 80 {
					artifactFile(t, filepath.Join(m.opts.Root, "runs", "r1", fmt.Sprintf("output-%02d.txt", i)))
				}
				m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				send(m, "3")
				caller := m.viewContent()
				send(m, "l")
				send(m, "ctrl+d")
				send(m, "ctrl+d")
				before := m.viewContent()
				if !strings.Contains(before, "output-") || !strings.Contains(before, "↑") {
					t.Fatal("fixture did not scroll into artifacts", before)
				}
				send(m, "l") // run -> job
				if m.currentView() != viewJob {
					t.Fatal("job link did not open")
				}
				_, reload := m.Update(press(back)) // render once before the scan completes
				if m.currentView() != viewRun || !strings.Contains(m.viewContent(), "loading artifacts") {
					t.Fatal("restore did not start an artifact scan", m.viewContent())
				}
				applyTestCommand(m, reload)
				if got := m.viewContent(); got != before {
					t.Fatal("restored run viewport changed after the scan", got, before)
				}
				send(m, "h")
				if m.currentView() != viewList || m.viewContent() != caller {
					t.Fatal("back did not return to the original run list", m.viewContent())
				}
			})
		}
	}
}

func TestArtifactPathsStayLeftTruncated(t *testing.T) {
	m, _ := fixture(t)
	m.width = 15
	m.artifactLoaded = true
	m.artifactListing = artifactListing{Paths: []string{"/srv/example/long-directory/artifact.txt"}}
	for _, frame := range []int{0, 7, 20} {
		m.frame = frame
		lines := m.artifactLines()
		if len(lines) != 1 || ansi.Strip(lines[0].Text) != "  …artifact.txt" || len(lines[0].Animations) > 0 {
			t.Fatalf("frame %d: %v", frame, lines)
		}
	}
}
