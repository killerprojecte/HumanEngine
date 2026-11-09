package snowlumaruntime

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"sync"
	"time"
)

type profileJob struct {
	value *jsjson.Object
	done  func()
}
type Profiles struct {
	mu      sync.Mutex
	sender  *Sender
	state   *jsjson.Object
	queued  []profileJob
	update  func(context.Context, *jsjson.Object) error
	failure func()
	now     func() time.Time
}

func NewProfiles(sender *Sender, update func(context.Context, *jsjson.Object) error, failure func(), now func() time.Time) (*Profiles, error) {
	if sender == nil || update == nil || failure == nil || now == nil {
		return nil, errors.New("snowluma_profile_ports_incomplete")
	}
	return &Profiles{sender: sender, update: update, failure: failure, now: now}, nil
}
func replaceObject(dst, src *jsjson.Object) {
	for _, key := range dst.Keys() {
		dst.Delete(key)
	}
	for _, key := range src.Keys() {
		v, _ := src.Get(key)
		_ = dst.Set(key, v)
	}
}
func (p *Profiles) Observe(ctx context.Context, message, scene *jsjson.Object) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	result, err := p.sender.call(obj("operation", "profiles_observe", "state", optionalState(p.state), "message", message, "scene", scene, "now", float64(p.now().UnixMilli())))
	if err != nil {
		return err
	}
	state, ok := get(result, "state").(*jsjson.Object)
	if !ok {
		return errors.New("invalid_guest_profile_state")
	}
	p.state = state
	updatedMessage, mok := get(result, "message").(*jsjson.Object)
	updatedScene, sok := get(result, "scene").(*jsjson.Object)
	if !mok || !sok {
		return errors.New("invalid_guest_profile_observation")
	}
	replaceObject(message, updatedMessage)
	replaceObject(scene, updatedScene)
	if jobs, ok := get(result, "jobs").([]any); ok {
		for _, v := range jobs {
			job, ok := v.(*jsjson.Object)
			if !ok {
				return errors.New("invalid_guest_profile_job")
			}
			done, accepted := p.sender.work.Start()
			if accepted {
				p.queued = append(p.queued, profileJob{job, done})
			}
		}
	}
	return nil
}

// Flush runs after the synchronous receive callback, matching source microtask
// scheduling. Reservations already belong to Sender's physical work group.
func (p *Profiles) Flush() {
	p.mu.Lock()
	jobs := p.queued
	p.queued = nil
	p.mu.Unlock()
	for _, job := range jobs {
		go p.run(job)
	}
}
func (p *Profiles) run(job profileJob) {
	defer job.done()
	if p.sender.current() != nil {
		return
	}
	params, ok := get(job.value, "params").(*jsjson.Object)
	if !ok {
		return
	}
	var value any
	var err error
	if p.sender.http != nil {
		value, err = p.sender.http.Action(p.sender.life, str(job.value, "action"), params)
	} else {
		value, err = p.sender.ws.Action(p.sender.life, str(job.value, "action"), params)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sender.current() != nil {
		return
	}
	input := obj("operation", "profiles_complete", "state", optionalState(p.state), "job", job.value, "now", float64(p.now().UnixMilli()), "failed", err != nil)
	if err == nil {
		_ = input.Set("value", value)
	}
	result, err := p.sender.call(input)
	if err != nil {
		return
	}
	if state, ok := get(result, "state").(*jsjson.Object); ok {
		p.state = state
	}
	if p.sender.current() != nil {
		return
	}
	if get(result, "failure") == true {
		p.failure()
	}
	if profile, ok := get(result, "profile").(*jsjson.Object); ok {
		// Source catches callback errors after replacing the cache entry. Its old
		// entry guard then suppresses the failure callback; do not invent a retry.
		_ = p.update(p.sender.life, profile)
	}
}
