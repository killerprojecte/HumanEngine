// Package habitengine composes actual Engine habit tool calls with the native
// habit store and source authority. It never delegates business logic to Node.
package habitengine

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/habitservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

type Object = jsjson.Object

// Turn is an owned caller snapshot. Nil SourceSnapshots means source runtime
// preview fallback; a nonnil empty slice means an explicitly captured empty set.
// Preview is supplied by the runtime caller, never by untrusted tool arguments.
type Turn struct {
	Scene                    botruntime.Scene
	Simulation               bool
	SourceSnapshots, Preview []*Object
}
type Service struct {
	habits    *habitservice.Service
	sources   *sourceevents.Store
	authority *sourceevents.RuntimeAuthority
	dates     *jsdate.Parser
}

func New(habits *habitservice.Service, sources *sourceevents.Store, dates *jsdate.Parser) (*Service, error) {
	if habits == nil || sources == nil || dates == nil {
		return nil, errors.New("habit_engine_dependencies_required")
	}
	a, e := sourceevents.NewRuntimeAuthority(sources)
	if e != nil {
		return nil, e
	}
	return &Service{habits, sources, a, dates}, nil
}
func object(kv ...any) *Object {
	o := new(Object)
	for i := 0; i < len(kv); i += 2 {
		_ = o.Set(kv[i].(string), kv[i+1])
	}
	return o
}
func get(o *Object, k string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(k)
	return v
}
func str(o *Object, k string) string { v, _ := get(o, k).(string); return v }
func reject(reason string) *Object   { return object("status", "rejected", "reason", reason) }

var ErrArguments = errors.New("habit_tool_arguments_invalid")

func ids(input *Object) ([]string, error) {
	a, ok := get(input, "sourceMessageIds").([]any)
	if !ok {
		return nil, ErrArguments
	}
	out := make([]string, len(a))
	for i, x := range a {
		s, ok := x.(string)
		if !ok {
			return nil, ErrArguments
		}
		out[i] = s
	}
	return out, nil
}
func proposal(input *Object) ([]string, error) {
	sourceIDs, e := ids(input)
	if e != nil {
		return nil, e
	}
	if len(sourceIDs) < 1 || len(sourceIDs) > 20 {
		return nil, ErrArguments
	}
	allowed := map[string]bool{"kind": true, "sourceMessageIds": true}
	switch str(input, "kind") {
	case "sceneRule":
		allowed["content"] = true
		v, ok := get(input, "content").(string)
		n, e := jsjson.UTF16Len(v)
		if !ok || e != nil || n < 1 || n > 1000 {
			return nil, ErrArguments
		}
	case "botExpression":
		allowed["preference"] = true
		allowed["direction"] = true
		switch str(input, "preference") {
		case "concise", "detail", "questions", "emoji", "meme_frequency":
		default:
			return nil, ErrArguments
		}
		if !direction(get(input, "direction")) {
			return nil, ErrArguments
		}
	default:
		return nil, ErrArguments
	}
	for _, k := range input.Keys() {
		if !allowed[k] {
			return nil, ErrArguments
		}
	}
	return sourceIDs, nil
}
func direction(v any) bool { n, ok := v.(float64); return ok && (n == 1 || n == -1) }
func messages(t Turn) ([]*Object, error) {
	rows := t.SourceSnapshots
	if rows == nil {
		rows = t.Preview
	}
	out := make([]*Object, len(rows))
	for i, r := range rows {
		if r == nil {
			return nil, ErrArguments
		}
		out[i] = copyValue(r).(*Object)
	}
	return out, nil
}
func sources(scene string, ids []string, rows []*Object) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		sender := ""
		for _, m := range rows {
			if str(m, "id") == id {
				sender = str(m, "senderId")
				break
			}
		}
		out[i] = object("sceneId", scene, "messageId", id, "senderId", sender)
	}
	return out
}
func (s *Service) evidence(t Turn, rows []*Object) ([]any, error) {
	out := []any{}
	for _, m := range rows {
		if str(m, "origin") == "simulation" {
			continue
		}
		text := []string{}
		parts, _ := get(m, "parts").([]any)
		for _, v := range parts {
			p, _ := v.(*Object)
			if str(p, "type") == "text" {
				text = append(text, str(p, "text"))
			}
		}
		ms, valid, e := s.dates.ParseMillis(str(m, "sentAt"))
		if e != nil {
			return nil, e
		}
		timestamp := float64(ms)
		if !valid {
			timestamp = math.NaN()
		}
		available := str(m, "status") == "available"
		if limited, ok := get(m, "sourceEventRegistrationLimited").(bool); ok && limited {
			available = false
		}
		if available {
			available, e = s.sources.CurrentRuntimeSnapshot(context.Background(), m)
			if e != nil {
				return nil, e
			}
		}
		out = append(out, object("sceneId", str(m, "sceneId"), "messageId", str(m, "id"), "senderId", str(m, "senderId"), "origin", str(m, "origin"), "text", strings.Join(text, "\n"), "timestamp", timestamp, "available", available, "isBot", str(m, "senderId") == "bot"))
	}
	return out, nil
}
func (s *Service) missingSnapshot(ctx context.Context, t Turn, ids []string) (bool, error) {
	if t.SourceSnapshots != nil {
		return false, nil
	}
	for _, id := range ids {
		registered := false
		e := s.sources.Read(ctx, func(v *sourceevents.View) error {
			m, e := v.Registered(t.Scene.ID, id)
			registered = m != nil
			return e
		})
		if e != nil {
			return false, e
		}
		if registered {
			return true, nil
		}
	}
	return false, nil
}

// Propose implements Engine.proposeHabit, including simulation before schema
// parsing. ToolPropose has a distinct, source-observable validation order.
func (s *Service) propose(ctx context.Context, t Turn, input *Object) (*Object, error) {
	if t.Simulation {
		return reject("simulation_does_not_learn"), nil
	}
	id, e := proposal(input)
	if e != nil {
		return nil, e
	}
	rows, e := messages(t)
	if e != nil {
		return nil, e
	}
	ev, e := s.evidence(t, rows)
	if e != nil {
		return nil, e
	}
	src := sources(t.Scene.ID, id, rows)
	var candidate *Object
	if str(input, "kind") == "sceneRule" {
		candidate = object("kind", "sceneRule", "content", get(input, "content"), "sources", src)
	} else {
		candidate = object("kind", "botExpression", "preference", get(input, "preference"), "direction", get(input, "direction"), "sources", src)
	}
	return s.habits.Propose(ctx, habitservice.Context{SceneID: t.Scene.ID, SourceEvidence: ev}, candidate)
}
func (s *Service) toolPropose(ctx context.Context, t Turn, input *Object) (*Object, error) {
	id, e := proposal(input)
	if e != nil {
		return nil, e
	}
	missing, e := s.missingSnapshot(ctx, t, id)
	if e != nil {
		return nil, e
	}
	if missing {
		return reject("source_snapshot_unavailable"), nil
	}
	return s.propose(ctx, t, input)
}
func (s *Service) toolFeedback(ctx context.Context, t Turn, input *Object) (*Object, error) {
	if t.Simulation {
		return reject("simulation_does_not_learn"), nil
	}
	id, e := ids(input)
	if e != nil {
		return nil, e
	}
	rows, e := messages(t)
	if e != nil {
		return nil, e
	}
	ev, e := s.evidence(t, rows)
	if e != nil {
		return nil, e
	}
	missing, e := s.missingSnapshot(ctx, t, id)
	if e != nil {
		return nil, e
	}
	if missing {
		return reject("source_snapshot_unavailable"), nil
	}
	habitID, ok := get(input, "id").(string)
	if !ok || !direction(get(input, "direction")) {
		return nil, ErrArguments
	}
	return s.habits.Feedback(ctx, habitservice.Context{SceneID: t.Scene.ID, SourceEvidence: ev}, habitID, object("direction", get(input, "direction"), "sources", sources(t.Scene.ID, id, rows)))
}

func copyValue(v any) any {
	switch x := v.(type) {
	case *Object:
		if x == nil {
			return (*Object)(nil)
		}
		o := new(Object)
		for _, m := range x.Members() {
			_ = o.Set(m.Key, copyValue(m.Value))
		}
		return o
	case []any:
		a := make([]any, len(x))
		for i, v := range x {
			a[i] = copyValue(v)
		}
		return a
	default:
		return v
	}
}

func (s *Service) Propose(ctx context.Context, t Turn, input *Object) (out *Object, err error) {
	if t.Simulation {
		return reject("simulation_does_not_learn"), nil
	}
	if _, e := proposal(input); e != nil {
		return nil, e
	}
	err = s.habits.TransactionWithSession(ctx, func(h *habitservice.Service, ws *recovery.WriteSession) error {
		bound := *s
		bound.habits = h
		bound.sources = s.sources.WithSession(ws)
		var e error
		out, e = bound.propose(ctx, t, input)
		return e
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) ToolPropose(ctx context.Context, t Turn, input *Object) (out *Object, err error) {
	if _, e := proposal(input); e != nil {
		return nil, e
	}
	// A provided snapshot skips registered-source reads in the source. Simulation
	// then short-circuits without touching storage, even when storage is unavailable.
	if t.Simulation {
		if t.SourceSnapshots == nil {
			id, e := proposal(input)
			if e != nil {
				return nil, e
			}
			missing, e := s.missingSnapshot(ctx, t, id)
			if e != nil {
				return nil, e
			}
			if missing {
				return reject("source_snapshot_unavailable"), nil
			}
		}
		return reject("simulation_does_not_learn"), nil
	}
	err = s.habits.TransactionWithSession(ctx, func(h *habitservice.Service, ws *recovery.WriteSession) error {
		bound := *s
		bound.habits = h
		bound.sources = s.sources.WithSession(ws)
		var e error
		out, e = bound.toolPropose(ctx, t, input)
		return e
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) ToolFeedback(ctx context.Context, t Turn, input *Object) (out *Object, err error) {
	if t.Simulation {
		return reject("simulation_does_not_learn"), nil
	}
	if _, e := ids(input); e != nil {
		return nil, e
	}
	err = s.habits.TransactionWithSession(ctx, func(h *habitservice.Service, ws *recovery.WriteSession) error {
		bound := *s
		bound.habits = h
		bound.sources = s.sources.WithSession(ws)
		var e error
		out, e = bound.toolFeedback(ctx, t, input)
		return e
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
