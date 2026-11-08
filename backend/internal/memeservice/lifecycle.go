package memeservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"os"
	"syscall"
	"time"
)

type deleteBarrier struct {
	hash   string
	scenes map[string]bool
	shared bool
}

func num(v any) float64 { n, _ := v.(float64); return n }
func (s *Service) withSession(ctx context.Context, ws *recovery.WriteSession, fn func(*scope) error) error {
	if e := ws.CheckBinding(s.authority, s.main); e != nil {
		return e
	}
	if e := ws.AssertReady(); e != nil {
		return e
	}
	return s.assertions.WithSession(ws).Read(ctx, func(r *assertions.Reader) error {
		a, e := assertionauthority.New(r, s.policy)
		if e != nil {
			return e
		}
		return fn(&scope{ws, r, a, s})
	})
}
func (s *Service) cancelAssetLocked(id string) {
	for _, task := range s.analyses {
		if task.id == id {
			task.cancel()
		}
	}
}
func (p *scope) validSource(a *Object) (bool, error) {
	for _, v := range arr(get(a, "sources")) {
		ok, e := p.proof.DomainSourceCurrent(obj(v))
		if e != nil {
			return false, e
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}
func (s *Service) stateEvent(a *Object) (recovery.MemeState, error) {
	q, e := s.qualification(a)
	return recovery.MemeState{ID: str(get(a, "id")), ContentHash: content(a), QualificationHash: q, State: recovery.MemeFlags{Available: yes(get(a, "available")), Disabled: yes(get(a, "disabled")), KnownOnly: yes(get(a, "knownOnly")), Shared: yes(get(a, "shared")), PrivacyReviewed: yes(get(a, "privacyReviewed"))}}, e
}
func (s *Service) Update(ctx context.Context, id string, patch *Object) (out *Object, e error) {
	e = s.transaction(ctx, func(p *scope) error {
		a, e := p.asset(id)
		if e != nil {
			return e
		}
		if a == nil {
			return errors.New("meme_not_found")
		}
		if yes(get(a, "knownOnly")) && (yes(get(patch, "available")) || yes(get(patch, "shared"))) {
			return errors.New("known_meme_requires_review")
		}
		next := copyObject(a)
		for _, k := range []string{"available", "shared", "privacyReviewed", "favorite", "disabled"} {
			if v, ok := patch.Get(k); ok {
				if _, ok := v.(bool); !ok {
					return errors.New("invalid_meme_setting")
				}
				set(next, k, v)
			}
		}
		if _, ok := patch.Get("available"); ok {
			set(next, "selectionOrigin", "manual")
		}
		if _, ok := patch.Get("shared"); ok {
			set(next, "sharingOrigin", "manual")
		}
		if _, ok := patch.Get("privacyReviewed"); ok {
			set(next, "sharingOrigin", "manual")
		}
		if v, ok := patch.Get("tags"); ok {
			tags, e := ValidateTags(v)
			if e != nil {
				return e
			}
			set(next, "tags", tags)
			set(next, "tagRevision", num(get(a, "tagRevision"))+1)
			set(next, "analyzed", true)
			next.Delete("analysisError")
		}
		if yes(get(next, "shared")) && !yes(get(next, "privacyReviewed")) {
			return errors.New("public_media_review_required")
		}
		if yes(get(next, "disabled")) {
			set(next, "available", false)
			s.mu.Lock()
			s.cancelAssetLocked(id)
			s.mu.Unlock()
		}
		if yes(get(next, "available")) && (!yes(get(next, "analyzed")) || len(arr(get(next, "sources"))) == 0) {
			return errors.New("meme_not_available")
		}
		event, e := s.stateEvent(next)
		if e != nil {
			return e
		}
		if _, e = p.ws.AppendIntent(event); e != nil {
			return e
		}
		if e = p.write(next); e != nil {
			return e
		}
		out = next
		return nil
	})
	return
}
func (s *Service) AdminSetIncluded(ctx context.Context, id string, included bool) (out *Object, e error) {
	e = s.transaction(ctx, func(p *scope) error { var e error; out, e = s.setIncluded(p, id, included); return e })
	return
}
func (s *Service) setIncluded(p *scope, id string, included bool) (*Object, error) {
	a, e := p.asset(id)
	if e != nil {
		return nil, e
	}
	_, deleting := s.epoch(id)
	if a == nil || deleting {
		return nil, errors.New("meme_not_found")
	}
	if included {
		if !yes(get(a, "analyzed")) || yes(get(a, "disabled")) {
			return nil, errors.New("meme_not_available")
		}
		valid, e := p.validSource(a)
		if e != nil {
			return nil, e
		}
		if !valid {
			return nil, errors.New("meme_not_available")
		}
	}
	s.mu.Lock()
	s.cancelAssetLocked(id)
	s.mu.Unlock()
	set(a, "knownOnly", !included)
	set(a, "available", included)
	set(a, "shared", false)
	set(a, "privacyReviewed", false)
	set(a, "selectionOrigin", "manual")
	set(a, "sharingOrigin", "manual")
	set(a, "tagRevision", num(get(a, "tagRevision"))+1)
	if state := str(get(a, "analysisState")); state == "running" || state == "retrying" {
		set(a, "analysisState", "cancelled")
		a.Delete("analysisNextRetryAt")
	}
	event, e := s.stateEvent(a)
	if e != nil {
		return nil, e
	}
	if _, e = p.ws.AppendIntent(event); e != nil {
		return nil, e
	}
	if e = p.write(a); e != nil {
		return nil, e
	}
	return a, nil
}
func (s *Service) PromoteKnown(ctx context.Context, c Context, id string) (out *Object, e error) {
	e = s.transaction(ctx, func(p *scope) error {
		if e := p.allow(c); e != nil {
			return e
		}
		a, e := p.asset(id)
		if e != nil {
			return e
		}
		ok, e := p.visible(c, a, false)
		if e != nil {
			return e
		}
		if !ok {
			return errors.New("meme_not_available")
		}
		if !yes(get(a, "knownOnly")) || !yes(get(a, "analyzed")) || yes(get(a, "disabled")) {
			return errors.New("known_meme_not_reviewable")
		}
		a, e = s.setIncluded(p, id, true)
		if e != nil {
			return e
		}
		out = sanitize(a)
		return nil
	})
	return
}
func (p *scope) retireReferences(where string, args ...any) error {
	rows, e := p.ws.Query("SELECT scene_id,reference_hash,message_id FROM meme_media_references WHERE "+where, args...)
	if e != nil {
		return e
	}
	keys := []string{}
	for rows.Next() {
		var scene, ref, msg string
		if e = rows.Scan(&scene, &ref, &msg); e != nil {
			rows.Close()
			return e
		}
		keys = append(keys, identity(scene, ref, msg))
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, key := range keys {
		if _, e = p.ws.Exec("INSERT OR IGNORE INTO meme_reference_denials VALUES(?)", key); e != nil {
			return e
		}
	}
	_, e = p.ws.Exec("DELETE FROM meme_media_references WHERE "+where, args...)
	return e
}

var errDeleteAbsent = errors.New("meme_delete_absent")

func (s *Service) AdminDelete(ctx context.Context, id string) (bool, error) {
	// Serialize this per-asset begin without holding the filesystem writer queue:
	// intake may already be awaiting IO, and the epoch invalidates its final write.
	s.mu.Lock()
	if s.deleting[id] {
		s.mu.Unlock()
		return false, errors.New("media_delete_in_progress")
	}
	s.deleting[id] = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.deleting, id); delete(s.deletingContent, id); s.mu.Unlock() }()
	var asset *Object
	ticket, e := s.authority.BeginDurable(ctx, s.main, func(ws *recovery.WriteSession) (recovery.Revocation, error) {
		var event recovery.MemeDelete
		e := s.withSession(ctx, ws, func(p *scope) error {
			a, e := p.asset(id)
			if e != nil {
				return e
			}
			if a == nil {
				return errDeleteAbsent
			}
			q, e := s.qualification(a)
			if e != nil {
				return e
			}
			asset = a
			event = recovery.MemeDelete{ID: id, ContentHash: content(a), QualificationHash: q, Format: str(get(a, "format"))}
			return nil
		})
		return event, e
	})
	if errors.Is(e, errDeleteAbsent) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	s.mu.Lock()
	barrier := deleteBarrier{hash: content(asset), scenes: map[string]bool{}, shared: yes(get(asset, "shared")) && yes(get(asset, "privacyReviewed")) && !yes(get(asset, "knownOnly"))}
	for _, v := range arr(get(asset, "sources")) {
		barrier.scenes[str(get(obj(v), "sceneId"))] = true
	}
	s.deletingContent[id] = barrier
	s.epochs[id]++
	s.cancelAssetLocked(id)
	s.evictThumbnailLocked(id)
	s.mu.Unlock()
	release := s.assetLock(id)
	defer release()
	path, e := s.file(id, str(get(asset, "format")))
	if e != nil {
		return false, e
	}
	if e = syscall.Unlink(path); e != nil && !errors.Is(e, os.ErrNotExist) {
		return false, errors.New("media_delete_failed")
	}
	e = s.authority.CommitDurable(ctx, s.main, ticket, func(ws *recovery.WriteSession) error {
		return s.withSession(ctx, ws, func(p *scope) error {
			if e := p.retireReferences("asset_id=?", id); e != nil {
				return e
			}
			if _, e := ws.Exec("DELETE FROM meme_assets WHERE id=?", id); e != nil {
				return e
			}
			s.mu.Lock()
			s.evictThumbnailLocked(id)
			s.mu.Unlock()
			return nil
		})
	})
	return e == nil, e
}
func (p *scope) recomputePreference(a *Object) error {
	rows, e := p.ws.Query("SELECT COALESCE(SUM(positive),0) FROM meme_feedback WHERE asset_id=?", get(a, "id"))
	if e != nil {
		return e
	}
	var score float64
	if rows.Next() {
		e = rows.Scan(&score)
	}
	rows.Close()
	if e != nil {
		return e
	}
	set(a, "preference", max(-5, min(5, score*0.2)))
	return p.write(a)
}
func (s *Service) RecordFeedback(ctx context.Context, c Context, id string, source *Object, positive bool) error {
	return s.transaction(ctx, func(p *scope) error { return p.recordFeedback(c, id, source, positive) })
}
func (p *scope) recordFeedback(c Context, id string, source *Object, positive bool) error {
	if e := p.allow(c); e != nil {
		return e
	}
	if e := p.observed(c, source); e != nil {
		return e
	}
	a, e := p.asset(id)
	if e != nil {
		return e
	}
	ok, e := p.visible(c, a, true)
	if e != nil {
		return e
	}
	if !ok {
		return errors.New("meme_not_available")
	}
	value := -1
	if positive {
		value = 1
	}
	r, e := p.ws.Exec("INSERT OR IGNORE INTO meme_feedback VALUES(?,?,?,?)", id, get(source, "sceneId"), get(source, "messageId"), value)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n > 0 {
		return p.recomputePreference(a)
	}
	return nil
}

// RecallWithSession is the synchronous lifecycle port for a runtime-owned recall.
// It borrows the exact active child; no detached source proof or nested owner.
func (s *Service) RecallWithSession(ctx context.Context, ws *recovery.WriteSession, scene, message string) (ids []string, e error) {
	e = s.withSession(ctx, ws, func(p *scope) error {
		if _, e := ws.AppendIntent(recovery.Source{SceneID: scene, MessageID: message}); e != nil {
			return e
		}
		if _, e := ws.Exec("INSERT OR IGNORE INTO recall_tombstones VALUES(?,?,?)", scene, message, time.Now().UTC().Format("2006-01-02T15:04:05.000Z")); e != nil {
			return e
		}
		rows, e := ws.Query("SELECT asset_id FROM meme_sources WHERE scene_id=? AND message_id=? UNION SELECT asset_id FROM meme_feedback WHERE scene_id=? AND message_id=?", scene, message, scene, message)
		if e != nil {
			return e
		}
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
		if e = p.retireReferences("scene_id=? AND message_id=?", scene, message); e != nil {
			return e
		}
		if _, e = ws.Exec("DELETE FROM meme_feedback WHERE scene_id=? AND message_id=?", scene, message); e != nil {
			return e
		}
		for _, id := range ids {
			s.mu.Lock()
			if task := s.analyses[scene+":"+id]; task != nil {
				task.cancel()
			}
			s.mu.Unlock()
			a, e := p.asset(id)
			if e != nil {
				return e
			}
			if a == nil {
				return errors.New("meme_not_found")
			}
			sources := []any{}
			for _, v := range arr(get(a, "sources")) {
				o := obj(v)
				if str(get(o, "sceneId")) != scene || str(get(o, "messageId")) != message {
					sources = append(sources, v)
				}
			}
			set(a, "sources", sources)
			if len(sources) == 0 {
				set(a, "available", false)
				set(a, "disabled", true)
			}
			if e = p.recomputePreference(a); e != nil {
				return e
			}
		}
		return nil
	})
	return
}
func (s *Service) Recall(ctx context.Context, scene, message string) (ids []string, e error) {
	e = s.authority.WithWriteSession(ctx, s.main, func(ws *recovery.WriteSession) error {
		var e error
		ids, e = s.RecallWithSession(ctx, ws, scene, message)
		return e
	})
	return
}
