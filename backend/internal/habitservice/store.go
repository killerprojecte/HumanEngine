package habitservice

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"math"
	"time"
)

const day = 86400000.0

// These are existing product decay/compaction policies from src/habits.ts, not
// resource admission limits introduced by the port.
type Policy struct{ HalfLifeDays, RetentionDays, MaxSources float64 }

func DefaultPolicy() Policy { return Policy{30, 90, 128} }

type Options struct {
	Policy *Policy
	// Now runs outside fixed publication snapshots. During a write, the supplied
	// service owns the current transaction; reentrant work must use that service.
	Now func(*Service) float64
	ID  func() (string, error)
}
type Context struct {
	SceneID        string
	Simulation     bool
	SourceEvidence []any
}
type Service struct {
	main         *sql.DB
	authority    *recovery.Authority
	session      *recovery.WriteSession
	bindingError error
	policy       Policy
	now          func(*Service) float64
	id           func() (string, error)
}

// New performs the actual source constructor readiness, policy and fixed DDL
// sequence. It never replays sources or recalculates persisted records on restart.
func New(ctx context.Context, main *sql.DB, authority *recovery.Authority, options Options) (*Service, error) {
	if main == nil || authority == nil {
		return nil, errors.New("habit_authority_required")
	}
	s := &Service{main: main, authority: authority, policy: DefaultPolicy(), now: options.Now, id: options.ID}
	if s.now == nil {
		s.now = func(*Service) float64 { return float64(time.Now().UnixMilli()) }
	}
	if s.id == nil {
		s.id = uuid
	}
	if options.Policy != nil {
		s.policy = *options.Policy
	}
	err := authority.WithWriteSession(ctx, main, func(ws *recovery.WriteSession) error {
		if e := ws.AssertReady(); e != nil {
			return e
		}
		p := s.policy
		if !finite(p.HalfLifeDays) || p.HalfLifeDays < 1 || p.HalfLifeDays > 365 || !finite(p.RetentionDays) || p.RetentionDays < p.HalfLifeDays || p.RetentionDays > 730 || !finite(p.MaxSources) || math.Trunc(p.MaxSources) != p.MaxSources || p.MaxSources < 8 || p.MaxSources > 512 {
			return errors.New("invalid_habit_policy")
		}
		return ws.EnsureHabitStorageSchema()
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Service) WithSession(ws *recovery.WriteSession) *Service {
	n := *s
	n.session = ws
	if ws == nil {
		n.bindingError = errors.New("recovery_transaction_required")
	} else {
		n.bindingError = ws.CheckBinding(s.authority, s.main)
	}
	return &n
}
func (s *Service) Transaction(ctx context.Context, fn func(*Service) error) error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if fn == nil {
		return errors.New("habit_transaction_required")
	}
	if s.session != nil {
		return s.session.WithSavepoint(func(ws *recovery.WriteSession) error { return fn(s.WithSession(ws)) })
	}
	return s.authority.WithWriteSession(ctx, s.main, func(ws *recovery.WriteSession) error { return fn(s.WithSession(ws)) })
}

type readScope struct {
	query func(string, ...any) (sourceevents.Rows, error)
	ready func() error
}

func (s *Service) read(ctx context.Context, fn func(*readScope) error) error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.session != nil {
		if e := s.session.CheckBinding(s.authority, s.main); e != nil {
			return e
		}
		if e := s.session.AssertReady(); e != nil {
			return e
		}
		return fn(&readScope{query: func(q string, a ...any) (sourceevents.Rows, error) { return s.session.Query(q, bind(a)...) }, ready: s.session.AssertReady})
	}
	return s.authority.Publish(ctx, s.main, func(conn *sql.Conn) ([]byte, error) {
		return nil, fn(&readScope{query: func(q string, a ...any) (sourceevents.Rows, error) { return conn.QueryContext(ctx, q, bind(a)...) }, ready: func() error { return nil }})
	}, func([]byte) error { return nil })
}
func (r *readScope) raw(q string, args ...any) (raw string, found bool, err error) {
	rows, e := r.query(q, args...)
	if e != nil {
		return "", false, e
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	if rows.Next() {
		err = rows.Scan(&raw)
		return raw, true, err
	}
	return "", false, rows.Err()
}
func (r *readScope) scene(id string) (*Object, error) {
	raw, found, e := r.raw("SELECT value FROM scenes WHERE id=?", sqlText(id))
	if e != nil || !found {
		return nil, e
	}
	v, e := jsjson.Parse(raw)
	if e != nil {
		return nil, e
	}
	return object(v), nil
}
func (s *Service) scene(ctx context.Context, id string) (out *Object, err error) {
	err = s.read(ctx, func(r *readScope) error { var e error; out, e = r.scene(id); return e })
	return
}
func (r *readScope) current(source *Object, events *sourceevents.View) (bool, error) {
	_, recalled, e := r.raw("SELECT 1 FROM recall_tombstones WHERE scene_id=? AND message_id=?", sqlText(str(source, "sceneId")), sqlText(str(source, "messageId")))
	if e != nil || recalled {
		return false, e
	}
	registered, e := events.Registered(str(source, "sceneId"), str(source, "messageId"))
	if e != nil {
		return false, e
	}
	refValue := get(source, "eventRef")
	if !truthy(refValue) && registered == nil {
		return true, nil
	}
	scene, e := r.scene(str(source, "sceneId"))
	if e != nil || scene == nil {
		return false, e
	}
	identity := sourceevents.Identity{SceneID: str(source, "sceneId"), AdapterID: str(scene, "adapterId"), AccountID: str(scene, "accountId"), MessageID: str(source, "messageId"), SenderID: str(source, "senderId")}
	if registered != nil && (registered.Identity.AdapterID != identity.AdapterID || registered.Identity.AccountID != identity.AccountID || registered.Identity.SenderID != identity.SenderID) {
		return false, nil
	}
	if truthy(refValue) {
		ref, e := sourceevents.ParseRef(refValue)
		if e != nil {
			return false, e
		}
		eligible, e := events.Current(ref, identity)
		return eligible.Eligible, e
	}
	edited, e := events.HasEdits(identity)
	return !edited, e
}

// CurrentSources performs one fixed source/authority snapshot. It never calls a
// clock or user callback, and errors are returned rather than treated as false.
func (s *Service) CurrentSources(ctx context.Context, sources []any) (out []bool, err error) {
	out = make([]bool, 0, len(sources))
	if len(sources) == 0 {
		return out, nil
	}
	// Copy before the trusted read, matching currentDomainSources materialization.
	raw, e := jsjson.Stringify(sources)
	if e != nil {
		return nil, e
	}
	v, e := jsjson.Parse(raw)
	if e != nil {
		return nil, e
	}
	owned := v.([]any)
	err = s.read(ctx, func(r *readScope) error {
		events, e := sourceevents.BorrowReadScope(r.ready, r.query)
		if e != nil {
			return e
		}
		for _, source := range owned {
			ok, e := r.current(object(source), events)
			if e != nil {
				return e
			}
			out = append(out, ok)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return
}
func (s *Service) get(ctx context.Context, id string) (out *Object, err error) {
	err = s.read(ctx, func(r *readScope) error {
		raw, found, e := r.raw("SELECT value FROM habits WHERE id=?", sqlText(id))
		if e != nil || !found {
			return e
		}
		v, e := jsjson.Parse(raw)
		if e != nil {
			return e
		}
		out = object(v)
		if out == nil {
			return errors.New("habit_record_invalid")
		}
		return nil
	})
	return
}
func (s *Service) save(record *Object) error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.session == nil {
		return errors.New("habit_transaction_required")
	}
	if e := s.session.AssertReady(); e != nil {
		return e
	}
	raw, e := jsjson.Stringify(record)
	if e != nil {
		return e
	}
	_, e = s.session.Exec("UPDATE habits SET value=? WHERE id=?", raw, sqlText(str(record, "id")))
	return e
}
func (s *Service) author(ctx context.Context, source *Object) (string, error) {
	scene, e := s.scene(ctx, str(source, "sceneId"))
	if e != nil {
		return "", e
	}
	adapter, account := get(scene, "adapterId"), get(scene, "accountId")
	if adapter == nil {
		adapter = get(source, "sceneId")
	}
	if account == nil {
		account = get(source, "sceneId")
	}
	return jsjson.Stringify([]any{adapter, account, get(source, "senderId")})
}
func copyContext(c Context) (Context, error) {
	if c.SourceEvidence == nil {
		return c, nil
	}
	raw, e := jsjson.Stringify(c.SourceEvidence)
	if e != nil {
		return c, e
	}
	v, e := jsjson.Parse(raw)
	if e != nil {
		return c, e
	}
	c.SourceEvidence = v.([]any)
	return c, nil
}
func (s *Service) policyValue() *Object {
	return obj("halfLifeDays", s.policy.HalfLifeDays, "retentionDays", s.policy.RetentionDays, "maxSources", float64(s.policy.MaxSources))
}
func optionalNumber(o *Object, k string, fallback float64) float64 {
	v := get(o, k)
	if v == nil {
		return fallback
	}
	return num(v)
}
func negativeInfinity() float64 { return math.Inf(-1) }

func bind(args []any) []any {
	out := append([]any{}, args...)
	for i, v := range out {
		if text, ok := v.(string); ok {
			out[i] = sqlText(text)
		}
	}
	return out
}
