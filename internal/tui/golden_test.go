package tui

import (
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

// updateGoldens enables `go test ./... -update-goldens`, which rewrites the
// golden frames instead of comparing them.
var updateGoldens = flag.Bool("update-goldens", false, "update UI golden frames")

// savedTheme is the color mode and renderer a test started with.
type savedTheme struct {
	mode colorMode
	dark bool
}

func captureTheme() savedTheme {
	return savedTheme{activeColorMode, activeHasDarkBackground}
}

// restore re-applies the saved mode, which rebuilds every role.
func (s savedTheme) restore() {
	applyColorMode(s.mode, s.dark)
}

func captureFrame(mode string, width, height int, render func() string) (string, error) {
	snapshot := captureTheme()
	defer snapshot.restore()
	env := displayEnvironment{"TERM": "xterm-256color", "KATA_COLOR_MODE": mode}
	switch mode {
	case "light", "dark", "none":
	case "no_color":
		env["NO_COLOR"] = "1"
		env["KATA_COLOR_MODE"] = "dark"
	default:
		return "", fmt.Errorf("unknown golden mode %q", mode)
	}
	resolved := resolveColorMode(env.Getenv)
	applyColorMode(resolved, true)
	text := render()
	if len(strings.Split(text, "\n")) > height {
		return "", fmt.Errorf("frame exceeds height %d", height)
	}
	for line := range strings.SplitSeq(text, "\n") {
		if ansi.StringWidth(line) > width {
			return "", fmt.Errorf("frame exceeds width %d: %q", width, line)
		}
	}
	if resolved == colorNone {
		if err := checkNoColors(text); err != nil {
			return "", err
		}
	}
	return text, nil
}
func checkNoColors(text string) error {
	// Accept structural SGR only, including explicit resets to default colors.
	for _, part := range strings.Split(text, "\x1b[")[1:] {
		before, _, ok := strings.Cut(part, "m")
		if !ok {
			return fmt.Errorf("non-SGR terminal escape")
		}
		for p := range strings.SplitSeq(before, ";") {
			switch p {
			case "", "0", "1", "2", "3", "4", "7", "9", "22", "23", "24", "27", "29", "39", "49":
			default:
				return fmt.Errorf("color/nonstructural SGR in None: %s", before)
			}
		}
	}
	return nil
}

// goldenModes are the color modes every frame is rendered in.
var goldenModes = []string{"light", "dark", "none", "no_color"}

// renderFrames renders one frame in every mode. Light and dark must show the
// same text, and NO_COLOR must render exactly as None.
func renderFrames(width, height int, render func(mode string) string) (map[string]string, error) {
	frames := map[string]string{}
	for _, mode := range goldenModes {
		text, err := captureFrame(mode, width, height, func() string { return render(mode) })
		if err != nil {
			return nil, fmt.Errorf("%s: %w", mode, err)
		}
		frames[mode] = text
	}
	if frames["no_color"] != frames["none"] {
		return nil, fmt.Errorf("NO_COLOR and None ANSI frames differ")
	}
	if ansi.Strip(frames["light"]) != ansi.Strip(frames["dark"]) {
		return nil, fmt.Errorf("light and dark frames differ in text")
	}
	return frames, nil
}

// compareFrames checks <name>-<W>x<H>.txt, the text light and dark share, and
// one <name>-<W>x<H>.<mode>.ansi per color mode. None's text gets its own
// <name>-<W>x<H>.none.txt only where it differs, as colorless fallback glyphs
// do. NO_COLOR equals None, so it has no file.
func compareFrames(dir, name string, width, height int, frames map[string]string, write bool) error {
	base := filepath.Join(dir, fmt.Sprintf("%s-%dx%d", name, width, height))
	text, noneText := ansi.Strip(frames["light"]), ansi.Strip(frames["none"])
	files := []struct{ path, content string }{
		{base + ".txt", text},
		{base + ".light.ansi", frames["light"]},
		{base + ".dark.ansi", frames["dark"]},
		{base + ".none.ansi", frames["none"]},
	}
	if noneText != text {
		files = append(files, struct{ path, content string }{base + ".none.txt", noneText})
	} else if write {
		if err := os.Remove(base + ".none.txt"); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else if _, err := os.Stat(base + ".none.txt"); err == nil {
		return fmt.Errorf("golden %s: None text equals the frame text (use -update-goldens to remove)", base+".none.txt")
	}
	var mismatched []string
	for _, file := range files {
		if write {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return err
			}
			if err := os.WriteFile(file.path, []byte(file.content), 0644); err != nil {
				return err
			}
			continue
		}
		want, err := os.ReadFile(file.path)
		if err != nil {
			return fmt.Errorf("golden %s: %w (use -update-goldens to record)", file.path, err)
		}
		if string(want) != file.content {
			mismatched = append(mismatched, file.path)
		}
	}
	if len(mismatched) > 0 {
		return fmt.Errorf("golden mismatch: %s", strings.Join(mismatched, ", "))
	}
	return nil
}

// AssertFrames checks one case at every size, in every color mode, against
// testdata/golden/<screen>.
func assertFrames(t *testing.T, screen, name string, sizes [][2]int, render func(width, height int) string) {
	t.Helper()
	assertModeFrames(t, screen, name, sizes, func(_ colorMode, width, height int) string { return render(width, height) })
}

// AssertModeFrames is AssertFrames for a render that also checks the roles of
// one color mode.
func assertModeFrames(t *testing.T, screen, name string, sizes [][2]int, render func(mode colorMode, width, height int) string) {
	t.Helper()
	preserveTheme(t)
	dir := filepath.Join("testdata", "golden", screen)
	for _, size := range sizes {
		width, height := size[0], size[1]
		frames, err := renderFrames(width, height, func(_ string) string { return render(activeColorMode, width, height) })
		if err == nil {
			err = compareFrames(dir, name, width, height, frames, *updateGoldens)
		}
		if err != nil {
			t.Fatalf("%s %dx%d: %v", name, width, height, err)
		}
	}
}

// preserveTheme restores the color mode and renderer after a test changes them.
func preserveTheme(t testing.TB) { t.Helper(); snapshot := captureTheme(); t.Cleanup(snapshot.restore) }

func TestCheckFrameModes(t *testing.T) {
	for _, mode := range []string{"light", "dark", "none", "no_color"} {
		s, err := captureFrame(mode, 20, 3, func() string { return titleStyle.Render("frame") })
		if err != nil || s == "" {
			t.Fatal(mode, s, err)
		}
	}
	if _, err := captureFrame("invalid", 20, 3, func() string { return "frame" }); err == nil {
		t.Fatal("invalid mode accepted")
	}
}
func TestCompareFramesFiles(t *testing.T) {
	dir := t.TempDir()
	frames := map[string]string{"light": "\x1b[1mlight\x1b[0m", "dark": "\x1b[2mlight\x1b[0m", "none": "light", "no_color": "light"}
	if err := compareFrames(dir, "frame", 20, 3, frames, false); err == nil {
		t.Fatal("missing golden accepted")
	}
	if err := compareFrames(dir, "frame", 20, 3, frames, true); err != nil {
		t.Fatal(err)
	}
	if err := compareFrames(dir, "frame", 20, 3, frames, false); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{"frame-20x3.dark.ansi", "frame-20x3.light.ansi", "frame-20x3.none.ansi", "frame-20x3.txt"}; !reflect.DeepEqual(names, want) {
		t.Fatal("NO_COLOR or an equal None text got a file", names)
	}
	for file, want := range map[string]string{"frame-20x3.txt": "light", "frame-20x3.light.ansi": "\x1b[1mlight\x1b[0m", "frame-20x3.dark.ansi": "\x1b[2mlight\x1b[0m", "frame-20x3.none.ansi": "light"} {
		if got, err := os.ReadFile(filepath.Join(dir, file)); err != nil || string(got) != want {
			t.Fatal(file, string(got), err)
		}
	}
	for _, mode := range []string{"light", "dark", "none"} {
		changed := maps.Clone(frames)
		changed[mode] = "changed"
		if err := compareFrames(dir, "frame", 20, 3, changed, false); err == nil {
			t.Fatal("mismatch accepted", mode)
		}
	}
}
func TestCompareFramesKeepsDifferentNoneText(t *testing.T) {
	dir := t.TempDir()
	frames := map[string]string{"light": "\x1b[1m▸ step\x1b[0m", "dark": "\x1b[2m▸ step\x1b[0m", "none": "\x1b[7m+ step\x1b[0m", "no_color": "\x1b[7m+ step\x1b[0m"}
	if err := compareFrames(dir, "frame", 20, 3, frames, true); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]string{"frame-20x3.txt": "▸ step", "frame-20x3.none.txt": "+ step"} {
		if got, err := os.ReadFile(filepath.Join(dir, file)); err != nil || string(got) != want {
			t.Fatal(file, string(got), err)
		}
	}
	changed := maps.Clone(frames)
	changed["none"] = "\x1b[7m- step\x1b[0m"
	if err := compareFrames(dir, "frame", 20, 3, changed, false); err == nil {
		t.Fatal("changed None text accepted")
	}
	same := maps.Clone(frames)
	same["none"] = "\x1b[7m▸ step\x1b[0m"
	if err := compareFrames(dir, "frame", 20, 3, same, false); err == nil {
		t.Fatal("stale None text accepted")
	}
	if err := compareFrames(dir, "frame", 20, 3, same, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "frame-20x3.none.txt")); !os.IsNotExist(err) {
		t.Fatal("recording an equal None text kept its file", err)
	}
}
func TestRenderFramesChecksModePairs(t *testing.T) {
	frames, err := renderFrames(20, 3, func(string) string { return titleStyle.Render("frame") })
	if err != nil || len(frames) != 4 || ansi.Strip(frames["light"]) != "frame" {
		t.Fatal(frames, err)
	}
	if _, err := renderFrames(20, 3, func(mode string) string {
		if mode == "dark" {
			return "dark"
		}
		return "frame"
	}); err == nil {
		t.Fatal("light and dark frames with different text accepted")
	}
	if _, err := renderFrames(20, 3, func(mode string) string {
		if mode == "none" || mode == "no_color" {
			return "+ frame"
		}
		return "▸ frame"
	}); err != nil {
		t.Fatal("colorless fallback text refused", err)
	}
	if _, err := renderFrames(20, 3, func(mode string) string {
		if mode == "no_color" {
			return "\x1b[1mframe\x1b[0m"
		}
		return "frame"
	}); err == nil {
		t.Fatal("NO_COLOR frame that differs from None accepted")
	}
}
func TestAssertFramesRestoresTheme(t *testing.T) {
	before := captureTheme()
	defer before.restore()
	applyColorMode(colorDark, true)
	t.Run("frame", func(t *testing.T) {
		t.Chdir(t.TempDir())
		frames, err := renderFrames(20, 3, func(string) string { return "plain" })
		if err != nil {
			t.Fatal(err)
		}
		if err := compareFrames(filepath.Join("testdata", "golden", "screen"), "frame", 20, 3, frames, true); err != nil {
			t.Fatal(err)
		}
		assertFrames(t, "screen", "frame", [][2]int{{20, 3}}, func(int, int) string { return "plain" })
	})
	if activeColorMode != colorDark || !activeHasDarkBackground || subtleStyle.GetForeground() != lipgloss.Color("246") {
		t.Fatal("theme was not restored")
	}
}

func goldenFixture(name string, width, height int) *model {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	m := newModel(Options{Root: "testdata", Now: func() time.Time { return now }})
	m.width, m.height = width, height
	m.data.Running, m.data.WorkerRunning = true, true
	m.data.Jobs = []defs.JobEntry{{UID: "job-a", Name: "Example build", Owner: defs.Adapter, Supported: true, Definition: json.RawMessage(`{"enabled":true,"trigger":{"kind":"manual"},"action":{"kind":"execute","prompt":"Check the example"}}`)}, {UID: "job-b", Name: "Example verification with a long descriptive name", Owner: defs.Adapter, Supported: true, Definition: json.RawMessage(`{"enabled":true,"trigger":{"kind":"manual"}}`)}, {UID: "job-c", Name: "Example report", Owner: defs.Adapter, Supported: true, Definition: json.RawMessage(`{"enabled":true,"trigger":{"kind":"manual"}}`)}}
	m.favorites["job-a"] = true
	started := now.Add(-2 * time.Minute)
	wf := kata.Workflow{UID: "workflow-a", Name: "Example workflow", Definition: json.RawMessage(`{"version":1,"steps":[{"key":"build","kind":"command","command":"true"}]}`)}
	m.data.Workflows = []kata.Workflow{wf}
	m.data.Runs = []runner.Run{{UID: "run-a", Job: kata.Job{UID: "job-a", Name: "Example build"}, Phase: runner.Running, StartedAt: &started, Workflow: &wf}, {UID: "run-b", Job: kata.Job{UID: "job-b", Name: "Example verification"}, Phase: runner.Held, Reason: "review required", StartedAt: &started, Workflow: &wf}, {UID: "run-c", Job: kata.Job{UID: "job-c", Name: "Example report"}, Phase: runner.Settled, Outcome: "succeeded", StartedAt: &started}}
	switch name {
	case "detail":
		m.rebuildJobIndex()
		m.rebuild()
		_ = m.pushDetail(viewRun, "", "run-b")
	case "detail-long":
		m.data.Runs[1].Steps = []runner.StepState{{Key: "build", Status: "failed", Note: strings.Repeat("A detailed local diagnosis explains what to inspect before resuming. ", 60)}}
		m.data.Runs[1].ActiveStep = "build"
		m.rebuildJobIndex()
		m.rebuild()
		_ = m.pushDetail(viewRun, "", "run-b")
	case "selected":
		for i := range 30 {
			r := m.data.Runs[0]
			r.UID = fmt.Sprintf("retained-%02d", i)
			m.data.Runs = append(m.data.Runs, r)
		}
		m.rebuildJobIndex()
		m.rebuild()
		_ = m.pushDetail(viewJob, "", "job-a")
	case "modal":
		m.confirm = []kata.Job{{UID: "job-a", Name: "Example build"}, {UID: "job-c", Name: "Example report"}}
		m.rebuildJobIndex()
		m.rebuild()
	default:
		m.rebuildJobIndex()
		m.rebuild()
	}
	m.frameValid = false
	return m
}
func TestGoldenFrames(t *testing.T) {
	for _, c := range []struct{ screen, name string }{{"jobs", "chrome"}, {"run-detail", "detail"}, {"run-detail", "detail-long"}, {"job-detail", "selected"}, {"jobs", "modal"}} {
		t.Run(c.name, func(t *testing.T) {
			assertFrames(t, c.screen, c.name, [][2]int{{120, 40}, {80, 24}}, func(w, h int) string { m := goldenFixture(c.name, w, h); m.frameValid = false; return m.viewContent() })
		})
	}
}

func TestGoldenStructuralRoles(t *testing.T) {
	_, err := renderFrames(120, 40, func(_ string) string {
		mode := activeColorMode
		w, h := 120, 40
		text := goldenFixture("chrome", w, h).viewContent()
		codes := map[string]bool{}
		for _, part := range strings.Split(text, "\x1b[")[1:] {
			if before, _, ok := strings.Cut(part, "m"); ok {
				for code := range strings.SplitSeq(before, ";") {
					codes[code] = true
				}
			}
		}
		if !codes["1"] || codes["4"] {
			t.Fatal("expected bold chrome without underlines", mode)
		}
		if mode == colorNone {
			if !codes["7"] || !codes["2"] {
				t.Fatal("missing colorless selection/tab structure", mode)
			}
		}
		return text
	})
	if err != nil {
		t.Fatal(err)
	}
}
