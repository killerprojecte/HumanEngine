package snowlumaruntime

import (
	"context"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	transport "github.com/killerprojecte/HumanEngine/backend/plugins/snowluma/host/transport"
	"sync"
	"time"
)

type ReceivePorts struct {
	Recalled        func(context.Context, string, string) (bool, error)
	ObserveProfiles func(context.Context, *jsjson.Object, *jsjson.Object) error
	Ingest          func(context.Context, *jsjson.Object, *jsjson.Object) error
	Recall          func(context.Context, string, string) error
	Now             func() time.Time
}
type Receiver struct {
	mu     sync.Mutex
	sender *Sender
	ports  ReceivePorts
	state  *jsjson.Object
}

func NewReceiver(sender *Sender, ports ReceivePorts) (*Receiver, error) {
	if sender == nil || ports.Recalled == nil || ports.ObserveProfiles == nil || ports.Ingest == nil || ports.Recall == nil || ports.Now == nil {
		return nil, errors.New("snowluma_receive_ports_incomplete")
	}
	return &Receiver{sender: sender, ports: ports}, nil
}
func (r *Receiver) Receive(ctx context.Context, raw string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.sender.current(); err != nil {
		return err
	}
	event, err := jsjson.Parse(raw)
	if err != nil {
		return err
	}
	input := obj("operation", "ingress_begin", "adapter", r.sender.adapter, "account", r.sender.account, "event", event, "receivedAt", r.ports.Now().UTC().Format("2006-01-02T15:04:05.000Z"), "state", optionalState(r.state))
	for {
		decision, e := r.sender.call(input)
		if e != nil {
			return e
		}
		if state, ok := get(decision, "state").(*jsjson.Object); ok {
			r.state = state
		}
		if err = r.sender.current(); err != nil {
			return err
		}
		switch str(decision, "kind") {
		case "ignore":
			return nil
		case "error":
			return errors.New(str(decision, "error"))
		case "is_recalled":
			recalled, e := r.ports.Recalled(ctx, str(decision, "sceneId"), str(decision, "messageId"))
			if e != nil {
				return e
			}
			request, ok := get(decision, "request").(*jsjson.Object)
			if !ok {
				return errors.New("invalid_guest_ingress_request")
			}
			input = request.Clone()
			_ = input.Set("operation", "ingress_recalled")
			_ = input.Set("recalled", recalled)
			_ = input.Set("state", optionalState(r.state))
		case "observe_profiles":
			message, mok := get(decision, "message").(*jsjson.Object)
			scene, sok := get(decision, "scene").(*jsjson.Object)
			if !mok || !sok {
				return errors.New("invalid_guest_ingress_message")
			}
			if e = r.ports.ObserveProfiles(ctx, message, scene); e != nil {
				return e
			}
			input = obj("operation", "ingress_callback", "state", optionalState(r.state), "adapter", r.sender.adapter, "account", r.sender.account, "message", message, "scene", scene)
		case "ingest":
			message, mok := get(decision, "message").(*jsjson.Object)
			scene, sok := get(decision, "scene").(*jsjson.Object)
			if !mok || !sok {
				return errors.New("invalid_guest_ingress_message")
			}
			return r.ports.Ingest(ctx, message, scene)
		case "recall":
			event, ok := get(decision, "recall").(*jsjson.Object)
			if !ok {
				return errors.New("invalid_guest_ingress_recall")
			}
			return r.ports.Recall(ctx, str(event, "sceneId"), str(event, "messageId"))
		default:
			return errors.New("invalid_guest_ingress_step")
		}
	}
}

func (r *Receiver) isRecalled(ctx context.Context, scene, id string) (bool, error) {
	result, err := r.sender.call(obj("operation", "recalled_lookup", "state", optionalState(r.state), "sceneId", scene, "messageId", id))
	if err != nil {
		return false, err
	}
	if str(result, "kind") == "recalled" {
		return get(result, "value") == true, nil
	}
	if str(result, "kind") != "is_recalled" {
		return false, errors.New("invalid_guest_recall_lookup")
	}
	if err = r.sender.current(); err != nil {
		return false, err
	}
	return r.ports.Recalled(ctx, str(result, "sceneId"), str(result, "messageId"))
}
func (r *Receiver) IsRecalled(ctx context.Context, scene, id string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.isRecalled(ctx, scene, id)
}
func (r *Receiver) Normalize(ctx context.Context, event, expected *jsjson.Object) (message *jsjson.Object, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	input := obj("operation", "identify_message", "event", event, "expected", expected, "adapter", r.sender.adapter, "account", r.sender.account, "origin", "history", "receivedAt", r.ports.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
	identified, err := r.sender.call(input)
	if err != nil {
		return nil, err
	}
	if code := str(identified, "error"); code != "" {
		if get(identified, "oneBot") == true {
			return nil, &transport.Error{Code: code}
		}
		return nil, errors.New(code)
	}
	scene, ok := get(identified, "scene").(*jsjson.Object)
	if !ok {
		return nil, errors.New("invalid_guest_identification")
	}
	recalled, err := r.isRecalled(ctx, str(scene, "id"), str(identified, "messageId"))
	if err != nil {
		return nil, err
	}
	_ = input.Set("operation", "normalize")
	_ = input.Set("recalled", recalled)
	normalized, err := r.sender.call(input)
	if err != nil {
		return nil, err
	}
	if code := str(normalized, "error"); code != "" {
		if get(normalized, "oneBot") == true {
			return nil, &transport.Error{Code: code}
		}
		return nil, errors.New(code)
	}
	message, mok := get(normalized, "message").(*jsjson.Object)
	scene, sok := get(normalized, "scene").(*jsjson.Object)
	if !mok || !sok {
		return nil, errors.New("invalid_guest_normalization")
	}
	if err = r.sender.current(); err != nil {
		return nil, err
	}
	if err = r.ports.ObserveProfiles(ctx, message, scene); err != nil {
		return nil, err
	}
	return message, nil
}
