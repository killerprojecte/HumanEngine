package memeservice

import (
	"bytes"
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/imagecodec"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

var assetID = regexp.MustCompile(`^[a-f0-9]{64}$`)

func sniff(b []byte) (string, error) {
	if bytes.HasPrefix(b, []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		return "png", nil
	}
	if bytes.HasPrefix(b, []byte{255, 216, 255}) {
		return "jpeg", nil
	}
	if bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a")) {
		return "gif", nil
	}
	if len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP" {
		return "webp", nil
	}
	return "", errors.New("unsupported_image_format")
}
func (s *Service) file(id, format string) (string, error) {
	if !assetID.MatchString(id) || (format != "png" && format != "jpeg" && format != "gif" && format != "webp") {
		return "", errors.New("invalid_asset_id")
	}
	return filepath.Join(s.root, id+"."+format), nil
}

type assetGate struct {
	mu    sync.Mutex
	users int
}

// Register both current holder and queued writers before waiting. Removal and
// reference accounting share the registry lock, so two mutexes cannot coexist.
func (s *Service) assetLock(id string) func() {
	s.mu.Lock()
	g := s.writes[id]
	if g == nil {
		g = &assetGate{}
		s.writes[id] = g
	}
	g.users++
	s.mu.Unlock()
	g.mu.Lock()
	return func() {
		s.mu.Lock()
		g.mu.Unlock()
		g.users--
		if g.users == 0 {
			delete(s.writes, id)
		}
		s.mu.Unlock()
	}
}
func (s *Service) epoch(id string) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.epochs[id], s.deleting[id]
}

// Intake preserves the source's intentional file-before-SQL partial outcome:
// cancellation, withdrawal, or SQL failure after write leaves bytes on disk.
func (s *Service) Intake(ctx context.Context, c Context, b []byte, source *Object, knownOnly bool, codec *imagecodec.Compressor) (out *Object, e error) {
	// Own immutable snapshots across asynchronous work; caller mutation cannot
	// turn a validated old byte buffer into a different event or file.
	b = append([]byte(nil), b...)
	source = copyObject(source)
	evidence := make([]*Object, len(c.SourceEvidence))
	for i, v := range c.SourceEvidence {
		evidence[i] = copyObject(v)
	}
	c.SourceEvidence = evidence
	var id string
	e = s.transaction(context.WithoutCancel(ctx), func(p *scope) error {
		if !knownOnly {
			if e := p.allow(c); e != nil {
				return e
			}
		}
		return p.observed(c, source)
	})
	if e != nil {
		return nil, e
	}
	if ctx.Err() != nil {
		return nil, errors.New("analysis_cancelled")
	}
	if len(b) == 0 || len(b) > 64*1024*1024 {
		return nil, errors.New("image_size_limit")
	}
	format, e := sniff(b)
	if e != nil {
		return nil, e
	}
	hash := digest(b)
	found, e := s.RecognizeBytes(context.WithoutCancel(ctx), c, b)
	if e != nil {
		return nil, e
	}
	if found != nil {
		id = str(get(found, "id"))
	} else {
		id = identity(c.SceneID, hash)
	}
	s.mu.Lock()
	for deletingID, barrier := range s.deletingContent {
		if barrier.hash == hash && (barrier.shared || barrier.scenes[c.SceneID]) && (found == nil || str(get(found, "id")) == deletingID) {
			s.mu.Unlock()
			return nil, errors.New("media_deleted")
		}
	}
	s.mu.Unlock()
	epoch, deleting := s.epoch(id)
	if deleting {
		return nil, errors.New("media_deleted")
	}
	metadata, e := codec.MemeMetadata(context.WithoutCancel(ctx), b)
	if e != nil {
		return nil, e
	}
	if metadata.Width == 0 || metadata.Height == 0 || metadata.Frames > 400 || int64(metadata.Width)*int64(metadata.Height)*int64(metadata.Frames) > 80000000 {
		return nil, errors.New("image_pixel_limit")
	}
	release := s.assetLock(id)
	defer release()
	current, deleting := s.epoch(id)
	if deleting || current != epoch {
		return nil, errors.New("media_deleted")
	}
	if e = os.MkdirAll(s.root, 0700); e != nil {
		return nil, e
	}
	path, e := s.file(id, format)
	if e != nil {
		return nil, e
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e == nil {
		_, e = f.Write(b)
		closeErr := f.Close()
		if e == nil {
			e = closeErr
		}
	} else if errors.Is(e, os.ErrExist) {
		e = nil
	}
	if e != nil {
		return nil, e
	}
	stored, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	if digest(stored) != hash {
		return nil, errors.New("media_hash_mismatch")
	}
	e = s.transaction(context.WithoutCancel(ctx), func(p *scope) error {
		current, deleting := s.epoch(id)
		if deleting || current != epoch {
			return errors.New("media_deleted")
		}
		if ctx.Err() != nil {
			return errors.New("analysis_cancelled")
		}
		if !knownOnly {
			if e := p.allow(c); e != nil {
				return e
			}
		}
		if e := p.observed(c, source); e != nil {
			return e
		}
		a, e := p.asset(id)
		if e != nil {
			return e
		}
		if a != nil {
			ok, e := p.visible(c, a, false)
			if e != nil {
				return e
			}
			if !ok {
				return errors.New("media_scope_changed")
			}
		} else {
			a = object()
			fields := map[string]any{"id": id, "contentHash": hash, "knownOnly": knownOnly, "format": format, "bytes": float64(len(b)), "width": float64(metadata.Width), "height": float64(metadata.Height), "frames": float64(metadata.Frames), "sources": []any{}, "tags": emptyTags(), "analyzed": false, "available": false, "shared": false, "privacyReviewed": false, "favorite": false, "disabled": false, "preference": float64(0), "usageCount": float64(0), "createdAt": time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}
			for _, k := range []string{"id", "contentHash", "knownOnly", "format", "bytes", "width", "height", "frames", "sources", "tags", "analyzed", "available", "shared", "privacyReviewed", "favorite", "disabled", "preference", "usageCount", "createdAt"} {
				set(a, k, fields[k])
			}
		}
		sources := arr(get(a, "sources"))
		present := false
		for _, v := range sources {
			old := obj(v)
			if get(old, "sceneId") == get(source, "sceneId") && get(old, "messageId") == get(source, "messageId") {
				present = true
			}
		}
		if !present {
			set(a, "sources", append(sources, source))
		}
		if e = p.write(a); e != nil {
			return e
		}
		out = sanitize(a)
		return nil
	})
	return
}
