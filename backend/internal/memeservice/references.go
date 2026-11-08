package memeservice

import (
	"context"
	"errors"
	"os"
	"time"
)

func (p *scope) referenceHash(c Context, ref string) (string, error) {
	scene, e := p.reader.Scene(c.SceneID)
	if e != nil {
		return "", e
	}
	if c.Simulation || scene == nil || ref == "" || units(ref) > 16*1024*1024 {
		return "", nil
	}
	return identity(c.SceneID, get(scene, "adapterId"), get(scene, "accountId"), ref), nil
}
func (p *scope) denied(scene, hash, message string) (bool, error) {
	r, e := p.ws.Query("SELECT 1 FROM meme_reference_denials WHERE key=?", identity(scene, hash, message))
	if e != nil {
		return false, e
	}
	defer r.Close()
	found := r.Next()
	return found, r.Err()
}
func (p *scope) reference(c Context, ref, message string) (*Object, bool, error) {
	hash, e := p.referenceHash(c, ref)
	if e != nil || hash == "" || message == "" {
		return nil, false, e
	}
	denied, e := p.denied(c.SceneID, hash, message)
	if e != nil || denied {
		return nil, denied, e
	}
	r, e := p.ws.Query("SELECT asset_id,message_id,sender_id,content_hash FROM meme_media_references WHERE scene_id=? AND reference_hash=? AND message_id=?", c.SceneID, hash, message)
	if e != nil {
		return nil, false, e
	}
	var id, msg, sender, contentHash string
	if !r.Next() {
		e = r.Err()
		r.Close()
		return nil, false, e
	}
	e = r.Scan(&id, &msg, &sender, &contentHash)
	r.Close()
	if e != nil {
		return nil, false, e
	}
	v := object()
	set(v, "id", id)
	set(v, "messageId", msg)
	set(v, "senderId", sender)
	set(v, "contentHash", contentHash)
	return v, false, nil
}
func (s *Service) HasMediaReference(ctx context.Context, c Context, ref, message string) (found bool, e error) {
	e = s.transaction(ctx, func(p *scope) error {
		row, denied, e := p.reference(c, ref, message)
		found = denied || row != nil
		return e
	})
	return
}
func (s *Service) BindMediaReference(ctx context.Context, c Context, ref, id string, source *Object, b []byte) error {
	hash := digest(b)
	length := len(b)
	return s.transaction(ctx, func(p *scope) error {
		if e := p.observed(c, source); e != nil {
			return e
		}
		rh, e := p.referenceHash(c, ref)
		if e != nil {
			return e
		}
		if rh == "" {
			return errors.New("invalid_media_reference")
		}
		denied, e := p.denied(c.SceneID, rh, str(get(source, "messageId")))
		if e != nil {
			return e
		}
		if denied {
			return errors.New("image_source_unavailable")
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
		if length == 0 || length > 64*1024*1024 || hash != content(a) {
			return errors.New("media_content_hash_mismatch")
		}
		found := false
		for _, v := range arr(get(a, "sources")) {
			if same(obj(v), source) {
				found = true
			}
		}
		if !found {
			return errors.New("invalid_source_evidence")
		}
		if _, e = p.ws.Exec(`INSERT INTO meme_media_references VALUES(?,?,?,?,?,?,?) ON CONFLICT(scene_id,reference_hash,message_id) DO UPDATE SET asset_id=excluded.asset_id,message_id=excluded.message_id,sender_id=excluded.sender_id,content_hash=excluded.content_hash,updated_at=excluded.updated_at`, c.SceneID, rh, id, get(source, "messageId"), get(source, "senderId"), content(a), time.Now().UnixMilli()); e != nil {
			return e
		}
		_, e = p.ws.Exec(`DELETE FROM meme_media_references WHERE scene_id=? AND rowid IN (SELECT rowid FROM meme_media_references WHERE scene_id=? ORDER BY updated_at DESC,rowid DESC LIMIT -1 OFFSET 2048)`, c.SceneID, c.SceneID)
		return e
	})
}
func (s *Service) LookupMediaReference(ctx context.Context, c Context, ref, message string) (out *Object, e error) {
	e = s.transaction(ctx, func(p *scope) error {
		row, denied, e := p.reference(c, ref, message)
		if e != nil || denied || row == nil {
			return e
		}
		a, e := p.asset(str(get(row, "id")))
		if e != nil {
			return e
		}
		ok, e := p.visible(c, a, false)
		if e != nil || !ok {
			return e
		}
		if content(a) != get(row, "contentHash") {
			return nil
		}
		for _, v := range arr(get(a, "sources")) {
			src := obj(v)
			if str(get(src, "sceneId")) != c.SceneID || get(src, "messageId") != get(row, "messageId") || get(src, "senderId") != get(row, "senderId") {
				continue
			}
			ok, e := p.proof.DomainSourceCurrent(src)
			if e != nil {
				return e
			}
			if ok {
				out = sanitize(a)
				return nil
			}
		}
		return nil
	})
	return
}
func (s *Service) ReadReference(ctx context.Context, c Context, ref, message string) ([]byte, error) {
	a, e := s.LookupMediaReference(ctx, c, ref, message)
	if e != nil {
		return nil, e
	}
	if a == nil {
		return nil, errors.New("image_source_unavailable")
	}
	path, e := s.file(str(get(a, "id")), str(get(a, "format")))
	if e != nil {
		return nil, e
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	current, e := s.LookupMediaReference(ctx, c, ref, message)
	if e != nil {
		return nil, e
	}
	if current == nil || get(current, "id") != get(a, "id") {
		return nil, errors.New("image_source_unavailable")
	}
	size, _ := get(current, "bytes").(float64)
	if float64(len(b)) != size || digest(b) != content(current) {
		return nil, errors.New("media_content_hash_mismatch")
	}
	return b, nil
}
