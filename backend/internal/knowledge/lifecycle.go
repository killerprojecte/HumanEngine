package knowledge

import (
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type maintenanceStorage interface {
	RequireTransaction() error
	Rows(*string, *int) ([]*Object, error)
	Remove(string) error
	Replace(*Object) error
}
type Lifecycle struct{ storage maintenanceStorage }

func NewLifecycle(storage *Store) *Lifecycle { return &Lifecycle{storage: storage} }

type Service struct{ Storage *Store }

func New(main *sql.DB, authority *recovery.Authority) (*Service, error) {
	s, e := NewStore(main, authority)
	if e != nil {
		return nil, e
	}
	return &Service{Storage: s}, nil
}
func (s *Service) WithSession(session *recovery.WriteSession) *Service {
	return &Service{Storage: s.Storage.WithSession(session)}
}
func (s *Service) PurgeAssertion(id string) error { return NewLifecycle(s.Storage).PurgeAssertion(id) }

// PurgeInAssertionScope borrows the exact live assertion child. The assertion
// owner supplies durable intent and rollback; no second connection is acquired.
func (s *Service) PurgeInAssertionScope(store *assertions.Store, id string) error {
	return store.WithWriteSession(func(session *recovery.WriteSession) error { return s.WithSession(session).PurgeAssertion(id) })
}
func (l *Lifecycle) PurgeAssertion(id string) error {
	return l.purge(func(proof, backing any) bool {
		for _, parent := range arr(get(proof, "parents")) {
			if str(parent) == id {
				return true
			}
		}
		return false
	}, &id, nil)
}
func (l *Lifecycle) PurgeSource(scene, message string) error {
	return l.purge(func(proof, backing any) bool {
		for _, source := range arr(get(proof, "sources")) {
			if str(get(source, "sceneId")) == scene && str(get(source, "messageId")) == message {
				return true
			}
		}
		return false
	}, nil, nil)
}
func (l *Lifecycle) PruneUnavailableProof(scene string, simulation bool, available func(backing, proof *Object) bool) error {
	if e := l.storage.RequireTransaction(); e != nil {
		return e
	}
	if simulation {
		return nil
	}
	return l.purge(func(proof, backing any) bool { return !available(object(backing), object(proof)) }, nil, &scene)
}

// orderedChanges models JS Map/Set insertion order, including overwrite without
// reordering. Order matters for captured dependencies and cycle settlement.
type orderedChanges struct {
	updates     map[string]*Object
	updateOrder []string
	removed     map[string]bool
	removeOrder []string
}

func changes() *orderedChanges {
	return &orderedChanges{updates: map[string]*Object{}, removed: map[string]bool{}}
}
func (c *orderedChanges) put(id string, edge *Object) {
	if _, ok := c.updates[id]; !ok {
		c.updateOrder = append(c.updateOrder, id)
	}
	c.updates[id] = edge
}
func (c *orderedChanges) remove(id string) {
	if !c.removed[id] {
		c.removeOrder = append(c.removeOrder, id)
		c.removed[id] = true
	}
}
func filterProofs(proofs any, backing any, matches func(any, any) bool) []any {
	out := []any{}
	for _, p := range arr(proofs) {
		if !matches(p, backing) {
			out = append(out, p)
		}
	}
	return out
}
func survivingSpans(spans any, proofs []any) []any {
	out := []any{}
	for _, span := range arr(spans) {
		found := false
		for _, p := range proofs {
			for _, s := range arr(get(p, "sources")) {
				if deepEqual(s, get(span, "source")) {
					found = true
				}
			}
		}
		if found {
			out = append(out, span)
		}
	}
	return out
}
func (l *Lifecycle) purge(matches func(any, any) bool, assertion, scene *string) error {
	if e := l.storage.RequireTransaction(); e != nil {
		return e
	}
	rows, e := l.storage.Rows(scene, nil)
	if e != nil {
		return e
	}
	c := changes()
	for _, edge := range rows {
		proofs := filterProofs(get(edge, "proofs"), get(edge, "backing"), matches)
		deps := []any{}
		reads := []any{}
		remove := len(proofs) == 0 || (assertion != nil && str(get(get(edge, "backing"), "id")) == *assertion)
		for _, d := range arr(get(edge, "dependencies")) {
			n := object(clone(d))
			backing := get(get(d, "entity"), "backing")
			p := filterProofs(get(d, "proofs"), backing, matches)
			_ = n.Set("proofs", p)
			deps = append(deps, n)
			if len(p) == 0 || (assertion != nil && str(get(backing, "id")) == *assertion) {
				remove = true
			}
		}
		for _, d := range arr(get(edge, "readDependencies")) {
			n := object(clone(d))
			backing := get(d, "backing")
			p := filterProofs(get(d, "proofs"), backing, matches)
			_ = n.Set("proofs", p)
			reads = append(reads, n)
			if len(p) == 0 || (assertion != nil && str(get(backing, "id")) == *assertion) {
				remove = true
			}
		}
		id := str(get(edge, "id"))
		if remove {
			c.remove(id)
			continue
		}
		spans := survivingSpans(get(edge, "spans"), proofs)
		if len(spans) == 0 {
			c.remove(id)
			continue
		}
		next := object(clone(edge))
		_ = next.Set("proofs", proofs)
		_ = next.Set("dependencies", deps)
		_ = next.Set("readDependencies", reads)
		_ = next.Set("spans", spans)
		if !deepEqual(next, edge) {
			_ = next.Set("version", num(get(edge, "version"))+1)
			c.put(id, next)
		}
	}
	return l.applyMaintenance(rows, c)
}
func sameRef(a, b any) bool {
	return deepEqual(get(a, "id"), get(b, "id")) && deepEqual(get(a, "version"), get(b, "version")) && deepEqual(get(a, "deletionEpoch"), get(b, "deletionEpoch"))
}

// Strict equality implies the canonical proof key agrees too, while retaining
// proof order, source order, optional-field presence, and complete AND groups.
func intersect(stored, live any) []any {
	out := []any{}
	for _, p := range arr(stored) {
		for _, q := range arr(live) {
			if deepEqual(get(p, "proofId"), get(q, "proofId")) && deepEqual(p, q) {
				out = append(out, p)
				break
			}
		}
	}
	return out
}
func (l *Lifecycle) applyMaintenance(rows []*Object, c *orderedChanges) error {
	originals := map[string]*Object{}
	for _, edge := range rows {
		originals[str(get(edge, "id"))] = edge
	}
	settled := false
	for round := 0; round <= len(rows); round++ {
		changed := false
		for _, original := range rows {
			id := str(get(original, "id"))
			if c.removed[id] {
				continue
			}
			edge := c.updates[id]
			if edge == nil {
				edge = original
			}
			remove := false
			for _, d := range arr(get(edge, "dependencies")) {
				if c.removed[str(get(get(d, "entity"), "originEdgeId"))] {
					remove = true
				}
			}
			for _, d := range arr(get(edge, "readDependencies")) {
				if has(d, "edgeId") && c.removed[str(get(d, "edgeId"))] {
					remove = true
				}
			}
			if remove {
				c.remove(id)
				changed = true
				continue
			}
			invalid := false
			reads := []any{}
			capturedReads := arr(get(original, "readDependencies"))
			for index, dep := range arr(get(edge, "readDependencies")) {
				next := dep
				if has(dep, "edgeId") {
					origin := str(get(dep, "edgeId"))
					before, after := originals[origin], c.updates[origin]
					if before != nil && after != nil && index < len(capturedReads) {
						captured := capturedReads[index]
						if deepEqual(get(captured, "edgeVersion"), get(before, "version")) && sameRef(get(captured, "backing"), get(before, "backing")) && deepEqual(get(captured, "backingFingerprint"), get(before, "backingFingerprint")) {
							proofs := intersect(get(dep, "proofs"), get(after, "proofs"))
							if len(proofs) == 0 {
								invalid = true
							} else {
								n := object(clone(dep))
								_ = n.Set("edgeVersion", get(after, "version"))
								_ = n.Set("backing", get(after, "backing"))
								_ = n.Set("proofs", proofs)
								next = n
							}
						}
					}
				}
				reads = append(reads, next)
			}
			if invalid {
				c.remove(id)
				changed = true
				continue
			}
			if !deepEqual(reads, get(edge, "readDependencies")) {
				next := object(clone(edge))
				_ = next.Set("readDependencies", reads)
				_ = next.Set("version", num(get(original, "version"))+1)
				c.put(id, next)
				changed = true
			}
		}
		if !changed {
			settled = true
			break
		}
	}
	if !settled {
		return errors.New("knowledge_maintenance_cycle")
	}
	// Validate every survivor before the first storage effect.
	for _, id := range c.updateOrder {
		if !c.removed[id] {
			if e := ValidateEdge(c.updates[id]); e != nil {
				return e
			}
		}
	}
	for _, id := range c.removeOrder {
		if e := l.storage.Remove(id); e != nil {
			return e
		}
	}
	for _, id := range c.updateOrder {
		if !c.removed[id] {
			if e := l.storage.Replace(c.updates[id]); e != nil {
				return e
			}
		}
	}
	return nil
}
