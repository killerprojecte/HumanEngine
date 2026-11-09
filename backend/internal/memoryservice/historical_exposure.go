package memoryservice

import (
	"context"
	"errors"
	"math"
	"sort"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

type HistoricalExposure struct {
	states *StateExposure
	port   *historicalExposurePort
}
type historicalExposurePort struct{ memory *ExposurePort }

func NewHistoricalExposure(memory *ExposurePort) (*HistoricalExposure, error) {
	if memory == nil {
		return nil, errors.New("historical_exposure_owner_required")
	}
	port := &historicalExposurePort{memory}
	states, err := NewStateExposure(port, nil)
	if err != nil {
		return nil, err
	}
	return &HistoricalExposure{states, port}, nil
}
func (e *HistoricalExposure) Query(ctx context.Context, c *botruntime.TurnContext, input *Object) ([]*Object, error) {
	return e.states.Query(ctx, c, input)
}
func (e *HistoricalExposure) Deliver(ctx context.Context, c *botruntime.TurnContext, values []any) ([]*Object, error) {
	return e.states.Deliver(ctx, c, values)
}
func (e *HistoricalExposure) CaptureGuard(c *botruntime.TurnContext) *botruntime.ToolResultGuard {
	return e.states.CaptureGuard(c)
}
func (e *HistoricalExposure) HasExposure(c *botruntime.TurnContext) bool {
	return e.states.HasExposure(c)
}

// The feature flag is read from the caller's actual config snapshot, including
// uncommitted writer changes. No process cache or always-enabled callback.
func (p *historicalExposurePort) enabled(reads botruntime.ReadAuthority) (enabled bool, err error) {
	err = p.memory.sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
		if err := scope.CheckBinding(p.memory.main, p.memory.authority); err != nil {
			return err
		}
		rows, err := scope.Query("SELECT value FROM config WHERE key='settings'")
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return err
		}
		v, err := jsjson.Parse(raw)
		if err != nil {
			return err
		}
		enabled = truthy(value(object(v), "memoryHistoricalReadEnabled"))
		return rows.Err()
	})
	return
}
func (p *historicalExposurePort) Query(ctx context.Context, c Context, input any) (out []*Object, err error) {
	out = []*Object{}
	if c.Simulation {
		return out, nil
	}
	var enabled bool
	err = p.memory.sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error { var err error; enabled, err = p.enabled(reads); return err })
	if err != nil || !enabled {
		return out, err
	}
	query := object(input)
	if query == nil {
		return nil, errors.New("memory_history_query_invalid")
	}
	allowed, err := p.memory.memory.prepareHistoricalAnchor(ctx, c, text(query, "memoryId"))
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, errors.New("memory_history_anchor_unavailable")
	}
	err = p.memory.sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		var err error
		out, err = p.current(reads, c, query)
		return err
	})
	return
}
func (p *historicalExposurePort) Valid(ctx context.Context, c Context, values []*Object) (bool, error) {
	var enabled bool
	err := p.memory.sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error { var err error; enabled, err = p.enabled(reads); return err })
	if err != nil || !enabled {
		return false, nil
	}
	// HistoricalExposure validates each retained query, not an aggregate fake gate.
	for _, v := range values {
		input := object(value(v, "query"))
		if input == nil {
			return false, nil
		}
		current, err := p.Query(ctx, c, input)
		if err != nil || len(current) != 1 || !deepEqual(current[0], v) {
			return false, nil
		}
	}
	return true, nil
}
func (p *historicalExposurePort) ValidScoped(reads botruntime.ReadAuthority, c Context, values []*Object) (bool, error) {
	enabled, err := p.enabled(reads)
	if err != nil || !enabled {
		return false, nil
	}
	for _, v := range values {
		input := object(value(v, "query"))
		if input == nil {
			return false, nil
		}
		current, err := p.current(reads, c, input)
		if err != nil || len(current) != 1 || !deepEqual(current[0], v) {
			return false, nil
		}
	}
	return true, nil
}
func (p *historicalExposurePort) SourceIDs(values []*Object) ([]string, error) {
	out := []string{}
	for _, v := range values {
		revisions, _ := value(v, "revisions").([]any)
		for _, raw := range revisions {
			revision := object(raw)
			proofs, _ := value(revision, "supports").([]any)
			for _, raw := range proofs {
				proof := object(raw)
				sources, _ := value(proof, "sources").([]any)
				for _, raw := range sources {
					source := object(raw)
					id, ok := value(source, "messageId").(string)
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
func historyOptional(o *Object, key string) any {
	v, ok := o.Get(key)
	if !ok {
		return jsjson.Undefined
	}
	return v
}
func historySafe(v any, min float64) bool {
	n, ok := v.(float64)
	return ok && !math.IsNaN(n) && !math.IsInf(n, 0) && n >= min && n <= 9007199254740991 && math.Trunc(n) == n
}
func (p *historicalExposurePort) current(reads botruntime.ReadAuthority, c Context, input *Object) (out []*Object, err error) {
	out = []*Object{}
	enabled, err := p.enabled(reads)
	if err != nil {
		return nil, err
	}
	if c.Simulation || !enabled {
		return nil, errors.New("memory_history_disabled")
	}
	err = p.memory.withReader(reads, func(r *assertions.Reader) error {
		available, err := p.memory.memory.historyForRead(r, c, text(input, "memoryId"))
		if err != nil {
			return err
		}
		if available == nil || value(available, "deleted") == true || !historySafe(value(available, "version"), 1) {
			return errors.New("memory_history_anchor_unavailable")
		}
		currentAnchor := epObj("id", text(input, "memoryId"), "version", value(available, "version"), "deletionEpoch", value(available, "deletionEpoch"))
		requested := historyOptional(input, "anchor")
		if truthy(requested) && !jsjson.IsUndefined(requested) && !deepEqual(requested, currentAnchor) {
			return errors.New("memory_history_anchor_changed")
		}
		anchor := object(requested)
		if requested == nil || jsjson.IsUndefined(requested) {
			anchor = currentAnchor
		}
		limit := historyOptional(input, "limit")
		if limit == nil || jsjson.IsUndefined(limit) {
			limit = float64(20)
		}
		asOf, validAt := historyOptional(input, "asOfRecorded"), historyOptional(input, "validAt")
		if anchor == nil || text(anchor, "id") == "" || !historySafe(value(anchor, "version"), 1) || !historySafe(value(anchor, "deletionEpoch"), 0) || !historySafe(limit, 1) || limit.(float64) > 20 || !jsjson.IsUndefined(asOf) && !assertions.ValidHistoryInstant(asOf) || !jsjson.IsUndefined(validAt) && !assertions.ValidHistoryInstant(validAt) {
			return errors.New("memory_history_query_invalid")
		}
		authorize := func() (string, *Object, error) {
			record, err := historicalCurrent(r, p.memory.memory.d.Authority, c, text(anchor, "id"))
			if err != nil || record == nil {
				return "", nil, err
			}
			state, err := r.State(text(anchor, "id"))
			if err != nil {
				return "", nil, err
			}
			if state.Deleted || float64(state.Version) != value(anchor, "version") || float64(state.DeletionEpoch) != value(anchor, "deletionEpoch") {
				return "", nil, nil
			}
			return "scene:" + c.SceneID, record, nil
		}
		scope, current, err := authorize()
		if err != nil {
			return err
		}
		if scope == "" {
			return errors.New("memory_history_anchor_unavailable")
		}
		options := epObj("throughVersion", value(anchor, "version"), "limit", limit)
		if !jsjson.IsUndefined(asOf) {
			_ = options.Set("asOfRecorded", asOf)
		}
		if !jsjson.IsUndefined(validAt) {
			_ = options.Set("validAt", validAt)
		}
		page, err := r.QueryRetained(text(anchor, "id"), scope, options)
		if err != nil {
			return err
		}
		authority, err := assertionauthority.New(r, p.memory.memory.d.Authority)
		if err != nil {
			return err
		}
		revisions, err := authority.RetainedHistoryViews(page.Revisions, true)
		if err != nil {
			return err
		}
		events, err := r.AuthoritySourceEvents()
		if err != nil {
			return err
		}
		reasons := map[string]bool{}
		if page.HasMore {
			reasons["result_limit"] = true
		}
		if page.UnknownRecordedTime {
			reasons["recorded_time_unknown"] = true
		}
		if page.RetentionIncomplete {
			reasons["retention_incomplete"] = true
		}
		retained := []any{}
		for _, revision := range revisions {
			record := object(value(revision, "record"))
			recordScope := object(value(record, "scope"))
			proofs, ok := value(revision, "supports").([]any)
			if !ok {
				return errors.New("assertion_array_required")
			}
			version, vok := value(revision, "version").(float64)
			if text(revision, "assertionId") != text(anchor, "id") || !vok || version > value(anchor, "version").(float64) || !assertionauthority.ParentScopeAllows(object(value(current, "scope")), recordScope) || text(recordScope, "kind") != "scene" || text(recordScope, "sceneId") != c.SceneID || text(record, "lineageStatus") == "verified" && len(proofs) == 0 {
				reasons["authorization_or_proof_incomplete"] = true
				continue
			}
			for _, raw := range proofs {
				proof := object(raw)
				sources, ok := value(proof, "sources").([]any)
				if !ok {
					return errors.New("assertion_array_required")
				}
				for _, raw := range sources {
					source := object(raw)
					refValue, present := source.Get("eventRef")
					if !present || jsjson.IsUndefined(refValue) {
						reasons["legacy_proof_binding_unknown"] = true
					}
					if truthy(refValue) && !jsjson.IsUndefined(refValue) {
						scene, err := r.Scene(text(source, "sceneId"))
						if err != nil {
							return err
						}
						if scene == nil {
							reasons["source_event_metadata_unavailable"] = true
						} else {
							ref, err := sourceevents.ParseRef(refValue)
							if err != nil {
								return err
							}
							historical, err := events.Historical(ref, sourceevents.Identity{SceneID: text(source, "sceneId"), AdapterID: text(scene, "adapterId"), AccountID: text(scene, "accountId"), MessageID: text(source, "messageId"), SenderID: text(source, "senderId")})
							if err != nil {
								return err
							}
							if !historical.Eligible {
								reasons["source_event_metadata_unavailable"] = true
							}
						}
					}
				}
				parents, _ := value(proof, "parents").([]any)
				refs, present := proof.Get("parentReferences")
				if (!present || jsjson.IsUndefined(refs)) && len(parents) > 0 {
					reasons["legacy_proof_binding_unknown"] = true
				}
			}
			if value(object(value(revision, "event")), "legacyImport") == true {
				reasons["legacy_import"] = true
			}
			if value(revision, "recordedAt") == nil {
				reasons["recorded_time_unknown"] = true
			}
			validity := "bounded"
			if value(revision, "validFrom") == nil || value(revision, "validTo") == nil {
				validity = "unknown"
				reasons["valid_time_unknown"] = true
			}
			copy := exposureClone(revision).(*Object)
			_ = copy.Set("ref", epObj("id", value(revision, "assertionId"), "version", value(revision, "version"), "deletionEpoch", value(anchor, "deletionEpoch")))
			_ = copy.Set("validityCoverage", validity)
			retained = append(retained, copy)
		}
		scopeAfter, _, err := authorize()
		if err != nil {
			return err
		}
		if scopeAfter != scope {
			return errors.New("memory_history_anchor_unavailable")
		}
		reasonNames := make([]string, 0, len(reasons))
		for reason := range reasons {
			reasonNames = append(reasonNames, reason)
		}
		sort.Strings(reasonNames)
		reasonValues := make([]any, len(reasonNames))
		for i, v := range reasonNames {
			reasonValues[i] = v
		}
		status := "retained_scope_only"
		if len(reasons) > 0 {
			status = "limited"
		}
		result := epObj("kind", "assertion_history", "interpretation", "previously_recorded_not_current_truth", "sourceBodyCoverage", "unavailable", "asOfRecorded", asOf, "validAt", validAt, "revisions", retained, "coverage", epObj("status", status, "reasons", reasonValues, "returned", float64(len(retained)), "hasMore", page.HasMore), "anchor", exposureClone(anchor), "stateKind", "assertion_history", "query", exposureClone(input))
		out = append(out, result)
		return nil
	})
	return
}
func (e *HistoricalExposure) ComposeExtension(base botruntime.Extension) *botruntime.Extension {
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
		if name == "memory.history" {
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
