package memoryservice

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/identityprivacy"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryread"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// IdentityExposure owns administrator-verified identity views, never assertion
// parent authority. Person visibility is rebuilt from original authorized rows.
type IdentityExposure struct {
	states *StateExposure
	port   *identityExposurePort
}
type identityExposurePort struct {
	memory *ExposurePort
	policy *modelread.RuntimePolicy
}

func NewIdentityExposure(memory *ExposurePort, policy *modelread.RuntimePolicy) (*IdentityExposure, error) {
	if memory == nil || policy == nil {
		return nil, errors.New("identity_exposure_owners_required")
	}
	if err := policy.CheckBinding(memory.authority, memory.main); err != nil {
		return nil, err
	}
	port := &identityExposurePort{memory, policy}
	states, err := NewStateExposure(port, nil)
	if err != nil {
		return nil, err
	}
	return &IdentityExposure{states, port}, nil
}
func (e *IdentityExposure) Query(ctx context.Context, c *botruntime.TurnContext, person string) ([]*Object, error) {
	return e.states.Query(ctx, c, person)
}
func (e *IdentityExposure) Deliver(ctx context.Context, c *botruntime.TurnContext, values []any) ([]*Object, error) {
	return e.states.Deliver(ctx, c, values)
}
func (e *IdentityExposure) CaptureGuard(c *botruntime.TurnContext) *botruntime.ToolResultGuard {
	return e.states.CaptureGuard(c)
}
func (e *IdentityExposure) HasExposure(c *botruntime.TurnContext) bool {
	return e.states.HasExposure(c)
}
func (e *IdentityExposure) ReadDelivered(ctx context.Context, c *botruntime.TurnContext) ([]*Object, error) {
	return e.states.ReadDelivered(ctx, c)
}
func (p *identityExposurePort) Query(ctx context.Context, c Context, input any) (out []*Object, err error) {
	out = []*Object{}
	if c.Simulation {
		return out, nil
	}
	person, ok := input.(string)
	if !ok {
		return nil, errors.New("identity_endpoint_invalid")
	}
	allowed, err := p.policy.PersonalizationAllowed(ctx, c.SceneID)
	if err != nil || !allowed {
		return out, err
	}
	if err = p.memory.memory.RefreshSupport(ctx); err != nil {
		return nil, err
	}
	err = p.memory.sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		var err error
		out, err = p.current(reads, c, person)
		return err
	})
	return
}
func identityPerson(values []*Object) (string, bool) {
	if len(values) != 1 || values[0] == nil {
		return "", false
	}
	page := object(value(values[0], "page"))
	if page == nil {
		return "", false
	}
	person, ok := value(page, "personId").(string)
	return person, ok
}

// Source catches invalid registry/permission/current-view errors during validity
// and retains a false guard. Query itself still surfaces its concrete error.
func (p *identityExposurePort) Valid(ctx context.Context, c Context, values []*Object) (bool, error) {
	person, ok := identityPerson(values)
	if !ok {
		return false, nil
	}
	current, err := p.Query(ctx, c, person)
	return err == nil && sameStateValues(values, current), nil
}
func (p *identityExposurePort) ValidScoped(reads botruntime.ReadAuthority, c Context, values []*Object) (bool, error) {
	person, ok := identityPerson(values)
	if !ok {
		return false, nil
	}
	current, err := p.current(reads, c, person)
	return err == nil && sameStateValues(values, current), nil
}
func sameStateValues(a, b []*Object) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !deepEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}
func (p *identityExposurePort) SourceIDs(values []*Object) ([]string, error) {
	out := []string{}
	for _, v := range values {
		page := object(value(v, "page"))
		items, _ := value(page, "items").([]any)
		for _, raw := range items {
			item := object(raw)
			aliases, _ := value(item, "aliases").([]any)
			for _, raw := range aliases {
				alias := object(raw)
				if v, present := alias.Get("sourceMessageId"); present && truthy(v) {
					id, ok := v.(string)
					if !ok || id == "" {
						return nil, errors.New("state_exposure_source_invalid")
					}
					out = append(out, id)
				}
			}
		}
	}
	return out, nil
}
func (p *identityExposurePort) current(reads botruntime.ReadAuthority, c Context, person string) (out []*Object, err error) {
	out = []*Object{}
	if c.Simulation {
		return out, nil
	}
	allowed, err := p.policy.PersonalizationAllowedScoped(reads, c.SceneID)
	if err != nil || !allowed {
		return out, err
	}
	err = p.memory.sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
		if err := scope.CheckBinding(p.memory.main, p.memory.authority); err != nil {
			return err
		}
		return p.memory.withReader(reads, func(r *assertions.Reader) error {
			scene, err := r.Scene(c.SceneID)
			if err != nil {
				return err
			}
			if scene == nil {
				return errors.New("identity_scene_missing")
			}
			read, err := memoryread.New(r, p.memory.memory.d.Authority)
			if err != nil {
				return err
			}
			records, err := read.List(memoryread.Input{SceneID: c.SceneID})
			if err != nil {
				return err
			}
			people := map[string]bool{}
			for _, record := range records {
				recordScope := object(value(record, "scope"))
				if !truthy(value(record, "internalUse")) && text(recordScope, "kind") == "scene" && text(recordScope, "sceneId") == c.SceneID {
					if person := text(recordScope, "personId"); person != "" {
						people[person] = true
					}
				}
			}
			state, err := identityprivacy.ReadRegistry(scope)
			if err != nil {
				return err
			}
			members, err := identityprivacy.Members(state, text(scene, "adapterId"), text(scene, "accountId"), person, people)
			if err != nil {
				return err
			}
			anchorPresent := false
			for _, member := range members {
				if text(member, "personId") == person {
					anchorPresent = true
				}
			}
			if !anchorPresent {
				return nil
			}
			authority, err := assertionauthority.New(r, p.memory.memory.d.Authority)
			if err != nil {
				return err
			}
			events, err := r.AuthoritySourceEvents()
			if err != nil {
				return err
			}
			rawAliases, _ := value(state, "aliases").([]any)
			items := []any{}
			totalAliases := 0
			for _, member := range members {
				now := float64(time.Now().UnixMilli())
				if nowFn := p.memory.memory.d.Authority.NowMillis; nowFn != nil {
					now = float64(nowFn())
				}
				if math.IsNaN(now) || math.IsInf(now, 0) || now < 0 || now > 9007199254740991 || math.Trunc(now) != now {
					return errors.New("identity_clock_invalid")
				}
				aliases := []any{}
				for _, raw := range rawAliases {
					alias := object(raw)
					endpoint := object(value(alias, "endpoint"))
					if text(alias, "scopeSceneId") != c.SceneID || !deepEqual(endpoint, member) {
						continue
					}
					active := true
					for _, bound := range []struct {
						key   string
						start bool
					}{{"validFrom", true}, {"validUntil", false}} {
						if v := value(alias, bound.key); v != nil {
							instant, ok, err := p.memory.memory.d.Authority.DateParser.ParseMillis(v.(string))
							if err != nil {
								return err
							}
							if !ok || bound.start && float64(instant) > now || !bound.start && now >= float64(instant) {
								active = false
							}
						}
					}
					if !active {
						continue
					}
					source := object(value(alias, "source"))
					if text(source, "kind") == "observation" {
						if text(source, "sceneId") != c.SceneID || text(source, "senderId") != text(member, "personId") {
							continue
						}
						if value(source, "eventRef") == nil {
							registered, err := events.Registered(text(source, "sceneId"), text(source, "messageId"))
							if err != nil {
								return err
							}
							if registered != nil {
								continue
							}
						}
						sourceScene, err := r.Scene(text(source, "sceneId"))
						if err != nil {
							return err
						}
						if sourceScene == nil || text(sourceScene, "adapterId") != text(member, "adapterId") || text(sourceScene, "accountId") != text(member, "accountId") {
							continue
						}
						current, err := authority.DomainSourceCurrent(source)
						if err != nil {
							return err
						}
						if !current {
							continue
						}
					}
					view := epObj("label", value(alias, "label"), "validFrom", value(alias, "validFrom"), "validUntil", value(alias, "validUntil"), "sourceKind", value(source, "kind"))
					if text(source, "kind") == "observation" {
						_ = view.Set("sourceMessageId", value(source, "messageId"))
					}
					aliases = append(aliases, view)
				}
				totalAliases += len(aliases)
				items = append(items, epObj("personId", value(member, "personId"), "aliases", aliases))
			}
			if len(members) > 32 || totalAliases > 64 {
				return errors.New("identity_query_limited")
			}
			out = append(out, epObj("stateKind", "identity_view", "page", epObj("sceneId", c.SceneID, "revision", value(state, "revision"), "personId", person, "coverage", "complete", "interpretation", "administrator_verified_links_not_read_authority_or_personal_facts", "items", items)))
			return nil
		})
	})
	return
}

// ComposeExtension adds only this concrete owner; the app retains source guard
// order and gates Execute before Query. Preparation preserves receipt pointers.
func (e *IdentityExposure) ComposeExtension(base botruntime.Extension) *botruntime.Extension {
	ext := base
	ext.CaptureToolResultGuards = func(c *botruntime.TurnContext) []*botruntime.ToolResultGuard {
		out := []*botruntime.ToolResultGuard{}
		if base.CaptureToolResultGuards != nil {
			out = append(out, base.CaptureToolResultGuards(c)...)
		}
		if guard := e.CaptureGuard(c); guard != nil {
			out = append(out, guard)
		}
		return out
	}
	ext.Delivered = func(name string, v any, c *botruntime.TurnContext, input any) (any, error) {
		if name == "memory.identities" {
			var selected []any
			switch values := v.(type) {
			case []any:
				selected = values
			case []*Object:
				selected = make([]any, len(values))
				for i, body := range values {
					selected[i] = body
				}
			}
			if selected != nil {
				out, err := e.Deliver(context.Background(), c, selected)
				if err != nil {
					return nil, err
				}
				values := make([]any, len(out))
				for i, body := range out {
					values[i] = body
				}
				v = values
			}
		}
		if base.Delivered != nil {
			return base.Delivered(name, v, c, input)
		}
		return v, nil
	}
	return &ext
}
