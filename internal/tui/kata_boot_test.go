package tui

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type routeWaitActions struct {
	*fakeActions
	failures int
}

func (a *routeWaitActions) KataCommand(string) (*exec.Cmd, error) {
	a.calls = append(a.calls, "kata")
	if a.failures > 0 {
		a.failures--
		return nil, errors.New("kata tab: recorded route missing a current check (maximum age 30s); wait for the runtime's next check")
	}
	return exec.Command("sh", "-c", "printf 'boot child'"), nil
}

func bootBoard(t *testing.T, failures int) (*model, *routeWaitActions) {
	t.Helper()
	a := &routeWaitActions{fakeActions: &fakeActions{}, failures: failures}
	m := newModel(Options{Root: t.TempDir(), OpenKata: true, Projects: map[string]*Lane{"example-project": {Project: "example-project", Service: a}}})
	t.Cleanup(func() { _ = m.Close() })
	m.kataRetryEvery = time.Millisecond
	m.kataRetryUntil = time.Now().Add(time.Minute)
	answerColors(t, m)
	return m, a
}

// At boot the worker's first route check can land after the TUI starts, so
// the Kata open is retried until the route evidence is current.
func TestOpenKataAtStartWaitsForRoute(t *testing.T) {
	m, a := bootBoard(t, 2)
	cmd := m.requestKata()
	for i := 0; cmd != nil && i < 20 && !strings.Contains(m.viewContent(), "boot child"); i++ {
		_, cmd = m.Update(cmd())
	}
	if strings.Join(a.calls, ",") != "kata,kata,kata" || !strings.Contains(m.viewContent(), "boot child") || !m.kataRetryUntil.IsZero() {
		t.Fatal(a.calls, m.viewContent())
	}
}

func TestOpenKataAtStartStopsOnKeyPress(t *testing.T) {
	m, a := bootBoard(t, 5)
	cmd := m.requestKata()
	_, cmd = m.Update(cmd())
	if cmd == nil || !strings.Contains(m.viewContent(), "Opening Kata once the runtime") {
		t.Fatal(m.viewContent())
	}
	send(m, "2")
	if _, retry := m.Update(cmd()); retry != nil || len(a.calls) != 1 {
		t.Fatal(a.calls)
	}
}

func TestOpenKataAtStartOpensRetryWindow(t *testing.T) {
	m := newModel(Options{Root: t.TempDir(), OpenKata: true, Projects: map[string]*Lane{"example-project": {Project: "example-project", Service: &fakeActions{}}}})
	if m.Init(); m.kataRetryUntil.IsZero() {
		t.Fatal("Init opened no retry window")
	}
	m = newModel(Options{Root: t.TempDir(), Projects: map[string]*Lane{"example-project": {Project: "example-project", Service: &fakeActions{}}}})
	if m.Init(); !m.kataRetryUntil.IsZero() {
		t.Fatal("retry window without OpenKata")
	}
}

// A refused Kata start must not trap Tab and Shift+Tab: the cycle skips the
// Kata tab and wraps, as it did before Tab reached Kata, and keeps the notice.
func TestTabWrapsPastRefusedKata(t *testing.T) {
	m, a := bootBoard(t, 100)
	m.kataRetryUntil = time.Time{}
	for _, tc := range []struct {
		key     string
		want    int
		refused bool
	}{{"4", 2, false}, {"tab", 0, true}, {"shift+tab", 2, true}, {"tab", 0, true}, {"tab", 1, false}} {
		send(m, tc.key)
		if m.kataActive || m.tab != tc.want {
			t.Fatalf("after %s: tab %d active %v, want tab %d (calls %v)", tc.key, m.tab, m.kataActive, tc.want, a.calls)
		}
		if tc.refused && !strings.Contains(m.viewContent(), "recorded route missing") {
			t.Fatalf("after %s: notice lost\n%s", tc.key, m.viewContent())
		}
	}
	if len(a.calls) != 3 {
		t.Fatal(a.calls)
	}
}

// With several projects and nothing selected, Tab into Kata asks for the
// project first. A refused start from that choice still wraps; cancelling the
// choice stays put and drops the pending wrap.
func TestTabWrapsPastRefusedKataAfterProjectChoice(t *testing.T) {
	m, _, _ := projectBoard(t)
	refusing := &routeWaitActions{fakeActions: &fakeActions{}, failures: 100}
	for _, lane := range m.opts.Projects {
		lane.Service = refusing
	}
	t.Cleanup(func() { _ = m.Close() })
	send(m, "4")
	send(m, "/")
	send(m, "nothing-matches")
	send(m, "enter")
	send(m, "tab")
	if len(m.projectChoices) == 0 {
		t.Fatal("Tab with nothing selected did not ask for a project")
	}
	send(m, "esc")
	if m.tab != 2 || m.kataActive {
		t.Fatalf("cancelled choice moved to tab %d", m.tab)
	}
	send(m, "K")
	send(m, "enter")
	if m.tab != 2 {
		t.Fatalf("K after a cancelled Tab wrapped to tab %d", m.tab)
	}
	send(m, "tab")
	send(m, "enter")
	if m.tab != 0 || m.kataActive || len(refusing.calls) != 2 {
		t.Fatalf("refused start from the project choice: tab %d active %v calls %v", m.tab, m.kataActive, refusing.calls)
	}
}
