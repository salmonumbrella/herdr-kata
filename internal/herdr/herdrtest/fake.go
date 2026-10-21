// Package herdrtest provides an in-process protocol-22 server. It never starts
// processes or connects to a real Herdr session.
package herdrtest

import (
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/herdr"
)

type Start string

const (
	Ready          Start = "ready"
	PendingReady   Start = "pending-ready"
	PendingBlocked Start = "pending-blocked"
	PendingExit    Start = "pending-exit"
	NameTaken      Start = "name-taken"
	DropStart      Start = "drop-start"
)

type Outcome string

const (
	Idle    Outcome = "idle"
	Blocked Outcome = "blocked"
	Stalled Outcome = "stalled"
	Timeout Outcome = "timeout"
	Drop    Outcome = "drop"
	Working Outcome = "working"
)

// Script controls one named agent. Get states advance on reads, then retain
// the last state. Prompt and Wait outcomes are consumed once per request.
type Script struct {
	Start              Start
	Get                []herdr.Agent
	Prompt, Wait       []Outcome
	SessionAfterPrompt *herdr.AgentSession
}
type Request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}
type occupant struct {
	herdr.Agent
	launch Start
}

type Server struct {
	Path string
	// BeforeRequest can advance a scripted lifecycle before dispatch. Set it
	// before making requests; it runs outside the server's state lock.
	BeforeRequest func(Request)

	// Configure these compatibility toggles before making any requests.
	OmitCompletionSeq, OmitSession bool
	mu                             sync.Mutex
	lifecycle                      sync.Mutex
	wg                             sync.WaitGroup
	listener                       net.Listener
	conns                          map[net.Conn]bool
	changed                        chan struct{}
	log                            []Request
	scripts                        map[string]Script
	errors                         map[string][]herdr.Error
	agents                         map[string]occupant
	workspaces                     map[string]herdr.WorkspaceInfo
	tabs                           map[string]herdr.TabInfo
	panes                          map[string]herdr.PaneInfo
	reads                          map[string]string
	next                           int
}

// SocketPath allocates a short Unix socket path, independent of TMPDIR and
// the test name. The directory is removed after the test's other cleanups.
func SocketPath(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "herdr.sock")
}

func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{Path: SocketPath(t), changed: make(chan struct{}), scripts: make(map[string]Script), errors: make(map[string][]herdr.Error)}
	s.reset()
	if err := s.listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}
func (s *Server) reset() {
	s.agents = make(map[string]occupant)
	s.workspaces = make(map[string]herdr.WorkspaceInfo)
	s.tabs = make(map[string]herdr.TabInfo)
	s.panes = make(map[string]herdr.PaneInfo)
	s.reads = make(map[string]string)
	s.conns = make(map[net.Conn]bool)
}
func (s *Server) listen() error {
	ln, err := net.Listen("unix", s.Path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()
	s.wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			if s.listener != ln {
				s.mu.Unlock()
				c.Close()
				continue
			}
			s.conns[c] = true
			s.wg.Add(1)
			s.mu.Unlock()
			go s.serve(c)
		}
	})
	return nil
}
func (s *Server) stop() {
	s.mu.Lock()
	if s.listener != nil {
		s.listener.Close()
		s.listener = nil
	}
	for c := range s.conns {
		c.Close()
	}
	s.signal()
	s.mu.Unlock()
	s.wg.Wait()
}
func (s *Server) Close() { s.lifecycle.Lock(); defer s.lifecycle.Unlock(); s.stop() }

// Restart keeps the old socket inode allocated until the replacement is bound,
// guaranteeing a new inode even on filesystems that immediately reuse inodes.
// It clears occupants and topology and allocates a new range of opaque IDs.
func (s *Server) Restart() error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	old := s.Path + ".old"
	s.mu.Lock()
	if ln, ok := s.listener.(*net.UnixListener); ok {
		ln.SetUnlinkOnClose(false)
	}
	err := os.Rename(s.Path, old)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.stop()
	s.mu.Lock()
	s.reset()
	s.next += 100
	s.mu.Unlock()
	err = s.listen()
	removeErr := os.Remove(old)
	if err != nil {
		return err
	}
	return removeErr
}
func (s *Server) signal() { close(s.changed); s.changed = make(chan struct{}) }
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]Request(nil), s.log...)
	for i := range out {
		out[i].Params = append(json.RawMessage(nil), out[i].Params...)
	}
	return out
}
func (s *Server) Script(name string, script Script) {
	// JSON cloning also isolates nested optional session and sequence pointers.
	b, _ := json.Marshal(script)
	var copy Script
	_ = json.Unmarshal(b, &copy)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scripts[name] = copy
}
func (s *Server) Fail(method string, errs ...herdr.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errors[method] = append(s.errors[method], errs...)
}

// SetAgent replaces the pane occupant or advances live state, waking waits.
// RemoveAgent models an exit.
func (s *Server) SetAgent(a herdr.Agent) {
	b, _ := json.Marshal(a)
	var copy herdr.Agent
	_ = json.Unmarshal(b, &copy)
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, old := range s.agents {
		if old.PaneID == a.PaneID && name != a.Name {
			s.dropAgent(name)
		}
	}
	s.agents[a.Name] = occupant{Agent: copy}
	s.signal()
}
func (s *Server) RemoveAgent(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropAgent(name)
}

// dropAgent is called with mu held and wakes waits on removed occupants.
func (s *Server) dropAgent(name string) {
	delete(s.agents, name)
	s.signal()
}

func (s *Server) SetRead(target, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads[target] = text
}

// SetPane and SetTab model changed provenance, additional panes and foreign work.
func (s *Server) SetPane(p herdr.PaneInfo) {
	b, _ := json.Marshal(p)
	var copy herdr.PaneInfo
	_ = json.Unmarshal(b, &copy)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.panes[p.PaneID] = copy
}
func (s *Server) SetTab(t herdr.TabInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tabs[t.TabID] = t
}
func (s *Server) serve(c net.Conn) {
	defer s.wg.Done()
	defer func() { c.Close(); s.mu.Lock(); delete(s.conns, c); s.mu.Unlock() }()
	var req Request
	if err := json.NewDecoder(c).Decode(&req); err != nil {
		return
	}
	if s.BeforeRequest != nil {
		s.BeforeRequest(req)
	}
	s.mu.Lock()
	s.log = append(s.log, req)
	var result any
	var apiErr *herdr.Error
	drop := false
	if es := s.errors[req.Method]; len(es) > 0 {
		e := es[0]
		apiErr = &e
		s.errors[req.Method] = es[1:]
	} else {
		result, apiErr, drop = s.dispatch(req)
	}
	s.mu.Unlock()
	if drop {
		return
	}
	if result == waitMarker {
		result, apiErr = s.wait(c, req)
	}
	response := map[string]any{"id": req.ID}
	if apiErr != nil {
		response["error"] = apiErr
	} else {
		response["result"] = result
	}
	_ = json.NewEncoder(c).Encode(response)
}

const waitMarker = "wait"

func apiError(code string) *herdr.Error { return &herdr.Error{Code: code, Message: "scripted " + code} }
func (s *Server) find(target string) (herdr.Agent, bool) {
	if a, ok := s.agents[target]; ok {
		return a.Agent, true
	}
	for _, a := range s.agents {
		if a.PaneID == target {
			return a.Agent, true
		}
	}
	return herdr.Agent{}, false
}
func (s *Server) agentWire(a herdr.Agent) map[string]any {
	// Include the actual required AgentInfo fields rather than a synthetic DTO.
	v := map[string]any{"name": a.Name, "pane_id": a.PaneID, "terminal_id": a.TerminalID, "workspace_id": a.WorkspaceID, "tab_id": a.TabID, "focused": false, "revision": 1, "agent_status": a.Status, "interactive_ready": a.InteractiveReady, "launch_pending": a.LaunchPending}
	if !s.OmitSession {
		v["agent_session"] = a.Session
	}
	if !s.OmitCompletionSeq {
		v["completion_seq"] = a.CompletionSeq
	}
	return v
}
func (s *Server) agentResult(typ string, a herdr.Agent) map[string]any {
	return map[string]any{"type": typ, "agent": s.agentWire(a)}
}
func (s *Server) newTab(ws, label string) (herdr.TabInfo, herdr.PaneInfo) {
	s.next++
	tab := herdr.TabInfo{TabID: fmt.Sprintf("%s:t%d", ws, s.next), WorkspaceID: ws, Label: label, Number: uint64(s.next), PaneCount: 1, Status: "idle"}
	pane := herdr.PaneInfo{PaneID: fmt.Sprintf("%s:p%d", ws, s.next), TerminalID: fmt.Sprintf("term%d", s.next), WorkspaceID: ws, TabID: tab.TabID, Status: "idle", Revision: 1}
	s.tabs[tab.TabID] = tab
	s.panes[pane.PaneID] = pane
	w := s.workspaces[ws]
	w.TabCount++
	w.PaneCount++
	w.ActiveTabID = tab.TabID
	s.workspaces[ws] = w
	return tab, pane
}
func (s *Server) dispatch(req Request) (any, *herdr.Error, bool) {
	var p struct {
		WorkspaceID                             string `json:"workspace_id"`
		TabID                                   string `json:"tab_id"`
		PaneID                                  string `json:"pane_id"`
		Name, Kind, Target, Text, Label, Source string
		Tokens                                  map[string]string `json:"tokens"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return nil, apiError("invalid_params"), false
	}
	ok := map[string]any{"type": "ok"}
	switch req.Method {
	case "ping":
		return map[string]any{"type": "pong", "version": "0.9.1", "protocol": 22}, nil, false
	case "workspace.create":
		s.next++
		id := fmt.Sprintf("w%d", s.next)
		s.workspaces[id] = herdr.WorkspaceInfo{WorkspaceID: id, Number: uint64(s.next), Label: p.Label, Status: "idle"}
		tab, pane := s.newTab(id, p.Label)
		return map[string]any{"type": "workspace_created", "workspace": s.workspaces[id], "tab": tab, "root_pane": pane}, nil, false
	case "workspace.get":
		w, found := s.workspaces[p.WorkspaceID]
		if !found {
			return nil, apiError("workspace_not_found"), false
		}
		return map[string]any{"type": "workspace_info", "workspace": w}, nil, false
	case "workspace.list":
		list := []herdr.WorkspaceInfo{}
		for _, w := range s.workspaces {
			list = append(list, w)
		}
		sort.Slice(list, func(i, j int) bool { return list[i].WorkspaceID < list[j].WorkspaceID })
		return map[string]any{"type": "workspace_list", "workspaces": list}, nil, false
	case "workspace.close":
		if _, found := s.workspaces[p.WorkspaceID]; !found {
			return nil, apiError("workspace_not_found"), false
		}
		for id, tab := range s.tabs {
			if tab.WorkspaceID == p.WorkspaceID {
				delete(s.tabs, id)
			}
		}
		for id, pane := range s.panes {
			if pane.WorkspaceID == p.WorkspaceID {
				delete(s.panes, id)
			}
		}
		for name, a := range s.agents {
			if a.WorkspaceID == p.WorkspaceID {
				s.dropAgent(name)
			}
		}
		delete(s.workspaces, p.WorkspaceID)
		return ok, nil, false
	case "tab.create":
		if _, found := s.workspaces[p.WorkspaceID]; !found {
			return nil, apiError("workspace_not_found"), false
		}
		tab, pane := s.newTab(p.WorkspaceID, p.Label)
		return map[string]any{"type": "tab_created", "tab": tab, "root_pane": pane}, nil, false
	case "tab.get":
		tab, found := s.tabs[p.TabID]
		if !found {
			return nil, apiError("tab_not_found"), false
		}
		return map[string]any{"type": "tab_info", "tab": tab}, nil, false
	case "tab.close":
		tab, found := s.tabs[p.TabID]
		if !found {
			return nil, apiError("tab_not_found"), false
		}
		for id, pane := range s.panes {
			if pane.TabID == p.TabID {
				delete(s.panes, id)
			}
		}
		for name, a := range s.agents {
			if a.TabID == p.TabID {
				s.dropAgent(name)
			}
		}
		delete(s.tabs, p.TabID)
		w := s.workspaces[tab.WorkspaceID]
		w.TabCount--
		w.PaneCount -= tab.PaneCount
		s.workspaces[w.WorkspaceID] = w
		s.signal()
		return ok, nil, false
	case "pane.send_input":
		if _, found := s.panes[p.PaneID]; !found {
			return nil, apiError("pane_not_found"), false
		}
		return ok, nil, false
	case "pane.get":
		pane, found := s.panes[p.PaneID]
		if !found {
			return nil, apiError("pane_not_found"), false
		}
		if a, found := s.find(p.PaneID); found {
			pane.Status = a.Status
			pane.Session = a.Session
		}
		if s.OmitSession {
			pane.Session = nil
		}
		pane.Tokens = maps.Clone(pane.Tokens)
		return map[string]any{"type": "pane_info", "pane": pane}, nil, false
	case "pane.process_info":
		if _, found := s.panes[p.PaneID]; !found {
			return nil, apiError("pane_not_found"), false
		}
		shell, fg := uint32(101), uint32(101)
		name := "sh"
		if _, found := s.find(p.PaneID); found {
			fg = 202
			name = "codex"
		}
		return map[string]any{"type": "pane_process_info", "process_info": herdr.ProcessInfo{PaneID: p.PaneID, ShellPID: &shell, ForegroundProcessGroupID: &fg, ForegroundProcesses: []herdr.Process{{PID: fg, Name: name}}}}, nil, false
	case "pane.report_metadata":
		pane, found := s.panes[p.PaneID]
		if !found {
			return nil, apiError("pane_not_found"), false
		}
		if p.Source == "" {
			return nil, apiError("invalid_params"), false
		}
		if pane.Tokens == nil {
			pane.Tokens = make(map[string]string)
		}
		maps.Copy(pane.Tokens, p.Tokens)
		s.panes[p.PaneID] = pane
		return ok, nil, false
	case "agent.start":
		script := s.scripts[p.Name]
		if _, found := s.agents[p.Name]; found || script.Start == NameTaken {
			return nil, apiError("agent_name_taken"), false
		}
		pane, found := s.panes[p.PaneID]
		if !found {
			return nil, apiError("pane_not_found"), false
		}
		if _, found := s.find(p.PaneID); found {
			return nil, apiError("agent_pane_busy"), false
		}
		seq := uint64(0)
		a := herdr.Agent{Name: p.Name, PaneID: p.PaneID, TerminalID: pane.TerminalID, WorkspaceID: pane.WorkspaceID, TabID: pane.TabID, Status: "idle", InteractiveReady: true, CompletionSeq: &seq, Session: &herdr.AgentSession{Source: "hook", Agent: p.Kind, Kind: "id", Value: "session-" + pane.TerminalID}}
		if script.Start == PendingReady || script.Start == PendingBlocked || script.Start == PendingExit {
			a.Status = "unknown"
			a.InteractiveReady = false
			a.LaunchPending = true
		}
		s.agents[p.Name] = occupant{Agent: a, launch: script.Start}
		s.signal()
		r := s.agentResult("agent_started", a)
		r["argv"] = []string{p.Kind}
		return r, nil, script.Start == DropStart
	case "agent.get":
		a, found := s.find(p.Target)
		if !found {
			return nil, apiError("agent_not_found"), false
		}
		if pending := s.agents[a.Name].launch; pending == PendingReady || pending == PendingBlocked || pending == PendingExit {
			if pending == PendingExit {
				s.dropAgent(a.Name)
				return nil, apiError("agent_not_found"), false
			}
			a.LaunchPending = false
			a.Status = "idle"
			a.InteractiveReady = true
			if pending == PendingBlocked {
				a.Status = "blocked"
				a.InteractiveReady = false
			}
		}
		script := s.scripts[a.Name]
		if len(script.Get) > 0 {
			next := script.Get[0]
			script.Get = script.Get[1:]
			s.scripts[a.Name] = script
			next.Name = a.Name
			if next.PaneID == "" {
				next.PaneID = a.PaneID
				next.TerminalID = a.TerminalID
				next.WorkspaceID = a.WorkspaceID
				next.TabID = a.TabID
			}
			a = next
		}
		s.agents[a.Name] = occupant{Agent: a}
		s.signal()
		return s.agentResult("agent_info", a), nil, false
	case "agent.list":
		list := []map[string]any{}
		names := []string{}
		for name := range s.agents {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			list = append(list, s.agentWire(s.agents[name].Agent))
		}
		return map[string]any{"type": "agent_list", "agents": list}, nil, false
	case "agent.prompt", "agent.wait":
		a, found := s.find(p.Target)
		if !found {
			return nil, apiError("agent_not_found"), false
		}
		script := s.scripts[a.Name]
		out := Outcome("")
		typ := "agent_info"
		if req.Method == "agent.prompt" {
			typ = "agent_prompted"
			if a.Status == "blocked" {
				return nil, apiError("agent_blocked"), false
			}
			if !a.InteractiveReady {
				return nil, apiError("agent_not_ready"), false
			}
			out = Idle
			if len(script.Prompt) > 0 {
				out = script.Prompt[0]
				script.Prompt = script.Prompt[1:]
			}
			if script.SessionAfterPrompt != nil {
				a.Session = script.SessionAfterPrompt
			}
		} else if len(script.Wait) > 0 {
			out = script.Wait[0]
			script.Wait = script.Wait[1:]
		}
		s.scripts[a.Name] = script
		if out == "" {
			return waitMarker, nil, false
		}
		a.InteractiveReady = false
		a.LaunchPending = false
		switch out {
		case Idle:
			a.Status = "idle"
			a.InteractiveReady = true
			seq := uint64(1)
			if a.CompletionSeq != nil {
				seq = *a.CompletionSeq + 1
			}
			a.CompletionSeq = &seq
		case Blocked:
			a.Status = "blocked"
		default:
			a.Status = "working"
		}
		s.agents[a.Name] = occupant{Agent: a}
		s.signal()
		if req.Method == "agent.prompt" && out == Working {
			var params struct{ Wait struct{ Until []string } }
			_ = json.Unmarshal(req.Params, &params)
			if !slices.Contains(params.Wait.Until, "working") {
				return waitMarker, nil, false
			}
		}
		switch out {
		case Stalled:
			return nil, apiError("agent_prompt_stalled"), false
		case Timeout:
			return nil, apiError("timeout"), false
		case Drop:
			return nil, nil, true
		}
		return s.agentResult(typ, a), nil, false
	case "agent.read":
		a, found := s.find(p.Target)
		if !found {
			return nil, apiError("agent_not_found"), false
		}
		return map[string]any{"type": "pane_read", "read": herdr.ReadResult{PaneID: a.PaneID, WorkspaceID: a.WorkspaceID, TabID: a.TabID, Source: p.Source, Format: "text", Text: s.reads[p.Target], Revision: 1}}, nil, false
	default:
		return nil, apiError("unknown_method"), false
	}
}
func (s *Server) wait(c net.Conn, req Request) (any, *herdr.Error) {
	var p struct {
		Target    string
		Until     []string
		TimeoutMS *uint64 `json:"timeout_ms"`
	}
	_ = json.Unmarshal(req.Params, &p)
	if len(p.Until) == 0 {
		p.Until = []string{"idle", "done", "blocked"}
	}
	var timeout <-chan time.Time
	if p.TimeoutMS != nil {
		// The client rounds nanoseconds up to milliseconds. Its maximum
		// duration therefore exceeds the timer's range after multiplication.
		duration := time.Duration(1<<63 - 1)
		if ms := *p.TimeoutMS; ms <= uint64(duration/time.Millisecond) {
			duration = time.Duration(ms) * time.Millisecond
		}
		timer := time.NewTimer(duration)
		defer timer.Stop()
		timeout = timer.C
	}
	// Connection closure cancels a wait independently of state notifications.
	gone := make(chan struct{})
	go func() { var b [1]byte; _, _ = c.Read(b[:]); close(gone) }()
	s.mu.Lock()
	original, found := s.find(p.Target)
	s.mu.Unlock()
	if !found {
		return nil, apiError("agent_not_running")
	}
	for {
		s.mu.Lock()
		a, found := s.find(p.Target)
		changed := s.changed
		closed := s.listener == nil
		if !found || a.Name != original.Name || a.TerminalID != original.TerminalID || (a.Session != nil && original.Session != nil && *a.Session != *original.Session) {
			s.mu.Unlock()
			return nil, apiError("agent_not_running")
		}
		if slices.Contains(p.Until, a.Status) {
			r := s.agentResult("agent_info", a)
			s.mu.Unlock()
			return r, nil
		}
		s.mu.Unlock()
		if closed {
			return nil, apiError("agent_not_running")
		}
		select {
		case <-changed:
		case <-timeout:
			return nil, apiError("timeout")
		case <-gone:
			return nil, apiError("agent_not_running")
		}
	}
}
