package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/salmonumbrella/herdr-kata/internal/runner"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

type artifactListing struct {
	Paths   []string
	More    int
	Missing bool
	Err     error
}

func artifactComponent(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00") && filepath.Base(name) == name
}
func scanArtifacts(fsys state.FS, root, uid string) artifactListing {
	var out artifactListing
	if !artifactComponent(uid) {
		out.Err = fmt.Errorf("invalid run identity")
		return out
	}
	root, err := filepath.Abs(root)
	if err != nil {
		out.Err = err
		return out
	}
	l := state.Layout{Root: root}
	seen := map[string]bool{}
	remember := func(err error) {
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			out.Err = errors.Join(out.Err, err)
		}
	}
	dir := func(path string) bool {
		info, e := fsys.Lstat(path)
		if path == root && errors.Is(e, fs.ErrNotExist) {
			out.Missing = true
		}
		remember(e)
		return e == nil && info.IsDir() && info.Mode()&fs.ModeSymlink == 0
	}
	add := func(path string) {
		if seen[path] {
			return
		}
		info, e := fsys.Lstat(path)
		remember(e)
		if e != nil || !info.Mode().IsRegular() {
			return
		}
		seen[path] = true
		if len(out.Paths) >= 512 {
			out.More++
			return
		}
		out.Paths = append(out.Paths, path)
	}
	read := func(path string) []fs.DirEntry {
		entries, e := fsys.ReadDir(path)
		remember(e)
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		return entries
	}
	runDir := l.RunDir(uid)
	if !dir(root) {
		return out
	}
	if !dir(filepath.Join(root, "runs")) {
		out.Missing = out.Err == nil
		if dir(filepath.Dir(l.LogFile())) {
			add(l.LogFile())
		}
		return out
	}
	info, e := fsys.Lstat(runDir)
	if errors.Is(e, fs.ErrNotExist) {
		out.Missing = true
	} else if e != nil {
		remember(e)
	} else if info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
		for _, entry := range read(runDir) {
			if artifactComponent(entry.Name()) && !entry.IsDir() {
				add(filepath.Join(runDir, entry.Name()))
			}
		}
		steps := filepath.Join(runDir, "steps")
		attemptCount := 0
		if dir(steps) {
			for _, step := range read(steps) {
				if !artifactComponent(step.Name()) {
					continue
				}
				stepDir := filepath.Join(steps, step.Name())
				if !dir(stepDir) {
					continue
				}
				for _, attempt := range read(stepDir) {
					n, e := strconv.Atoi(attempt.Name())
					if e != nil || n <= 0 || strconv.Itoa(n) != attempt.Name() {
						continue
					}
					attemptDir := l.AttemptDir(uid, step.Name(), n)
					if !dir(attemptDir) {
						continue
					}
					if attemptCount >= 64 || len(out.Paths) >= 512 {
						out.More++
						continue
					}
					attemptCount++
					for _, file := range read(attemptDir) {
						if artifactComponent(file.Name()) && !file.IsDir() {
							add(filepath.Join(attemptDir, file.Name()))
						}
					}
				}
			}
		}
	}
	if dir(filepath.Dir(l.LogFile())) {
		add(l.LogFile())
	}
	return out
}

type artifactsMsg struct {
	UID, ProjectUID string
	Seq, Generation uint64
	Listing         artifactListing
}

func (m *model) activeRunKey() string {
	if m.currentView() != viewRun {
		return ""
	}
	f := m.nav[len(m.nav)-1]
	return detailItemKey("run", f.projectUID, f.uid)
}
func artifactSet(r runner.Run) string {
	type attempt struct {
		Key     string
		Attempt int
		Receipt *runner.Receipt
	}
	set := func(steps []runner.StepState) []attempt {
		out := make([]attempt, 0, len(steps))
		for _, s := range steps {
			out = append(out, attempt{s.Key, s.Attempt, s.Receipt})
		}
		return out
	}
	data, _ := json.Marshal(struct{ Current, Past []attempt }{set(r.Steps), set(r.PastAttempts)})
	return string(data)
}
func (m *model) requestArtifacts() tea.Cmd {
	if m.currentView() != viewRun {
		return nil
	}
	f := m.nav[len(m.nav)-1]
	if _, ok := m.detailRun(); !ok {
		return nil
	}
	m.artifactSeq++
	seq, generation := m.artifactSeq, m.artifactGeneration
	root := m.opts.Root
	scanner := m.artifactScanner
	if scanner == nil {
		scanner = scanArtifacts
	}
	return func() tea.Msg {
		return artifactsMsg{UID: f.uid, ProjectUID: f.projectUID, Seq: seq, Generation: generation, Listing: scanner(state.OSFS{}, root, f.uid)}
	}
}
func (m *model) updateArtifacts(accepted bool) tea.Cmd {
	key := m.activeRunKey()
	if key != m.artifactVisit {
		m.artifactVisit = key
		m.artifactGeneration++
		m.artifactLoaded = false
		m.artifactListing = artifactListing{}
		m.artifactPolls = 0
		m.artifactSignature = ""
		m.refreshGeneration = 0
		if r, ok := m.detailRun(); ok {
			m.artifactSignature = artifactSet(r)
			return m.requestArtifacts()
		}
		return nil
	}
	if !accepted || key == "" {
		return nil
	}
	r, ok := m.detailRun()
	if !ok {
		return nil
	}
	m.artifactPolls++
	signature := artifactSet(r)
	scan := signature != m.artifactSignature || m.artifactPolls%5 == 0
	m.artifactSignature = signature
	if m.refreshGeneration > 0 && m.acceptedPoll >= m.refreshGeneration {
		scan = true
		m.refreshGeneration = 0
	}
	if scan {
		return m.requestArtifacts()
	}
	return nil
}
func (m *model) acceptArtifacts(v artifactsMsg) {
	if m.currentView() != viewRun {
		return
	}
	f := m.nav[len(m.nav)-1]
	if f.uid == v.UID && f.projectUID == v.ProjectUID && v.Seq == m.artifactSeq && v.Generation == m.artifactGeneration {
		m.artifactListing = v.Listing
		m.artifactLoaded = true
	}
}
func (m *model) artifactLines() bodyLines {
	if !m.artifactLoaded {
		return bodyLines{{Text: subtleStyle.Render("  loading artifacts…")}}
	}
	listing := m.artifactListing
	var body bodyLines
	if listing.Missing {
		body = append(body, bodyLine{Text: subtleStyle.Render("  run directory removed")})
	}
	for _, path := range listing.Paths {
		body = append(body, bodyLine{Text: "  " + subtleStyle.Render(truncateLeft(path, max(0, m.width-2)))})
	}
	if listing.Err != nil {
		for _, line := range wrapText(listing.Err.Error(), max(1, m.width-2)) {
			body = append(body, bodyLine{Text: "  " + failStyle.Render(line)})
		}
	}
	if listing.More > 0 {
		body = append(body, bodyLine{Text: subtleStyle.Render(fmt.Sprintf("  +%d more not listed", listing.More))})
	}
	if len(body) == 0 {
		body = append(body, bodyLine{Text: subtleStyle.Render("  none")})
	}
	return body
}

// artifactState is owned by this view.
type artifactState struct {
	artifactScanner    func(state.FS, string, string) artifactListing
	artifactListing    artifactListing
	artifactLoaded     bool
	artifactSeq        uint64
	artifactGeneration uint64
	artifactVisit      string
	artifactSignature  string
	artifactPolls      int
}
