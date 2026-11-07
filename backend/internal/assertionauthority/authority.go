package assertionauthority

import (
	"errors"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

type readPort interface {
	AssertReady() error
	CheckScope() error
	Record(string) (*Object, error)
	Proofs(string) ([]any, error)
	State(string) (assertions.State, error)
	Revision(string, float64, string) (*Object, error)
	Recalled(string, string) (bool, error)
	Scene(string) (*Object, error)
}
type sourcePort interface {
	Registered(string, string) (*sourceevents.Metadata, error)
	Current(sourceevents.Ref, sourceevents.Identity) (sourceevents.Eligibility, error)
	HasEdits(sourceevents.Identity) (bool, error)
}
type Options struct {
	DateParser *jsdate.Parser
	// Compare must be the source deployment's native localeCompare (for equal
	// observation instants). Requiring it avoids a silent Go lexical fallback.
	Compare   func(string, string) (int, error)
	NowMillis func() int64
}
type Authority struct {
	read    readPort
	events  sourcePort
	options Options
}

// New is the production composition boundary. All providers are obtained from
// this Reader, not supplied as independently acquired stores. The authority is
// synchronous and expires with the existing Reader scope.
func New(reader *assertions.Reader, options Options) (*Authority, error) {
	if reader == nil {
		return nil, errors.New("assertion_authority_reader_required")
	}
	if options.DateParser == nil || options.Compare == nil {
		return nil, errors.New("assertion_authority_time_policy_required")
	}
	if e := reader.CheckScope(); e != nil {
		return nil, e
	}
	events, e := reader.AuthoritySourceEvents()
	if e != nil {
		return nil, e
	}
	return &Authority{read: reader, events: events, options: options}, nil
}
func (a *Authority) recalled(source any) (bool, error) {
	return a.read.Recalled(str(get(source, "sceneId")), str(get(source, "messageId")))
}

// DomainSourceCurrent qualifies current use, never historical support/privacy.
func (a *Authority) DomainSourceCurrent(source *Object) (bool, error) {
	if e := a.read.CheckScope(); e != nil {
		return false, e
	}
	recalled, e := a.recalled(source)
	if e != nil || recalled {
		return false, e
	}
	registered, e := a.events.Registered(str(get(source, "sceneId")), str(get(source, "messageId")))
	if e != nil {
		return false, e
	}
	refValue := get(source, "eventRef")
	if !truthy(refValue) && registered == nil {
		return true, nil
	}
	scene, e := a.read.Scene(str(get(source, "sceneId")))
	if e != nil || scene == nil {
		return false, e
	}
	identity := sourceevents.Identity{SceneID: str(get(source, "sceneId")), AdapterID: str(get(scene, "adapterId")), AccountID: str(get(scene, "accountId")), MessageID: str(get(source, "messageId")), SenderID: str(get(source, "senderId"))}
	if registered != nil && (registered.Identity.AdapterID != identity.AdapterID || registered.Identity.AccountID != identity.AccountID || registered.Identity.SenderID != identity.SenderID) {
		return false, nil
	}
	if truthy(refValue) {
		ref, e := sourceevents.ParseRef(refValue)
		if e != nil {
			return false, e
		}
		eligible, e := a.events.Current(ref, identity)
		return eligible.Eligible, e
	}
	edited, e := a.events.HasEdits(identity)
	return !edited, e
}
func (a *Authority) sourcesCurrent(value any) (bool, error) {
	sources, e := array(value)
	if e != nil {
		return false, e
	}
	for _, source := range sources {
		ok, e := a.DomainSourceCurrent(object(source))
		if e != nil || !ok {
			return false, e
		}
	}
	return true, nil
}
func (a *Authority) CurrentApplicable(memory *Object) (bool, error) {
	if e := a.read.CheckScope(); e != nil {
		return false, e
	}
	now := time.Now().UnixMilli()
	if a.options.NowMillis != nil {
		now = a.options.NowMillis()
	}
	return a.applicable(memory, now, path{}, positiveCache{})
}
func (a *Authority) applicable(memory *Object, now int64, p path, cache positiveCache) (bool, error) {
	id := str(get(memory, "id"))
	status := get(memory, "status")
	if p[id] || (status != "active" && status != "disputed") {
		return false, nil
	}
	if value, ok := cache[id]; ok {
		return value, nil
	}
	for _, field := range []string{"validFrom", "validUntil"} {
		if !has(memory, field) {
			continue
		}
		ms, valid, e := a.options.DateParser.ParseMillis(str(get(memory, field)))
		if e != nil {
			return false, e
		}
		if valid && ((field == "validFrom" && ms > now) || (field == "validUntil" && ms <= now)) {
			return false, nil
		}
	}
	if get(memory, "lineageStatus") != "verified" {
		sources, e := fieldArray(memory, "sources")
		if e != nil {
			return false, e
		}
		for _, source := range sources {
			recalled, e := a.recalled(source)
			if e != nil || recalled {
				return false, e
			}
			ok, e := a.DomainSourceCurrent(object(source))
			if e != nil || !ok {
				return false, e
			}
		}
		return true, nil
	}
	proofs, e := a.read.Proofs(id)
	if e != nil {
		return false, e
	}
	for _, v := range proofs {
		proof := object(v)
		if proof == nil {
			return false, errors.New("assertion_proof_object_required")
		}
		if truthy(get(proof, "invalidatedBy")) {
			continue
		}
		sources, e := fieldArray(proof, "sources")
		if e != nil {
			return false, e
		}
		if len(sources) == 0 {
			continue
		}
		ok, e := a.ProofParentsAvailable(object(get(memory, "scope")), proof, "current")
		if e != nil {
			return false, e
		}
		if !ok {
			continue
		}
		ok, e = a.sourcesCurrent(sources)
		if e != nil {
			return false, e
		}
		if !ok {
			continue
		}
		// Source applicability calls recalled and sourceCurrent independently.
		for _, source := range sources {
			recalled, e := a.recalled(source)
			if e != nil {
				return false, e
			}
			if recalled {
				ok = false
				break
			}
			current, e := a.DomainSourceCurrent(object(source))
			if e != nil {
				return false, e
			}
			if !current {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		parents, e := fieldArray(proof, "parents")
		if e != nil {
			return false, e
		}
		for _, value := range parents {
			parent, e := a.read.Record(str(value))
			if e != nil {
				return false, e
			}
			if parent == nil {
				ok = false
				break
			}
			supported, e := a.Supported(parent)
			if e != nil {
				return false, e
			}
			if !supported {
				ok = false
				break
			}
			applicable, e := a.applicable(parent, now, extend(p, id), cache)
			if e != nil {
				return false, e
			}
			if !applicable {
				ok = false
				break
			}
		}
		if ok {
			cache[id] = true
			return true, nil
		}
	}
	return false, nil
}
func (a *Authority) FilterHistorySupports(scope *Object, proofs []any) ([]any, error) {
	if e := a.read.CheckScope(); e != nil {
		return nil, e
	}
	return a.filterHistorySupports(scope, proofs, newParentTraversal())
}
func (a *Authority) filterHistorySupports(scope *Object, proofs []any, t *parentTraversal) ([]any, error) {
	out := []any{}
	for _, v := range proofs {
		proof := object(v)
		if proof == nil {
			return nil, errors.New("assertion_proof_object_required")
		}
		sources, e := fieldArray(proof, "sources")
		if e != nil {
			return nil, e
		}
		if e = t.spend(1 + len(sources)); e != nil {
			return nil, e
		}
		if truthy(get(proof, "invalidatedBy")) || len(sources) == 0 {
			continue
		}
		ok, e := a.sourcesNotRecalled(sources, nil)
		if e != nil {
			return nil, e
		}
		if !ok {
			continue
		}
		ok, e = a.boundParents(scope, proof, "history", path{}, t)
		if e != nil {
			return nil, e
		}
		if ok {
			out = append(out, proof)
		}
	}
	return out, nil
}
func (a *Authority) HistorySupports(id string) ([]any, error) {
	record, e := a.read.Record(id)
	if e != nil {
		return nil, e
	}
	if record == nil {
		return []any{}, nil
	}
	proofs, e := a.read.Proofs(id)
	if e != nil {
		return nil, e
	}
	return a.FilterHistorySupports(object(get(record, "scope")), proofs)
}
func (a *Authority) currentProofs(memory *Object) ([]any, error) {
	proofs, e := a.HistorySupports(str(get(memory, "id")))
	if e != nil {
		return nil, e
	}
	out := []any{}
	for _, v := range proofs {
		proof := object(v)
		ok, e := a.ProofParentsAvailable(object(get(memory, "scope")), proof, "current")
		if e != nil {
			return nil, e
		}
		if !ok {
			continue
		}
		ok, e = a.sourcesCurrent(get(proof, "sources"))
		if e != nil {
			return nil, e
		}
		if ok {
			out = append(out, proof)
		}
	}
	return out, nil
}
func proofSources(proofs []any) ([]any, error) {
	out := []any{}
	for _, p := range proofs {
		sources, e := fieldArray(p, "sources")
		if e != nil {
			return nil, e
		}
		out = append(out, sources...)
	}
	return uniqueSources(out)
}
func (a *Authority) Sanitize(memory *Object) (*Object, error) {
	if e := a.read.CheckScope(); e != nil {
		return nil, e
	}
	if get(get(memory, "scope"), "kind") == "sharedPublicKnowledge" {
		record := memory.Clone()
		record.Delete("observedAt")
		_ = record.Set("sources", []any{})
		record.Delete("supersedesId")
		return record, nil
	}
	proofs, e := a.currentProofs(memory)
	if e != nil {
		return nil, e
	}
	var sources []any
	if get(memory, "lineageStatus") == "verified" {
		sources, e = proofSources(proofs)
	} else {
		sources = []any{}
		original, err := fieldArray(memory, "sources")
		if err != nil {
			return nil, err
		}
		for _, source := range original {
			ok, err := a.DomainSourceCurrent(object(source))
			if err != nil {
				return nil, err
			}
			if ok {
				sources = append(sources, source)
			}
		}
	}
	if e != nil {
		return nil, e
	}
	record := memory.Clone()
	_ = record.Set("sources", sources)
	return a.WithObservation(record, proofs)
}

type Context struct {
	SceneID    string
	Simulation bool
}
type Reference struct {
	ID                     string
	Version, DeletionEpoch float64
}
type Result struct {
	Record *Object
	Proofs []any
}

func (r *Result) Value() any {
	if r == nil {
		return nil
	}
	o := new(Object)
	_ = o.Set("record", r.Record)
	_ = o.Set("proofs", r.Proofs)
	return o
}
func result(record *Object, proofs []any) (*Result, error) {
	out := []any{}
	for _, v := range proofs {
		proof := object(v)
		key, e := assertions.CanonicalProofKey(proof)
		if e != nil {
			return nil, e
		}
		copy := clone(proof).(*Object)
		_ = copy.Set("proofId", key)
		out = append(out, copy)
	}
	return &Result{record, out}, nil
}
func (a *Authority) HistoricalAssertion(ctx Context, ref Reference) (*Result, error) {
	if e := a.read.AssertReady(); e != nil {
		return nil, e
	}
	if ctx.Simulation {
		return nil, nil
	}
	current, e := a.read.Record(ref.ID)
	if e != nil {
		return nil, e
	}
	state, e := a.read.State(ref.ID)
	if e != nil {
		return nil, e
	}
	if current == nil || get(get(current, "scope"), "kind") != "scene" || get(get(current, "scope"), "sceneId") != ctx.SceneID || state.Deleted || float64(state.DeletionEpoch) != ref.DeletionEpoch {
		return nil, nil
	}
	ok, e := a.Supported(current)
	if e != nil || !ok {
		return nil, e
	}
	revision, e := a.read.Revision(ref.ID, ref.Version, scopeKey(get(current, "scope")))
	if e != nil || revision == nil {
		return nil, e
	}
	record := object(get(revision, "record"))
	scope := object(get(record, "scope"))
	if get(scope, "kind") != "scene" || get(scope, "sceneId") != ctx.SceneID || !ParentScopeAllows(object(get(current, "scope")), scope) {
		return nil, nil
	}
	proofs, e := fieldArray(revision, "supports")
	if e != nil {
		return nil, e
	}
	proofs, e = a.FilterHistorySupports(scope, proofs)
	if e != nil {
		return nil, e
	}
	if get(record, "lineageStatus") == "verified" && len(proofs) == 0 {
		return nil, nil
	}
	sources, e := proofSources(proofs)
	if e != nil {
		return nil, e
	}
	record = record.Clone()
	_ = record.Set("sources", sources)
	record, e = a.WithObservation(record, proofs)
	if e != nil {
		return nil, e
	}
	return result(record, proofs)
}
func (a *Authority) RelationAuthority(ctx Context, ref Reference) (*Result, error) {
	if e := a.read.AssertReady(); e != nil {
		return nil, e
	}
	if ctx.Simulation {
		return nil, nil
	}
	record, e := a.read.Record(ref.ID)
	if e != nil {
		return nil, e
	}
	state, e := a.read.State(ref.ID)
	if e != nil {
		return nil, e
	}
	if record == nil || get(get(record, "scope"), "kind") != "scene" || get(get(record, "scope"), "sceneId") != ctx.SceneID || state.Deleted || float64(state.Version) != ref.Version || float64(state.DeletionEpoch) != ref.DeletionEpoch {
		return nil, nil
	}
	ok, e := a.Supported(record)
	if e != nil || !ok {
		return nil, e
	}
	ok, e = a.CurrentApplicable(record)
	if e != nil || !ok {
		return nil, e
	}
	proofs, e := a.currentProofs(record)
	if e != nil {
		return nil, e
	}
	record, e = a.Sanitize(record)
	if e != nil {
		return nil, e
	}
	return result(record, proofs)
}
