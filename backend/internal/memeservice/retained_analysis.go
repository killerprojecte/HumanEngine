package memeservice

import (
	"context"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/imagecodec"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

type retainedAnalysisMember struct {
	ctx     context.Context
	source  *Object
	current func(context.Context, *recovery.WriteSession) (bool, error)
}

type retainedAnalysis struct {
	contextual bool
	cancel     context.CancelFunc
	done       chan struct{}
	members    map[*retainedAnalysisMember]struct{}
	err        error
}

// AnalyzeRetainedSticker labels admitted, exact retained pixels without
// personal prompt material or selection/sharing authority. Each caller supplies
// the qualification for its own original occurrence; that capability cannot
// be borrowed by a different scene or by a contextual/admin analysis.
func (s *Service) AnalyzeRetainedSticker(ctx context.Context, c Context, asset, source *Object, models *providertransport.NativeModels, codec *imagecodec.Compressor, current func(context.Context, *recovery.WriteSession) (bool, error)) error {
	return s.analyzeRetainedSticker(ctx, c, asset, source, models, codec, current, nil)
}

// LearnRetainedSticker uses the same retained-pixel owner as the tag-only lane.
// It anchors the prompt to its exact donor rather than accepting replacement
// evidence from a different occurrence of the same sticker.
func (s *Service) LearnRetainedSticker(ctx context.Context, c Context, asset, source *Object, models *providertransport.NativeModels, codec *imagecodec.Compressor, current func(context.Context, *recovery.WriteSession) (bool, error), selection func(context.Context) (*SelectionContext, error)) error {
	return s.analyzeRetainedSticker(ctx, c, asset, source, models, codec, current, selection)
}

func (s *Service) analyzeRetainedSticker(ctx context.Context, c Context, asset, source *Object, models *providertransport.NativeModels, codec *imagecodec.Compressor, current func(context.Context, *recovery.WriteSession) (bool, error), selection func(context.Context) (*SelectionContext, error)) error {
	contextual := selection != nil
	id := str(get(asset, "id"))
	hash := content(asset)
	if id != identity(c.SceneID, hash) {
		return nil
	}
	key := c.SceneID + ":" + hash
	epoch, deleting := s.epoch(id)
	if deleting {
		return errors.New("meme_not_available")
	}
	member := &retainedAnalysisMember{ctx, copyObject(source), current}
	s.mu.Lock()
	task := s.retainedAnalyses[key]
	for task != nil && (len(task.members) == 0 || task.contextual || contextual) {
		// The last departure irreversibly canceled this task. Do not attach
		// new evidence or overlap a replacement with its physical drain.
		// Contextual tasks never substitute a second donor or inherit an off-lane result.
		s.mu.Unlock()
		select {
		case <-task.done:
			if ctx.Err() != nil {
				return errors.New("analysis_cancelled")
			}
		case <-ctx.Done():
			return errors.New("analysis_cancelled")
		}
		s.mu.Lock()
		task = s.retainedAnalyses[key]
	}
	if task == nil {
		root, release, err := s.authority.ForkTaskOperation(ctx, s.main)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		signal, cancel := context.WithCancel(root)
		task = &retainedAnalysis{contextual: contextual, cancel: cancel, done: make(chan struct{}), members: map[*retainedAnalysisMember]struct{}{member: {}}}
		if s.retainedAnalyses == nil {
			s.retainedAnalyses = make(map[string]*retainedAnalysis)
		}
		s.retainedAnalyses[key] = task
		go func() {
			defer func() {
				cancel()
				release()
				s.mu.Lock()
				delete(s.retainedAnalyses, key)
				close(task.done)
				s.mu.Unlock()
			}()
			eligible := func(p *scope, current *Object) error {
				return s.qualifyRetainedAnalysis(signal, p, c.SceneID, task, asset, current, epoch)
			}
			need := false
			task.err = s.transaction(signal, func(p *scope) error {
				current, err := p.asset(id)
				if err != nil {
					return err
				}
				if yes(get(current, "analyzed")) {
					return nil
				}
				if err = eligible(p, current); err != nil {
					return err
				}
				need = true
				return nil
			})
			if task.err != nil || !need {
				return
			}
			router, releaseRouter, err := models.AcquireRouter(signal)
			if err != nil {
				task.err = err
				return
			}
			defer releaseRouter()
			if !router.Policy.HasConfiguredRoute("vision") {
				return
			}
			options := AnalyzeOptions{retained: eligible}
			if contextual {
				options.Selection, task.err = selection(signal)
				if task.err != nil {
					return
				}
			}
			signal = providertransport.WithFinalModelReadGuard(signal, func(call context.Context) error {
				return s.transaction(call, func(p *scope) error {
					current, err := p.asset(id)
					if err != nil {
						return err
					}
					return eligible(p, current)
				})
			})
			_, task.err = s.analyzeOnce(signal, c, asset, router, codec, options)
		}()
	} else {
		task.members[member] = struct{}{}
	}
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		s.mu.Lock()
		delete(task.members, member)
		last := len(task.members) == 0
		if last {
			task.cancel()
		}
		s.mu.Unlock()
		if last {
			<-task.done
		}
		return errors.New("analysis_cancelled")
	case <-task.done:
		if ctx.Err() != nil {
			return errors.New("analysis_cancelled")
		}
		return task.err
	}
}

func (s *Service) qualifyRetainedAnalysis(ctx context.Context, p *scope, sceneID string, task *retainedAnalysis, original, asset *Object, epoch uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if asset == nil || yes(get(asset, "disabled")) || yes(get(asset, "analyzed")) ||
		!yes(get(asset, "knownOnly")) || yes(get(asset, "shared")) ||
		content(asset) != content(original) || get(asset, "tagRevision") != get(original, "tagRevision") {
		return errors.New("meme_not_available")
	}
	currentEpoch, deleting := s.epoch(str(get(asset, "id")))
	if deleting || currentEpoch != epoch {
		return errors.New("meme_not_available")
	}
	scene, err := p.reader.Scene(sceneID)
	if err != nil {
		return err
	}
	if get(scene, "learningEnabled") != task.contextual {
		return errors.New("model_read_revoked")
	}
	s.mu.Lock()
	members := make([]*retainedAnalysisMember, 0, len(task.members))
	for m := range task.members {
		members = append(members, m)
	}
	s.mu.Unlock()
	for _, m := range members {
		if m.ctx.Err() != nil {
			continue
		}
		for _, raw := range arr(get(asset, "sources")) {
			if !same(obj(raw), m.source) {
				continue
			}
			valid, err := p.proof.DomainSourceCurrent(m.source)
			if err != nil {
				return err
			}
			if !valid {
				break
			}
			valid, err = m.current(ctx, p.ws)
			if err != nil {
				return err
			}
			if valid && m.ctx.Err() == nil {
				return nil
			}
			break
		}
	}
	return errors.New("model_read_revoked")
}
