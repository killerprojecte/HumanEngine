package snowlumatransport

import (
	"context"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/onebotwire"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
)

type WS = onebotwire.WS
type WSConfig struct {
	URL, AccountID, Token string
	Timeout, Reconnect    time.Duration
}
type Preparation = onebotwire.Preparation

type wsPolicy struct {
	guest             func(context.Context, []byte) ([]byte, error)
	builtinImageReply bool
}

func NewWS(config WSConfig, host *plugins.WASMHost, id string, callbacks HTTPCallbacks) (*WS, error) {
	return newWS(config, host, id, callbacks, false)
}

// NewBuiltinWS binds the first-party SnowLuma reactor. Its known-echo policy
// returns the original data unchanged on success. Alternate guests must use NewWS.
func NewBuiltinWS(config WSConfig, host *plugins.WASMHost, id string, callbacks HTTPCallbacks) (*WS, error) {
	return newWS(config, host, id, callbacks, true)
}

func newWS(config WSConfig, host *plugins.WASMHost, id string, callbacks HTTPCallbacks, builtinImageReply bool) (*WS, error) {
	guest, err := host.BindCall(id)
	if err != nil {
		return nil, err
	}
	return bindWS(config, guest, callbacks, builtinImageReply), nil
}

func bindWS(config WSConfig, guest func(context.Context, []byte) ([]byte, error), callbacks HTTPCallbacks, builtinImageReply bool) *WS {
	policy := wsPolicy{guest: guest, builtinImageReply: builtinImageReply}
	return onebotwire.NewWS(onebotwire.WSConfig{URL: config.URL, Token: config.Token, Timeout: config.Timeout, Reconnect: config.Reconnect}, onebotwire.WSCallbacks{
		Receive: callbacks.Receive, Status: callbacks.Status, Error: callbacks.Error,
		Login: func(ctx context.Context, value any) error {
			result, err := jsjson.Stringify(value)
			if err != nil {
				return err
			}
			decision, err := policy.decide(ctx, wsObject("operation", "http_login", "raw", result, "account", config.AccountID))
			if err != nil {
				return err
			}
			if code, ok := decision.Get("error"); ok {
				return failure(code.(string), false)
			}
			return nil
		},
		Reply: policy.reply,
	})
}

func (w *wsPolicy) decide(ctx context.Context, input *jsjson.Object) (*jsjson.Object, error) {
	wire, err := jsjson.Marshal(input)
	if err != nil {
		return nil, err
	}
	out, err := w.guest(ctx, wire)
	if err != nil {
		return nil, err
	}
	value, err := jsjson.ParseBytes(out)
	if err != nil {
		return nil, err
	}
	o, ok := value.(*jsjson.Object)
	if !ok {
		return nil, failure("invalid_guest_response", false)
	}
	return o, nil
}
func wsObject(pairs ...any) *jsjson.Object {
	o := new(jsjson.Object)
	for i := 0; i < len(pairs); i += 2 {
		_ = o.Set(pairs[i].(string), pairs[i+1])
	}
	return o
}
func (w *wsPolicy) reply(ctx context.Context, event *jsjson.Object, echo, action string) (any, error) {
	imageReply := w.builtinImageReply && action == "get_image"
	policyEvent := event
	if imageReply {
		// The reactor qualifies only echo, status and retcode. Do not send the
		// already parsed (potentially large) data through WASM just to echo it.
		policyEvent = new(jsjson.Object)
		for _, key := range [...]string{"echo", "status", "retcode"} {
			if value, exists := event.Get(key); exists {
				_ = policyEvent.Set(key, value)
			}
		}
	}
	decision, err := w.decide(ctx, wsObject("operation", "receive", "event", policyEvent, "knownEcho", true))
	if err != nil {
		return nil, err
	}
	code, hasError := decision.Get("error")
	if !imageReply {
		if hasError {
			return nil, failure(code.(string), false)
		}
		data, _ := decision.Get("data")
		return data, nil
	}
	kind, _ := decision.Get("kind")
	returnedEcho, _ := decision.Get("echo")
	if returnedEcho == echo {
		if kind == "response_error" && hasError {
			if code, ok := code.(string); ok {
				return nil, failure(code, false)
			}
		}
		if kind == "response" && !hasError {
			if _, synthesized := decision.Get("data"); !synthesized {
				data, _ := event.Get("data")
				return data, nil
			}
		}
	}
	return nil, failure("invalid_guest_response", false)
}
