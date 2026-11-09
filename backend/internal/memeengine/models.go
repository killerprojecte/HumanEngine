package memeengine

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/providerwire"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"sync"
)

type RuntimeModels struct {
	Native *providertransport.NativeModels
	Read   *modelread.RuntimePolicy
}
type continuationState struct {
	mu            sync.Mutex
	routerOnce    sync.Once
	router        *providertransport.NativeModels
	releaseRouter func()
	routerError   error
	closed        bool
	messages      map[*Object]*providertransport.Message
}

func (s *continuationState) ReleaseTurnState() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	messages := s.messages
	s.messages = nil
	releaseRouter := s.releaseRouter
	s.releaseRouter = nil
	s.mu.Unlock()
	var failures []error
	for _, message := range messages {
		failures = append(failures, message.Release())
	}
	if releaseRouter != nil {
		releaseRouter()
	}
	return errors.Join(failures...)
}

func (s *continuationState) retain(key *Object, message *providertransport.Message) (bool, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false, message.Release()
	}
	s.messages[key] = message
	s.mu.Unlock()
	return true, nil
}

func plain(v any) any {
	switch x := v.(type) {
	case *Object:
		out := map[string]any{}
		for _, m := range x.Members() {
			out[m.Key] = plain(m.Value)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = plain(v)
		}
		return out
	default:
		return v
	}
}
func objectValue(v any) (*Object, error) {
	b, e := providerwire.EncodeJSON(v)
	if e != nil {
		return nil, e
	}
	v, e = jsjson.ParseBytes(b)
	if e != nil {
		return nil, e
	}
	o, ok := v.(*Object)
	if !ok {
		return nil, errors.New("model_response_object_required")
	}
	return o, nil
}
func (m *RuntimeModels) Start(ctx context.Context, r botruntime.ModelRequest, settle func(botruntime.Completion, error)) {
	if m.Native == nil || m.Read == nil {
		settle(botruntime.Completion{}, errors.New("native_model_read_composition_required"))
		return
	}
	state := &continuationState{messages: map[*Object]*providertransport.Message{}}
	if r.Turn != nil {
		state = r.Turn.OwnedExtensionState(m, func() any { return state }).(*continuationState)
		if err := m.BeginTurn(ctx, r.Turn); err != nil {
			settle(botruntime.Completion{}, err)
			return
		}
		ctx = providertransport.WithModelReadGuard(ctx, func(c context.Context) error {
			if !m.Read.ValidContext(c, r.Turn) {
				return errors.New("memory_read_revoked")
			}
			valid, e := r.Turn.ValidateMemoryContext()
			if e != nil {
				return e
			}
			if !valid {
				return errors.New("memory_read_revoked")
			}
			return nil
		})
	}
	messages := make([]*providertransport.Message, len(r.Messages))
	state.mu.Lock()
	if state.closed {
		state.mu.Unlock()
		settle(botruntime.Completion{}, errors.New("turn_finished"))
		return
	}
	for i, message := range r.Messages {
		if saved := state.messages[message]; saved != nil {
			messages[i] = saved
		} else {
			messages[i] = &providertransport.Message{Visible: plain(message).(map[string]any)}
		}
	}
	native := state.router
	if native == nil {
		native = m.Native
	}
	state.mu.Unlock()
	tools := make([]map[string]any, len(r.Tools))
	for i, t := range r.Tools {
		tools[i] = plain(t).(map[string]any)
	}
	go func() {
		result, e := native.Complete(ctx, providertransport.CompletionRequest{Role: r.Role, Messages: messages, Tools: tools, FirstContent: r.FirstContent})
		if e != nil {
			settle(botruntime.Completion{}, e)
			return
		}
		if result == nil || result.Assistant == nil {
			settle(botruntime.Completion{}, errors.New("model_response_object_required"))
			return
		}
		assistant, e := objectValue(result.Assistant.Visible)
		if e != nil {
			_ = result.Release()
			settle(botruntime.Completion{}, e)
			return
		}
		calls := []replyplan.ToolCall{}
		for _, raw := range result.ToolCalls {
			call, e := objectValue(raw)
			if e != nil {
				_ = result.Release()
				settle(botruntime.Completion{}, e)
				return
			}
			calls = append(calls, replyplan.ToolCall{ID: str(get(call, "id")), Name: str(get(call, "name")), Arguments: get(call, "arguments")})
		}
		retained, releaseError := state.retain(assistant, result.Assistant)
		if !retained {
			settle(botruntime.Completion{}, errors.Join(errors.New("turn_finished"), releaseError))
			return
		}
		if r.Turn == nil {
			defer state.ReleaseTurnState()
		}
		settle(botruntime.Completion{Completion: replyplan.Completion{ToolCalls: calls, Assistant: assistant}, Text: result.Text}, nil)
	}()
}

func (m *RuntimeModels) BeginTurn(ctx context.Context, turn *botruntime.TurnContext) error {
	if m.Native == nil || turn == nil {
		return errors.New("native_model_turn_dependencies_required")
	}
	state := turn.OwnedExtensionState(m, func() any { return &continuationState{messages: map[*Object]*providertransport.Message{}} }).(*continuationState)
	state.routerOnce.Do(func() {
		router, release, err := m.Native.AcquireRouter(ctx)
		state.mu.Lock()
		if state.closed {
			state.routerError = errors.New("turn_finished")
			state.mu.Unlock()
			if release != nil {
				release()
			}
			return
		}
		state.router, state.releaseRouter, state.routerError = router, release, err
		state.mu.Unlock()
	})
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return errors.New("turn_finished")
	}
	return state.routerError
}
