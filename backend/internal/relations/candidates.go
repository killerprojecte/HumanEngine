package relations

import "errors"

func (c *candidates) admit(ctx Context, backing *Object, proposals []any) (out Admission, err error) {
	out = Admission{Created: []*Object{}, Rejected: []Rejection{}}
	if err = c.storage.RequireTransaction(); err != nil {
		return
	}
	if !truthy(get(backing, "id")) {
		return out, errors.New("invalid_relation_reference")
	}
	if _, e := safe(1)(get(backing, "version")); e != nil {
		return out, errors.New("invalid_relation_reference")
	}
	if _, e := safe(0)(get(backing, "deletionEpoch")); e != nil {
		return out, errors.New("invalid_relation_reference")
	}
	enabled := false
	if !ctx.Simulation {
		enabled, err = c.options.Enabled(ctx)
		if err != nil {
			return
		}
		if enabled {
			enabled, err = c.options.LearningEnabled(ctx)
			if err != nil {
				return
			}
		}
	}
	if !enabled {
		out.Rejected = append(out.Rejected, Rejection{-1, "relation_admission_disabled"})
		return
	}
	if len(proposals) > 3 {
		out.Rejected = append(out.Rejected, Rejection{-1, "relation_candidate_limit"})
		return
	}
	reject := func(index int, reason string) { out.Rejected = append(out.Rejected, Rejection{index, reason}) }
	for index, input := range proposals {
		proposal, e := parseProposal(input)
		if e != nil {
			reject(index, "invalid_relation_proposal")
			continue
		}
		view, e := c.current(ctx, backing)
		if e != nil {
			return out, e
		}
		person := str(get(proposal, "personId"))
		if !validBacking(ctx, view, person) {
			reject(index, "relation_backing_unavailable")
			continue
		}
		key, e := digest([]any{ctx.SceneID, backing, proposal})
		if e != nil {
			return out, e
		}
		var target, newEntity *Object
		request := get(proposal, "target")
		if get(request, "kind") == "new" {
			target = obj("id", c.id(), "sceneId", ctx.SceneID, "kind", get(request, "entityKind"), "title", get(request, "title"), "backing", clone(backing))
			newEntity = target
		} else {
			entityID := str(get(request, "entityId"))
			var exposed *Object
			for _, v := range ctx.ExposedEntities {
				if get(v, "entityId") == entityID {
					exposed = v
					break
				}
			}
			target, e = c.read.Entity(entityID)
			if e != nil {
				return out, e
			}
			valid := ctx.Provenance == "contextual" && target != nil && get(target, "sceneId") == ctx.SceneID && exposed != nil && sameReference(get(exposed, "backingRef"), get(target, "backing"))
			if valid {
				v, e := c.current(ctx, object(get(target, "backing")))
				if e != nil {
					return out, e
				}
				valid = validBacking(ctx, v, "")
			}
			if !valid {
				reject(index, "relation_target_not_exposed")
				continue
			}
		}
		existing, e := c.read.ByKey(key)
		if e != nil {
			return out, e
		}
		if existing != nil {
			projected, e := c.projection(ctx, existing)
			if e != nil {
				return out, e
			}
			if projected != nil {
				out.Created = append(out.Created, projected)
			} else {
				reject(index, "relation_backing_unavailable")
			}
			continue
		}
		record := obj("id", c.id(), "version", float64(1), "sceneId", ctx.SceneID, "predicate", get(proposal, "predicate"), "modality", get(proposal, "modality"), "temporalScope", get(proposal, "temporalScope"), "personId", person, "target", target, "polarity", get(proposal, "polarity"), "conditions", clone(get(proposal, "conditions")))
		for _, field := range []string{"occurredAt", "validFrom", "validUntil"} {
			if v := get(proposal, field); truthy(v) {
				_ = record.Set(field, v)
			}
		}
		if v := get(view.Record, "epistemicKind"); truthy(v) {
			_ = record.Set("epistemicKind", v)
		}
		_ = record.Set("backing", clone(backing))
		_ = record.Set("proofs", clone(personProofs(view, person)))
		_ = record.Set("status", "candidate")
		_ = record.Set("admission", "candidate")
		createdAt, e := c.timestamp()
		if e != nil {
			return out, e
		}
		_ = record.Set("createdAt", createdAt)
		rows, e := c.read.Rows(ctx.SceneID)
		if e != nil {
			return out, e
		}
		conflict := len(rows) > 200
		if !conflict {
			for _, row := range rows {
				if get(row, "status") != "rejected" && get(row, "personId") == person && get(get(row, "target"), "id") == get(target, "id") && (get(row, "polarity") != get(record, "polarity") || get(row, "modality") != "asserted" || get(row, "temporalScope") != "current" || len(arr(get(row, "conditions"))) > 0) {
					ok, e := c.eligible(ctx, row)
					if e != nil {
						return out, e
					}
					if ok {
						conflict = true
						break
					}
				}
			}
		}
		originClear := get(request, "kind") == "new"
		if !originClear {
			for _, row := range rows {
				if get(get(row, "target"), "id") == get(target, "id") && sameReference(get(row, "backing"), get(target, "backing")) && get(row, "status") == "accepted" && get(row, "modality") == "asserted" && get(row, "temporalScope") == "current" && get(row, "polarity") == "positive" && len(arr(get(row, "conditions"))) == 0 {
					ok, e := c.eligible(ctx, row)
					if e != nil {
						return out, e
					}
					if ok {
						originClear = true
						break
					}
				}
			}
		}
		targetClear, e := c.targetClear(ctx, target)
		if e != nil {
			return out, e
		}
		auto, e := c.options.AutoAdmission(ctx)
		if e != nil {
			return out, e
		}
		if auto && targetClear && originClear {
			auto, e = c.autoEligible(record, view, c.now(), conflict)
			if e != nil {
				return out, e
			}
			if auto {
				_ = record.Set("status", "accepted")
				_ = record.Set("admission", "automatic")
				_ = record.Set("autoReason", "self_explicit_no_visible_conflict")
			}
		}
		if e = c.storage.Put(record, key, newEntity); e != nil {
			return out, e
		}
		out.Created = append(out.Created, clone(record).(*Object))
	}
	return
}
func (c *candidates) review(ctx Context, id string, expected float64, status, actor string) (*Object, error) {
	if e := c.storage.RequireTransaction(); e != nil {
		return nil, e
	}
	if _, e := safe(1)(expected); e != nil || (status != "accepted" && status != "rejected") {
		return nil, errors.New("invalid_relation_review")
	}
	record, e := c.read.Get(id)
	if e != nil {
		return nil, e
	}
	if record == nil {
		return nil, errors.New("relation_unavailable")
	}
	ok, e := c.eligible(ctx, record)
	if e != nil {
		return nil, e
	}
	if !ok {
		return nil, errors.New("relation_unavailable")
	}
	if get(record, "version") != expected {
		return nil, errors.New("relation_version_conflict")
	}
	next := record.Clone()
	_ = next.Set("version", num(get(record, "version"))+1)
	_ = next.Set("status", status)
	_ = next.Set("admission", "reviewed")
	reviewedAt, e := c.timestamp()
	if e != nil {
		return nil, e
	}
	_ = next.Set("reviewedAt", reviewedAt)
	if actor != "" {
		_ = next.Set("actorId", actor)
	}
	next.Delete("autoReason")
	if e = c.storage.Update(next, expected); e != nil {
		return nil, e
	}
	return c.projection(ctx, next)
}
func (c *candidates) list(ctx Context, options ListOptions) (out Listing, err error) {
	out = Listing{Items: []*Object{}, Coverage: "complete"}
	limit := float64(40)
	if options.Limit != nil {
		limit = *options.Limit
	}
	if _, e := safe(1)(limit); e != nil || limit > 40 {
		return out, errors.New("relation_read_limit")
	}
	if ctx.Simulation {
		return
	}
	enabled, e := c.options.Enabled(ctx)
	if e != nil {
		return out, e
	}
	if !enabled {
		return
	}
	rows, e := c.read.Rows(ctx.SceneID)
	if e != nil {
		return out, e
	}
	first := rows
	if len(first) > 200 {
		first = first[:200]
	}
	eligible := []*Object{}
	// Source filter completes all first-pass projections before its map starts.
	for _, row := range first {
		if (options.PersonID == "" || get(row, "personId") == options.PersonID) && (options.Status == "" || get(row, "status") == options.Status) {
			ok, e := c.eligible(ctx, row)
			if e != nil {
				return out, e
			}
			if ok {
				eligible = append(eligible, row)
			}
		}
	}
	projected := []*Object{}
	for _, row := range eligible {
		v, e := c.projection(ctx, row)
		if e != nil {
			return out, e
		}
		if v != nil {
			projected = append(projected, v)
		}
	}
	if len(rows) > 200 || len(projected) > int(limit) {
		out.Coverage = "limited"
	}
	if len(projected) > int(limit) {
		projected = projected[:int(limit)]
	}
	out.Items = projected
	return
}
