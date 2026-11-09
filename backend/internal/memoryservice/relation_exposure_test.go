package memoryservice

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"testing"
)

func TestRelationExposureOriginalScopeAndWriter(t *testing.T) {
	s, f, options := outerSetup(t)
	ctx := context.Background()
	f.main.SetMaxOpenConns(1)
	in, candidate := outerInput(t, "relation-seed")
	candidate.Set("relations", []any{outerRelation(t)})
	seed, err := s.Propose(ctx, in, candidate, options)
	requireOuterSaved(t, seed, err)
	if seed.Relations == nil || len(seed.Relations.Created) != 1 {
		t.Fatal("missing actual relation seed", seed.Relations)
	}
	turn := exposureLiveTurn(t, s, f)
	settings, err := jsjson.Parse(string(contract.DefaultSettingsInput()))
	exposureMust(t, err)
	settingsObject := settings.(*Object)
	settingsObject.Set("memoryGraphEnabled", true)
	settingsObject.Set("memoryGraphAnswerEnabled", true)
	encodedSettings, err := jsjson.Stringify(settingsObject)
	exposureMust(t, err)
	exposureWrite(t, f, "INSERT OR REPLACE INTO config(key,value) VALUES('settings',?)", encodedSettings)
	port, err := NewExposurePort(s, f.events, f.main, f.recovery)
	exposureMust(t, err)
	ordinary, err := NewModelMemoryExposure(port, func(context.Context, *botruntime.TurnContext, []Reference) error { return nil })
	exposureMust(t, err)
	relation, err := NewRelationExposure(port, ordinary, nil)
	exposureMust(t, err)
	values, err := relation.Query(ctx, turn, "p")
	exposureMust(t, err)
	if len(values) != 1 {
		t.Fatal("real prepared relation", len(values))
	}
	if relation.CaptureLearningGuard(turn) != nil {
		t.Fatal("preparation grants entity authority")
	}
	copied, err := relation.Deliver(ctx, turn, []any{values[0].Clone()})
	exposureMust(t, err)
	if len(copied) != 0 {
		t.Fatal("copy accepted")
	}
	foreign := exposureLiveTurn(t, s, f)
	copied, err = relation.Deliver(ctx, foreign, []any{values[0]})
	exposureMust(t, err)
	if len(copied) != 0 {
		t.Fatal("foreign turn accepted")
	}
	oldScene := foreign.Scene
	foreignValues, err := relation.Query(ctx, foreign, "p")
	exposureMust(t, err)
	foreign.Scene.ID = "other"
	copied, err = relation.Deliver(ctx, foreign, []any{foreignValues[0]})
	foreign.Scene = oldScene
	exposureMust(t, err)
	if len(copied) != 0 {
		t.Fatal("changed scene accepted")
	}
	delivered, err := relation.Deliver(ctx, turn, []any{values[0]})
	exposureMust(t, err)
	if len(delivered) != 1 || delivered[0] != values[0] {
		t.Fatal("original receipt identity lost")
	}
	entities, err := relation.ReadExposedEntities(ctx, turn)
	exposureMust(t, err)
	if len(entities) != 1 {
		t.Fatal("entity authority missing")
	}
	guard := relation.CaptureLearningGuard(turn)
	if !exposureCheck(t, f, guard) {
		t.Fatal("current guard invalid")
	}
	var expired botruntime.ReadAuthority
	exposureMust(t, f.events.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error { expired = reads; return nil }))
	if ok, err := guard.Check(expired); err == nil || ok {
		t.Fatal("expired authority accepted", ok, err)
	}
	_, other, _ := outerSetup(t)
	exposureMust(t, other.events.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		ok, err := guard.Check(reads)
		if err == nil || ok {
			t.Fatal("foreign database accepted", ok, err)
		}
		return nil
	}))
	exposureMust(t, f.recovery.WithWriteSession(ctx, f.main, func(writer *recovery.WriteSession) error {
		return f.events.WithSession(writer).WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			ok, err := guard.Check(reads)
			if err != nil {
				return err
			}
			if !ok {
				t.Fatal("same writer guard rejected")
			}
			return nil
		})
	}))
	exposureMust(t, f.recovery.WithWriteSession(ctx, f.main, func(writer *recovery.WriteSession) error {
		settingsObject.Set("memoryGraphAnswerEnabled", false)
		revoked, err := jsjson.Stringify(settingsObject)
		if err != nil {
			return err
		}
		if _, err := writer.Exec("UPDATE config SET value=? WHERE key='settings'", revoked); err != nil {
			return err
		}
		return f.events.WithSession(writer).WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			ok, err := guard.Check(reads)
			if ok {
				t.Fatal("uncommitted writer flag revocation ignored")
			}
			return err
		})
	}))
}

func TestRelationExposureCompleteGroupDetachedAndExpiry(t *testing.T) {
	for _, scenario := range []string{"complete", "late-conflict", "detached-cancel", "expiry"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			s, f, options := outerSetup(t)
			turn := exposureLiveTurn(t, s, f)
			exposureWrite(t, f, "UPDATE scenes SET value=? WHERE id='s'", `{"id":"s","kind":"group","adapterId":"adapter","accountId":"account","platformSceneId":"s","participationEnabled":true,"learningEnabled":true,"proactiveEnabled":false}`)
			settings, err := jsjson.Parse(string(contract.DefaultSettingsInput()))
			exposureMust(t, err)
			flags := settings.(*Object)
			flags.Set("memoryGraphEnabled", true)
			flags.Set("memoryGraphAnswerEnabled", true)
			encoded, err := jsjson.Stringify(flags)
			exposureMust(t, err)
			exposureWrite(t, f, "INSERT OR REPLACE INTO config(key,value) VALUES('settings',?)", encoded)
			in, candidate := outerInput(t, "group-first")
			candidate.Set("relations", []any{outerRelation(t)})
			seed, err := s.Propose(ctx, in, candidate, options)
			requireOuterSaved(t, seed, err)
			if seed.Relations == nil || len(seed.Relations.Created) != 1 {
				t.Fatal("real seed relation missing")
			}
			target := object(value(seed.Relations.Created[0], "target"))
			targetID := text(target, "id")
			ref := object(value(target, "backing"))
			add := func(message, polarity string) {
				in, candidate := outerInput(t, message)
				in.Set("provenance", "contextual")
				in.Set("dependencyMemoryIds", []any{})
				in.Set("dependencyParentReferences", []any{})
				in.Set("exposedEntities", []any{relationObject("entityId", targetID, "backingRef", ref.Clone())})
				proposal := outerRelation(t)
				proposal.Set("target", relationObject("kind", "existing", "entityId", targetID))
				proposal.Set("polarity", polarity)
				if scenario == "expiry" {
					proposal.Set("validUntil", "2026-10-29T12:00:00.000Z")
				}
				candidate.Set("relations", []any{proposal})
				saved, err := s.Propose(ctx, in, candidate, options)
				requireOuterSaved(t, saved, err)
				if saved.Relations == nil || len(saved.Relations.Created) != 1 {
					t.Fatalf("actual existing target proposal failed %+v", saved.Relations)
				}
			}
			add("group-second", "positive")
			port, err := NewExposurePort(s, f.events, f.main, f.recovery)
			exposureMust(t, err)
			ordinary, err := NewModelMemoryExposure(port, func(context.Context, *botruntime.TurnContext, []Reference) error { return nil })
			exposureMust(t, err)
			now := float64(1793188800000)
			relation, err := NewRelationExposure(port, ordinary, func() float64 { return now })
			exposureMust(t, err)
			values, err := relation.Query(ctx, turn, "p")
			exposureMust(t, err)
			if len(values) != 2 {
				t.Fatal("real two-backing group absent", len(values))
			}
			partial, err := relation.Deliver(ctx, turn, []any{values[0]})
			exposureMust(t, err)
			if len(partial) != 0 || len(ordinary.ReadExposed(turn)) != 0 || relation.CaptureLearningGuard(turn) != nil {
				t.Fatal("partial group granted memory or entity authority")
			}
			complete, err := relation.Deliver(ctx, turn, selectedMemory(values))
			exposureMust(t, err)
			if len(complete) != 2 || complete[0] != values[0] || complete[1] != values[1] {
				t.Fatal("complete original group lost")
			}
			guard := relation.CaptureLearningGuard(turn)
			if !exposureCheck(t, f, guard) {
				t.Fatal("initial exact group guard invalid")
			}
			switch scenario {
			case "late-conflict":
				add("group-third-conflict", "negative")
				if exposureCheck(t, f, guard) {
					t.Fatal("uncaptured counterclaim accepted")
				}
			case "detached-cancel":
				turn.Signal().Abort("synthetic turn completed")
				if !exposureCheck(t, f, guard) {
					t.Fatal("captured learning authority incorrectly tied to turn cancellation")
				}
				entities, err := relation.ReadExposedEntities(ctx, turn)
				exposureMust(t, err)
				if len(entities) != 0 {
					t.Fatal("aborted turn exposed fresh entities")
				}
			case "expiry":
				now += 2 * 24 * 60 * 60 * 1000
				if exposureCheck(t, f, guard) {
					t.Fatal("clock-only applicability drift accepted")
				}
			}
		})
	}
}
