package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/salmonumbrella/herdr-kata/internal/agentlimit"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

func TestBoardChromeGeometry(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}, {42, 20}, {30, 10}, {80, 4}} {
		m, _ := fixture(t)
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		s := ansi.Strip(m.viewContent())
		lines := strings.Split(s, "\n")
		if len(lines) > size[1] {
			t.Fatal(size, s)
		}
		for _, l := range lines {
			if ansi.StringWidth(l) > size[0] {
				t.Fatal(size, l)
			}
		}
		if size[1] > 4 && !strings.Contains(s, "page 1/") {
			t.Fatal(s)
		}
	}
}
func TestBoardPersistentConditions(t *testing.T) {
	m, _ := fixture(t)
	m.data.Running = true
	m.data.WorkerRunning = true
	m.data.Route = map[string]any{"allowed": false, "error": "route refused", "signing_blocked": true, "project": "example-project"}
	m.notice.Text = "saved"
	m.notice.Kind = statusSuccess
	m.searching = true
	m.query = "Job"
	m.frameValid = false
	s := ansi.Strip(m.viewContent())
	for _, want := range []string{"route refused", "root-key repin --project example-project", "/Job▏", "saved"} {
		if !strings.Contains(s, want) {
			t.Error(want, s)
		}
	}
	m.Update(dataMsg{data: m.data, err: errors.New("snapshot failed")})
	if m.healthProblem() != "snapshot error" {
		t.Fatal(m.healthProblem())
	}
	m.Update(dataMsg{data: m.data})
	if m.snapshotErr != nil {
		t.Fatal(m.snapshotErr)
	}
}
func TestBoardSharedLabels(t *testing.T) {
	m, _ := fixture(t)
	m.tab = 1
	m.data.Runs[0].Phase = runner.Held
	m.data.Runs[0].Outcome = "failed"
	m.data.Runs[0].Ops = []runner.Op{{SigningRefused: true}}
	m.rebuildJobIndex()
	m.rebuild()
	m.frameValid = false
	s := ansi.Strip(m.viewContent())
	if !strings.Contains(s, "parked") || !strings.Contains(s, "signing refused") {
		t.Fatal(s)
	}
}

func TestFinishedOneShotKeepsItsListMarker(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			m, _ := fixture(t)
			m.data.Jobs = m.data.Jobs[:1]
			m.data.Jobs[0].Name = "Example"
			m.data.Jobs[0].Definition = json.RawMessage(fmt.Sprintf(`{"enabled":%t,"trigger":{"kind":"once","at":"2026-01-01T00:00:00Z"}}`, enabled))
			m.data.Runs = m.data.Runs[:1]
			m.data.Runs[0].Phase = runner.Settled
			m.data.Runs[0].Outcome = "succeeded"
			m.data.Runs[0].Manual = false
			m.data.Runs[0].Occurrence = "2026-01-01T00:00:00Z"
			m.data.Runs[0].Job.DefinitionEventUID = m.data.Jobs[0].DefinitionEventUID
			m.rebuildJobIndex()
			m.rebuild()
			m.frameValid = false
			if strings.Contains(ansi.Strip(m.viewContent()), "Example") {
				t.Fatal("finished one-shot visible before F")
			}
			send(m, "F")
			text := ansi.Strip(m.viewContent())
			if !strings.Contains(text, "Example (finished)") {
				t.Fatal("finished one-shot lost its marker after F", text)
			}
		})
	}
}
func TestBoardSearchExcludesRawDefinitions(t *testing.T) {
	m, _ := fixture(t)
	m.data.Jobs[0].Definition = json.RawMessage(`{"action":{"prompt":"hidden-sentinel"}}`)
	m.query = "hidden-sentinel"
	m.rebuildJobIndex()
	m.rebuild()
	if len(m.rows) != 0 {
		t.Fatal(m.rows)
	}
	m.query = "Job 00"
	m.rebuildJobIndex()
	m.rebuild()
	if len(m.rows) != 1 {
		t.Fatal(m.rows)
	}
}

func TestFrameCacheInvalidation(t *testing.T) {
	m, _ := fixture(t)
	m.selectTab(1)
	m.frameValid = false
	count := 0
	m.now = func() time.Time { count++; return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) }
	m.renderFrame()
	v := count
	for _, msg := range []tea.Msg{struct{}{}, press("j"), press("/"), press("esc"), tea.WindowSizeMsg{Width: 80, Height: 24}} {
		before := count
		m.Update(msg)
		if count <= before {
			t.Fatal("renders per update", before, count)
		}
		renderedCount := count
		m.viewContent()
		m.viewContent()
		if count != renderedCount {
			t.Fatal("View rendered again")
		}
	}
	if count == v {
		t.Fatal("no frames")
	}
}

func TestExpandedRunStepMarqueesDiagnosisAndOpensRun(t *testing.T) {
	m, _ := fixture(t)
	m.width = 120
	m.tab = 1
	m.expanded["r1"] = true
	m.data.Runs[0].Steps[0].Status = "failed"
	m.data.Runs[0].Steps[0].Reason = "command exited"
	m.rebuildJobIndex()
	m.rebuild()
	m.frame = 7
	m.frameValid = false
	text := ansi.Strip(m.viewContent())
	if !strings.Contains(text, "✗ build") || !strings.Contains(text, "ailed (command e") {
		t.Fatal(text)
	}
	m.cursor = 0
	if len(m.rows) != 1 {
		t.Fatal("steps became rows")
	}
	_ = m.open()
	r, ok := m.selected()
	if !ok || m.currentView() != viewRun || r.uid != "r1" {
		t.Fatal("step opens missing run", r, m.nav)
	}
	if text := ansi.Strip(runDetailText(t, m)); !strings.Contains(text, "failed (command exited)") {
		t.Fatal("run detail lost the complete diagnosis", text)
	}
}

func TestInspectorTypedProseAndScopedIdentity(t *testing.T) {
	m, _ := fixture(t)
	m.opts.Projects = map[string]*Lane{"one": {}, "two": {}}
	m.data.Jobs = m.data.Jobs[:1]
	m.data.Jobs[0].UID = strings.Repeat("identity", 12)
	m.data.Jobs[0].Definition = json.RawMessage(`{"action":{"kind":"execute","prompt":"Instructions: long prose must wrap across several lines in this inspector without becoming a marquee value"}}`)
	m.rebuildJobIndex()
	m.rebuild()
	m.data.Jobs[0].Reasons = []string{"Instructions: long prose must wrap across several lines without animating"}
	m.rebuildJobIndex()
	m.rebuild()
	m.frame = 7
	b := m.renderInspector(32)
	if len(b) < 6 {
		t.Fatal("prose did not wrap", b)
	}
	foundProse := false
	for _, line := range b {
		text := ansi.Strip(line.Text)
		if strings.Contains(text, "identity") {
			t.Fatal("inspector exposed identifier", text)
		}
		if strings.Contains(text, "Instructions:") {
			foundProse = true
			if len(line.Animations) > 0 {
				t.Fatal("prose animated", line)
			}
		}
	}
	if !foundProse {
		t.Fatal(foundProse, b)
	}
}

func TestExpandedRunStepProjectAlignmentAndColumnWidth(t *testing.T) {
	m, _ := fixture(t)
	m.tab = 1
	m.width = 120
	m.opts.Projects = map[string]*Lane{"example-project": {}, "other-project": {}}
	m.data.Projects = map[string]Data{"p": {Project: "example-project"}}
	m.data.Runs[0].Target.Identity.ProjectUID = "p"
	m.expanded["r1"] = true
	m.data.Runs[0].Steps[0].Status = "failed"
	m.data.Runs[0].Steps[0].Reason = strings.Repeat("long reason ", 30)
	m.rebuildJobIndex()
	m.rebuild()
	m.frame = 7
	m.frameValid = false
	frame := m.renderFrame()
	lines := strings.Split(ansi.Strip(frame.Text), "\n")
	step := lines[5]
	if !strings.HasPrefix(step, "      ✗ build") || !strings.Contains(step, "ailed (long reas") || ansi.StringWidth(step) > 120 {
		t.Fatal(step)
	}
	for y, hit := range frame.Hits.Rows {
		if hit.Key == "r1/build" || y == 5 {
			t.Fatal("step hit displaced", frame.Hits)
		}
	}
}

func TestQuotaResetReasonAppearsOnce(t *testing.T) {
	m, _ := fixture(t)
	m.tab = 1
	m.width = 200
	reset := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	m.data.Runs[0].Phase = runner.Held
	m.data.Runs[0].Reason = "agent_quota"
	m.data.Runs[0].ActiveStep = "build"
	m.data.Runs[0].Steps[0].Limit = &agentlimit.Limit{ResetsAt: &reset}
	m.rebuildJobIndex()
	m.rebuild()
	m.frameValid = false
	text := ansi.Strip(m.viewContent())
	if strings.Count(text, "agent_quota") != 1 || !strings.Contains(text, "resets in") {
		t.Fatal(text)
	}
}

func TestTabsSubsetAndHits(t *testing.T) {
	labels := []string{"kata カタ", "jobs", "runs", "workflows"}
	for _, width := range []int{30, 42, 80} {
		s, hits := renderTabs(labels, 3, 60, width)
		lines := strings.Split(ansi.Strip(s), "\n")
		if len(lines) != 3 || !strings.Contains(lines[1], "workflows") {
			t.Fatal(s)
		}
		for _, s := range lines {
			if ansi.StringWidth(s) > width {
				t.Fatal(s)
			}
		}
		for _, h := range hits {
			if !strings.Contains(ansi.Cut(lines[1], h.X0, h.X1), labels[h.Index]) {
				t.Fatal(h, s)
			}
		}
	}
}
func TestTabStatusCompaction(t *testing.T) {
	counts := []tabCount{{Running: 4, Held: 2}, {Running: 2, Held: 1, Project: "example-project"}, {Running: 2, Held: 1, Project: "other-project"}}
	for _, w := range []int{120, 80, 42, 30} {
		s := ansi.Strip(tabStatus(tabInfoData{Counts: counts, Problem: "stopped"}, w))
		if !strings.Contains(s, "stopped") || ansi.StringWidth(s) > w {
			t.Fatal(s)
		}
	}
}

func TestTabStatusSeparatesCounts(t *testing.T) {
	got := ansi.Strip(tabStatus(tabInfoData{Counts: []tabCount{{Running: 1, Held: 2}}}, 120))
	if got != "1 running · 2 parked" {
		t.Fatal(got)
	}
}
func TestFooterConditionsAndSearch(t *testing.T) {
	if len(footer(statusNotice{}, nil, searchState{})) != 0 {
		t.Fatal("empty footer")
	}
	got := footer(statusNotice{Text: "error: failed", Kind: statusError}, []string{"signing blocked: sign-key refresh"}, searchState{Query: "word", Editing: true})
	var lines []string
	for _, l := range got {
		lines = append(lines, ansi.Strip(l.Text))
	}
	want := []string{"", "error: signing blocked: sign-key refresh", "error: failed", "", "/word▏  enter keep · esc clear"}
	if !reflect.DeepEqual(lines, want) {
		t.Fatal(lines)
	}
}
func TestHelpPriority(t *testing.T) {
	h := footerHelp{Full: []helpItem{{Key: "long", Desc: "a very long description"}}, Short: []helpItem{{Key: "x", Desc: "extra", Prio: 0}, {Key: "q", Desc: "quit", Prio: 1}, {Key: "M", Desc: "mouse", Prio: 1}}}
	s := ansi.Strip(h.Render(18))
	if !strings.Contains(s, "q quit") || !strings.Contains(s, "M mouse") || strings.Contains(s, "extra") {
		t.Fatal(s)
	}
	if ansi.StringWidth(h.Render(3)) > 3 {
		t.Fatal("help overflow")
	}
}
func TestPager(t *testing.T) {
	a, b, p, ps := pager(0, 0, 3)
	if a != 0 || b != 0 || p != 1 || ps != 1 {
		t.Fatal(a, b, p, ps)
	}
	a, b, p, ps = pager(10, 8, 3)
	if a != 6 || b != 9 || p != 3 || ps != 4 {
		t.Fatal(a, b, p, ps)
	}
	if ansi.Strip(pageLabel(1, 1)) != "  page 1/1" {
		t.Fatal(pageLabel(1, 1))
	}
}
func TestInspector(t *testing.T) {
	for _, w := range []int{9, 7} {
		b := inspectorKV("state", "abcdefghij", w+5, 0, w)
		if got := ansi.Strip(b[0].Text); got != "state"+strings.Repeat(" ", w-5)+" abcd" {
			t.Fatal(got)
		}
	}
}
func TestInspectorMarquee(t *testing.T) {
	a := inspectorLine("abcdefghij", 4, 0, titleStyle)
	b := inspectorLine("abcdefghij", 4, 7, titleStyle)
	if ansi.Strip(a.Text) != "abcd" || ansi.Strip(b.Text) != "bcde" || !reflect.DeepEqual(a.Animations, []animationSpan{{0, 4}}) {
		t.Fatal(a, b)
	}
}
func TestInspectorKVCells(t *testing.T) {
	for _, text := range []string{"", "  ", "\u2003\t", "\x1b[31m \x00"} {
		if b := inspectorKVCells("state", []cell{{Text: text}}, 15, 7); len(b) != 0 {
			t.Fatal(b)
		}
	}
	for _, text := range []string{"abcdefghijklmnop", "你好世界abcdef", "e\u0301abcdef long"} {
		runes := []rune(text)
		for frame := range 35 {
			parts := []cell{{Text: string(runes[:2]), Style: attentionStyle}, {Text: string(runes[2:]), Style: subtleStyle}}
			got := inspectorKVCells("state", parts, 15, frame, 7)
			want := inspectorKV("state", text, 15, frame, 7)
			if ansi.Strip(got[0].Text) != ansi.Strip(want[0].Text) || !reflect.DeepEqual(got[0].Animations, want[0].Animations) {
				t.Fatal(got, want)
			}
		}
	}
	if got := ansi.Strip(inspectorKV("x", " a  b ", 20, 0)[0].Text); !strings.Contains(got, " a  b ") {
		t.Fatal(got)
	}
}
func TestBeside(t *testing.T) {
	left := bodyLines{{Text: "abcdefgh", Selected: true, Hit: &rowHit{Key: "row", X0: 0, X1: 8}, Animations: []animationSpan{{2, 7}, {8, 10}}}}
	right := bodyLines{{Text: "xyz", Hit: &rowHit{Key: "inspector", X0: 0, X1: 3}, Animations: []animationSpan{{0, 3}}}}
	got := beside(left, right, 4, 3)
	if !got[0].Selected || ansi.Strip(got[0].Text) != "abcd   xyz" || got[0].Hit.X1 != 4 || !reflect.DeepEqual(got[0].Animations, []animationSpan{{2, 4}, {7, 10}}) {
		t.Fatal(got)
	}
	if left[0].Hit.X1 != 8 {
		t.Fatal("mutated caller")
	}
	if got := beside(nil, right, 4, 3); got[0].Hit != nil {
		t.Fatal("inspector hit")
	}
}

func TestTabStatusHidesZeroCounts(t *testing.T) {
	for _, tc := range []struct {
		counts []tabCount
		want   string
	}{
		{[]tabCount{{}}, ""},
		{[]tabCount{{Running: 1}}, "1 running"},
		{[]tabCount{{Held: 1}}, "1 parked"},
		{[]tabCount{{Running: 1, Held: 2}}, "1 running · 2 parked"},
		{[]tabCount{{Running: 1}, {Running: 1, Project: "example-project"}, {Project: "idle-project"}}, "1 running · example-project: 1 running"},
	} {
		if got := ansi.Strip(tabStatus(tabInfoData{Counts: tc.counts}, 150)); got != tc.want {
			t.Fatalf("got %q, want %q", got, tc.want)
		}
	}
}

func TestTabStatusPriority(t *testing.T) {
	for _, tc := range []struct {
		width int
		info  tabInfoData
		want  string
	}{
		{80, tabInfoData{Counts: []tabCount{{Running: 1, Project: "example-project"}, {Held: 2, Project: "other-project"}}}, "example-project: 1 running · other-project: 2 parked"},
		{5, tabInfoData{Counts: []tabCount{{Running: 1, Project: "example-project"}, {Held: 2, Project: "other-project"}}}, "1r/2p"},
		{2, tabInfoData{Counts: []tabCount{{Running: 1, Held: 2}}}, "2p"},
		{15, tabInfoData{Counts: []tabCount{{Running: 1, Held: 2}}, Problem: "runtime stopped"}, "runtime stopped"},
	} {
		if got := ansi.Strip(tabStatus(tc.info, tc.width)); got != tc.want {
			t.Fatalf("width %d: %q, want %q", tc.width, got, tc.want)
		}
	}
}

func TestLegacyTableHeadingsAndPinnedChrome(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode colorMode
	}{{"light", colorLight}, {"dark", colorDark}, {"none", colorNone}} {
		t.Run(tc.name, func(t *testing.T) {
			preserveTheme(t)
			applyColorMode(tc.mode, true)
			m, _ := fixture(t)
			m.width = 180
			view := ansi.Strip(uncachedView(m))
			for _, heading := range []string{"issue", "status", "job", "model", "schedule", "updated"} {
				if !strings.Contains(view, heading) {
					t.Fatal("missing list column", heading)
				}
			}
			send(m, "l")
			send(m, "]")
			// Key and description styles insert ANSI between visible words.
			view = ansi.Strip(uncachedView(m))
			if strings.Contains(view, "│ Jobs │") || !strings.Contains(view, "q quit") {
				t.Fatal("chrome scrolled away", view)
			}
		})
	}
}

func TestBoardShowsAccessLossAndSavedSigningRefusal(t *testing.T) {
	for _, reason := range []string{"access_lost", "signing_blocked"} {
		t.Run(reason, func(t *testing.T) {
			m, _ := fixture(t)
			lane := cacheTestLane(t, m.opts.Root)
			if reason == "access_lost" {
				if err := state.WriteJSON(state.OSFS{}, lane.RouteFile(), map[string]any{"allowed": false, "access_lost": true, "error": "access_lost: example-project"}); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := state.WriteJSON(state.OSFS{}, lane.RouteFile(), map[string]any{"allowed": true, "project": "example-project"}); err != nil {
					t.Fatal(err)
				}
				if err := state.WriteJSON(state.OSFS{}, filepath.Join(m.opts.Root, "runs", "signed", "run.json"), runner.Run{Version: 1, UID: "signed", Target: runner.Target{Identity: kata.Identity{ProjectUID: "project"}}, Phase: runner.Prepared, Ops: []runner.Op{{ID: "create", Kind: "create", Payload: json.RawMessage(`{}`), SigningRefused: true}}}); err != nil {
					t.Fatal(err)
				}
			}
			d, err := m.cache.Poll()
			if err != nil {
				t.Fatal(err)
			}
			m.Update(dataMsg{data: d})
			if !strings.Contains(uncachedView(m), reason) || reason == "signing_blocked" && !strings.Contains(uncachedView(m), "--project example-project") {
				t.Fatal("route problem absent from board", uncachedView(m))
			}
		})
	}
}

// Sidebar counts are plain text; the header classifies the same run phases.
func TestCountsEveryPhase(t *testing.T) {
	for _, tc := range []struct {
		phase         runner.Phase
		want          string
		running, held int
	}{
		{runner.Prepared, "", 0, 0},
		{runner.Admitted, "1 running", 1, 0},
		{runner.Running, "1 running", 1, 0},
		{runner.Blocked, "1 parked", 0, 1},
		{runner.Held, "1 parked", 0, 1},
		{runner.HandedOff, "", 0, 0},
		{runner.Settled, "", 0, 0},
		{runner.Phase("unknown"), "", 0, 0},
	} {
		t.Run(string(tc.phase), func(t *testing.T) {
			runs := []runner.Run{{Phase: tc.phase}}
			got := Counts(Data{Runs: runs})
			if got != tc.want || got != ansi.Strip(got) {
				t.Fatalf("Counts = %q, want plain %q", got, tc.want)
			}
			if counts := countRuns(runs); counts.Running != tc.running || counts.Held != tc.held {
				t.Fatalf("phase %s classified as %+v", tc.phase, counts)
			}
		})
	}
	if got := Counts(Data{Runs: []runner.Run{{Phase: runner.Admitted}, {Phase: runner.Running}, {Phase: runner.Held}, {Phase: runner.Blocked}, {Phase: runner.Settled}}}); got != "2 running · 2 parked" {
		t.Fatal(got)
	}
	if got := Counts(Data{}); got != "" {
		t.Fatal(got)
	}
}
