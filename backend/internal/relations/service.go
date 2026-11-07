// Package relations implements relation admission, review and bounded reads.
// Model exposure and REST content/identity projection remain separate owners.
package relations

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/relationstorage"
	"time"
)

type Context struct {
	SceneID         string
	Simulation      bool
	Provenance      string
	ExposedEntities []*Object
}
type Options struct {
	Authority       assertionauthority.Options
	Enabled         func(Context) (bool, error)
	LearningEnabled func(Context) (bool, error)
	AutoAdmission   func(Context) (bool, error)
	NowMillis       func() int64
	ID              func() string
}
type Service struct {
	Storage    *relationstorage.Store
	assertions *assertions.Store
	options    Options
}

func New(main *sql.DB, authority *recovery.Authority, assertionOptions assertions.Options, options Options) (*Service, error) {
	if options.Enabled == nil || options.LearningEnabled == nil || options.AutoAdmission == nil || options.Authority.DateParser == nil || options.Authority.Compare == nil {
		return nil, errors.New("relation_dependencies_required")
	}
	a, e := assertions.New(main, authority, assertionOptions)
	if e != nil {
		return nil, e
	}
	s, e := relationstorage.New(main, authority)
	if e != nil {
		return nil, e
	}
	return &Service{s, a, options}, nil
}
func (s *Service) WithSession(session *recovery.WriteSession) *Service {
	return &Service{s.Storage.WithSession(session), s.assertions.WithSession(session), s.options}
}
func (s *Service) Initialize(ctx context.Context) error { return s.Storage.Initialize(ctx) }
func (s *Service) Transaction(ctx context.Context, fn func(*Service) error) error {
	return s.Storage.Transaction(ctx, func(b *relationstorage.Store) error {
		return b.WithWriteSession(func(session *recovery.WriteSession) error { return fn(s.WithSession(session)) })
	})
}

// scope uses a single assertions.Reader for relation SQL and current authority.
func (s *Service) scope(ctx context.Context, fn func(*candidates) error) error {
	return s.assertions.Read(ctx, func(reader *assertions.Reader) error {
		r, e := reader.Relations()
		if e != nil {
			return e
		}
		a, e := assertionauthority.New(reader, s.options.Authority)
		if e != nil {
			return e
		}
		return fn(&candidates{storage: s.Storage, read: r, options: s.options, current: func(ctx Context, ref *Object) (*assertionauthority.Result, error) {
			return a.RelationAuthority(assertionauthority.Context{SceneID: ctx.SceneID, Simulation: ctx.Simulation}, assertionauthority.Reference{ID: str(get(ref, "id")), Version: num(get(ref, "version")), DeletionEpoch: num(get(ref, "deletionEpoch"))})
		}})
	})
}

type Rejection struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}
type Admission struct {
	Created  []*Object
	Rejected []Rejection
}
type ListOptions struct {
	PersonID, Status string
	Limit            *float64
}
type Listing struct {
	Items    []*Object
	Coverage string
}

func (s *Service) Admit(ctx context.Context, relationContext Context, backing *Object, proposals []any) (out Admission, err error) {
	if err = s.Storage.RequireTransaction(); err != nil {
		return
	}
	err = s.scope(ctx, func(c *candidates) error { out, err = c.admit(relationContext, backing, proposals); return err })
	return
}
func (s *Service) Review(ctx context.Context, relationContext Context, id string, expected float64, status, actor string) (out *Object, err error) {
	if err = s.Storage.RequireTransaction(); err != nil {
		return
	}
	err = s.scope(ctx, func(c *candidates) error {
		out, err = c.review(relationContext, id, expected, status, actor)
		return err
	})
	return
}
func (s *Service) List(ctx context.Context, relationContext Context, options ListOptions) (out Listing, err error) {
	err = s.scope(ctx, func(c *candidates) error { out, err = c.list(relationContext, options); return err })
	return
}
func (s *Service) PurgeAssertion(id string) ([]string, error) { return s.Storage.PurgeAssertion(id) }
func (s *Service) AssertionChanged(id string) ([]string, error) {
	return s.Storage.AssertionChanged(id)
}

type candidates struct {
	storage *relationstorage.Store
	read    *relationstorage.Reader
	options Options
	current func(Context, *Object) (*assertionauthority.Result, error)
}

func (c *candidates) now() int64 {
	if c.options.NowMillis != nil {
		return c.options.NowMillis()
	}
	return time.Now().UnixMilli()
}

// Date.toISOString rejects values outside the ECMAScript TimeClip range.
func (c *candidates) timestamp() (string, error) {
	ms := c.now()
	if ms < -8640000000000000 || ms > 8640000000000000 {
		return "", errors.New("Invalid time value")
	}
	return isoMillis(ms), nil
}
func (c *candidates) id() string {
	if c.options.ID != nil {
		return c.options.ID()
	}
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func personProofs(view *assertionauthority.Result, person string) []any {
	out := []any{}
	if view == nil {
		return out
	}
	for _, p := range view.Proofs {
		sources := arr(get(p, "sources"))
		w := get(p, "subjectWitness")
		if len(sources) == 0 || get(w, "personId") != person || !has(w, "sourceMessageId") {
			continue
		}
		for _, s := range sources {
			if get(s, "messageId") == get(w, "sourceMessageId") {
				out = append(out, p)
				break
			}
		}
	}
	return out
}
func validBacking(ctx Context, view *assertionauthority.Result, person string) bool {
	if view == nil {
		return false
	}
	r := view.Record
	scope := get(r, "scope")
	return get(scope, "kind") == "scene" && get(scope, "sceneId") == ctx.SceneID && (get(r, "status") == "active" || get(r, "status") == "disputed") && get(r, "lineageStatus") != "quarantined" && (person == "" || (get(scope, "personId") == person && len(personProofs(view, person)) > 0))
}
func sameReference(a, b any) bool {
	return get(a, "id") == get(b, "id") && get(a, "version") == get(b, "version") && get(a, "deletionEpoch") == get(b, "deletionEpoch")
}
func (c *candidates) projection(ctx Context, record *Object) (*Object, error) {
	if ctx.Simulation || get(record, "sceneId") != ctx.SceneID {
		return nil, nil
	}
	enabled, e := c.options.Enabled(ctx)
	if e != nil || !enabled {
		return nil, e
	}
	backing, e := c.current(ctx, object(get(record, "backing")))
	if e != nil {
		return nil, e
	}
	person := str(get(record, "personId"))
	if !validBacking(ctx, backing, person) {
		return nil, nil
	}
	target, e := c.current(ctx, object(get(get(record, "target"), "backing")))
	if e != nil || !validBacking(ctx, target, "") {
		return nil, e
	}
	live := personProofs(backing, person)
	proofs := []any{}
	for _, p := range arr(get(record, "proofs")) {
		for _, q := range live {
			if get(q, "proofId") != get(p, "proofId") {
				continue
			}
			a, e := assertions.CanonicalProofKey(object(q))
			if e != nil {
				return nil, e
			}
			b, e := assertions.CanonicalProofKey(object(p))
			if e != nil {
				return nil, e
			}
			if a == b && deepEqual(q, p) {
				proofs = append(proofs, p)
				break
			}
		}
	}
	if len(proofs) == 0 {
		return nil, nil
	}
	out := record.Clone()
	_ = out.Set("proofs", proofs)
	return clone(out).(*Object), nil
}
func (c *candidates) eligible(ctx Context, r *Object) (bool, error) {
	v, e := c.projection(ctx, r)
	return v != nil, e
}
func (c *candidates) dateCompare(v any, now int64, lower bool) (bool, error) {
	ms, valid, e := c.options.Authority.DateParser.ParseMillis(str(v))
	if e != nil || !valid {
		return false, e
	}
	if lower {
		return ms <= now, nil
	}
	return ms > now, nil
}
func (c *candidates) targetClear(ctx Context, target *Object) (bool, error) {
	v, e := c.current(ctx, object(get(target, "backing")))
	if e != nil {
		return false, e
	}
	if !validBacking(ctx, v, "") || get(v.Record, "status") != "active" || get(v.Record, "epistemicKind") != "explicit" || has(v.Record, "occurredAt") {
		return false, nil
	}
	for _, field := range []string{"validFrom", "validUntil"} {
		value := get(v.Record, field)
		if truthy(value) {
			ok, e := c.dateCompare(value, c.now(), field == "validFrom")
			if e != nil || !ok {
				return false, e
			}
		}
	}
	return true, nil
}
func (c *candidates) autoEligible(record *Object, view *assertionauthority.Result, now int64, conflict bool) (bool, error) {
	if conflict || get(view.Record, "status") != "active" || get(view.Record, "epistemicKind") != "explicit" || get(record, "polarity") != "positive" || get(record, "modality") != "asserted" || get(record, "temporalScope") != "current" || len(arr(get(record, "conditions"))) > 0 || has(record, "occurredAt") || has(record, "validFrom") || has(record, "validUntil") {
		return false, nil
	}
	for _, field := range []string{"validFrom", "validUntil"} {
		if value := get(view.Record, field); truthy(value) {
			ok, e := c.dateCompare(value, now, field == "validFrom")
			if e != nil || !ok {
				return false, e
			}
		}
	}
	if has(view.Record, "occurredAt") || len(arr(get(record, "proofs"))) == 0 {
		return false, nil
	}
	for _, p := range arr(get(record, "proofs")) {
		if get(get(p, "subjectWitness"), "kind") != "self" {
			return false, nil
		}
	}
	return true, nil
}
