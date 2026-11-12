// Package journal owns durable run identity and single-writer state.
package journal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/salmonumbrella/herdr-kata/internal/defs"
	"github.com/salmonumbrella/herdr-kata/internal/hc"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

var ErrExists = errors.New("journal already exists")

type Receipt struct {
	ExitCode           int
	StartedAt, EndedAt time.Time
}
type Attempt struct {
	Spec                                                    hc.RunSpec
	Cleanup, SettlementStatus, SettlementNote, ResultDigest string
	ExpiringAt                                              *time.Time
	TaskStarted                                             *bool
	StopReason                                              string
	Blocked                                                 bool
	BlockedSpell                                            int
	Step                                                    string
	N                                                       int
	Nonce, CoreID, Name, Address                            string
	CreatedMs                                               int64
	PID                                                     uint32
	Turn                                                    uint64 `json:",omitempty"`
	ForegroundPID                                           uint32 `json:",omitempty"`
	LaunchedAt                                              time.Time
	Status, Note, Hold                                      string
	Summary                                                 string
	Receipt                                                 *Receipt
	TurnEndedAt, IdleSince, ResultStableSince               *time.Time
}
type Run struct {
	IssueTitle                                                                string
	Actor, Executor, Detail                                                   string
	DeadlineDone, ReportDone, ReportRefused                                   bool
	ReportBody                                                                []byte
	Version                                                                   int
	Key, RunUID, Project, JobUID, Occurrence, RequestID, Phase, Hold, Outcome string
	Plan                                                                      defs.Plan
	AdmissionBody                                                             []byte
	IssueUID, IssueRef                                                        string
	ObsRev                                                                    int
	Attempts                                                                  []Attempt
	Notified                                                                  []string
	ExpiresAt                                                                 time.Time
	StartedAt, EndedAt                                                        *time.Time
}
type Store struct {
	Layout state.Layout
	mu     sync.Mutex
}

func New(l state.Layout) *Store { return &Store{Layout: l} }
func digest(s string) string    { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:])[:32] }
func RunKey(executor, project, job, occurrence string) string {
	return digest(strings.Join([]string{executor, project, job, occurrence}, "|"))
}
func ManualKey(executor, project, request string) string {
	return digest(strings.Join([]string{executor, project, "manual", request}, "|"))
}
func validKey(key string) bool { return len(key) == 32 && strings.Trim(key, "0123456789abcdef") == "" }
func validate(r Run) error {
	if r.Version != 1 || !validKey(r.Key) || len(r.RunUID) != 26 || r.Project == "" {
		return fmt.Errorf("invalid journal identity or version")
	}
	switch r.Phase {
	case "prepared", "running", "held", "settled", "reported", "released":
		return nil
	}
	return fmt.Errorf("invalid journal phase %q", r.Phase)
}
func (s *Store) Create(r Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validate(r); err != nil {
		return err
	}
	dir := s.Layout.Run(r.Key)
	parent := filepath.Dir(dir)
	if err := state.EnsureDir(parent); err != nil {
		return err
	}
	if _, err := os.Lstat(dir); err == nil {
		return ErrExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".prepare-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if err := state.WriteJSON(filepath.Join(staging, "run.json"), r); err != nil {
		return err
	}
	// The lifetime worker lock and Store mutex serialize publication. A crash
	// exposes either a complete final journal or an ignored staging directory.
	if err := os.Rename(staging, dir); err != nil {
		return err
	}
	return state.SyncDir(parent)
}
func (s *Store) Exists(key string) bool {
	if !validKey(key) {
		return false
	}
	_, err := os.Stat(s.Layout.Run(key))
	return err == nil
}
func (s *Store) Load(key string) (Run, error) {
	if !validKey(key) {
		return Run{}, fmt.Errorf("invalid run key")
	}
	var r Run
	b, err := os.ReadFile(filepath.Join(s.Layout.Run(key), "run.json"))
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(b, &r); err != nil {
		return r, err
	}
	if r.Key != key {
		return r, fmt.Errorf("journal key mismatch")
	}
	return r, validate(r)
}
func (s *Store) Update(key string, fn func(*Run) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.Load(key)
	if err != nil {
		return err
	}
	if err = fn(&r); err != nil {
		return err
	}
	if r.Key != key {
		return fmt.Errorf("journal identity changed")
	}
	if err = validate(r); err != nil {
		return err
	}
	return state.WriteJSON(filepath.Join(s.Layout.Run(key), "run.json"), r)
}
func (s *Store) Active() ([]Run, error) {
	runs, err := s.All()
	if err != nil {
		return nil, err
	}
	out := runs[:0]
	for _, r := range runs {
		if r.Phase != "reported" && r.Phase != "released" {
			out = append(out, r)
		}
	}
	return out, nil
}
func (s *Store) All() ([]Run, error) {
	entries, err := os.ReadDir(filepath.Join(s.Layout.Root, "runs"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Run
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".prepare-") {
			continue
		}
		r, err := s.Load(e.Name())
		if err != nil {
			slog.Error("skip unreadable run journal", "key", e.Name(), "error", err)
			continue
		}
		out = append(out, r)
	}
	return out, nil
}
func (s *Store) Find(ref string) (Run, error) {
	if validKey(ref) {
		return s.Load(ref)
	}
	runs, err := s.All()
	if err != nil {
		return Run{}, err
	}
	for _, r := range runs {
		if r.RunUID == ref {
			return r, nil
		}
	}
	return Run{}, os.ErrNotExist
}
