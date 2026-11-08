package recoverydomains

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"math"
	"os"
	"path/filepath"
	"sort"
)

func (o *Owner) memeQualification(a *object) (string, error) {
	v := new(object)
	for _, k := range []string{"id", "format", "bytes", "width", "height", "frames", "analyzed", "tags"} {
		if x, ok := a.Get(k); ok {
			set(v, k, x)
		}
	}
	content := get(a, "contentHash")
	if content == nil {
		content = get(a, "id")
	}
	set(v, "contentHash", content)
	input, e := requiredArray(a, "sources")
	if e != nil {
		return "", e
	}
	sources := append([]any{}, input...)
	var failure error
	if len(sources) > 1 && o.options.Compare == nil {
		return "", errors.New("recovery_domain_collation_required")
	}
	sort.SliceStable(sources, func(i, j int) bool {
		a, e := encode(sources[i])
		if e != nil {
			failure = e
		}
		b, e := encode(sources[j])
		if e != nil {
			failure = e
		}
		n, e := o.options.Compare(a, b)
		if e != nil {
			failure = e
		}
		return n < 0
	})
	if failure != nil {
		return "", failure
	}
	set(v, "sources", sources)
	return hash(v, o.options)
}
func (o *Owner) stateMeme(ctx context.Context, tx *recovery.MaintenanceTx, event *object) error {
	a, e := read(ctx, tx, "SELECT value FROM meme_assets WHERE id=?", get(event, "id"))
	if e != nil {
		return e
	}
	if a == nil {
		return errors.New("recovery_meme_qualification_missing")
	}
	content := get(a, "contentHash")
	if content == nil {
		content = get(a, "id")
	}
	h, e := o.memeQualification(a)
	if e != nil {
		return e
	}
	if content != get(event, "contentHash") || h != str(get(event, "qualificationHash")) {
		return errors.New("recovery_meme_qualification_missing")
	}
	state := obj(get(event, "state"))
	if yes(get(state, "available")) {
		valid := false
		for _, s := range arr(get(a, "sources")) {
			ok, e := sourceCurrent(ctx, tx, str(get(obj(s), "sceneId")), obj(s), false, true)
			if e != nil {
				return e
			}
			if ok {
				valid = true
				break
			}
		}
		if !yes(get(a, "analyzed")) || !valid || yes(get(state, "disabled")) || yes(get(state, "knownOnly")) {
			return errors.New("recovery_meme_grant_invalid")
		}
	}
	if yes(get(state, "shared")) && (!yes(get(state, "privacyReviewed")) || yes(get(state, "knownOnly"))) {
		return errors.New("recovery_meme_grant_invalid")
	}
	for _, k := range state.Keys() {
		set(a, k, get(state, k))
	}
	set(a, "selectionOrigin", "manual")
	set(a, "sharingOrigin", "manual")
	set(a, "tagRevision", num(get(a, "tagRevision"))+1)
	return o.writeMeme(ctx, tx, a)
}
func (o *Owner) writeMeme(ctx context.Context, tx *recovery.MaintenanceTx, a *object) error {
	raw, e := encode(a)
	if e != nil {
		return e
	}
	if e = exec(ctx, tx, "INSERT INTO meme_assets VALUES(?,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value", get(a, "id"), raw); e != nil {
		return e
	}
	if e = o.indexConcepts(ctx, tx, a); e != nil {
		return e
	}
	if e = exec(ctx, tx, "DELETE FROM meme_sources WHERE asset_id=?", get(a, "id")); e != nil {
		return e
	}
	for _, v := range arr(get(a, "sources")) {
		s := obj(v)
		if e = exec(ctx, tx, "INSERT INTO meme_sources VALUES(?,?,?,?)", get(a, "id"), get(s, "sceneId"), get(s, "messageId"), get(s, "senderId")); e != nil {
			return e
		}
	}
	return nil
}
func retireReferences(ctx context.Context, tx *recovery.MaintenanceTx, where string, args ...any) error {
	rows, e := tx.QueryContext(ctx, "SELECT scene_id,reference_hash,message_id FROM meme_media_references WHERE "+where, args...)
	if e != nil {
		return e
	}
	keys := []string{}
	for rows.Next() {
		var s, r, m string
		if e = rows.Scan(&s, &r, &m); e != nil {
			rows.Close()
			return e
		}
		keys = append(keys, key(s, r, m))
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, k := range keys {
		if e = exec(ctx, tx, "INSERT OR IGNORE INTO meme_reference_denials VALUES(?)", k); e != nil {
			return e
		}
	}
	return exec(ctx, tx, "DELETE FROM meme_media_references WHERE "+where, args...)
}

// File deletion requires the owner's exclusive staging connection to remain
// in autocommit between the checked SQL phases; no outer SQL scope may span unlink.
func (o *Owner) deleteMeme(ctx context.Context, event *object) error {
	if e := o.owner.RequireAutocommit(ctx); e != nil {
		return e
	}
	var present bool
	e := o.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		if e := assertSchema(ctx, tx); e != nil {
			return e
		}
		a, e := read(ctx, tx, "SELECT value FROM meme_assets WHERE id=?", get(event, "id"))
		if e != nil {
			return e
		}
		present = a != nil
		if !present {
			return nil
		}
		h, e := o.memeQualification(a)
		if e != nil {
			return e
		}
		content := get(a, "contentHash")
		if content == nil {
			content = get(a, "id")
		}
		if content != get(event, "contentHash") || get(a, "format") != get(event, "format") || h != str(get(event, "qualificationHash")) {
			return errors.New("recovery_meme_identity_mismatch")
		}
		return nil
	})
	if e != nil {
		return e
	}
	path := filepath.Join(o.mediaRoot, str(get(event, "id"))+"."+str(get(event, "format")))
	bytes, e := os.ReadFile(path)
	if e != nil && !os.IsNotExist(e) {
		return e
	}
	if e == nil && digest(string(bytes)) != str(get(event, "contentHash")) {
		return errors.New("recovery_meme_content_mismatch")
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	e = os.Remove(path)
	if e != nil && !os.IsNotExist(e) {
		if present {
			return errors.New("media_delete_failed")
		}
		return e
	}
	return o.owner.Apply(ctx, 1, func(tx *recovery.MaintenanceTx) error {
		if e := retireReferences(ctx, tx, "asset_id=?", get(event, "id")); e != nil {
			return e
		}
		if present {
			return exec(ctx, tx, "DELETE FROM meme_assets WHERE id=?", get(event, "id"))
		}
		return nil
	})
}
func (o *Owner) recall(ctx context.Context, tx *recovery.MaintenanceTx, event *object) error {
	scene, msg := str(get(event, "sceneId")), str(get(event, "messageId"))
	rows, e := tx.QueryContext(ctx, "SELECT asset_id FROM meme_sources WHERE scene_id=? AND message_id=? UNION SELECT asset_id FROM meme_feedback WHERE scene_id=? AND message_id=?", scene, msg, scene, msg)
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	outcomes, e := sourceOutcomes(ctx, tx, scene, msg)
	if e != nil {
		return e
	}
	for _, outcome := range outcomes {
		policies, e := policiesForOutcome(ctx, tx, scene, str(get(outcome, "id")))
		if e != nil {
			return e
		}
		for _, p := range policies {
			o.removedPolicies[key(str(get(p, "sceneId")), str(get(p, "id")))] = true
		}
	}
	if e = tx.RecordReplayEffect(ctx, recovery.Source{SceneID: scene, MessageID: msg}); e != nil {
		return e
	}
	if e = exec(ctx, tx, "INSERT OR IGNORE INTO recall_tombstones VALUES(?,?,?)", scene, msg, o.options.Now().UTC().Format("2006-01-02T15:04:05.000Z")); e != nil {
		return e
	}
	if e = o.recallHabits(ctx, tx, scene, msg); e != nil {
		return e
	}
	if e = tx.RecordReplayEffect(ctx, recovery.Source{SceneID: scene, MessageID: msg}); e != nil {
		return e
	}
	if e = retireReferences(ctx, tx, "scene_id=? AND message_id=?", scene, msg); e != nil {
		return e
	}
	if e = exec(ctx, tx, "DELETE FROM meme_feedback WHERE scene_id=? AND message_id=?", scene, msg); e != nil {
		return e
	}
	for _, id := range ids {
		a, e := read(ctx, tx, "SELECT value FROM meme_assets WHERE id=?", id)
		if e != nil {
			return e
		}
		if a == nil {
			return errors.New("recovery_domain_material_invalid")
		}
		remaining := []any{}
		sources, e := requiredArray(a, "sources")
		if e != nil {
			return e
		}
		for _, v := range sources {
			s := obj(v)
			if str(get(s, "sceneId")) != scene || str(get(s, "messageId")) != msg {
				remaining = append(remaining, v)
			}
		}
		set(a, "sources", remaining)
		if len(remaining) == 0 {
			set(a, "available", false)
			set(a, "disabled", true)
		}
		var score float64
		if e = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(positive),0) FROM meme_feedback WHERE asset_id=?", id).Scan(&score); e != nil {
			return e
		}
		set(a, "preference", math.Max(-5, math.Min(5, score*.2)))
		if e = o.writeMeme(ctx, tx, a); e != nil {
			return e
		}
	}
	if e = o.revokeExperienceSource(ctx, tx, scene, msg); e != nil {
		return e
	}
	for _, id := range ids {
		if e = o.captureMedia(ctx, tx, id); e != nil {
			return e
		}
	}
	return nil
}
