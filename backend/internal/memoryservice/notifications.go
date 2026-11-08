package memoryservice

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// NotificationPorts are the remaining runtime composition boundary. Current
// must be actual semanticAssertion (including provenance roots), not ordinary
// assertion RelationAuthority. Episodes must be actual episode discovery.
// PostStack queues a callback after the current synchronous owner stack, even
// if that stack rolls back. It must never inline or launch a competing goroutine.
// Each pending exact reference is requalified at delivery, so a rolled-back
// revision cannot become a job. This object is owned by that serial dispatcher.
type NotificationPorts struct {
	Enabled        func(string) bool
	PolicyIdentity func(string) string
	Current        func(string, Reference) (*Object, error)
	Episodes       func(string, Reference) ([]*Object, error)
	PostStack      func(func())
	Failed         func()
}
type notification struct {
	scene          string
	ref            Reference
	reason, policy string
}
type listener struct {
	fn         func(*Object) error
	active     bool
	prev, next *listener
	deadNext   *listener
}
type NotificationQueue struct {
	ports               NotificationPorts
	pending             map[string]notification
	order               []string
	head, tail, dead    *listener
	live, traversals    int
	scheduled, disposed bool
}

func NewNotificationQueue(p NotificationPorts) (*NotificationQueue, error) {
	if p.Enabled == nil || p.PolicyIdentity == nil || p.Current == nil || p.Episodes == nil || p.PostStack == nil || p.Failed == nil {
		return nil, errors.New("semantic_notification_owners_required")
	}
	return &NotificationQueue{ports: p, pending: map[string]notification{}}, nil
}
func (q *NotificationQueue) Subscribe(fn func(*Object) error) (func(), error) {
	if fn == nil {
		return nil, errors.New("semantic_notification_listener_required")
	}
	l := &listener{fn: fn, active: true, prev: q.tail}
	if q.tail == nil {
		q.head = l
	} else {
		q.tail.next = l
	}
	q.tail = l
	q.live++
	return func() { q.removeListener(l) }, nil
}
func (q *NotificationQueue) AssertionCommitted(scene string, r Reference) error {
	return q.Notify(scene, r, "assertion_committed")
}
func refObject(r Reference) *Object {
	o := new(Object)
	_ = o.Set("id", r.ID)
	_ = o.Set("version", float64(r.Version))
	_ = o.Set("deletionEpoch", float64(r.DeletionEpoch))
	return o
}
func (q *NotificationQueue) Notify(scene string, r Reference, reason string) error {
	if q.disposed || q.live == 0 || !q.ports.Enabled(scene) {
		return nil
	}
	key, e := jsjson.Stringify([]any{scene, refObject(r)})
	if e != nil {
		return e
	}
	if _, exists := q.pending[key]; !exists {
		// 1024 is the existing source notification policy, not a new admission cap.
		if len(q.pending) >= 1024 {
			q.ports.Failed()
			return nil
		}
		q.order = append(q.order, key)
	}
	q.pending[key] = notification{scene, r, reason, q.ports.PolicyIdentity(scene)}
	if !q.scheduled {
		q.scheduled = true
		q.ports.PostStack(q.flush)
	}
	return nil
}
func (q *NotificationQueue) flush() {
	q.scheduled = false
	batch := make([]notification, 0, len(q.order))
	for _, k := range q.order {
		batch = append(batch, q.pending[k])
	}
	q.pending = map[string]notification{}
	q.order = nil
	if q.disposed {
		return
	}
	for _, n := range batch {
		if e := q.deliver(n); e != nil {
			q.ports.Failed()
		}
	}
}
func (q *NotificationQueue) deliver(n notification) (err error) {
	// Optional maintenance never retroactively fails an already committed write.
	defer func() {
		if recover() != nil {
			err = errors.New("semantic_notification_unavailable")
		}
	}()
	if !q.ports.Enabled(n.scene) || q.ports.PolicyIdentity(n.scene) != n.policy {
		return nil
	}
	current, e := q.ports.Current(n.scene, n.ref)
	if e != nil {
		return e
	}
	if current != nil && value(current, "derived") == false && text(current, "rootCoverage") == "complete" {
		var person any
		scope := object(value(object(value(current, "record")), "scope"))
		if text(scope, "kind") == "scene" {
			person = value(scope, "personId")
		}
		trigger := new(Object)
		_ = trigger.Set("ref", refObject(n.ref))
		_ = trigger.Set("dependencyKey", value(current, "dependencyKey"))
		dirty := new(Object)
		_ = dirty.Set("sceneId", n.scene)
		_ = dirty.Set("personId", person)
		_ = dirty.Set("trigger", trigger)
		_ = dirty.Set("reason", n.reason)
		if e = q.emit(dirty); e != nil {
			return e
		}
	}
	groups, e := q.ports.Episodes(n.scene, n.ref)
	if e != nil {
		return e
	}
	for _, group := range groups {
		parents, _ := value(group, "parents").([]any)
		if len(parents) == 0 {
			continue
		}
		dirty := new(Object)
		_ = dirty.Set("sceneId", n.scene)
		_ = dirty.Set("personId", nil)
		_ = dirty.Set("trigger", parents[0])
		_ = dirty.Set("episodeAnchor", value(group, "anchor"))
		_ = dirty.Set("reason", n.reason)
		if e = q.emit(dirty); e != nil {
			return e
		}
	}
	return nil
}
func (q *NotificationQueue) emit(dirty *Object) error {
	// Keep links stable while any iterator is active, including nested emits.
	// Read next after the callback so an append at the old tail is visited.
	q.traversals++
	defer func() { q.traversals--; q.unlinkDeadListeners() }()
	for l := q.head; l != nil; l = l.next {
		if !l.active {
			continue
		}
		raw, e := jsjson.Stringify(dirty)
		if e != nil {
			return e
		}
		copy, e := jsjson.Parse(raw)
		if e != nil {
			return e
		}
		if e = l.fn(copy.(*Object)); e != nil {
			return e
		}
	}
	return nil
}
func (q *NotificationQueue) Dispose() {
	q.disposed = true
	q.pending = map[string]notification{}
	q.order = nil
	for l := q.head; l != nil; {
		next := l.next
		q.removeListener(l)
		l = next
	}
}

// Each subscription has its own handle. Clear its closure immediately, and
// unlink in constant time when no live iterator could still refer to its links.
func (q *NotificationQueue) removeListener(l *listener) {
	if !l.active {
		return
	}
	l.active = false
	l.fn = nil
	q.live--
	if q.traversals != 0 {
		l.deadNext = q.dead
		q.dead = l
		return
	}
	q.unlinkListener(l)
}

func (q *NotificationQueue) unlinkListener(l *listener) {
	if l.prev == nil {
		q.head = l.next
	} else {
		l.prev.next = l.next
	}
	if l.next == nil {
		q.tail = l.prev
	} else {
		l.next.prev = l.prev
	}
	// An old unsubscribe handle must not retain other nodes or their closures.
	l.prev, l.next, l.deadNext = nil, nil, nil
}

// The last iterator removes only dead nodes, never rescanning live listeners.
// Deferred cleanup also runs on error and panic unwinding.
func (q *NotificationQueue) unlinkDeadListeners() {
	if q.traversals != 0 {
		return
	}
	for q.dead != nil {
		l := q.dead
		q.dead = l.deadNext
		q.unlinkListener(l)
	}
}
