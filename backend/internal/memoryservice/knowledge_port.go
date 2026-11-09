package memoryservice

import (
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/knowledge"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"math"
)

// Every operation borrows the supplied authority. No successful result or policy
// is cached, and active writers observe their own uncommitted settings.
type knowledgeExposurePort struct {
	memory *ExposurePort
	policy *modelread.RuntimePolicy
	now    func() int64
}

func (p *knowledgeExposurePort) allowed(reads botruntime.ReadAuthority, c Context) (bool, error) {
	if c.Simulation {
		return false, nil
	}
	allowed, err := p.policy.AllowedScoped(reads, c.SceneID)
	if err != nil || !allowed {
		return false, err
	}
	err = p.memory.sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
		settings, e := (&commitmentExposurePort{p.memory}).settings(scope)
		if e != nil {
			return e
		}
		allowed = truthy(value(settings, "memoryKnowledgeEnabled")) && truthy(value(settings, "memoryKnowledgeAnswerEnabled"))
		return nil
	})
	return allowed, err
}
func (p *knowledgeExposurePort) policyIdentity(reads botruntime.ReadAuthority) (revision float64, err error) {
	err = p.memory.sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
		if e := scope.CheckBinding(p.memory.main, p.memory.authority); e != nil {
			return e
		}
		rows, e := scope.Query("SELECT value FROM config WHERE key='memory-knowledge-policy-revision'")
		if e != nil {
			return e
		}
		defer rows.Close()
		if rows.Next() {
			var raw string
			if e = rows.Scan(&raw); e != nil {
				return e
			}
			v, e := jsjson.Parse(raw)
			if e != nil {
				return e
			}
			n, ok := v.(float64)
			if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 9007199254740991 || math.Trunc(n) != n {
				return errors.New("knowledge_policy_invalid")
			}
			revision = n
		}
		return rows.Err()
	})
	return
}
func (p *knowledgeExposurePort) load(reads botruntime.ReadAuthority, c Context, entities []any, query, backing *Object) (page *Object, err error) {
	err = p.memory.sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
		if e := scope.CheckBinding(p.memory.main, p.memory.authority); e != nil {
			return e
		}
		settings, e := (&commitmentExposurePort{p.memory}).settings(scope)
		if e != nil {
			return e
		}
		authority := p.memory.memory.d.Authority
		authority.NowMillis = p.now
		opts := knowledge.ReadOptions{KnowledgeEnabled: truthy(value(settings, "memoryKnowledgeEnabled")), CommitmentEnabled: truthy(value(settings, "memoryCommitmentsEnabled")), Authority: authority}
		return p.memory.memory.d.Knowledge.Storage.ReadRuntimeCurrent(scope, p.memory.memory.d.Assertions, p.memory.memory.d.Commitments, opts, func(reader *knowledge.Reader) error {
			kc := knowledge.Context{SceneID: c.SceneID, Simulation: c.Simulation, Provenance: "contextual", ExposedEntities: entities}
			if backing == nil {
				page, e = reader.Query(kc, query)
			} else {
				page, e = reader.Discover(kc, backing, query)
			}
			return e
		})
	})
	if err != nil {
		return nil, err
	}
	personal, err := p.policy.PersonalizationAllowedScoped(reads, c.SceneID)
	if err != nil {
		return nil, err
	}
	if personal {
		return page, nil
	}
	edges, _ := value(page, "edges").([]any)
	closure := object(value(page, "supportClosure"))
	support, _ := value(closure, "edges").([]any)
	for _, raw := range edges {
		edge := object(raw)
		if truthy(value(edge, "requiresPersonalization")) {
			return nil, errors.New("personalization_disabled")
		}
		for _, key := range []string{"subject", "object"} {
			if text(object(value(edge, key)), "kind") == "person" {
				return nil, errors.New("personalization_disabled")
			}
		}
	}
	refs := []*Object{}
	for _, raw := range append(append([]any{}, edges...), support...) {
		edge := object(raw)
		refs = append(refs, object(value(edge, "backing")))
		dependencies, _ := value(edge, "dependencies").([]any)
		for _, d := range dependencies {
			refs = append(refs, object(value(object(value(object(d), "entity")), "backing")))
		}
		dependencies, _ = value(edge, "readDependencies").([]any)
		for _, d := range dependencies {
			refs = append(refs, object(value(object(d), "backing")))
		}
	}
	err = p.memory.withReader(reads, func(reader *assertions.Reader) error {
		a, e := assertionauthority.New(reader, p.memory.memory.d.Authority)
		if e != nil {
			return e
		}
		for _, ref := range refs {
			result, e := a.RelationAuthority(assertionauthority.Context{SceneID: c.SceneID, Simulation: c.Simulation}, assertionauthority.Reference{ID: text(ref, "id"), Version: commitmentNumber(ref, "version"), DeletionEpoch: commitmentNumber(ref, "deletionEpoch")})
			if e != nil {
				return e
			}
			if result == nil {
				return errors.New("personalization_disabled")
			}
			record := result.Record
			kind := text(record, "kind")
			scope := object(value(record, "scope"))
			if (kind != "fact" && kind != "claim" && kind != "term") || (text(scope, "kind") == "scene" && truthy(value(scope, "personId"))) {
				return errors.New("personalization_disabled")
			}
		}
		return nil
	})
	return page, err
}
