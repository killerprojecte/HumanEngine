package externaladapter

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsoncompat"
)

// scope is established by the native installer from its descriptor and active
// account. No guest response can name the host's authority namespace.
type scope struct {
	adapter string
	account string
	onebot  bool
}

type peer struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func (s scope) scene(p peer) (dispatch.SceneRef, error) {
	if !validWireIdentity(p.ID) || (p.Kind != "group" && p.Kind != "private") {
		return dispatch.SceneRef{}, errors.New("external_adapter_peer_invalid")
	}
	var id string
	if s.onebot {
		id = "ob:s:" + encodeOneBot(s.adapter, s.account, p.Kind, p.ID)
	} else {
		id = "v2:s:" + encode(s.adapter, s.account, p.Kind, p.ID)
	}
	return dispatch.SceneRef{ID: id, Kind: p.Kind, AdapterID: s.adapter, AccountID: s.account, PlatformSceneID: p.ID}, nil
}

func (s scope) sender(id string) string {
	if s.onebot {
		return "ob:u:" + encodeOneBot(s.adapter, s.account, id)
	}
	return "v2:u:" + encode(s.adapter, s.account, id)
}

func messageID(scene dispatch.SceneRef, id string) string {
	if strings.HasPrefix(scene.ID, "ob:s:") {
		raw := jsoncompat.StringArray(id)
		return scene.ID + ":m:" + base64.RawURLEncoding.EncodeToString(raw[1:len(raw)-1])
	}
	return scene.ID + ":m:" + encode(id)
}

func encodeOneBot(values ...string) string {
	return base64.RawURLEncoding.EncodeToString(jsoncompat.StringArray(values...))
}

func platformMessage(scene dispatch.SceneRef, id string) (string, error) {
	prefix := scene.ID + ":m:"
	opaque, ok := strings.CutPrefix(id, prefix)
	if ok {
		raw, err := base64.RawURLEncoding.DecodeString(opaque)
		if strings.HasPrefix(scene.ID, "ob:s:") {
			var value string
			if err == nil && json.Unmarshal(raw, &value) == nil && value != "" && messageID(scene, value) == id {
				return value, nil
			}
			return "", errors.New("external_adapter_reply_scope_mismatch")
		}
		var values [1]string
		if err == nil && json.Unmarshal(raw, &values) == nil && values[0] != "" && encode(values[0]) == opaque {
			return values[0], nil
		}
	}
	return "", errors.New("external_adapter_reply_scope_mismatch")
}

func encode(values ...string) string {
	raw, _ := json.Marshal(values) // Array of strings is always representable.
	return base64.RawURLEncoding.EncodeToString(raw)
}

// event is the versioned guest projection of a signed peer event. It has no raw
// host IDs, origin, source authority refs, or peer-controlled receipt clock.
// This first transport profile intentionally supports text ingress and recall.
type event struct {
	Type    string
	Peer    peer
	Message string
	Sender  string
	ReplyTo string
	SentAt  time.Time
	Text    string
}

func validWireIdentity(value string) bool { return value != "" && utf8.ValidString(value) }

func text(o *jsjson.Object, key string) string {
	raw, _ := o.Get(key)
	value, _ := raw.(string)
	return value
}

func fieldObject(o *jsjson.Object, key string) *jsjson.Object {
	raw, _ := o.Get(key)
	value, _ := raw.(*jsjson.Object)
	return value
}

func guestObject(raw []byte) (*jsjson.Object, error) {
	value, err := jsjson.ParseBytes(raw)
	if err != nil {
		return nil, err
	}
	o, ok := value.(*jsjson.Object)
	if !ok {
		return nil, errors.New("external_adapter_guest_object_required")
	}
	return o, nil
}

func projectEvent(raw []byte) (event, error) {
	o, err := guestObject(raw)
	if err != nil {
		return event{}, err
	}
	p := fieldObject(o, "peer")
	ev := event{Type: text(o, "type"), Message: text(o, "message")}
	switch ev.Type {
	case "message", "recall":
		if p == nil || !validWireIdentity(ev.Message) {
			return event{}, errors.New("external_adapter_event_invalid")
		}
		ev.Peer = peer{Kind: text(p, "kind"), ID: text(p, "id")}
		if ev.Type == "message" {
			ev.Sender, ev.ReplyTo, ev.Text = text(o, "sender"), text(o, "replyTo"), text(o, "text")
			ev.SentAt, err = time.Parse(time.RFC3339Nano, text(o, "sentAt"))
			if err != nil || !validWireIdentity(ev.Sender) || (ev.ReplyTo != "" && !validWireIdentity(ev.ReplyTo)) || ev.Text == "" {
				return event{}, errors.New("external_adapter_message_invalid")
			}
		}
	case "ignore":
	default:
		return event{}, errors.New("external_adapter_event_invalid")
	}
	return ev, nil
}

func (s scope) message(scene dispatch.SceneRef, in event, now time.Time) (*jsjson.Object, error) {
	const clock = "2006-01-02T15:04:05.000Z"
	part, _ := jsjson.NewObject(jsjson.Member{Key: "type", Value: "text"}, jsjson.Member{Key: "text", Value: in.Text})
	out, err := jsjson.NewObject(
		jsjson.Member{Key: "id", Value: messageID(scene, in.Message)},
		jsjson.Member{Key: "sceneId", Value: scene.ID},
		jsjson.Member{Key: "senderId", Value: s.sender(in.Sender)},
		jsjson.Member{Key: "senderPlatformId", Value: in.Sender},
		jsjson.Member{Key: "origin", Value: "live"},
		jsjson.Member{Key: "status", Value: "available"},
		jsjson.Member{Key: "sentAt", Value: in.SentAt.UTC().Format(clock)},
		jsjson.Member{Key: "receivedAt", Value: now.UTC().Format(clock)},
		jsjson.Member{Key: "parts", Value: []any{part}},
	)
	if err == nil && in.ReplyTo != "" {
		err = out.Set("replyToId", messageID(scene, in.ReplyTo))
	}
	return out, err
}
