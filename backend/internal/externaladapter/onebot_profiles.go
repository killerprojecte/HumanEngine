package externaladapter

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
)

// A generation owns its pending actions and existing OneBot profile cooldowns.
// No waiting lookup holds ingress, a database reservation, or a guest call open.
type profileLookups struct {
	mu      sync.Mutex
	next    map[string]time.Time
	pending map[string]struct{}
	flights map[string]*profileFlight
}

func (g *profileLookups) admit(id string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, active := g.pending[id]; active || len(g.pending) == 4 || g.next[id].After(now) {
		return false
	}
	if g.next == nil {
		g.next = make(map[string]time.Time)
		g.pending = make(map[string]struct{})
	} else if len(g.next) == 500 {
		var first string
		for key, until := range g.next {
			if _, active := g.pending[key]; active {
				continue
			}
			if first == "" || until.Before(g.next[first]) {
				first = key
			}
		}
		delete(g.next, first)
	}
	g.next[id] = now.Add(5 * time.Minute)
	g.pending[id] = struct{}{}
	return true
}

func (g *profileLookups) finish(id string, found bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.pending, id)
	if found {
		g.next[id] = time.Now().Add(30 * time.Minute)
	}
}

type profileFlight struct {
	done chan struct{}
	name string
	err  error
}

func (g *profileLookups) resolve(ctx context.Context, key string, fetch func(context.Context) (string, error)) (string, error) {
	g.mu.Lock()
	if active := g.flights[key]; active != nil {
		g.mu.Unlock()
		select {
		case <-active.done:
			return active.name, active.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if g.flights == nil {
		g.flights = make(map[string]*profileFlight)
	}
	flight := &profileFlight{done: make(chan struct{})}
	g.flights[key] = flight
	g.mu.Unlock()
	flight.name, flight.err = fetch(ctx)
	g.mu.Lock()
	delete(g.flights, key)
	close(flight.done)
	g.mu.Unlock()
	return flight.name, flight.err
}

type profileAction struct{ action, idField, operation, inputField string }

var groupProfile = profileAction{"get_group_info", "group_id", "adapterGroupName", "group"}
var userProfile = profileAction{"get_stranger_info", "user_id", "adapterPrivateName", "peer"}

func (s *httpSession) queueProfiles(p *oneBotHTTP, scene dispatch.SceneRef, ev event) {
	if ev.SceneDisplayName == nil {
		var command profileAction
		switch {
		case ev.Peer.Kind == "group" && p.groupNames:
			command = groupProfile
		case ev.Peer.Kind == "private" && p.privateNames:
			command = userProfile
		}
		if command.action != "" {
			s.lookupName(scene.ID, scene.ID, scene.PlatformSceneID, command, func(ctx context.Context, lookup nameLookup) (bool, error) {
				return s.source.sceneName(ctx, scene, lookup)
			})
		}
	}
	if p.senderNames && ev.SenderDisplayName == nil {
		sender := "bot"
		if ev.Sender != s.profile.botID {
			sender = s.profile.sender(ev.Sender)
		}
		s.lookupName(scene.ID+":"+sender, scene.ID, ev.Sender, userProfile, func(ctx context.Context, lookup nameLookup) (bool, error) {
			return s.source.senderName(ctx, scene, sender, ev.Sender, lookup)
		})
	}
}

type nameLookup = func(context.Context) (string, error)

func (s *httpSession) lookupName(key, scene, platform string, command profileAction, apply func(context.Context, nameLookup) (bool, error)) {
	if !s.profiles.admit(key, time.Now()) {
		return
	}
	done, ok := s.work.Start()
	if !ok {
		s.profiles.finish(key, false)
		return
	}
	go func() {
		defer done()
		ctx, cancel := context.WithTimeout(s.life, 20*time.Second)
		defer cancel()
		found, err := apply(ctx, func(ctx context.Context) (string, error) {
			return s.profiles.resolve(ctx, command.action+":"+scene+":"+platform, func(ctx context.Context) (string, error) {
				req, err := json.Marshal(map[string]string{command.idField: platform})
				if err != nil {
					return "", err
				}
				raw, err := s.oneBotRead(ctx, command.action, req, nil)
				if err != nil {
					return "", err
				}
				answer, err := s.invoke(ctx, command.operation, map[string]any{command.inputField: platform, "response": json.RawMessage(raw)})
				if err != nil {
					return "", err
				}
				name, err := guestObject(answer)
				if err != nil {
					return "", err
				}
				return text(name, "displayName"), nil
			})
		})
		s.profiles.finish(key, err == nil && found)
	}()
}
