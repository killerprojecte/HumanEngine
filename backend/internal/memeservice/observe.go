package memeservice

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/imagecodec"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"time"
)

type ObserveOptions struct {
	Selection *SelectionContext
	Admit     *bool
}

func (s *Service) ObserveBytes(ctx context.Context, c Context, b []byte, source *Object, models *providertransport.NativeModels, codec *imagecodec.Compressor, o ObserveOptions) (*Object, error) {
	if ctx.Err() != nil {
		return nil, errors.New("analysis_cancelled")
	}
	if e := s.transaction(ctx, func(p *scope) error { return p.observed(c, source) }); e != nil {
		return nil, e
	}
	known, e := s.RecognizeBytes(ctx, c, b)
	if e != nil {
		return nil, e
	}
	if known == nil && o.Admit != nil && !*o.Admit {
		return nil, nil
	}
	asset, e := s.Intake(ctx, c, b, source, true, codec)
	if e != nil {
		return nil, e
	}
	if models == nil || yes(get(asset, "analyzed")) {
		return asset, nil
	}
	key := c.SceneID + ":" + content(asset)
	failureKey := key + ":" + str(get(source, "messageId"))
	s.mu.Lock()
	if s.observationFailures[failureKey] > time.Now().UnixMilli() {
		s.mu.Unlock()
		return nil, errors.New("analysis_failed_recently")
	}
	task := s.observations[key]
	if task == nil {
		taskRoot, releaseTask, err := s.authority.ForkTaskOperation(ctx, s.main)
		if err != nil {
			s.mu.Unlock()
			return nil, err
		}
		signal, cancel := context.WithCancel(taskRoot)
		task = &observationTask{root: taskRoot, done: make(chan struct{}), cancel: cancel}
		for _, v := range c.SourceEvidence {
			task.evidence = append(task.evidence, copyObject(v))
		}
		s.observations[key] = task
		go func() {
			task.asset, task.err = s.Analyze(signal, c, str(get(asset, "id")), models, codec, AnalyzeOptions{Observation: true, Selection: o.Selection, observation: task})
			releaseTask() // Analyze and its cleanup writes have completed before publishing done.
			s.mu.Lock()
			if s.observations[key] == task {
				delete(s.observations, key)
			}
			close(task.done)
			s.mu.Unlock()
			cancel()
		}()
	} else {
		for _, v := range c.SourceEvidence {
			found := false
			for _, old := range task.evidence {
				if get(old, "sceneId") == get(v, "sceneId") && get(old, "messageId") == get(v, "messageId") {
					found = true
					break
				}
			}
			if !found {
				task.evidence = append(task.evidence, copyObject(v))
			}
		}
	}
	task.consumers++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		task.consumers--
		if task.consumers == 0 {
			task.cancel()
		}
		s.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return nil, errors.New("analysis_cancelled")
	case <-task.done:
		if task.err != nil {
			if ctx.Err() == nil {
				s.mu.Lock()
				if _, present := s.observationFailures[failureKey]; !present {
					s.failureOrder = append(s.failureOrder, failureKey)
				}
				s.observationFailures[failureKey] = time.Now().UnixMilli() + 15000
				for len(s.failureOrder) > 128 {
					delete(s.observationFailures, s.failureOrder[0])
					s.failureOrder = s.failureOrder[1:]
				}
				s.mu.Unlock()
			}
			return nil, task.err
		}
		return copyObject(task.asset), nil
	}
}
