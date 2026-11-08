// Package identityprivacy owns identity observation cleanup in an admitted root
// source recall. It neither links identities nor infers new identity material.
package identityprivacy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

// RecallSource matches IdentityFacade.sourceRecalled: capture the exact registry
// revision, record a metadata-only identity mutation, then remove observations.
// The caller must already have recorded root source intent in this same session.
// There is no independent writer, recovery replay, or synthetic completion ACK.
func RecallSource(session *recovery.WriteSession, sceneID, messageID string) error {
	if session == nil {
		return errors.New("identity_transaction_required")
	}
	if err := session.AssertReady(); err != nil {
		return err
	}
	state, err := read(session)
	if err != nil {
		return err
	}
	aliases := get(state, "aliases").([]any)
	removed := []string{}
	kept := []any{}
	for _, raw := range aliases {
		alias := raw.(*jsjson.Object)
		source := get(alias, "source").(*jsjson.Object)
		if get(source, "kind") == "observation" && get(source, "sceneId") == sceneID && get(source, "messageId") == messageID {
			removed = append(removed, get(alias, "id").(string))
		} else {
			kept = append(kept, alias)
		}
	}
	if len(removed) == 0 {
		return nil
	}
	revision := get(state, "revision").(float64)
	operationID := "recall-" + hash([]any{sceneID, messageID, revision})
	digest := hash(object("kind", "source_recall", "payload", object("sceneId", sceneID, "messageId", messageID), "expectedRevision", revision))
	operations := get(state, "operations").([]any)
	for _, raw := range operations {
		op := raw.(*jsjson.Object)
		if get(op, "id") == operationID {
			if get(op, "hash") != digest {
				return errors.New("identity_operation_conflict")
			}
			return nil
		}
	}
	if len(operations) >= 16384 || revision >= 9007199254740991 {
		return errors.New("identity_capacity")
	}
	before := hash(state)
	_ = state.Set("aliases", kept)
	_ = state.Set("revision", revision+1)
	operations = append(operations, object("id", operationID, "hash", digest, "revision", revision+1))
	_ = state.Set("operations", operations)
	if len(operations)+len(get(state, "edges").([]any))+len(kept) > 16384 {
		return errors.New("identity_revocation_reserve_capacity")
	}
	// Revalidate as the source registry does before its durable beforeWrite hook.
	state, err = parseState(state)
	if err != nil {
		return err
	}
	if err = session.RequireReader(6); err != nil {
		return err
	}
	if _, err = session.AppendIntent(recovery.IdentityMutation{
		Operation: "source_recall", OperationID: operationID, ExpectedRevision: revision, NextRevision: revision + 1,
		BeforeHash: before, QualificationHash: hash(state),
		Targets: recovery.IdentityTargets{Endpoints: []recovery.IdentityEndpoint{}, AliasIDs: removed, Source: &recovery.Source{SceneID: sceneID, MessageID: messageID}},
	}); err != nil {
		return err
	}
	if err = session.AssertReady(); err != nil {
		return err
	}
	raw, err := jsjson.Stringify(state)
	if err != nil {
		return err
	}
	// A matching alias necessarily came from the existing registry table. Reads
	// never create an empty registry, and no schema capability is needed here.
	_, err = session.Exec("INSERT INTO memory_identity_registry(id,value) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value", raw)
	return err
}

func read(s *recovery.WriteSession) (*jsjson.Object, error) {
	rows, err := s.Query("SELECT 1 FROM sqlite_master WHERE type='table' AND name='memory_identity_registry'")
	if err != nil {
		return nil, err
	}
	exists := rows.Next()
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return nil, err
	}
	if !exists {
		return object("format", float64(1), "revision", float64(0), "edges", []any{}, "aliases", []any{}, "operations", []any{}), nil
	}
	rows, err = s.Query("SELECT value FROM memory_identity_registry WHERE id=1")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err = rows.Err(); err != nil {
			return nil, err
		}
		return nil, errCorrupt
	}
	var raw any
	if err = rows.Scan(&raw); err != nil {
		return nil, err
	}
	text, ok := raw.(string)
	if !ok {
		return nil, errCorrupt
	}
	value, err := jsjson.Parse(text)
	if err != nil {
		return nil, errCorrupt
	}
	return parseState(value)
}

func object(kv ...any) *jsjson.Object {
	o := new(jsjson.Object)
	for i := 0; i < len(kv); i += 2 {
		_ = o.Set(kv[i].(string), kv[i+1])
	}
	return o
}
func get(o *jsjson.Object, k string) any { v, _ := o.Get(k); return v }
func hash(v any) string {
	raw, err := jsjson.Stringify(v)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
