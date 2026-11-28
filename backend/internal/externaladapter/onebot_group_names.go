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
type groupNameLookups struct {
	mu      sync.Mutex
	next    map[string]time.Time
	pending map[string]struct{}
}

func (g *groupNameLookups) admit(id string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.pending) == 4 || g.next[id].After(now) {
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

func (g *groupNameLookups) finish(id string, found bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.pending, id)
	if found {
		g.next[id] = time.Now().Add(30 * time.Minute)
	}
}

func (s *httpSession) lookupGroupName(scene dispatch.SceneRef) {
	if !s.groupNames.admit(scene.ID, time.Now()) {
		return
	}
	done, ok := s.work.Start()
	if !ok {
		s.groupNames.finish(scene.ID, false)
		return
	}
	go func() {
		defer done()
		ctx, cancel := context.WithTimeout(s.life, 20*time.Second)
		defer cancel()
		found, err := s.source.sceneName(ctx, scene, func(ctx context.Context) (string, error) {
			req, err := json.Marshal(map[string]string{"group_id": scene.PlatformSceneID})
			if err != nil {
				return "", err
			}
			raw, err := s.oneBotRead(ctx, "get_group_info", req, nil)
			if err != nil {
				return "", err
			}
			answer, err := s.invoke(ctx, "adapterGroupName", struct {
				Group    string          `json:"group"`
				Response json.RawMessage `json:"response"`
			}{scene.PlatformSceneID, raw})
			if err != nil {
				return "", err
			}
			name, err := guestObject(answer)
			if err != nil {
				return "", err
			}
			return text(name, "displayName"), nil
		})
		s.groupNames.finish(scene.ID, err == nil && found)
	}()
}
