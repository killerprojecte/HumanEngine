// Package modeldiagnostics owns source Store's ephemeral, redacted model I/O.
// It never persists diagnostics or reads credentials from logs.
package modeldiagnostics

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
	"regexp"
	"sort"

	"sync"
	"time"
)

type Entry struct {
	ModelID, Role string
	Messages      []*jsjson.Object
	Output        any
}
type Row struct{ ID, At, ModelID, Role, Input, Output string }
type Ports struct {
	Enabled func() bool
	// Secrets must resolve both the credential store's redactionValues and all
	// configured providers (including environment credentials) on every capture.
	Secrets func(context.Context) ([]string, error)
	Now     func() time.Time
}
type Store struct {
	mu         sync.Mutex
	ports      Ports
	rows       []Row
	timers     map[string]*time.Timer
	work       quiescence.Group
	generation uint64
	closed     bool
}

func New(ports Ports) (*Store, error) {
	if ports.Enabled == nil || ports.Secrets == nil {
		return nil, errors.New("diagnostic_owners_required")
	}
	if ports.Now == nil {
		ports.Now = time.Now
	}
	return &Store{ports: ports, timers: map[string]*time.Timer{}}, nil
}
func (s *Store) Clear() { s.mu.Lock(); defer s.mu.Unlock(); s.clearLocked() }
func (s *Store) clearLocked() {
	s.generation++
	s.rows = nil
	for _, t := range s.timers {
		t.Stop()
	}
	s.timers = map[string]*time.Timer{}
}
func (s *Store) CloseAndWait(ctx context.Context) error {
	s.work.Seal()
	s.mu.Lock()
	s.closed = true
	s.clearLocked()
	s.mu.Unlock()
	return s.work.Wait(ctx)
}
func (s *Store) Rows() []Row { rows, _ := s.ReadRows(); return rows }
func (s *Store) ReadRows() ([]Row, error) {
	done, ok := s.work.Start()
	if !ok {
		return nil, errors.New("diagnostics_closed")
	}
	defer done()
	enabled := s.ports.Enabled()
	now := s.ports.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !enabled {
		s.clearLocked()
	}
	rows := []Row{}
	for _, r := range s.rows {
		at, e := time.Parse(time.RFC3339Nano, r.At)
		if e == nil && now.Sub(at) < 15*time.Minute {
			rows = append(rows, r)
		}
	}
	return rows, nil
}
func (s *Store) Capture(ctx context.Context, entry Entry, credentials []string) error {
	return s.CaptureLazy(ctx, func() (Entry, error) { return entry, nil }, credentials)
}

// CaptureLazy owns the complete diagnostic attempt before consulting Enabled.
// Disabled debug mode does not materialize messages/output or resolve secrets.
// Generation spans projection as well as credential reads, so Clear cannot be
// undone by work that started before disabling/re-enabling the diagnostic owner.
func (s *Store) CaptureLazy(ctx context.Context, project func() (Entry, error), credentials []string) error {
	done, ok := s.work.Start()
	if !ok {
		return errors.New("diagnostics_closed")
	}
	defer done()
	s.mu.Lock()
	generation, closed := s.generation, s.closed
	s.mu.Unlock()
	if closed {
		return errors.New("diagnostics_closed")
	}
	if !s.ports.Enabled() {
		return nil
	}
	if project == nil {
		return errors.New("diagnostic_projection_required")
	}
	entry, err := project()
	if err != nil {
		return err
	}
	secrets, err := s.ports.Secrets(ctx)
	if err != nil {
		return err
	}
	secrets = append(append([]string{}, credentials...), secrets...)
	expanded := []string{}
	for _, secret := range secrets {
		once, e := jsjson.Stringify(secret)
		if e != nil {
			return e
		}
		once = once[1 : len(once)-1]
		twice, e := jsjson.Stringify(once)
		if e != nil {
			return e
		}
		expanded = append(expanded, secret, once, twice[1:len(twice)-1])
	}
	sort.SliceStable(expanded, func(i, j int) bool {
		a, _ := jsjson.UTF16Len(expanded[i])
		b, _ := jsjson.UTF16Len(expanded[j])
		return a > b
	})
	messages := entry.Messages
	if len(messages) > 12 {
		messages = messages[len(messages)-12:]
	}
	input := []any{}
	for _, message := range messages {
		o := new(jsjson.Object)
		for _, key := range []string{"role", "content", "toolCalls", "toolCallId"} {
			if v, exists := message.Get(key); exists {
				_ = o.Set(key, v)
			}
		}
		if raw, exists := message.Get("images"); exists && raw != nil {
			images, yes := raw.([]any)
			if !yes {
				return errors.New("diagnostic_images_invalid")
			}
			masked := make([]any, len(images))
			for i := range masked {
				masked[i] = "[image input]"
			}
			_ = o.Set("images", masked)
		}
		input = append(input, o)
	}
	redact := func(v any) (string, error) {
		clean, e := clean(v, expanded)
		if e != nil {
			return "", e
		}
		raw, e := jsjson.Stringify(clean)
		if e != nil {
			return "", e
		}
		units, e := jsjson.UTF16(raw)
		if e != nil {
			return "", e
		}
		if len(units) > 48000 {
			units = units[:48000]
		}
		return jsjson.FromUTF16(units), nil
	}
	in, err := redact(input)
	if err != nil {
		return err
	}
	out, err := redact(entry.Output)
	if err != nil {
		return err
	}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		return err
	}
	id[6] = (id[6] & 15) | 64
	id[8] = (id[8] & 63) | 128
	row := Row{fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]), s.ports.Now().UTC().Format("2006-01-02T15:04:05.000Z"), entry.ModelID, modelservice.CanonicalRole(entry.Role), in, out}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Clear/Close during an asynchronous credential resolution must not restore
	// old secret-bearing diagnostics after their owner was explicitly cleared.
	if s.closed || generation != s.generation {
		return nil
	}
	s.rows = append(s.rows, row)
	if len(s.rows) > 20 {
		old := s.rows[0]
		s.rows = s.rows[1:]
		s.timers[old.ID].Stop()
		delete(s.timers, old.ID)
	}
	s.timers[row.ID] = time.AfterFunc(15*time.Minute, func() {
		done, ok := s.work.Start()
		if !ok {
			return
		}
		defer done()
		s.mu.Lock()
		defer s.mu.Unlock()
		for i, r := range s.rows {
			if r.ID == row.ID {
				s.rows = append(s.rows[:i], s.rows[i+1:]...)
				break
			}
		}
		delete(s.timers, row.ID)
	})
	return nil
}

var sensitive = regexp.MustCompile(`authorization|api[-_]?key|access[-_]?token|credential|secret`)

func clean(value any, secrets []string) (any, error) {
	switch v := value.(type) {
	case string:
		units, e := jsjson.UTF16(v)
		if e != nil {
			return nil, e
		}
		replacement, _ := jsjson.UTF16("[redacted]")
		for _, secret := range secrets {
			needle, e := jsjson.UTF16(secret)
			if e != nil {
				return nil, e
			}
			next := []uint16{}
			if len(needle) == 0 {
				for i, u := range units {
					if i > 0 {
						next = append(next, replacement...)
					}
					next = append(next, u)
				}
			} else {
				for i := 0; i < len(units); {
					matches := i+len(needle) <= len(units)
					if matches {
						for j, u := range needle {
							if units[i+j] != u {
								matches = false
								break
							}
						}
					}
					if matches {
						next = append(next, replacement...)
						i += len(needle)
					} else {
						next = append(next, units[i])
						i++
					}
				}
			}
			units = next
		}
		return jsjson.FromUTF16(units), nil
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			var e error
			result[i], e = clean(item, secrets)
			if e != nil {
				return nil, e
			}
		}
		return result, nil
	case *jsjson.Object:
		result := new(jsjson.Object)
		for _, m := range v.Members() {
			var e error
			next := any("[redacted]")
			if !sensitive.MatchString(asciiLower(m.Key)) {
				next, e = clean(m.Value, secrets)
				if e != nil {
					return nil, e
				}
			}
			if e = result.Set(m.Key, next); e != nil {
				return nil, e
			}
		}
		return result, nil
	default:
		return value, nil
	}
}

// Source's non-Unicode JavaScript /i expression does not fold non-ASCII long-s
// or Kelvin-sign keys into ASCII sensitive words. Go regexp (?i) would.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
