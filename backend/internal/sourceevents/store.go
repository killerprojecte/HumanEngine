package sourceevents

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type Options struct {
	// AssertReady is local-only; it must not call Authority methods. Read already
	// holds a fixed publication scope, so recursive authority checks would deadlock.
	AssertReady                     func() error
	BeforeWrite                     func(*Store, Mutation) error
	ID                              func() string
	Now                             func() string
	MaxRevisions, MaxEditKeys       *int
	MaxHeadsPerScene, MaxHeadsTotal *int
}
type Store struct {
	maintenance        *recovery.Maintenance
	maintenanceTx      *recovery.MaintenanceTx
	maintenanceContext context.Context
	main               *sql.DB
	authority          *recovery.Authority
	session            *recovery.WriteSession
	bindingError       error
	options            Options
	revisions, edits   int
	listeners          *listeners
}

// Listener identity is its pointer, not a Go function's code address. Registering
// the same pointer twice has JS Set semantics, including either unsubscribe.
type Listener struct{ Handle func(*Store, Mutation) error }
type listenerNode struct {
	value  *Listener
	next   *listenerNode
	active bool
}
type listeners struct {
	mu         sync.Mutex
	members    map[*Listener]*listenerNode
	head, tail *listenerNode
	iterations int
}

func New(main *sql.DB, authority *recovery.Authority, options Options) (*Store, error) {
	copyLimit := func(p *int) *int {
		if p == nil {
			return nil
		}
		n := *p
		return &n
	}
	options.MaxHeadsPerScene = copyLimit(options.MaxHeadsPerScene)
	options.MaxHeadsTotal = copyLimit(options.MaxHeadsTotal)
	s := &Store{main: main, authority: authority, options: options, listeners: &listeners{members: map[*Listener]*listenerNode{}}, revisions: 32, edits: 128}
	valid := func(v *int, max int) bool { return v == nil || (*v >= 1 && *v <= max) }
	if main == nil || authority == nil {
		return nil, errors.New("source_event_authority_required")
	}
	if !valid(options.MaxRevisions, 256) || !valid(options.MaxEditKeys, 1024) || !valid(options.MaxHeadsPerScene, 1000000) || !valid(options.MaxHeadsTotal, 1000000) {
		return nil, errors.New("invalid_event_limit")
	}
	if options.MaxRevisions != nil {
		s.revisions = *options.MaxRevisions
	}
	if options.MaxEditKeys != nil {
		s.edits = *options.MaxEditKeys
	}
	return s, nil
}

// WithSession joins the caller's existing transaction, without savepoints. The
// returned store and all its query handles expire with that transaction.
func (s *Store) WithSession(session *recovery.WriteSession) *Store {
	if s.maintenance != nil {
		n := *s
		n.bindingError = errors.New("source_event_live_scope_required")
		return &n
	}
	copy := *s
	copy.session = session
	if session == nil {
		copy.bindingError = errors.New("recovery_transaction_required")
	} else {
		copy.bindingError = session.CheckBinding(s.authority, s.main)
	}
	return &copy
}
func (s *Store) SubscribeCommitted(listener *Listener) func() {
	l := s.listeners
	l.mu.Lock()
	if _, ok := l.members[listener]; !ok {
		node := &listenerNode{value: listener, active: true}
		l.members[listener] = node
		if l.tail == nil {
			l.head = node
		} else {
			l.tail.next = node
		}
		l.tail = node
	}
	l.mu.Unlock()
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if node, ok := l.members[listener]; ok {
			node.active = false
			delete(l.members, listener)
		}
		l.compact()
	}
}
func (l *listeners) compact() {
	if l.iterations != 0 {
		return
	}
	var head, tail *listenerNode
	for n := l.head; n != nil; {
		next := n.next
		if n.active {
			if tail == nil {
				head = n
			} else {
				tail.next = n
			}
			tail = n
			n.next = nil
		}
		n = next
	}
	l.head, l.tail = head, tail
}
func (s *Store) notify(m Mutation) error {
	l := s.listeners
	l.mu.Lock()
	l.iterations++
	node := l.head
	l.mu.Unlock()
	defer func() { l.mu.Lock(); l.iterations--; l.compact(); l.mu.Unlock() }()
	for node != nil {
		l.mu.Lock()
		active, listener := node.active, node.value
		l.mu.Unlock()
		if active && listener != nil && listener.Handle != nil {
			if e := listener.Handle(s, cloneMutation(m)); e != nil {
				return e
			}
		}
		l.mu.Lock()
		node = node.next
		l.mu.Unlock()
	}
	return nil
}
func cloneMetadata(m Metadata) Metadata {
	if m.OccurredAt != nil {
		x := *m.OccurredAt
		m.OccurredAt = &x
	}
	return m
}
func cloneMutation(m Mutation) Mutation {
	if m.PreviousRef != nil {
		x := *m.PreviousRef
		m.PreviousRef = &x
	}
	if m.Metadata != nil {
		x := cloneMetadata(*m.Metadata)
		m.Metadata = &x
	}
	return m
}
func (s *Store) hook(m Mutation) error {
	if s.options.BeforeWrite != nil {
		return s.options.BeforeWrite(s, cloneMutation(m))
	}
	return s.RecordMutation(m)
}
func (s *Store) RecordMutation(m Mutation) error {
	if s.maintenance != nil {
		return s.recordOffline(m)
	}
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.session == nil {
		return errors.New("recovery_transaction_required")
	}
	if e := s.session.RequireReader(2); e != nil {
		return e
	}
	switch m.Kind {
	case "recalled":
		_, e := s.session.AppendIntent(recovery.Source{SceneID: m.Identity.SceneID, MessageID: m.Identity.MessageID})
		return e
	case "edited":
		if m.Metadata == nil || m.Metadata.Ref != m.Ref || m.Metadata.Identity != m.Identity || m.HeadHash == "" {
			return errors.New("recovery_source_event_invalid")
		}
		key, e := IdentityKey(m.Identity)
		if e != nil {
			return e
		}
		_, e = s.session.AppendIntent(recovery.SourceEditHead{SceneID: m.Identity.SceneID, MessageID: m.Identity.MessageID, EventID: m.Ref.EventID, Revision: m.Ref.Revision, DeletionEpoch: m.Ref.DeletionEpoch, IdentityHash: key, HeadHash: m.HeadHash})
		return e
	}
	return nil
}
func (s *Store) transaction(ctx context.Context, fn func(*Store) error) error {
	if s.maintenance != nil {
		if e := s.ready(); e != nil {
			return e
		}
		return fn(s)
	}
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.session != nil {
		return fn(s)
	}
	return s.authority.WithWriteSession(ctx, s.main, func(tx *recovery.WriteSession) error { return fn(s.WithSession(tx)) })
}
func (s *Store) ready() error {
	if s.maintenance != nil {
		if s.bindingError != nil {
			return s.bindingError
		}
		return s.maintenanceTx.CheckBinding(s.maintenanceContext, s.maintenance)
	}
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.options.AssertReady != nil {
		if e := s.options.AssertReady(); e != nil {
			return e
		}
	}
	if s.session != nil {
		return s.session.AssertReady()
	}
	return nil
}
func (s *Store) initialize() error {
	if s.maintenance != nil {
		if e := s.ready(); e != nil {
			return e
		}
		return s.maintenanceTx.EnsureSourceEventSchema(s.maintenanceContext)
	}
	if s.bindingError != nil {
		return s.bindingError
	}
	if s.options.AssertReady != nil {
		if e := s.options.AssertReady(); e != nil {
			return e
		}
	}
	// The fixed enrollment capability checks native recovery readiness itself.
	return s.session.EnsureSourceEventSchema()
}
func (s *Store) EnsureLookupIndex(ctx context.Context) error {
	if s.maintenance != nil {
		if e := s.ready(); e != nil {
			return e
		}
		_, e := s.maintenanceTx.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_source_event_heads_scene_message ON source_event_heads(scene_id,message_id)")
		return e
	}
	return s.transaction(ctx, func(bound *Store) error { return bound.session.EnsureSourceEventLookupIndex() })
}
func text(s string) string {
	v, e := jsjson.UTF8Text(s)
	if e != nil {
		return s
	}
	return v
}
func bind(args []any) []any {
	out := make([]any, len(args))
	for i, v := range args {
		if s, ok := v.(string); ok {
			v = text(s)
		}
		out[i] = v
	}
	return out
}
func (s *Store) exec(query string, args ...any) error {
	if s.maintenance != nil {
		if e := s.ready(); e != nil {
			return e
		}
		_, e := s.maintenanceTx.ExecContext(s.maintenanceContext, query, bind(args)...)
		return e
	}
	_, e := s.session.Exec(query, bind(args)...)
	return e
}
func uuid() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func (s *Store) now() string {
	if s.options.Now != nil {
		return s.options.Now()
	}
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}
func (s *Store) id() string {
	if s.options.ID != nil {
		return s.options.ID()
	}
	return uuid()
}
func (s *Store) Observe(ctx context.Context, change Change) (result Result, err error) {
	if !change.Valid() {
		return result, ErrInvalid
	}
	// Schema.parse snapshots the source input before any readiness/clock/write
	// callbacks run. Do not retain the caller's mutable timestamp pointer.
	if change.OccurredAt != nil {
		occurredAt := *change.OccurredAt
		change.OccurredAt = &occurredAt
	}
	err = s.transaction(ctx, func(bound *Store) error { var e error; result, e = bound.observe(change); return e })
	return
}
func (s *Store) observe(c Change) (Result, error) {
	result := func(status string, ref, previous *Ref) (Result, error) { return Result{status, ref, previous}, nil }
	if e := s.initialize(); e != nil {
		return Result{}, e
	}
	if e := s.ready(); e != nil {
		return Result{}, e
	}
	v := s.view()
	revoked, e := v.revoked(c.Identity)
	if e != nil {
		return Result{}, e
	}
	if revoked {
		return result("recalled", nil, nil)
	}
	key, _ := IdentityKey(c.Identity)
	rows, e := v.query("SELECT identity_key FROM source_event_heads WHERE scene_id=? AND message_id=?", c.Identity.SceneID, c.Identity.MessageID)
	if e != nil {
		return Result{}, e
	}
	collision := false
	for rows.Next() {
		var other string
		if e = rows.Scan(&other); e != nil {
			break
		}
		if other != key {
			collision = true
		}
	}
	e = errors.Join(e, rows.Err(), rows.Close())
	if e != nil {
		return Result{}, e
	}
	if collision {
		return result("conflict", nil, nil)
	}
	old, e := v.head(c.Identity)
	if e != nil {
		return Result{}, e
	}
	if old != nil && old.Deleted {
		return result("recalled", nil, nil)
	}
	if old == nil && c.Kind == "ingress" {
		for _, cap := range []struct {
			limit *int
			query string
			args  []any
		}{{s.options.MaxHeadsTotal, "SELECT count(event_id) FROM source_event_heads", nil}, {s.options.MaxHeadsPerScene, "SELECT count(event_id) FROM source_event_heads WHERE scene_id=?", []any{c.Identity.SceneID}}} {
			if cap.limit != nil {
				var count int
				if _, e := v.one(cap.query, cap.args, &count); e != nil {
					return Result{}, e
				}
				if count >= *cap.limit {
					return result("capacity", nil, nil)
				}
			}
		}
	}
	if c.Kind == "ingress" && old != nil {
		status := "conflict"
		if old.Metadata.BodyHash == c.BodyHash {
			status = "duplicate"
		}
		return result(status, &old.Metadata.Ref, nil)
	}
	changeHash, e := ChangeHash(c)
	if e != nil {
		return Result{}, e
	}
	if c.Kind == "edit" {
		if old == nil {
			return result("unorderable", nil, nil)
		}
		var rawRevision any
		var hash string
		exists, e := v.one("SELECT revision,change_hash FROM source_event_edit_keys WHERE event_id=? AND edit_key=?", []any{old.Metadata.Ref.EventID, c.EditID}, &rawRevision, &hash)
		if e != nil {
			return Result{}, e
		}
		if exists {
			revision, integer := rawRevision.(int64)
			if !integer || revision < 1 || revision > old.Metadata.Ref.Revision || !hashPattern.MatchString(hash) {
				return Result{}, ErrCorrupt
			}
			ref := old.Metadata.Ref
			ref.Revision = revision
			status := "conflict"
			if hash == changeHash {
				status = "duplicate"
			}
			return result(status, &ref, nil)
		}
		if old.OrderMode != nil && *old.OrderMode != c.Order.Kind {
			return result("unorderable", &old.Metadata.Ref, nil)
		}
		if c.Order.Kind == "observed_cas" {
			if c.Order.ExpectedBase != old.Metadata.Ref {
				return result("conflict", &old.Metadata.Ref, nil)
			}
		} else if old.Sequence != nil && c.Order.Sequence <= *old.Sequence {
			return result("conflict", &old.Metadata.Ref, nil)
		}
	}
	ref := Ref{Revision: 1}
	var previous *Ref
	kind := "new"
	if old != nil {
		ref = old.Metadata.Ref
		ref.Revision++
		previous = &old.Metadata.Ref
		kind = "edited"
	} else {
		ref.EventID = s.id()
	}
	metadata := Metadata{c.Identity, ref, c.BodyHash, s.now(), c.OccurredAt}
	if !metadata.Valid() {
		return Result{}, ErrInvalid
	}
	head := Head{Metadata: metadata}
	if c.Kind == "edit" {
		mode := c.Order.Kind
		head.OrderMode = &mode
		if mode == "platform_sequence" {
			seq := c.Order.Sequence
			head.Sequence = &seq
		}
	}
	hash, e := HeadHash(metadata, head.Sequence, head.OrderMode)
	if e != nil {
		return Result{}, e
	}
	mutation := Mutation{kind, c.Identity, ref, previous, &metadata, hash}
	if e = s.hook(mutation); e != nil {
		return Result{}, e
	}
	value, e := jsjson.Stringify(head.Value())
	if e != nil {
		return Result{}, e
	}
	if e = s.exec("INSERT INTO source_event_heads(identity_key,event_id,scene_id,message_id,value) VALUES(?,?,?,?,?) ON CONFLICT(identity_key) DO UPDATE SET value=excluded.value", key, ref.EventID, c.Identity.SceneID, c.Identity.MessageID, value); e != nil {
		return Result{}, e
	}
	value, e = jsjson.Stringify(metadata.Value())
	if e != nil {
		return Result{}, e
	}
	if e = s.exec("INSERT INTO source_event_revisions VALUES(?,?,?)", ref.EventID, ref.Revision, value); e != nil {
		return Result{}, e
	}
	if c.Kind == "edit" {
		if e = s.exec("INSERT INTO source_event_edit_keys VALUES(?,?,?,?)", ref.EventID, c.EditID, ref.Revision, changeHash); e != nil {
			return Result{}, e
		}
	}
	if e = s.exec("DELETE FROM source_event_revisions WHERE event_id=? AND revision NOT IN (SELECT revision FROM source_event_revisions WHERE event_id=? ORDER BY revision DESC LIMIT ?)", ref.EventID, ref.EventID, s.revisions); e != nil {
		return Result{}, e
	}
	if e = s.exec("DELETE FROM source_event_edit_keys WHERE event_id=? AND edit_key NOT IN (SELECT edit_key FROM source_event_edit_keys WHERE event_id=? ORDER BY revision DESC LIMIT ?)", ref.EventID, ref.EventID, s.edits); e != nil {
		return Result{}, e
	}
	if e = s.notify(mutation); e != nil {
		return Result{}, e
	}
	return result(kind, &ref, previous)
}
func (s *Store) RevokeAll(ctx context.Context, sceneID, messageID string) error {
	if !identifier(sceneID) || !identifier(messageID) {
		return ErrInvalid
	}
	return s.transaction(ctx, func(bound *Store) error {
		if e := bound.initialize(); e != nil {
			return e
		}
		v := bound.view()
		rows, e := v.query("SELECT value FROM source_event_heads WHERE scene_id=? AND message_id=?", sceneID, messageID)
		if e != nil {
			return e
		}
		var values []string
		for rows.Next() {
			var value string
			if e = rows.Scan(&value); e != nil {
				break
			}
			values = append(values, value)
		}
		e = errors.Join(e, rows.Err(), rows.Close())
		if e != nil {
			return e
		}
		// The source materializes SQL rows first, then parses each immediately
		// before its mutation. A caught later corrupt row preserves earlier work.
		for _, value := range values {
			parsed, p := jsjson.Parse(value)
			if p != nil {
				return ErrCorrupt
			}
			h, p := ParseHead(parsed)
			if p != nil {
				return p
			}
			if h.Deleted {
				continue
			}
			previous := h.Metadata.Ref
			ref := previous
			ref.DeletionEpoch++
			if !ref.Valid() {
				return ErrInvalid
			}
			if e = bound.hook(Mutation{Kind: "recalled", Identity: h.Metadata.Identity, Ref: ref, PreviousRef: &previous}); e != nil {
				return e
			}
			for _, table := range []string{"source_event_revisions", "source_event_edit_keys", "source_event_heads"} {
				if e = bound.exec("DELETE FROM "+table+" WHERE event_id=?", ref.EventID); e != nil {
					return e
				}
			}
		}
		return bound.exec("INSERT OR IGNORE INTO source_event_tombstones VALUES(?,?)", sceneID, messageID)
	})
}
