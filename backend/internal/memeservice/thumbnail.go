package memeservice

import (
	"container/list"
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/imagecodec"
	"os"
)

type thumbnailTask struct {
	done  chan struct{}
	bytes []byte
	err   error
}
type thumbnailEntry struct {
	id    string
	bytes []byte
}

func (s *Service) initializeAsync() {
	s.thumbnailTasks = map[string]*thumbnailTask{}
	s.thumbnails = map[string]*list.Element{}
	s.thumbnailLRU = list.New()
	s.analyses = map[string]*analysisTask{}
	s.observations = map[string]*observationTask{}
	s.observationFailures = map[string]int64{}
	s.deletingContent = map[string]deleteBarrier{}
}
func (s *Service) thumbnailSlot() func() {
	s.mu.Lock()
	var wait chan struct{}
	if s.thumbnailActive >= 3 {
		wait = make(chan struct{})
		s.thumbnailWaiters = append(s.thumbnailWaiters, wait)
	} else {
		s.thumbnailActive++
	}
	s.mu.Unlock()
	if wait != nil {
		<-wait
	}
	return func() {
		s.mu.Lock()
		if len(s.thumbnailWaiters) > 0 {
			next := s.thumbnailWaiters[0]
			s.thumbnailWaiters = s.thumbnailWaiters[1:]
			close(next)
		} else {
			s.thumbnailActive--
		}
		s.mu.Unlock()
	}
}
func (s *Service) evictThumbnailLocked(id string) {
	if e := s.thumbnails[id]; e != nil {
		s.thumbnailLRU.Remove(e)
		delete(s.thumbnails, id)
	}
}
func (s *Service) Thumbnail(ctx context.Context, id string, codec *imagecodec.Compressor) ([]byte, error) {
	var a *Object
	if e := s.transaction(ctx, func(p *scope) error { var e error; a, e = p.asset(id); return e }); e != nil {
		return nil, e
	}
	s.mu.Lock()
	if a == nil || s.deleting[id] {
		s.mu.Unlock()
		return nil, errors.New("meme_not_found")
	}
	epoch := s.epochs[id]
	if cached := s.thumbnails[id]; cached != nil {
		s.thumbnailLRU.MoveToBack(cached)
		b := append([]byte(nil), cached.Value.(thumbnailEntry).bytes...)
		s.mu.Unlock()
		return b, nil
	}
	task := s.thumbnailTasks[id]
	if task == nil {
		task = &thumbnailTask{done: make(chan struct{})}
		s.thumbnailTasks[id] = task
		go s.makeThumbnail(context.WithoutCancel(ctx), a, epoch, codec, task)
	}
	s.mu.Unlock()
	<-task.done
	if task.err != nil {
		return nil, task.err
	}
	if e := s.transaction(ctx, func(p *scope) error { return p.ws.AssertReady() }); e != nil {
		return nil, e
	}
	return append([]byte(nil), task.bytes...), nil
}
func (s *Service) makeThumbnail(ctx context.Context, a *Object, epoch uint64, codec *imagecodec.Compressor, task *thumbnailTask) {
	id := str(get(a, "id"))
	release := s.thumbnailSlot()
	defer func() {
		release()
		s.mu.Lock()
		if s.thumbnailTasks[id] == task {
			delete(s.thumbnailTasks, id)
		}
		close(task.done)
		s.mu.Unlock()
	}()
	current, deleting := s.epoch(id)
	if deleting || current != epoch {
		task.err = errors.New("meme_not_found")
		return
	}
	path, e := s.file(id, str(get(a, "format")))
	if e != nil {
		task.err = e
		return
	}
	b, e := os.ReadFile(path)
	if e == nil {
		b, e = codec.MemeThumbnailWebP(ctx, b)
	}
	if e != nil {
		task.err = e
		return
	}
	e = s.transaction(ctx, func(p *scope) error {
		current, e := p.asset(id)
		if e != nil {
			return e
		}
		now, _ := s.epoch(id)
		if current == nil || now != epoch {
			return errors.New("meme_not_found")
		}
		return nil
	})
	if e != nil {
		task.err = e
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.epochs[id] != epoch {
		task.err = errors.New("meme_not_found")
		return
	}
	s.evictThumbnailLocked(id)
	s.thumbnails[id] = s.thumbnailLRU.PushBack(thumbnailEntry{id, b})
	for s.thumbnailLRU.Len() > 128 {
		front := s.thumbnailLRU.Front()
		delete(s.thumbnails, front.Value.(thumbnailEntry).id)
		s.thumbnailLRU.Remove(front)
	}
	task.bytes = b
}
