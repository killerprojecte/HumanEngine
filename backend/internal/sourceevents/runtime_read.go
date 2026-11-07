package sourceevents

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type runtimeReads struct {
	store *Store
	lease *atomic.Bool
}

func (r *runtimeReads) read(fn func(*View) error) error {
	if r.lease != nil && !r.lease.Load() {
		return sql.ErrTxDone
	}
	if r.store.session != nil {
		if err := r.store.ready(); err != nil {
			return err
		}
		if err := fn(r.store.view()); err != nil {
			return err
		}
		return r.store.ready()
	}
	return r.store.Read(context.Background(), fn)
}
func (r *runtimeReads) Ready() error { return r.read(func(*View) error { return nil }) }
func rawRow(v *View, query string, args ...any) (string, bool, error) {
	var raw string
	exists, err := v.one(query, args, &raw)
	return raw, exists, err
}
func valueRow(v *View, query string, args ...any) (any, bool, error) {
	raw, exists, err := rawRow(v, query, args...)
	if err != nil || !exists {
		return nil, exists, err
	}
	value, err := jsjson.Parse(raw)
	return value, true, err
}
func recalledAt(v *View, sceneID, messageID string) (bool, error) {
	var one int
	return v.one("SELECT 1 FROM recall_tombstones WHERE scene_id=? AND message_id=?", []any{sceneID, messageID}, &one)
}
func (r *runtimeReads) IsRecalled(scene botruntime.Scene, id string) (recalled bool, err error) {
	err = r.read(func(v *View) error { var e error; recalled, e = recalledAt(v, scene.ID, id); return e })
	return
}
func currentRuntimeSource(v *View, message *jsjson.Object) (bool, error) {
	if str(message, "origin") == "simulation" {
		return true, nil
	}
	recalled, err := recalledAt(v, str(message, "sceneId"), str(message, "id"))
	if err != nil || recalled {
		return false, err
	}
	sceneValue, _, err := valueRow(v, "SELECT value FROM scenes WHERE id=?", str(message, "sceneId"))
	if err != nil {
		return false, err
	}
	refValue := get(message, "sourceEventRef")
	if !runtimeTruthy(sceneValue) {
		return !runtimeTruthy(refValue), nil
	}
	scene, ok := sceneValue.(*jsjson.Object)
	if !ok {
		return false, errors.New("source_event_scope_mismatch")
	}
	identity, err := RuntimeSourceIdentity(sceneFromValue(scene), message)
	if err != nil {
		return false, err
	}
	if !runtimeTruthy(refValue) {
		enrolled, err := v.Registered(identity.SceneID, identity.MessageID)
		if err != nil || enrolled != nil {
			return false, err
		}
		edited, err := v.HasEdits(identity)
		return !edited, err
	}
	hash, err := RuntimeSourceBodyHash(message)
	if err != nil {
		return false, err
	}
	exists, err := v.exists()
	if err != nil || !exists {
		return false, err
	}
	head, err := v.head(identity)
	if err != nil || head == nil {
		return false, err
	}
	if head.Metadata.BodyHash != hash {
		return false, nil
	}
	ref, err := ParseRef(refValue)
	if err != nil {
		return false, err
	}
	if head.Metadata.Ref != ref {
		return false, nil
	}
	eligible, err := v.Current(head.Metadata.Ref, identity)
	return eligible.Eligible, err
}
func (r *runtimeReads) ValidateSources(scene botruntime.Scene, messages []*jsjson.Object, dependencies []string) (valid bool, err error) {
	// nil messages selects the dependency-only read; source skips its snapshot
	// entirely for an empty dependency set. A nonnil empty source slice still reads.
	if r.lease != nil && !r.lease.Load() {
		return false, sql.ErrTxDone
	}
	if messages == nil && len(dependencies) == 0 {
		return true, nil
	}
	copies := make([]*jsjson.Object, len(messages))
	for i, m := range messages {
		if m == nil {
			return false, ErrInvalid
		}
		copies[i] = cloneRuntimeJSON(m).(*jsjson.Object)
	}
	dependencies = append([]string{}, dependencies...)
	valid = true
	err = r.read(func(v *View) error {
		for _, m := range copies {
			ok, e := currentRuntimeSource(v, m)
			if e != nil {
				return e
			}
			if !ok {
				valid = false
				return nil
			}
		}
		for _, id := range dependencies {
			recalled, e := recalledAt(v, scene.ID, id)
			if e != nil {
				return e
			}
			if recalled {
				valid = false
				return nil
			}
		}
		return nil
	})
	return valid && err == nil, err
}
func (r *runtimeReads) PromptAuthority(scene botruntime.Scene, tools []*jsjson.Object) (any, error) {
	copied := make([]any, len(tools))
	for i, tool := range tools {
		if tool == nil {
			return nil, ErrInvalid
		}
		copied[i] = cloneRuntimeJSON(tool).(*jsjson.Object)
	}
	var result string
	err := r.read(func(v *View) error {
		rows, err := v.query("SELECT 'config' AS kind,key AS id,value FROM config WHERE key IN ('settings','models','rules') UNION ALL SELECT 'scene' AS kind,id,value FROM scenes WHERE id=? ORDER BY kind,id", scene.ID)
		if err != nil {
			return err
		}
		values := []any{}
		for rows.Next() {
			var kind, id, value string
			if err = rows.Scan(&kind, &id, &value); err != nil {
				break
			}
			values = append(values, obj("kind", kind, "id", id, "value", value))
		}
		if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
			return err
		}
		result, err = jsjson.Stringify([]any{values, copied})
		return err
	})
	return result, err
}
func presentRuntimeMessage(v *View, m *jsjson.Object) error {
	sceneValue, _, err := valueRow(v, "SELECT value FROM scenes WHERE id=?", str(m, "sceneId"))
	if err != nil {
		return err
	}
	scene, ok := sceneValue.(*jsjson.Object)
	if !ok || !oneBotScene(scene) {
		return nil
	}
	identity, exists, err := valueRow(v, "SELECT value FROM config WHERE key=?", "identity:"+str(m, "sceneId")+":"+str(m, "senderId"))
	if err != nil {
		return err
	}
	if !exists {
		identity = obj()
	}
	platform, platformPresent, err := coalescedIdentity(m, identity, "senderPlatformId")
	if err != nil {
		return err
	}
	name, namePresent, err := coalescedIdentity(m, identity, "senderDisplayName")
	if err != nil {
		return err
	}
	avatar, err := qqAvatar(platform)
	if err != nil {
		return err
	}
	optionalProperty(m, "senderDisplayName", name, namePresent)
	optionalProperty(m, "senderPlatformId", platform, platformPresent)
	optionalProperty(m, "senderAvatarUrl", avatar, avatar != nil)
	return nil
}
func (r *runtimeReads) ProjectDisplay(scene botruntime.Scene, messages []*jsjson.Object, locallyRecalled []string) ([]*jsjson.Object, error) {
	out := make([]*jsjson.Object, len(messages))
	for i, m := range messages {
		if m == nil {
			return nil, ErrInvalid
		}
		out[i] = cloneRuntimeJSON(m).(*jsjson.Object)
	}
	local := map[string]bool{}
	for _, id := range locallyRecalled {
		local[id] = true
	}
	if r.lease != nil && !r.lease.Load() {
		return nil, sql.ErrTxDone
	}
	if len(out) == 0 {
		return out, nil
	}
	err := r.read(func(v *View) error {
		for _, m := range out {
			recalled := local[str(m, "id")]
			if !recalled {
				var e error
				recalled, e = recalledAt(v, scene.ID, str(m, "id"))
				if e != nil {
					return e
				}
			}
			if recalled {
				_ = m.Set("parts", []any{})
				_ = m.Set("status", "recalled")
			} else if str(m, "origin") != "simulation" {
				if e := presentRuntimeMessage(v, m); e != nil {
					return e
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SceneParticipationEnabled reads the persisted row on every live eligibility
// check, including when prompt-window authority is disabled.
func (r *runtimeReads) SceneParticipationEnabled(scene botruntime.Scene) (enabled bool, err error) {
	err = r.read(func(v *View) error {
		value, _, e := valueRow(v, "SELECT value FROM scenes WHERE id=?", scene.ID)
		if e != nil {
			return e
		}
		if row, ok := value.(*jsjson.Object); ok {
			enabled = runtimeTruthy(get(row, "participationEnabled"))
		}
		return nil
	})
	return
}
