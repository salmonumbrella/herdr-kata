package tui

import (
	"fmt"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/kata"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
)

type jobsCountingPoll struct {
	calls int
	data  Data
}

func (p *jobsCountingPoll) Poll() (Data, error) { p.calls++; return p.data, nil }
func performanceJobs(t testing.TB) *model {
	t.Helper()
	m := newModel(Options{Root: t.TempDir(), Now: func() time.Time { return runNow }})
	m.width, m.height = 200, 50
	for i := range 500 {
		uid := fmt.Sprintf("job-%03d", i)
		m.data.Jobs = append(m.data.Jobs, indexJob("p", uid, "Example scheduled work "+uid))
	}
	for i := range 2000 {
		m.data.Runs = append(m.data.Runs, runner.Run{UID: fmt.Sprintf("run-%04d", i), Target: runner.Target{Identity: kata.Identity{ProjectUID: "p"}}, Job: kata.Job{UID: fmt.Sprintf("job-%03d", i%500)}, Phase: runner.Held})
	}
	m.rebuildJobIndex()
	m.rebuild()
	return m
}
func TestJobsNoReadDuringFrames(t *testing.T) {
	m := performanceJobs(t)
	p := &jobsCountingPoll{data: m.data}
	m.cache = p
	actions := &fakeActions{}
	m.opts.Service = actions
	index := m.jobs
	memo := index.jobMemo[jobKey{"p", "job-000"}]
	m.Update(struct{}{})
	for range 40 {
		m.viewContent()
		m.viewContent()
		m.Update(marqueeTickMsg{generation: m.marqueeGeneration})
	}
	m.pushDetail(viewJob, "p", "job-000")
	for range 20 {
		m.Update(struct{}{})
		m.viewContent()
	}
	if p.calls != 0 || len(actions.calls) != 0 || m.jobs != index || m.jobs.jobMemo[jobKey{"p", "job-000"}] != memo {
		t.Fatal("frame performed background work", p.calls, actions.calls)
	}
	m.Update(dataMsg{data: m.data})
	if m.jobs == index || m.jobs.jobMemo[jobKey{"p", "job-000"}] != memo {
		t.Fatal("unchanged poll did not preserve decode memo")
	}
	m.data.Jobs[0].DefinitionEventUID = "changed"
	m.Update(dataMsg{data: m.data})
	if m.jobs.jobMemo[jobKey{"p", "job-000"}] == memo {
		t.Fatal("changed event reused an old decode")
	}
}
func BenchmarkJobsRebuild(b *testing.B) {
	m := performanceJobs(b)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		m.rebuildJobIndex()
		m.rebuild()
	}
}
func BenchmarkJobsView(b *testing.B) {
	m := performanceJobs(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		m.frame = i
		m.frameValid = false
		m.viewContent()
	}
}

func BenchmarkRender500Jobs2000Runs(b *testing.B) {
	m, _ := fixture(b)
	for i := len(m.data.Jobs); i < 500; i++ {
		j := m.data.Jobs[0]
		j.UID = fmt.Sprintf("job-%d", i)
		j.Name = fmt.Sprintf("Job %d", i)
		m.data.Jobs = append(m.data.Jobs, j)
	}
	for i := len(m.data.Runs); i < 2000; i++ {
		r := m.data.Runs[0]
		r.UID = fmt.Sprintf("run-%d", i)
		r.Job.UID = m.data.Jobs[i%500].UID
		m.data.Runs = append(m.data.Runs, r)
	}
	m.rebuildJobIndex()
	m.rebuild()
	m.frameValid = false
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.cursor = i % 500
		uncachedView(m)
	}
}
