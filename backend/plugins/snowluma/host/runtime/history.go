package snowlumaruntime

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/topicroute"
	transport "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/host/transport"
)

type Queries struct {
	compare  topicroute.LocaleCompare
	sender   *Sender
	receiver *Receiver
	profiles *Profiles
}

func (q *Queries) Run(ctx context.Context, scene botruntime.Scene, request *jsjson.Object, single bool, complete func(*jsjson.Object, error)) {
	done, ok := q.sender.work.Start()
	if !ok {
		complete(nil, errors.New("plugin_disabled"))
		return
	}
	go func() {
		defer done()
		defer q.profiles.Flush()
		result, err := q.query(ctx, scene, request, single)
		complete(result, err)
	}()
}
func (q *Queries) query(ctx context.Context, scene botruntime.Scene, request *jsjson.Object, single bool) (*jsjson.Object, error) {
	ref, err := jsonObject(scene.SceneRef)
	if err != nil {
		return nil, err
	}
	input := obj("operation", "history_begin", "adapter", q.sender.adapter, "account", q.sender.account, "scene", ref, "input", request)
	if single {
		_ = input.Set("operation", "message_begin")
		_ = input.Set("messageId", get(request, "messageId"))
	}
	for {
		result, err := q.sender.call(input)
		if err != nil {
			return nil, err
		}
		state := get(result, "state")
		switch str(result, "kind") {
		case "history":
			value, ok := get(result, "result").(*jsjson.Object)
			if !ok {
				return nil, errors.New("invalid_guest_history_result")
			}
			return value, nil
		case "is_recalled":
			recalled, e := q.receiver.IsRecalled(ctx, str(result, "sceneId"), str(result, "messageId"))
			input = obj("operation", "message_recalled", "state", state, "recalled", recalled)
			historyIOError(input, e)
		case "action":
			params, ok := get(result, "params").(*jsjson.Object)
			if !ok {
				return nil, errors.New("invalid_guest_history_action")
			}
			var value any
			var e error
			if q.sender.http != nil {
				value, e = q.sender.http.Action(ctx, str(result, "action"), params)
			} else {
				value, e = q.sender.ws.Action(ctx, str(result, "action"), params)
			}
			input = obj("operation", "history_response", "state", state)
			if e == nil {
				_ = input.Set("value", value)
			}
			historyIOError(input, e)
		case "compare":
			order, e := q.compare(str(result, "left"), str(result, "right"))
			input = obj("operation", "history_sorted", "state", state, "comparison", float64(order))
			historyIOError(input, e)
		case "normalize":
			event, eok := get(result, "event").(*jsjson.Object)
			scene, sok := get(result, "scene").(*jsjson.Object)
			if !eok || !sok {
				return nil, errors.New("invalid_guest_history_normalization")
			}
			message, e := q.receiver.Normalize(ctx, event, scene)
			input = obj("operation", "history_normalized", "state", state)
			if e == nil {
				_ = input.Set("message", message)
			}
			historyIOError(input, e)
		default:
			return nil, errors.New("invalid_guest_history_step")
		}
	}
}
func historyIOError(input *jsjson.Object, err error) {
	if err == nil {
		return
	}
	_ = input.Set("failed", true)
	var typed *transport.Error
	if errors.As(err, &typed) {
		_ = input.Set("oneBot", true)
		_ = input.Set("code", typed.Code)
	}
}
