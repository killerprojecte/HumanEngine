package knowledge

import "errors"

// Requalification records exact before/after references captured by one privacy
// mutation. It is not permission to bind an old proof to an arbitrary latest row.
type Requalification struct{ Before, After *Object }

// Requalify keeps source traversal and all-plan-before-write ordering. Current
// must be the same transaction's concrete assertion authority; errors propagate.
func (l *Lifecycle) Requalify(ctx Context, transitions []Requalification, current func(Context, Reference) (*Assertion, error)) error {
	if e := l.storage.RequireTransaction(); e != nil {
		return e
	}
	if ctx.Simulation {
		return nil
	}
	resolve := func(reference any, fingerprint any, proofs any) (*Object, error) {
		var change *Requalification
		for i := range transitions {
			if sameRef(transitions[i].Before, reference) {
				change = &transitions[i]
				break
			}
		}
		if change == nil {
			return nil, nil
		}
		if get(change.Before, "id") != get(change.After, "id") {
			return nil, errors.New("knowledge_requalification_invalid")
		}
		if _, e := ref(change.After); e != nil {
			return nil, errors.New("knowledge_requalification_invalid")
		}
		if current == nil {
			return nil, errors.New("knowledge_current_authority_required")
		}
		view, e := current(ctx, referenceValue(change.After))
		if e != nil || view == nil {
			return nil, e
		}
		fp, e := BackingFingerprint(view.Record)
		if e != nil {
			return nil, e
		}
		if fp != fingerprint {
			return nil, nil
		}
		survivors := intersect(proofs, view.Proofs)
		if len(survivors) == 0 {
			return nil, nil
		}
		return obj("ref", change.After, "proofs", survivors), nil
	}
	rows, e := l.storage.Rows(&ctx.SceneID, nil)
	if e != nil {
		return e
	}
	updates := changes()
	for _, edge := range rows {
		own, e := resolve(get(edge, "backing"), get(edge, "backingFingerprint"), get(edge, "proofs"))
		if e != nil {
			return e
		}
		dependencies := []any{}
		for _, dep := range arr(get(edge, "dependencies")) {
			next, e := resolve(get(get(dep, "entity"), "backing"), get(dep, "backingFingerprint"), get(dep, "proofs"))
			if e != nil {
				return e
			}
			if next == nil {
				dependencies = append(dependencies, dep)
				continue
			}
			d := object(dep).Clone()
			entity := object(get(dep, "entity")).Clone()
			_ = entity.Set("backing", get(next, "ref"))
			_ = d.Set("entity", entity)
			_ = d.Set("proofs", get(next, "proofs"))
			dependencies = append(dependencies, d)
		}
		reads := []any{}
		for _, dep := range arr(get(edge, "readDependencies")) {
			next, e := resolve(get(dep, "backing"), get(dep, "backingFingerprint"), get(dep, "proofs"))
			if e != nil {
				return e
			}
			if next == nil {
				reads = append(reads, dep)
				continue
			}
			d := object(dep).Clone()
			_ = d.Set("backing", get(next, "ref"))
			_ = d.Set("proofs", get(next, "proofs"))
			reads = append(reads, d)
		}
		endpoint := func(point any) any {
			if get(point, "kind") != "entity" {
				return point
			}
			if get(get(point, "entity"), "originEdgeId") == get(edge, "id") && own != nil {
				p := object(point).Clone()
				entity := object(get(point, "entity")).Clone()
				_ = entity.Set("backing", get(own, "ref"))
				_ = p.Set("entity", entity)
				return p
			}
			for _, dep := range dependencies {
				if get(get(dep, "entity"), "id") == get(get(point, "entity"), "id") {
					return obj("kind", "entity", "entity", get(dep, "entity"))
				}
			}
			return point
		}
		next := edge.Clone()
		proofs := get(edge, "proofs")
		if own != nil {
			_ = next.Set("backing", get(own, "ref"))
			proofs = get(own, "proofs")
		}
		_ = next.Set("proofs", proofs)
		_ = next.Set("dependencies", dependencies)
		_ = next.Set("readDependencies", reads)
		_ = next.Set("subject", endpoint(get(edge, "subject")))
		_ = next.Set("object", endpoint(get(edge, "object")))
		_ = next.Set("spans", survivingSpans(get(edge, "spans"), arr(proofs)))
		if !deepEqual(next, edge) {
			_ = next.Set("version", num(get(edge, "version"))+1)
			updates.put(str(get(edge, "id")), next)
		}
	}
	return l.applyMaintenance(rows, updates)
}
