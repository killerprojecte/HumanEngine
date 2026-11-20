package identityprivacy

import (
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type transition struct {
	kind, operationID string
	expectedRevision  float64
	payload           any
	targets           recovery.IdentityTargets
}

type Acknowledgement struct {
	Revision  float64 `json:"revision"`
	Duplicate bool    `json:"duplicate"`
}

// planTransition operates only on a freshly decoded registry. The sealed
// event and canonical blob are written together by the existing session.
func planTransition(state *jsjson.Object, t transition) (Acknowledgement, *recovery.IdentityMutation, error) {
	var ack Acknowledgement
	digest := hash(object("kind", t.kind, "payload", t.payload, "expectedRevision", t.expectedRevision))
	operations := get(state, "operations").([]any)
	for _, raw := range operations {
		prior := raw.(*jsjson.Object)
		if get(prior, "id") != t.operationID {
			continue
		}
		if get(prior, "hash") != digest {
			return ack, nil, errors.New("identity_operation_conflict")
		}
		return Acknowledgement{get(prior, "revision").(float64), true}, nil, nil
	}
	revision := get(state, "revision").(float64)
	if revision != t.expectedRevision {
		return ack, nil, errors.New("identity_revision_conflict")
	}
	if len(operations) >= 16384 || revision >= 9007199254740991 {
		return ack, nil, errors.New("identity_capacity")
	}
	before := hash(state)
	if err := applyTransition(state, t); err != nil {
		return ack, nil, err
	}
	next := revision + 1
	_ = state.Set("revision", next)
	_ = state.Set("operations", append(operations, object("id", t.operationID, "hash", digest, "revision", next)))
	safe, err := parseState(state)
	if err != nil {
		return ack, nil, err
	}
	*state = *safe
	return Acknowledgement{Revision: next}, &recovery.IdentityMutation{
		Operation: t.kind, OperationID: t.operationID, ExpectedRevision: revision, NextRevision: next,
		BeforeHash: before, QualificationHash: hash(state), Targets: t.targets,
	}, nil
}

func writeTransition(session *recovery.WriteSession, state *jsjson.Object, exists bool, t transition) (Acknowledgement, error) {
	ack, event, err := planTransition(state, t)
	if err != nil || event == nil {
		return ack, err
	}
	if err = session.RequireReader(6); err != nil {
		return ack, err
	}
	if _, err = session.AppendIntent(*event); err != nil {
		return ack, err
	}
	if err = session.AssertReady(); err != nil {
		return ack, err
	}
	if !exists {
		if err = session.EnsureMemoryIdentitySchema(); err != nil {
			return ack, err
		}
	}
	raw, err := jsjson.Stringify(state)
	if err != nil {
		return ack, err
	}
	_, err = session.Exec("INSERT INTO memory_identity_registry(id,value) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value", raw)
	return ack, err
}

func matchesSource(alias *jsjson.Object, sceneID, messageID any) bool {
	source := get(alias, "source").(*jsjson.Object)
	return get(source, "kind") == "observation" && get(source, "sceneId") == sceneID && get(source, "messageId") == messageID
}

func applyTransition(state *jsjson.Object, t transition) error {
	switch t.kind {
	case "link", "unlink":
		pair := t.payload.([]any)
		a, b := endpointKey(pair[0].(*jsjson.Object)), endpointKey(pair[1].(*jsjson.Object))
		edges := get(state, "edges").([]any)
		for i, raw := range edges {
			edge := raw.([]any)
			if endpointKey(edge[0].(*jsjson.Object)) == a && endpointKey(edge[1].(*jsjson.Object)) == b {
				if t.kind == "unlink" {
					_ = state.Set("edges", append(edges[:i], edges[i+1:]...))
				}
				return nil
			}
		}
		if t.kind == "link" {
			if a == b || len(edges) >= 4096 {
				return ErrRejected
			}
			_ = state.Set("edges", append(edges, pair))
		}
	case "split":
		selected := t.payload.([]any)
		keys := make(map[string]bool, len(selected))
		for _, ep := range selected {
			keys[endpointKey(ep.(*jsjson.Object))] = true
		}
		if len(keys) != len(selected) || len(selected) == 0 {
			return ErrRejected
		}
		whole, err := identityComponent(state, selected[0].(*jsjson.Object))
		if err != nil {
			return err
		}
		if len(keys) == len(whole) {
			return ErrRejected
		}
		for key := range keys {
			if !whole[key] {
				return ErrRejected
			}
		}
		edges := get(state, "edges").([]any)
		kept := edges[:0]
		for _, raw := range edges {
			edge := raw.([]any)
			if keys[endpointKey(edge[0].(*jsjson.Object))] == keys[endpointKey(edge[1].(*jsjson.Object))] {
				kept = append(kept, edge)
			}
		}
		_ = state.Set("edges", kept)
	case "alias_put":
		value := t.payload.(*jsjson.Object)
		aliases := get(state, "aliases").([]any)
		for _, raw := range aliases {
			prior := raw.(*jsjson.Object)
			if get(prior, "id") == get(value, "id") {
				if hash(prior) != hash(value) {
					return ErrRejected
				}
				return nil
			}
		}
		if len(aliases) >= 4096 {
			return ErrRejected
		}
		_ = state.Set("aliases", append(aliases, value))
	case "alias_remove", "source_recall":
		p := t.payload.(*jsjson.Object)
		aliases := get(state, "aliases").([]any)
		kept := aliases[:0]
		for _, raw := range aliases {
			row := raw.(*jsjson.Object)
			if t.kind == "alias_remove" && get(row, "id") == get(p, "aliasId") ||
				t.kind == "source_recall" && matchesSource(row, get(p, "sceneId"), get(p, "messageId")) {
				continue
			}
			kept = append(kept, row)
		}
		_ = state.Set("aliases", kept)
	default:
		return ErrRejected
	}
	return nil
}
