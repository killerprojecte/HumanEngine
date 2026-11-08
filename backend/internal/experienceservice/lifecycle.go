package experienceservice

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

func (c *call) eraseTrial(id string) {
	t := c.trial(id)
	if t == nil {
		return
	}
	c.writeTrial(nil, t, "erase", func() {
		for _, item := range t.Positives {
			if o := c.outcome(item.OutcomeID); o != nil {
				c.tombstone(o.RootID)
				c.eraseOutcome(o.ID)
			}
		}
	})
}
func (c *call) erase(p Policy) {
	c.eraseTrial(p.ID)
	take(c.session.AppendIntent(recovery.ExperienceDelete{SceneID: p.SceneID, ID: p.ID}))
	c.tombstone(Key("policy", p.ID))
	c.erasePolicy(p.ID)
}
func (s *Service) Delete(ctx context.Context, scope Context, id string, version int64) error {
	return s.run(ctx, func(c *call) {
		p := c.get(scope, id, &version)
		for _, oid := range append(append([]string{}, p.SupportIDs...), p.CounterexampleIDs...) {
			if o := c.outcome(oid); o != nil {
				c.tombstone(o.RootID)
				c.eraseOutcome(oid)
			}
		}
		c.erase(p)
	})
}
func (s *Service) RevokeSource(ctx context.Context, scene, message string) error {
	return s.run(ctx, func(c *call) {
		c.tombstone(SourceKey(scene, message))
		for _, o := range c.sourceOutcomes(scene, message) {
			for _, t := range c.trials(scene) {
				for _, positive := range t.Positives {
					if positive.OutcomeID == o.ID {
						c.eraseTrial(t.PolicyID)
						break
					}
				}
			}
			for _, p := range c.policiesForOutcome(scene, o.ID) {
				if contains(p.SupportIDs, o.ID) || contains(p.CounterexampleIDs, o.ID) {
					c.erase(p)
				}
			}
			c.tombstone(o.RootID)
			c.eraseOutcome(o.ID)
		}
	})
}

type AdminPolicy struct {
	Policy
	Expired       bool             `json:"expired"`
	Trial         *Trial           `json:"trial,omitempty"`
	TrialEligible bool             `json:"trialEligible"`
	Sources       []Outcome        `json:"sources"`
	Validation    *Receipt         `json:"validation,omitempty"`
	Uses          map[string]int64 `json:"uses"`
}
type AdminResult struct {
	Items    []AdminPolicy `json:"items"`
	Coverage string        `json:"coverage"`
}

func (s *Service) AdminList(ctx context.Context, scope Context, options ListOptions) (out AdminResult, err error) {
	out.Items = []AdminPolicy{}
	err = s.run(ctx, func(c *call) {
		rows := c.list(scope, options)
		out.Coverage = rows.Coverage
		for _, p := range rows.Items {
			item := AdminPolicy{Policy: p, Expired: expired(p.ExpiresAt, c.now()), Trial: c.trial(p.ID), TrialEligible: c.current(p) != nil, Sources: []Outcome{}, Uses: c.useCounts(scope.SceneID, p.ID)}
			for _, id := range p.SupportIDs {
				if o := c.outcome(id); o != nil && o.SceneID == scope.SceneID {
					item.Sources = append(item.Sources, *o)
				}
			}
			if p.ValidationID != "" {
				item.Validation = c.receipt(p.ValidationID)
			}
			out.Items = append(out.Items, item)
		}
	})
	return
}

// RuntimeRecallPreparation joins deletion to ingress's owned recall transaction.
// Offline recovery remains owned by recoverydomains; live code cannot replay
// authority events or fabricate missing instruction/positive evidence.
func (s *Service) RuntimeRecallPreparation() sourceevents.RecallPreparation {
	return func(ctx context.Context, ws *recovery.WriteSession, _ botruntime.ReadAuthority, scene botruntime.Scene, id string) (func() error, error) {
		bound := s.WithSession(ws)
		if bound.bindingError != nil {
			return nil, bound.bindingError
		}
		return func() error { return bound.RevokeSource(ctx, scene.ID, id) }, nil
	}
}

func (p AdminPolicy) MarshalJSON() (raw []byte, err error) {
	err = guarded(func() {
		o := take(jsjson.Parse(encoded(p.Policy))).(*jsjson.Object)
		check(o.Set("expired", p.Expired))
		check(o.Set("trialEligible", p.TrialEligible))
		for k, v := range map[string]any{"sources": p.Sources, "uses": p.Uses} {
			check(o.Set(k, take(jsjson.Parse(encoded(v)))))
		}
		if p.Trial != nil {
			check(o.Set("trial", take(jsjson.Parse(encoded(p.Trial)))))
		}
		if p.Validation != nil {
			check(o.Set("validation", take(jsjson.Parse(encoded(p.Validation)))))
		}
		raw = take(jsjson.Marshal(o))
	})
	return
}
