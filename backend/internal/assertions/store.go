// Package assertions owns current and historical assertion persistence. Its
// storage reads do not authorize model/API exposure.
package assertions

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sync/atomic"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type Object = jsjson.Object
type Rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}
type Options struct {
	OperationID func() string
	AssertReady func() error
	Now         func() string
	DateParser  *jsdate.Parser
}
type Store struct {
	main         *sql.DB
	authority    *recovery.Authority
	session      *recovery.WriteSession
	bindingError error
	options      Options
}
type Reader struct {
	query func(string, ...any) (Rows, error)
	ready func() error
}

func New(main *sql.DB, authority *recovery.Authority, options Options) (*Store, error) {
	if main == nil || authority == nil {
		return nil, errors.New("assertion_authority_required")
	}
	if options.DateParser == nil {
		p, e := jsdate.New("UTC")
		if e != nil {
			return nil, e
		}
		options.DateParser = p
	}
	return &Store{main: main, authority: authority, options: options}, nil
}

// WithSession borrows an existing transaction. It does not start one or alter its
// commit semantics. Methods reject an expired or differently-owned capability.
func (s *Store) WithSession(session *recovery.WriteSession) *Store {
	n := *s
	n.session = session
	if session == nil {
		n.bindingError = errors.New("assertion_transaction_required")
	} else {
		n.bindingError = session.CheckBinding(s.authority, s.main)
	}
	return &n
}
func (s *Store) requireTransaction() error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.session == nil {
		return errors.New("assertion_transaction_required")
	}
	return s.session.CheckBinding(s.authority, s.main)
}
func (s *Store) ready() error {
	if e := s.requireTransaction(); e != nil {
		return e
	}
	if s.options.AssertReady != nil {
		if e := s.options.AssertReady(); e != nil {
			return e
		}
	}
	return s.session.AssertReady()
}
func (s *Store) Transaction(ctx context.Context, fn func(*Store) error) error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.session != nil {
		return s.session.WithSavepoint(func(child *recovery.WriteSession) error { return fn(s.WithSession(child)) })
	}
	return s.authority.WithWriteSession(ctx, s.main, func(child *recovery.WriteSession) error { return fn(s.WithSession(child)) })
}

// Initialize is explicit writer initialization. Constructing a reader never
// creates tables or imports legacy assertions.
func (s *Store) Initialize(ctx context.Context) error {
	return s.Transaction(ctx, func(b *Store) error {
		if e := b.session.EnsureMemoryStorageSchema(); e != nil {
			return e
		}
		return b.session.EnsureAssertionHistorySchema()
	})
}
func (s *Store) reader() *Reader {
	return &Reader{ready: s.ready, query: func(q string, args ...any) (Rows, error) {
		if e := s.requireTransaction(); e != nil {
			return nil, e
		}
		return s.session.Query(q, bind(args)...)
	}}
}
func (s *Store) Read(ctx context.Context, fn func(*Reader) error) error {
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.session != nil {
		return fn(s.reader())
	}
	return s.authority.Publish(ctx, s.main, func(conn *sql.Conn) ([]byte, error) {
		var active atomic.Bool
		active.Store(true)
		defer active.Store(false)
		alive := func() error {
			if !active.Load() {
				return sql.ErrTxDone
			}
			if s.options.AssertReady != nil {
				return s.options.AssertReady()
			}
			return nil
		}
		r := &Reader{ready: alive, query: func(q string, args ...any) (Rows, error) {
			if !active.Load() {
				return nil, sql.ErrTxDone
			}
			return conn.QueryContext(ctx, q, bind(args)...)
		}}
		return nil, fn(r)
	}, func([]byte) error { return nil })
}
func (s *Store) exec(q string, args ...any) error {
	if e := s.requireTransaction(); e != nil {
		return e
	}
	_, e := s.session.Exec(q, bind(args)...)
	return e
}
func bind(args []any) []any {
	out := make([]any, len(args))
	for i, v := range args {
		if str, ok := v.(string); ok {
			bound, e := jsjson.UTF8Text(str)
			if e == nil {
				v = bound
			}
		}
		out[i] = v
	}
	return out
}
func (r *Reader) one(q string, args []any, dest ...any) (exists bool, err error) {
	rows, err := r.query(q, args...)
	if err != nil {
		return
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	if rows.Next() {
		return true, rows.Scan(dest...)
	}
	return false, rows.Err()
}
func (s *Store) now() string {
	if s.options.Now != nil {
		return s.options.Now()
	}
	return isoMillis(time.Now().UnixMilli())
}
func obj(pairs ...any) *Object {
	o := new(Object)
	for i := 0; i < len(pairs); i += 2 {
		_ = o.Set(pairs[i].(string), pairs[i+1])
	}
	return o
}
func get(v any, k string) any {
	if o, ok := v.(*Object); ok && o != nil {
		x, _ := o.Get(k)
		return x
	}
	return nil
}
func str(v any) string  { s, _ := v.(string); return s }
func array(v any) []any { a, _ := v.([]any); return a }
func scope(record *Object) string {
	v := get(record, "scope")
	if get(v, "kind") == "scene" {
		return "scene:" + str(get(v, "sceneId"))
	}
	return "shared:public"
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0 && !math.IsNaN(x)
	}
	return true
}
