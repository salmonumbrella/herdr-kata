package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/salmonumbrella/herdr-kata/internal/config"
	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type controlledPoll struct {
	calls, active, peak atomic.Int32
	entered             chan struct{}
	release             chan struct{}
	data                Data
	err                 error
}

func (p *controlledPoll) Poll() (Data, error) {
	p.calls.Add(1)
	n := p.active.Add(1)
	for old := p.peak.Load(); n > old; old = p.peak.Load() {
		if p.peak.CompareAndSwap(old, n) {
			break
		}
	}
	p.entered <- struct{}{}
	<-p.release
	p.active.Add(-1)
	return p.data, p.err
}
func TestU1PollCoalesces(t *testing.T) {
	m := newModel(Options{Root: t.TempDir()})
	p := &controlledPoll{entered: make(chan struct{}, 2), release: make(chan struct{}, 2)}
	m.cache = p
	first := m.requestPoll()
	result := make(chan tea.Msg, 1)
	go func() { result <- first() }()
	<-p.entered
	if m.requestPoll() != nil {
		t.Fatal("overlapping poll")
	}
	for range 5 {
		if c := m.routeKey(press("r")); c != nil {
			t.Fatal("refresh launched during poll")
		}
	}
	if p.calls.Load() != 1 || !m.pollAgain {
		t.Fatal("refresh not coalesced")
	}
	p.release <- struct{}{}
	_, follow := m.Update(<-result)
	go func() { result <- follow() }()
	<-p.entered
	p.release <- struct{}{}
	m.Update(<-result)
	if p.calls.Load() != 2 || p.peak.Load() != 1 || m.polling {
		t.Fatal("poll gate did not drain", p.calls.Load(), p.peak.Load())
	}
}
func TestU1PollErrorCompletes(t *testing.T) {
	m := newModel(Options{Root: t.TempDir()})
	m.tab = 1
	m.polling = true
	m.Update(dataMsg{data: Data{Runs: []runner.Run{{UID: "retained"}}}, err: errors.New("snapshot unavailable")})
	if !strings.Contains(m.viewContent(), "snapshot unavailable") || len(m.rows) != 1 || m.requestPoll() == nil {
		t.Fatal("partial error lost snapshot or wedged gate", m.viewContent())
	}
}
func TestU1PollPinAndTimer(t *testing.T) {
	pins := 0
	m := newModel(Options{Root: t.TempDir(), Pin: func(Data) error { pins++; return nil }})
	m.polling = true
	m.pollAgain = true
	_, cmd := m.Update(dataMsg{})
	if cmd == nil {
		t.Fatal("missing pin and queued refresh")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatal("pin or follow-up omitted")
	}
	batch[0]()
	if pins != 1 || !m.polling || m.busy {
		t.Fatal("pin polluted action state")
	}
	_, timer := m.Update(tickMsg{})
	if timer == nil {
		t.Fatal("timer chain stopped")
	}
}

type runnerActions struct {
	fakeActions
	k *kata.CLI
}

func TestNoKataRunnerCallsOverFakeHour(t *testing.T) {
	m, _ := fixture(t)
	calls := 0
	f := &runnerActions{k: kata.New(config.Kata{Bin: "/fake"}, m.opts.Root, func(_ context.Context, a, e []string, d string, in []byte) ([]byte, []byte, int, error) {
		calls++
		return []byte(`{"runs":[]}`), nil, 0, nil
	}, nil)}
	m.opts.Service = f
	if err := state.WriteJSON(state.OSFS{}, filepath.Join(m.opts.Root, "defs.json"), defs.Snapshot{Version: 2, Jobs: m.data.Jobs, Workflows: m.data.Workflows}); err != nil {
		t.Fatal(err)
	}
	for i := range 1800 {
		m.Update(tickMsg(time.Unix(int64(i*2), 0)))
		m.Update(m.poll()())
		uncachedView(m)
	}
	if calls != 0 {
		t.Fatal("idle spawned Kata", calls)
	}
	// Opening detail also reads only accepted local data.
	m.data.Jobs = []defs.JobEntry{{UID: "one", Name: "One"}}
	m.rebuildJobIndex()
	m.rebuild()
	m.frameValid = false
	send(m, "l")
	if calls != 0 {
		t.Fatal(calls)
	}
}

func TestPinRefreshDoesNotUseActionBusyState(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(fmt.Sprint(busy), func(t *testing.T) {
			m, _ := fixture(t)
			m.busy, m.notice.Text = busy, "action status"
			calls := 0
			m.opts.Pin = func(d Data) error {
				calls++
				if len(d.Jobs) != len(m.data.Jobs) {
					t.Fatal("pin got stale data")
				}
				return errors.New("pin unavailable")
			}
			_, cmd := m.Update(dataMsg{data: m.data})
			if cmd == nil {
				t.Fatal("pin refresh skipped")
			}
			m.Update(cmd())
			if calls != 1 || m.busy != busy || m.notice.Text != "action status" {
				t.Fatalf("pin affected action: calls=%d busy=%v status=%q", calls, m.busy, m.notice.Text)
			}
		})
	}
}
