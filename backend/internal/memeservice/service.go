package memeservice

import (
	"container/list"
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertionauthority"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"path/filepath"
	"sync"
	"time"
)

type Context struct {
	SceneID        string
	Simulation     bool
	SourceEvidence []*Object
}
type Service struct {
	main                *sql.DB
	authority           *recovery.Authority
	assertions          *assertions.Store
	policy              assertionauthority.Options
	root                string
	mu                  sync.Mutex
	writes              map[string]*assetGate
	epochs              map[string]uint64
	deleting            map[string]bool
	thumbnailTasks      map[string]*thumbnailTask
	thumbnails          map[string]*list.Element
	thumbnailLRU        *list.List
	thumbnailActive     int
	thumbnailWaiters    []chan struct{}
	analyses            map[string]*analysisTask
	observations        map[string]*observationTask
	observationFailures map[string]int64
	failureOrder        []string
	deletingContent     map[string]deleteBarrier
}
type scope struct {
	ws     *recovery.WriteSession
	reader *assertions.Reader
	proof  *assertionauthority.Authority
	owner  *Service
}

func New(ctx context.Context, main *sql.DB, authority *recovery.Authority, store *assertions.Store, policy assertionauthority.Options, root string) (*Service, error) {
	if main == nil || authority == nil || store == nil {
		return nil, errors.New("meme_authority_required")
	}
	if e := store.CheckReadBinding(authority, main, nil); e != nil {
		return nil, e
	}
	path, e := filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	s := &Service{main: main, authority: authority, assertions: store, policy: policy, root: path, writes: map[string]*assetGate{}, epochs: map[string]uint64{}, deleting: map[string]bool{}}
	e = authority.WithWriteSession(ctx, main, func(ws *recovery.WriteSession) error { return ws.EnsureMemeStorageSchema() })
	if e != nil {
		return nil, e
	}
	s.initializeAsync()
	e = s.transaction(ctx, func(p *scope) error {
		all, e := p.assets()
		if e != nil {
			return e
		}
		for _, a := range all {
			state := str(get(a, "analysisState"))
			if state == "running" || state == "retrying" {
				set(a, "analysisState", "interrupted")
				a.Delete("analysisNextRetryAt")
			}
			if e = p.write(a); e != nil {
				return e
			}
		}
		return nil
	})
	return s, e
}

// This scope never encloses file IO, image decoding, model calls, or waits.
// Source eligibility and each dependent mutation use this SAME child session.
func (s *Service) transaction(ctx context.Context, fn func(*scope) error) error {
	return s.authority.WithWriteSession(ctx, s.main, func(ws *recovery.WriteSession) error {
		if e := ws.AssertReady(); e != nil {
			return e
		}
		run := func() error {
			return s.assertions.WithSession(ws).Read(ctx, func(r *assertions.Reader) error {
				a, e := assertionauthority.New(r, s.policy)
				if e != nil {
					return e
				}
				return fn(&scope{ws, r, a, s})
			})
		}
		if capture, _ := ctx.Value(pageCaptureKey{}).(*pageCapture); capture != nil {
			var e error
			capture.witness, e = ws.CaptureReadWitness(run)
			return e
		}
		return run()
	})
}
func (p *scope) assets() ([]*Object, error) {
	return p.queryAssets("SELECT value FROM meme_assets ORDER BY rowid DESC")
}
func (p *scope) queryAssets(query string, args ...any) ([]*Object, error) {
	rows, e := p.ws.Query(query, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []*Object{}
	for rows.Next() {
		var raw string
		if e = rows.Scan(&raw); e != nil {
			return nil, e
		}
		v, e := jsjson.Parse(raw)
		if e != nil {
			return nil, e
		}
		if obj(v) == nil {
			return nil, errors.New("invalid_meme_asset")
		}
		out = append(out, obj(v))
	}
	return out, rows.Err()
}

// Match narrowing occurs in SQL before JSON decoding and source qualification.
func (p *scope) candidates(c Context, key, value string) ([]*Object, error) {
	where := "(json_extract(value,'$.shared')=1 AND json_extract(value,'$.privacyReviewed')=1 AND COALESCE(json_extract(value,'$.knownOnly'),0)=0)"
	args := []any{}
	if !c.Simulation {
		where = "(" + where + " OR id IN (SELECT asset_id FROM meme_sources WHERE scene_id=?))"
		args = append(args, c.SceneID)
	}
	if key == "id" {
		where = "id=? AND " + where
		args = append([]any{value}, args...)
	} else if key == "contentHash" {
		where = "COALESCE(json_extract(value,'$.contentHash'),id)=? AND " + where
		args = append([]any{value}, args...)
	}
	return p.queryAssets("SELECT value FROM meme_assets WHERE "+where, args...)
}
func (p *scope) asset(id string) (*Object, error) {
	rows, e := p.ws.Query("SELECT value FROM meme_assets WHERE id=?", id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var raw string
	if e = rows.Scan(&raw); e != nil {
		return nil, e
	}
	v, e := jsjson.Parse(raw)
	return obj(v), e
}
func (p *scope) write(a *Object) error {
	raw, e := jsjson.Stringify(a)
	if e != nil {
		return e
	}
	id := str(get(a, "id"))
	if _, e = p.ws.Exec("INSERT INTO meme_assets VALUES(?,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value", id, raw); e != nil {
		return e
	}
	if _, e = p.ws.Exec("DELETE FROM meme_sources WHERE asset_id=?", id); e != nil {
		return e
	}
	for _, v := range arr(get(a, "sources")) {
		o := obj(v)
		if _, e = p.ws.Exec("INSERT INTO meme_sources VALUES(?,?,?,?)", id, get(o, "sceneId"), get(o, "messageId"), get(o, "senderId")); e != nil {
			return e
		}
	}
	if _, e = p.ws.Exec("DELETE FROM meme_search_concepts WHERE asset_id=?", id); e != nil {
		return e
	}
	tags := obj(get(a, "tags"))
	texts := []string{str(get(tags, "ocr"))}
	for _, k := range Categories {
		for _, v := range arr(get(tags, k)) {
			texts = append(texts, str(v))
		}
	}
	seen := map[string]bool{}
	for _, t := range texts {
		cs, e := concepts(t)
		if e != nil {
			return e
		}
		for _, c := range cs {
			if !seen[c] {
				if _, e = p.ws.Exec("INSERT INTO meme_search_concepts VALUES(?,?)", id, c); e != nil {
					return e
				}
				seen[c] = true
			}
		}
	}
	return nil
}
func (p *scope) allow(c Context) error {
	if c.Simulation {
		return errors.New("simulation_does_not_learn")
	}
	scene, e := p.reader.Scene(c.SceneID)
	if e != nil {
		return e
	}
	if !yes(get(scene, "learningEnabled")) {
		return errors.New("learning_disabled")
	}
	return nil
}
func (p *scope) observed(c Context, source *Object) error {
	if c.Simulation {
		return errors.New("simulation_does_not_learn")
	}
	scene, e := p.reader.Scene(c.SceneID)
	if e != nil {
		return e
	}
	if scene == nil || str(get(source, "sceneId")) != c.SceneID {
		return errors.New("invalid_source_evidence")
	}
	valid, e := p.proof.DomainSourceCurrent(source)
	if e != nil {
		return e
	}
	if !valid {
		return errors.New("invalid_source_evidence")
	}
	for _, v := range c.SourceEvidence {
		if same(v, source) && yes(get(v, "available")) && !yes(get(v, "isBot")) {
			return nil
		}
	}
	return errors.New("invalid_source_evidence")
}
func (p *scope) visible(c Context, a *Object, required bool) (bool, error) {
	if a != nil && p.owner != nil {
		_, deleting := p.owner.epoch(str(get(a, "id")))
		if deleting {
			return false, nil
		}
	}
	if a == nil || yes(get(a, "disabled")) || required && (!yes(get(a, "available")) || yes(get(a, "knownOnly"))) {
		return false, nil
	}
	valid, local := false, false
	for _, v := range arr(get(a, "sources")) {
		src := obj(v)
		ok, e := p.proof.DomainSourceCurrent(src)
		if e != nil {
			return false, e
		}
		if ok {
			valid = true
			if str(get(src, "sceneId")) == c.SceneID {
				local = true
			}
		}
	}
	return valid && ((yes(get(a, "shared")) && yes(get(a, "privacyReviewed")) && !yes(get(a, "knownOnly"))) || !c.Simulation && local), nil
}
func (s *Service) AdminList(ctx context.Context) (out []*Object, e error) {
	e = s.transaction(ctx, func(p *scope) error { var e error; out, e = p.assets(); return e })
	return
}
func (s *Service) RecognizeBytes(ctx context.Context, c Context, b []byte) (out *Object, e error) {
	if len(b) == 0 || len(b) > 64*1024*1024 {
		return nil, errors.New("image_size_limit")
	}
	hash := digest(b)
	e = s.transaction(ctx, func(p *scope) error {
		all, e := p.candidates(c, "contentHash", hash)
		if e != nil {
			return e
		}
		for _, a := range all {
			if content(a) != hash {
				continue
			}
			ok, e := p.visible(c, a, false)
			if e != nil {
				return e
			}
			if ok {
				if out == nil {
					out = sanitize(a)
				}
				for _, v := range arr(get(a, "sources")) {
					if str(get(obj(v), "sceneId")) == c.SceneID {
						out = sanitize(a)
						return nil
					}
				}
			}
		}
		return nil
	})
	return
}
func (s *Service) RecordUsage(ctx context.Context, c Context, id string) error {
	return s.transaction(ctx, func(p *scope) error {
		if e := p.allow(c); e != nil {
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
		n, _ := get(a, "usageCount").(float64)
		set(a, "usageCount", n+1)
		set(a, "lastUsedAt", time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
		return p.write(a)
	})
}
