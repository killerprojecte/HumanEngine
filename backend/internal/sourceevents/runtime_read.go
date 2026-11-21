package sourceevents

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"sync/atomic"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

type runtimeReads struct {
	store *Store
	view  *View
	lease *atomic.Bool
}

func (r *runtimeReads) read(fn func(*View) error) error {
	if r.lease != nil && !r.lease.Load() {
		return sql.ErrTxDone
	}
	if r.view != nil {
		if err := r.view.ready(); err != nil {
			return err
		}
		if err := fn(r.view); err != nil {
			return err
		}
		return r.view.ready()
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

// UnversionedBrowserImage checks only the exact source ID needed for adapter
// fallback. It does not enroll, edit, or retain the returned adapter body.
func (v *View) UnversionedBrowserImage(sceneID, messageID string) (bool, error) {
	recalled, err := recalledAt(v, sceneID, messageID)
	if err != nil || recalled {
		return false, err
	}
	return v.notEnrolledBrowserImage(sceneID, messageID)
}

func (v *View) notEnrolledBrowserImage(sceneID, messageID string) (bool, error) {
	meta, err := v.Registered(sceneID, messageID)
	return meta == nil && err == nil, err
}

func recalledAt(v *View, sceneID, messageID string) (bool, error) {
	var one int
	return v.one("SELECT 1 FROM recall_tombstones WHERE scene_id=? AND message_id=?", []any{sceneID, messageID}, &one)
}
func (r *runtimeReads) IsRecalled(scene botruntime.Scene, id string) (recalled bool, err error) {
	err = r.read(func(v *View) error { var e error; recalled, e = recalledAt(v, scene.ID, id); return e })
	return
}

// FilterUnrecalled compacts a private candidate slice in one fresh read. It
// preserves the exact recall lookup, caller order, and duplicate rows.
func (r *RuntimeAuthority) FilterUnrecalled(scene botruntime.Scene, rows []*jsjson.Object) ([]*jsjson.Object, error) {
	kept := rows[:0]
	err := r.read(func(v *View) error {
		for _, row := range rows {
			recalled, err := recalledAt(v, scene.ID, str(row, "id"))
			if err != nil {
				return err
			}
			if !recalled {
				kept = append(kept, row)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return kept, nil
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
func presentRuntimeMessage(v *View, m *jsjson.Object) (*jsjson.Object, error) {
	sceneValue, _, err := valueRow(v, "SELECT value FROM scenes WHERE id=?", str(m, "sceneId"))
	if err != nil {
		return nil, err
	}
	scene, ok := sceneValue.(*jsjson.Object)
	if !ok || !oneBotScene(scene) {
		return scene, nil
	}
	identity, exists, err := valueRow(v, "SELECT value FROM config WHERE key=?", "identity:"+str(m, "sceneId")+":"+str(m, "senderId"))
	if err != nil {
		return nil, err
	}
	if !exists {
		identity = obj()
	}
	platform, platformPresent, err := coalescedIdentity(m, identity, "senderPlatformId")
	if err != nil {
		return nil, err
	}
	name, namePresent, err := coalescedIdentity(m, identity, "senderDisplayName")
	if err != nil {
		return nil, err
	}
	avatar, err := qqAvatar(platform)
	if err != nil {
		return nil, err
	}
	optionalProperty(m, "senderDisplayName", name, namePresent)
	optionalProperty(m, "senderPlatformId", platform, platformPresent)
	optionalProperty(m, "senderAvatarUrl", avatar, avatar != nil)
	return scene, nil
}
func (r *runtimeReads) ProjectDisplay(scene botruntime.Scene, messages []*jsjson.Object, locallyRecalled []string) ([]*jsjson.Object, error) {
	return r.projectDisplay(scene, messages, locallyRecalled, nil)
}

func (r *runtimeReads) ProjectBoundDisplay(scene botruntime.Scene, messages []*jsjson.Object, locallyRecalled []string) ([]*jsjson.Object, bool, error) {
	var bound bool
	out, err := r.projectDisplay(scene, messages, locallyRecalled, &bound)
	return out, bound, err
}

func (r *runtimeReads) projectDisplay(scene botruntime.Scene, messages []*jsjson.Object, locallyRecalled []string, bound *bool) ([]*jsjson.Object, error) {
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
		checkBinding := bound != nil
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
				stored, err := presentRuntimeMessage(v, m)
				if err != nil {
					return err
				}
				// Reuse the immutable scene already read for this visible row.
				// Recalled rows do not consume the one binding comparison.
				if checkBinding && str(m, "sceneId") == scene.ID {
					checkBinding = false
					if stored != nil {
						actual := sceneFromValue(stored)
						*bound = actual.ID == scene.ID && actual.Kind == scene.Kind &&
							actual.AdapterID == scene.AdapterID && actual.AccountID == scene.AccountID &&
							actual.PlatformSceneID == scene.PlatformSceneID
					}
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

// ProjectRemoteHistory checks recall in one authority snapshot, keeps only the
// latest 30 eligible adapter rows, then copies and presents those rows.
func (r *runtimeReads) ProjectRemoteHistory(scene botruntime.Scene, messages []*jsjson.Object, locallyRecalled map[string]bool, bot string, candidate func(*jsjson.Object) botruntime.DisplayImageLinks) ([]*jsjson.Object, map[*jsjson.Object]botruntime.DisplayImageLinks, error) {
	var links map[*jsjson.Object]botruntime.DisplayImageLinks
	out := make([]*jsjson.Object, 0, min(len(messages), botruntime.BrowserHistoryWindow))
	err := r.read(func(v *View) error {
		for i := len(messages) - 1; i >= 0 && len(out) < botruntime.BrowserHistoryWindow; i-- {
			m := messages[i]
			id := str(m, "id")
			if str(m, "sceneId") != scene.ID || locallyRecalled[id] {
				continue
			}
			recalled, err := recalledAt(v, scene.ID, id)
			if err != nil {
				return err
			}
			if !recalled {
				out = append(out, cloneRuntimeJSON(m).(*jsjson.Object))
			}
		}
		slices.Reverse(out)
		for _, m := range out {
			botruntime.NormalizeOwnedBotIdentity(m, bot)
			persisted, err := presentRuntimeMessage(v, m)
			if err != nil {
				return err
			}
			if candidate == nil {
				continue
			}
			proof := candidate(m)
			if proof == (botruntime.DisplayImageLinks{}) || persisted == nil || !sameRuntimeBinding(scene, sceneFromValue(persisted)) {
				continue
			}
			unversioned, err := v.notEnrolledBrowserImage(scene.ID, str(m, "id"))
			if err != nil {
				return err
			}
			if unversioned {
				if links == nil {
					links = make(map[*jsjson.Object]botruntime.DisplayImageLinks)
				}
				links[m] = proof
			}
		}
		return nil
	})
	return out, links, err
}

func sameRuntimeBinding(a, b botruntime.Scene) bool {
	return a.ID == b.ID && a.Kind == b.Kind && a.AdapterID == b.AdapterID &&
		a.AccountID == b.AccountID && a.PlatformSceneID == b.PlatformSceneID
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
