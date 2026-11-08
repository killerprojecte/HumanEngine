package habitservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"time"
)

func (s *Service) Propose(ctx context.Context, c Context, input *Object) (out *Object, err error) {
	owned, e := clone(input)
	if e != nil {
		return nil, e
	}
	c, e = copyContext(c)
	if e != nil {
		return nil, e
	}
	err = s.Transaction(ctx, func(b *Service) error { var e error; out, e = b.propose(ctx, c, owned); return e })
	if err != nil {
		return nil, err
	}
	return
}
func (s *Service) propose(ctx context.Context, c Context, input *Object) (*Object, error) {
	reason, e := s.validate(ctx, c, get(input, "sources"))
	if e != nil {
		return nil, e
	}
	if reason != "" {
		return rejected(reason), nil
	}
	if !validCandidate(input) {
		return rejected("invalid_candidate"), nil
	}
	original, e := fingerprint(c, input, "", "")
	if e != nil {
		return nil, e
	}
	legacy := original
	if str(input, "kind") == "botExpression" {
		legacy, e = hash([]any{"bot", get(input, "preference")})
		if e != nil {
			return nil, e
		}
	}
	records, e := s.AdminList(ctx)
	if e != nil {
		return nil, e
	}
	for _, r := range records {
		if !truthy(get(r, "locked")) || number(r, "evidenceVersion") == 2 || str(r, "kind") != str(input, "kind") {
			continue
		}
		match := false
		if str(input, "kind") == "botExpression" {
			rp, ip := str(r, "preference"), str(input, "preference")
			match = rp == ip || (rp == "concise" || rp == "detail") && (ip == "concise" || ip == "detail")
		} else {
			match = str(r, "sceneId") == c.SceneID && str(r, "content") == str(input, "content")
		}
		if match {
			return rejected("locked_legacy_candidate"), nil
		}
	}
	p, e := s.prepare(ctx, c, input)
	if e != nil {
		return nil, e
	}
	if p.reason != "" {
		return rejected(p.reason), nil
	}
	input = p.input
	fp, e := fingerprint(c, input, p.scene, p.context)
	if e != nil {
		return nil, e
	}
	fps := []string{fp, original, legacy}
	if str(input, "kind") == "botExpression" && str(input, "preference") == "concise" {
		for _, pref := range []string{"concise", "detail"} {
			v, e := hash([]any{"bot", pref})
			if e != nil {
				return nil, e
			}
			fps = append(fps, v)
		}
	}
	var deleted bool
	var record *Object
	e = s.read(ctx, func(r *readScope) error {
		for _, candidate := range fps {
			_, found, e := r.raw("SELECT 1 FROM habit_deleted WHERE fingerprint=?", candidate)
			if e != nil {
				return e
			}
			if found {
				deleted = true
				return nil
			}
		}
		raw, found, e := r.raw("SELECT value FROM habits WHERE fingerprint=?", fp)
		if e != nil || !found {
			return e
		}
		record, e = parseRecord(raw)
		return e
	})
	if e != nil {
		return nil, e
	}
	if deleted {
		return rejected("manually_deleted"), nil
	}
	if record == nil {
		id, e := s.id()
		if e != nil {
			return nil, e
		}
		learned, e := iso(s.now(s))
		if e != nil {
			return nil, e
		}
		record = obj("id", id, "kind", get(input, "kind"))
		if str(input, "kind") == "sceneRule" {
			_ = record.Set("sceneId", c.SceneID)
			_ = record.Set("content", trim(str(input, "content")))
		} else {
			_ = record.Set("preference", get(input, "preference"))
		}
		if p.scene != "" {
			_ = record.Set("sceneId", p.scene)
		}
		if p.context != "" {
			_ = record.Set("context", p.context)
		}
		for _, m := range obj("evidenceVersion", float64(2), "sources", []any{}, "weight", float64(0), "confidence", float64(0), "status", "active", "locked", false, "learnedAt", learned).Members() {
			_ = record.Set(m.Key, m.Value)
		}
	}
	if truthy(get(record, "locked")) || str(record, "status") == "invalidated" {
		return rejected("locked_or_invalidated"), nil
	}
	direction := number(input, "direction")
	if str(input, "kind") == "sceneRule" {
		direction = 1
	}
	if e = s.add(ctx, c, record, arr(get(input, "sources")), direction); e != nil {
		return nil, e
	}
	raw, e := jsjson.Stringify(record)
	if e != nil {
		return nil, e
	}
	if _, e = s.session.Exec("INSERT INTO habits VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value", sqlText(str(record, "id")), fp, raw); e != nil {
		return nil, e
	}
	return saved(record), nil
}
func (s *Service) Feedback(ctx context.Context, c Context, id string, input *Object) (out *Object, err error) {
	owned, e := clone(input)
	if e != nil {
		return nil, e
	}
	c, e = copyContext(c)
	if e != nil {
		return nil, e
	}
	err = s.Transaction(ctx, func(b *Service) error {
		reason, e := b.validate(ctx, c, get(owned, "sources"))
		if e != nil {
			return e
		}
		if reason != "" {
			out = rejected(reason)
			return nil
		}
		r, e := b.get(ctx, id)
		if e != nil {
			return e
		}
		if r == nil || truthy(get(r, "sceneId")) && str(r, "sceneId") != c.SceneID {
			out = rejected("not_available")
			return nil
		}
		direction := number(owned, "direction")
		if truthy(get(r, "locked")) || str(r, "status") == "invalidated" || !validDirection(get(owned, "direction")) {
			out = rejected("locked_or_invalid_feedback")
			return nil
		}
		if str(r, "kind") == "botExpression" {
			p, e := b.prepare(ctx, c, obj("kind", "botExpression", "preference", get(r, "preference"), "direction", direction, "sources", get(owned, "sources")))
			if e != nil {
				return e
			}
			if p.reason != "" || p.scene != str(r, "sceneId") || p.context != str(r, "context") {
				out = rejected("incompatible_evidence_scope")
				return nil
			}
		}
		if e = b.add(ctx, c, r, arr(get(owned, "sources")), direction); e != nil {
			return e
		}
		if e = b.save(r); e != nil {
			return e
		}
		out = saved(r)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return
}
func (s *Service) Update(ctx context.Context, id string, patch *Object) (out *Object, err error) {
	owned, e := clone(patch)
	if e != nil {
		return nil, e
	}
	err = s.Transaction(ctx, func(b *Service) error {
		r, e := b.get(ctx, id)
		if e != nil {
			return e
		}
		if r == nil {
			return errors.New("habit_not_found")
		}
		if owned == nil {
			return errors.New("invalid_habit_patch")
		}
		for _, key := range owned.Keys() {
			if key != "locked" {
				return errors.New("invalid_habit_patch")
			}
		}
		if v, present := owned.Get("locked"); present {
			if _, ok := v.(bool); !ok {
				return errors.New("invalid_habit_patch")
			}
			_ = r.Set("locked", v)
		}
		if e = b.save(r); e != nil {
			return e
		}
		out = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return
}
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.Transaction(ctx, func(b *Service) error {
		if e := b.session.AssertReady(); e != nil {
			return e
		}
		var fp string
		var found bool
		e := b.read(ctx, func(r *readScope) error {
			var e error
			fp, found, e = r.raw("SELECT fingerprint FROM habits WHERE id=?", sqlText(id))
			return e
		})
		if e != nil {
			return e
		}
		if found {
			if _, e = b.session.AppendIntent(recovery.HabitDelete{ID: id, Fingerprint: fp}); e != nil {
				return e
			}
		}
		if _, e = b.session.Exec("INSERT OR IGNORE INTO habit_deleted SELECT fingerprint FROM habits WHERE id=?", sqlText(id)); e != nil {
			return e
		}
		_, e = b.session.Exec("DELETE FROM habits WHERE id=?", sqlText(id))
		return e
	})
}

// Recall preserves source order: durable source intent and tombstone first,
// then rowid-ordered habit retractions. A failed retraction rolls back main,
// never the independent durable intent. It deliberately does not replay edits.
func (s *Service) Recall(ctx context.Context, scene, id string) (changed int, err error) {
	err = s.Transaction(ctx, func(b *Service) error {
		if _, e := b.session.AppendIntent(recovery.Source{SceneID: scene, MessageID: id}); e != nil {
			return e
		}
		if _, e := b.session.Exec("INSERT OR IGNORE INTO recall_tombstones VALUES(?,?,?)", sqlText(scene), sqlText(id), time.Now().UTC().Format("2006-01-02T15:04:05.000Z")); e != nil {
			return e
		}
		var e error
		changed, e = b.retract(ctx, scene, id)
		return e
	})
	if err != nil {
		return 0, err
	}
	return
}

func (s *Service) retract(ctx context.Context, scene, id string) (int, error) {
	records, e := s.AdminList(ctx)
	if e != nil {
		return 0, e
	}
	changed := 0
	for _, r := range records {
		sources, e := requiredSources(r)
		if e != nil {
			return 0, e
		}
		remaining := []any{}
		for _, source := range sources {
			p := object(source)
			if str(p, "sceneId") != scene || str(p, "messageId") != id {
				remaining = append(remaining, source)
			}
		}
		if len(remaining) == len(sources) {
			continue
		}
		_ = r.Set("sources", remaining)
		if e = s.recalculate(r); e != nil {
			return 0, e
		}
		if len(remaining) == 0 {
			_ = r.Set("status", "invalidated")
		}
		if e = s.save(r); e != nil {
			return 0, e
		}
		changed++
	}
	return changed, nil
}
