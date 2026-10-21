package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/salmonumbrella/herdr-kata/internal/crashpoint"
	"github.com/salmonumbrella/herdr-kata/internal/state"
)

const maxRunBytes = 4 << 20

type Store struct {
	fsys   state.FS
	layout state.Layout
	locks  sync.Map
}

func NewStore(fsys state.FS, layout state.Layout) *Store { return &Store{fsys: fsys, layout: layout} }
func validUID(uid string) bool {
	return utf8.ValidString(uid) && uid != "" && uid != "." && uid != ".." && !strings.ContainsAny(uid, "/\\\x00")
}
func (s *Store) lock(uid string) *sync.Mutex {
	m, _ := s.locks.LoadOrStore(uid, &sync.Mutex{})
	return m.(*sync.Mutex)
}
func validate(r *Run, uid string) error {
	if r == nil || (r.Version != 1 && r.Version != 2) || r.UID != uid || !validUID(uid) {
		return errors.New("invalid run identity or schema")
	}
	if r.Version == 2 || r.IssueRequest != nil || strings.HasPrefix(r.Source, "issue-request:") {
		if err := validateRequestJournal(r); err != nil {
			return err
		}
	}
	switch r.Phase {
	case Prepared, Admitted, Running, Blocked, Held, HandedOff, Settled:
	default:
		return errors.New("invalid run phase")
	}
	ids := map[string]bool{}
	for _, op := range r.Ops {
		if op.ID == "" || ids[op.ID] || !json.Valid(op.Payload) || !utf8.Valid(op.Payload) || op.Attempts < 0 {
			return errors.New("invalid operation")
		}
		ids[op.ID] = true
	}
	if !validText(reflect.ValueOf(r)) {
		return errors.New("invalid UTF-8 in run")
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(data)+1 > maxRunBytes {
		return errors.New("run exceeds size limit")
	}
	return nil
}

// encoding/json replaces invalid UTF-8 silently. Reject it so saved request
// text and keys retain exactly the intent accepted by the Store.
func validText(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		return utf8.ValidString(v.String())
	case reflect.Pointer, reflect.Interface:
		return v.IsNil() || validText(v.Elem())
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() && !validText(v.Field(i)) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if !validText(v.Index(i)) {
				return false
			}
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			if !validText(k) || !validText(v.MapIndex(k)) {
				return false
			}
		}
	}
	return true
}
func (s *Store) load(uid string) (*Run, error) {
	if !validUID(uid) {
		return nil, errors.New("invalid run UID")
	}
	var r Run
	err := state.ReadJSON(s.fsys, s.layout.RunFile(uid), maxRunBytes, &r)
	if err == nil {
		if e := validate(&r, uid); e != nil {
			err = &state.CorruptError{Path: s.layout.RunFile(uid), Err: e}
		}
	}
	if _, ok := errors.AsType[*state.CorruptError](err); ok {
		if e := state.QuarantineRun(s.fsys, s.layout, uid, s.layout.RunFile(uid)); e != nil {
			return nil, errors.Join(err, e)
		}
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}
func (s *Store) Load(uid string) (*Run, error) {
	m := s.lock(uid)
	m.Lock()
	defer m.Unlock()
	return s.load(uid)
}

var errJournalBusy = errors.New("run journal busy")

// Capacity counts a busy journal as occupied instead of waiting under its
// shared mutex while a different lane proves that an occupant has stopped.
func (s *Store) tryLoad(uid string) (*Run, error) {
	m := s.lock(uid)
	if !m.TryLock() {
		return nil, errJournalBusy
	}
	defer m.Unlock()
	return s.load(uid)
}

// LoadContext bounds waiting for a journal owned by another worker activity.
func (s *Store) LoadContext(ctx context.Context, uid string) (*Run, error) {
	m := s.lock(uid)
	if err := lockControl(ctx, m); err != nil {
		return nil, err
	}
	defer m.Unlock()
	return s.load(uid)
}

func (s *Store) Create(r *Run) error {
	if r == nil {
		return errors.New("nil run")
	}
	if err := validate(r, r.UID); err != nil {
		return err
	}
	m := s.lock(r.UID)
	m.Lock()
	defer m.Unlock()
	if state.Quarantined(s.fsys, s.layout, r.UID) {
		return errors.New("run UID quarantined")
	}
	if _, err := s.fsys.Lstat(s.layout.RunFile(r.UID)); err == nil {
		return fs.ErrExist
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	crashpoint.Hit("prepared.save.before")
	err := state.WriteJSON(s.fsys, s.layout.RunFile(r.UID), r)
	if err == nil {
		crashpoint.Hit("prepared.save.after")
	}
	return err
}
func (s *Store) Transition(uid string, fn func(*Run) error) (*Run, error) {
	m := s.lock(uid)
	m.Lock()
	defer m.Unlock()
	return s.transition(uid, fn)
}

func (s *Store) TransitionContext(ctx context.Context, uid string, fn func(*Run) error) (*Run, error) {
	m := s.lock(uid)
	if err := lockControl(ctx, m); err != nil {
		return nil, err
	}
	defer m.Unlock()
	return s.transition(uid, fn)
}

func (s *Store) transition(uid string, fn func(*Run) error) (*Run, error) {
	r, err := s.load(uid)
	if err != nil {
		return nil, err
	}
	snapshot := journalSnapshot(r)
	wasPrepared := r.Phase == Prepared
	handoff := r.Phase == HandedOff
	var beforeSteps, beforeHerdr []byte
	if handoff {
		beforeSteps, _ = json.Marshal(r.Steps)
		beforeHerdr, _ = json.Marshal(r.Herdr)
	}
	frozen := map[string]string{}
	for _, op := range r.Ops {
		if op.FirstSent != nil {
			frozen[op.ID] = intent(op)
		}
	}
	if err := fn(r); err != nil {
		return nil, err
	}
	if handoff {
		steps, _ := json.Marshal(r.Steps)
		herdr, _ := json.Marshal(r.Herdr)
		if r.Phase != HandedOff || string(steps) != string(beforeSteps) || string(herdr) != string(beforeHerdr) {
			return nil, errors.New("run handed off")
		}
	}
	for id, want := range frozen {
		op, e := findOp(r, id)
		if e != nil || intent(*op) != want {
			return nil, errors.New("sent operation intent is immutable")
		}
	}
	if err := validate(r, uid); err != nil {
		return nil, err
	}
	point := journalPoint(snapshot, r)
	admissionSaved := wasPrepared && r.Phase == Admitted
	if admissionSaved {
		crashpoint.Hit("admitted.save.before")
	}
	crashpoint.Hit(point + ".save.before")
	if err := state.WriteJSON(s.fsys, s.layout.RunFile(uid), r); err != nil {
		return nil, err
	}
	crashpoint.Hit(point + ".save.after")
	if admissionSaved {
		crashpoint.Hit("admitted.save.after")
	}
	return r, nil
}
func intent(op Op) string {
	op.CommentUID = ""
	op.Attempts = 0
	op.NextAt = time.Time{}
	op.Done = false
	op.Inspect = ""
	op.Replayed = false
	op.SigningRefused = false
	op.RetryIdentical = false
	b, _ := json.Marshal(op)
	return string(b)
}

// candidates fails closed on enumeration errors; Queue uses it rather than
// relying on a potentially incomplete active index.
func (s *Store) candidates() ([]string, error) {
	entries, err := s.fsys.ReadDir(filepath.Join(s.layout.Root, "runs"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() && !state.Quarantined(s.fsys, s.layout, entry.Name()) {
			ids = append(ids, entry.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// Project membership is frozen in atomically replaced journals. Inspect it
// before waiting on a different project's in-progress transition.
func (s *Store) projectCandidates(ctx context.Context, project string) ([]string, error) {
	ids, err := s.candidates()
	if err != nil || project == "" {
		return ids, err
	}
	selected := make([]string, 0, len(ids))
	for _, uid := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if validUID(uid) {
			var snapshot Run
			e := state.ReadJSON(s.fsys, s.layout.RunFile(uid), maxRunBytes, &snapshot)
			if errors.Is(e, fs.ErrNotExist) {
				continue
			}
			if e != nil {
				var corrupt *state.CorruptError
				_, joined := e.(interface{ Unwrap() []error })
				if !errors.As(e, &corrupt) || joined {
					return nil, e
				}
			} else if validate(&snapshot, uid) == nil && snapshot.Target.Identity.ProjectUID != project {
				continue
			}
		}
		// Unknown or corrupt membership still takes the normal locked load,
		// including its existing quarantine and error handling.
		selected = append(selected, uid)
	}
	return selected, nil
}

// Active preserves the plan's convenience API but fails explicitly if state
// cannot be scanned. Admission/recovery callers should use ScanActive.
func (s *Store) Active() []string {
	ids, err := s.ScanActive()
	if err != nil {
		panic(fmt.Errorf("scan active runs: %w", err))
	}
	return ids
}

// ScanActive includes settled runs whose reports are still pending. Enumeration
// and load failures are errors, never evidence that occupied gates are free.
func (s *Store) ScanActive() ([]string, error) {
	return s.scanActive(context.Background(), "")
}

func (s *Store) scanActive(ctx context.Context, project string) ([]string, error) {
	ids, err := s.projectCandidates(ctx, project)
	if err != nil {
		return nil, err
	}
	active := []string{}
	for _, uid := range ids {
		r, err := s.LoadContext(ctx, uid)
		if errors.Is(err, fs.ErrNotExist) || s.quarantinedError(uid, err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if r.Phase != Settled {
			active = append(active, uid)
			continue
		}
		for _, op := range r.Ops {
			if !op.Done {
				active = append(active, uid)
				break
			}
		}
	}
	return active, nil
}
func findOp(r *Run, id string) (*Op, error) {
	for i := range r.Ops {
		if r.Ops[i].ID == id {
			return &r.Ops[i], nil
		}
	}
	return nil, fmt.Errorf("operation %q disappeared", id)
}

func (s *Store) quarantinedError(uid string, err error) bool {
	return JournalQuarantined(s.fsys, s.layout, uid, err)
}

// JournalQuarantined permits handling a corrupt journal as quarantined only after
// the move succeeded. Preservation failures and uncertain source probes stay fatal.
func JournalQuarantined(fsys state.FS, l state.Layout, uid string, err error) bool {
	if _, ok := errors.AsType[*state.CorruptError](err); !ok {
		return false
	}
	// A joined error includes a quarantine failure and must not be hidden.
	if _, joined := err.(interface{ Unwrap() []error }); joined {
		return false
	}
	_, e := fsys.Lstat(l.RunFile(uid))
	return errors.Is(e, fs.ErrNotExist) && state.Quarantined(fsys, l, uid)
}
