// Package identityprivacy owns the authoritative identity registry, explicit
// administration changes, and source observation revocation.
package identityprivacy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// RecallSource matches IdentityFacade.sourceRecalled: capture the exact registry
// revision, record a metadata-only identity mutation, then remove observations.
// The caller must already have recorded root source intent in this same session.
// There is no independent writer, recovery replay, or synthetic completion ACK.
func RecallSource(session *recovery.WriteSession, sceneID, messageID string) error {
	if session == nil {
		return errors.New("identity_transaction_required")
	}
	state, exists, err := loadSessionRegistry(session)
	if err != nil {
		return err
	}
	removed := []string{}
	for _, raw := range get(state, "aliases").([]any) {
		alias := raw.(*jsjson.Object)
		if matchesSource(alias, sceneID, messageID) {
			removed = append(removed, get(alias, "id").(string))
		}
	}
	if len(removed) == 0 {
		return nil
	}
	revision := get(state, "revision").(float64)
	operationID := "recall-" + hash([]any{sceneID, messageID, revision})
	_, err = writeTransition(session, state, exists, transition{
		kind: "source_recall", operationID: operationID, expectedRevision: revision,
		payload: object("sceneId", sceneID, "messageId", messageID),
		targets: recovery.IdentityTargets{AliasIDs: removed, Source: &recovery.Source{SceneID: sceneID, MessageID: messageID}},
	})
	return err
}

func loadSessionRegistry(session *recovery.WriteSession) (*jsjson.Object, bool, error) {
	return loadRegistry(session.AssertReady, func(q string, args ...any) (sourceevents.Rows, error) {
		return session.Query(q, args...)
	})
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
