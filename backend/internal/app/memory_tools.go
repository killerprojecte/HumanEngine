package app

import (
	"context"
	"errors"
	"math"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memoryservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/observability"
	"github.com/killerprojecte/HumanEngine/backend/internal/relations"
)

func memoryToolObject(values ...any) *jsjson.Object {
	out := new(jsjson.Object)
	for i := 0; i < len(values); i += 2 {
		_ = out.Set(values[i].(string), values[i+1])
	}
	return out
}

// composeModelMemory registers actual person/search owners and their exact
// delivered-reference registry. Other memory tool branches are not advertised
// until their concrete read and delivery owners have been connected.
func (a *Application) composeModelMemory(o Owners, memory *memoryOwners) error {
	tracker, err := memoryservice.NewWorkspaceSelectionTracker(a.Semantic.Memory, func() bool {
		return memory.snapshot.enabled("memoryWorkingSetEnabled") || memory.snapshot.enabled("memoryEpisodesEnabled")
	}, nil)
	if err != nil {
		return err
	}
	port, err := memoryservice.NewExposurePort(a.Semantic.Memory, o.Sources, o.Main, o.Authority)
	if err != nil {
		return err
	}
	exposure, err := memoryservice.NewModelMemoryExposure(port, tracker.Capture)
	if err != nil {
		return err
	}
	a.MemoryExposure = exposure
	a.WorkspaceSelections = tracker
	// Shadow retrieval is a diagnostic-only read owner. It is not used for
	// relation admission and does not grant relation exposure to the model.
	shadow, err := relations.New(o.Main, o.Authority, assertions.Options{DateParser: o.Dates}, relations.Options{
		Authority: assertionauthority.Options{DateParser: o.Dates, Compare: o.Compare},
		Enabled:   func(relations.Context) (bool, error) { return memory.snapshot.enabled("memoryGraphEnabled"), nil },
		AutoAdmission: func(relations.Context) (bool, error) {
			return memory.snapshot.enabled("memoryGraphAutoAdmissionEnabled"), nil
		},
		LearningEnabled: func(c relations.Context) (bool, error) {
			return memorySceneLearningEnabled(context.Background(), o.Assertions, c.SceneID)
		},
	})
	if err != nil {
		return err
	}
	base := botruntime.Extension{
		Tools: []*jsjson.Object{memoryToolObject("name", "memory.search", "description", "Search current-scene memories and safe shared public knowledge.", "inputSchema", memoryToolObject("type", "object", "properties", memoryToolObject("query", memoryToolObject("type", "string", "maxLength", float64(500))), "required", []any{"query"}, "additionalProperties", false)), memoryToolObject("name", "memory.person", "description", "Read person memory from this scene only.", "inputSchema", memoryToolObject("type", "object", "properties", memoryToolObject("personId", memoryToolObject("type", "string")), "required", []any{"personId"}, "additionalProperties", false))},
		Execute: func(ctx context.Context, name string, input any, c *botruntime.TurnContext, done func(any, error)) {
			value, err := a.executeMemoryTool(ctx, name, input, c, memory, shadow)
			done(value, err)
		},
		// ModelMemoryExposure invokes this only after the selected original objects
		// have actually committed. Experience must never observe a prepared page.
		Delivered: func(name string, value any, c *botruntime.TurnContext, input any) (any, error) {
			if name == "memory.person" || name == "memory.search" {
				if err := a.ExperienceHints.DeliveredTool(c, name, input, value); err != nil {
					return nil, err
				}
			}
			return value, nil
		},
	}
	a.Engine.Runtime.AddExtension(exposure.ComposeExtension(*tracker.ComposeExtension(base)))
	return nil
}

func (a *Application) executeMemoryTool(ctx context.Context, name string, input any, c *botruntime.TurnContext, memory *memoryOwners, shadow *relations.Service) (any, error) {
	if name != "memory.person" && name != "memory.search" {
		return nil, errors.New("runtime_unknown_tool")
	}
	if name == "memory.person" {
		if err := a.Engine.TurnRead.GateTool(ctx, c); err != nil {
			return nil, err
		}
	} else if c.Mode == "real" {
		if _, err := a.Engine.TurnRead.Begin(ctx, c); err != nil {
			return nil, err
		}
	}
	args, ok := input.(*jsjson.Object)
	if !ok {
		return nil, errors.New("memory_tool_arguments_invalid")
	}
	if name == "memory.search" {
		raw, _ := args.Get("query")
		query, ok := raw.(string)
		if !ok {
			return nil, errors.New("memory_tool_arguments_invalid")
		}
		length, err := jsjson.UTF16Len(query)
		if err != nil || length > 500 {
			return nil, errors.New("memory_tool_arguments_invalid")
		}
		var records []*jsjson.Object
		if c.Mode == "simulation" {
			records, err = a.Semantic.Memory.Search(ctx, memoryservice.Context{SceneID: c.Scene.ID, Simulation: true}, query, "", nil)
		} else {
			records, err = a.Learning.Owner.Search(ctx, c.Scene.ID, query)
		}
		if err != nil {
			return nil, err
		}
		if c.Mode == "real" && memory.snapshot.enabled("memoryGraphEnabled") && memory.snapshot.enabled("memoryGraphShadowEnabled") {
			graph, e := shadow.List(ctx, relations.Context{SceneID: c.Scene.ID, Provenance: "contextual"}, relations.ListOptions{})
			if e != nil {
				emit(a.options.Logs, observability.Event{Level: "info", Module: "memory", Code: "memory_relation_shadow_unavailable", Message: "memory_relation_shadow_unavailable", SceneID: c.Scene.ID})
			} else {
				emit(a.options.Logs, observability.Event{Level: "info", Module: "memory", Code: "memory_relation_shadow_compared", Message: "memory_relation_shadow_compared", SceneID: c.Scene.ID, Fields: memoryShadowCounts(records, graph)})
			}
		}
		return a.trackModelMemory(ctx, c, records)
	}
	raw, _ := args.Get("personId")
	person, ok := raw.(string)
	if !ok {
		return nil, errors.New("memory_tool_arguments_invalid")
	}
	records, err := a.Semantic.Memory.List(ctx, memoryservice.Context{SceneID: c.Scene.ID, Simulation: c.Mode == "simulation"}, person)
	if err != nil {
		return nil, err
	}
	return a.trackModelMemory(ctx, c, records)
}

func (a *Application) trackModelMemory(ctx context.Context, c *botruntime.TurnContext, records []*jsjson.Object) (any, error) {
	if c.Mode == "real" {
		if _, err := a.Engine.TurnRead.Begin(ctx, c); err != nil {
			return nil, err
		}
	}
	allowed, err := a.Engine.TurnRead.PersonalizationAllowed(ctx, c.Scene.ID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		filtered := make([]*jsjson.Object, 0, len(records))
		for _, record := range records {
			kind, _ := record.Get("kind")
			if kind != "fact" && kind != "claim" && kind != "term" {
				continue
			}
			raw, _ := record.Get("scope")
			scope, ok := raw.(*jsjson.Object)
			if !ok {
				return nil, errors.New("memory_record_scope_invalid")
			}
			scopeKind, _ := scope.Get("kind")
			person, _ := scope.Get("personId")
			if scopeKind != "scene" || !memoryValueTruthy(person) {
				filtered = append(filtered, record)
			}
		}
		records = filtered
	}
	prepared, err := a.MemoryExposure.Prepare(ctx, c, records)
	if err != nil {
		return nil, err
	}
	if c.MemoryExposureDeferred() {
		return memoryToolArray(prepared), nil
	}
	selected := make([]any, len(prepared))
	for i, v := range prepared {
		selected[i] = v
	}
	committed, err := a.MemoryExposure.Commit(ctx, c, selected)
	return memoryToolArray(committed), err
}

func memoryValueTruthy(value any) bool {
	switch v := value.(type) {
	case nil, jsjson.UndefinedValue:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case float64:
		return v != 0 && !math.IsNaN(v)
	default:
		return true
	}
}

func memoryShadowCounts(records []*jsjson.Object, graph relations.Listing) map[string]any {
	baseline, related := map[any]bool{}, map[any]bool{}
	for _, record := range records {
		id, _ := record.Get("id")
		baseline[id] = true
	}
	for _, edge := range graph.Items {
		backing, _ := edge.Get("backing")
		if ref, ok := backing.(*jsjson.Object); ok {
			id, _ := ref.Get("id")
			related[id] = true
		}
		target, _ := edge.Get("target")
		if o, ok := target.(*jsjson.Object); ok {
			backing, _ := o.Get("backing")
			if ref, ok := backing.(*jsjson.Object); ok {
				id, _ := ref.Get("id")
				related[id] = true
			}
		}
	}
	overlap := 0
	for id := range related {
		if baseline[id] {
			overlap++
		}
	}
	return map[string]any{"baselineCount": len(baseline), "graphCount": len(related), "overlapCount": overlap, "graphCoverage": graph.Coverage, "interpretation": "bounded_set_comparison_not_quality_evidence"}
}

// memorySceneLearningEnabled preserves the actual scoped-store failure rather
// than converting unavailable authorization state to a successful false value.
func memorySceneLearningEnabled(ctx context.Context, store *assertions.Store, sceneID string) (bool, error) {
	var enabled bool
	err := store.Read(ctx, func(r *assertions.Reader) error {
		scene, err := r.Scene(sceneID)
		if err != nil {
			return err
		}
		if scene != nil {
			v, _ := scene.Get("learningEnabled")
			enabled = v == true
		}
		return nil
	})
	return enabled, err
}

// This is a JSON-array container conversion only. The original object pointers
// are the authority receipts; cloning or serializing them here loses ownership.
func memoryToolArray(values []*jsjson.Object) []any {
	result := make([]any, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}
