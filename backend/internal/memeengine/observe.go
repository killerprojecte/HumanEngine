package memeengine

import (
	"context"
	"errors"
	"fmt"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/habitservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"strings"
)

func jsString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return "null"
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		b, _ := jsjson.Stringify(x)
		return b
	case []any:
		values := []string{}
		for _, v := range x {
			if v == nil {
				values = append(values, "")
			} else {
				values = append(values, jsString(v))
			}
		}
		return strings.Join(values, ",")
	default:
		return "[object Object]"
	}
}
func IsStickerSource(message *Object, assetID string) bool {
	parts := arr(get(message, "parts"))
	for i, part := range parts {
		p := obj(part)
		if get(p, "type") != "media" || get(p, "assetId") != assetID || i+1 >= len(parts) {
			continue
		}
		meta := obj(parts[i+1])
		prefix := "Untrusted platform image metadata: "
		description := str(get(meta, "description"))
		if get(meta, "type") != "unsupported" || !strings.HasPrefix(description, prefix) {
			continue
		}
		v, e := jsjson.Parse(strings.TrimPrefix(description, prefix))
		if e != nil {
			continue
		}
		o := obj(v)
		if o == nil {
			continue
		}
		kind, has := o.Get("kind")
		if !has {
			continue
		}
		switch jsString(kind) {
		case "mface", "sticker", "emoji":
			return true
		}
		if kind == "image" {
			sub, has := o.Get("subtype")
			if has && (jsString(sub) == "1" || jsString(sub) == "7") {
				return true
			}
		}
	}
	return false
}

// ObserveImageRoot is a real Engine image-observation root. The transport must
// supply bytes it just read for the captured runtime message/asset. Source
// identity and retained presence are revalidated; metadata never supplies hash.
func (s *Service) ObserveImageRoot(ctx context.Context, message *Object, assetID string, b []byte) (result *Object, err error) {
	root, release, e := s.RootContext(ctx)
	if e != nil {
		return nil, e
	}
	defer release()
	ctx = root
	if ctx.Err() != nil {
		return nil, errors.New("analysis_cancelled")
	}
	scene := str(get(message, "sceneId"))
	messageID := str(get(message, "id"))
	allowed, e := s.Read.PersonalizationAllowed(ctx, scene)
	if e != nil || !allowed {
		return nil, e
	}
	ticket, e := s.Read.Require(ctx, scene)
	if e != nil {
		return nil, e
	}
	source := fields("sceneId", scene, "messageId", messageID, "senderId", get(message, "senderId"))
	recalled := false
	e = s.Sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		var e error
		recalled, e = reads.IsRecalled(botruntime.Scene{SceneRef: dispatch.SceneRef{ID: scene}}, messageID)
		return e
	})
	if e != nil {
		return nil, e
	}
	c := memeservice.Context{SceneID: scene, Simulation: get(message, "origin") == "simulation", SourceEvidence: []*Object{fields("sceneId", scene, "messageId", messageID, "senderId", get(message, "senderId"), "available", !recalled, "isBot", get(message, "senderId") == "bot")}}
	if c.Simulation {
		return nil, nil
	}
	known, e := s.Memes.RecognizeBytes(ctx, c, b)
	if e != nil {
		return nil, e
	}
	if e = s.Read.Assert(ctx, ticket); e != nil {
		return nil, e
	}
	if get(message, "origin") != "live" || get(message, "senderId") == "bot" || get(message, "sourceEventRegistrationLimited") == true {
		return known, nil
	}
	sticker := IsStickerSource(message, assetID)
	if !sticker && known == nil {
		return nil, nil
	}
	valid := func() (bool, error) {
		if ctx.Err() != nil || !s.Read.Valid(ctx, ticket) {
			return false, nil
		}
		current, e := s.Sources.CurrentRuntimeSnapshot(ctx, message)
		if e != nil || !current {
			return false, e
		}
		snapshot, e := s.Runtime.Snapshot(scene)
		if e != nil {
			return false, e
		}
		for _, item := range snapshot.Messages {
			if get(item, "id") == messageID {
				if get(item, "status") != "available" {
					return false, nil
				}
				for _, part := range arr(get(item, "parts")) {
					p := obj(part)
					if get(p, "type") == "media" && get(p, "assetId") == assetID {
						return true, nil
					}
				}
				return false, nil
			}
		}
		return false, nil
	}
	if ok, e := valid(); e != nil || !ok {
		if e == nil {
			e = errors.New("image_source_unavailable")
		}
		return nil, e
	}
	defer func() {
		ok, e := valid()
		if e != nil {
			err = e
			return
		}
		if !ok {
			return
		}
		saved, e := s.Memes.RecognizeBytes(ctx, c, b)
		if e != nil {
			err = e
			return
		}
		if saved == nil {
			return
		}
		ok, e = valid()
		if e != nil {
			err = e
			return
		}
		if !ok {
			return
		}
		var ref *sourceevents.Ref
		if raw := get(message, "sourceEventRef"); raw != nil {
			v, e := sourceevents.ParseRef(raw)
			if e != nil {
				err = e
				return
			}
			ref = &v
		}
		reference, e := sourceevents.RuntimeSourceMediaReference(assetID, ref)
		if e == nil {
			e = s.Memes.BindMediaReference(ctx, c, reference, str(get(saved, "id")), source, b)
		}
		if e != nil {
			err = e
		}
	}()
	snapshot, e := s.Runtime.Snapshot(scene)
	if e != nil {
		return nil, e
	}
	start := len(snapshot.Messages) - 8
	if start < 0 {
		start = 0
	}
	messages := []any{}
	for _, row := range snapshot.Messages[start:] {
		texts := []string{}
		for _, part := range arr(get(row, "parts")) {
			p := obj(part)
			if get(p, "type") == "text" {
				texts = append(texts, str(get(p, "text")))
			}
		}
		messages = append(messages, fields("isBot", get(row, "senderId") == "bot", "text", prefix(strings.Join(texts, " "), 2000)))
	}
	hints, e := s.Habits.List(ctx, habitservice.Context{SceneID: scene})
	if e != nil {
		return nil, e
	}
	expression := []any{}
	for _, hint := range hints {
		if get(hint, "kind") == "botExpression" {
			expression = append(expression, hint)
		}
	}
	persona, e := s.persona(ctx)
	if e != nil {
		return nil, e
	}
	var models *providertransport.NativeModels
	router, releaseRouter, e := s.Models.AcquireRouter(ctx)
	if e != nil {
		return nil, e
	}
	defer releaseRouter()
	if router.Policy.HasConfiguredRoute("vision") {
		models = router
	}
	ctx = providertransport.WithModelReadGuard(ctx, func(c context.Context) error { return s.Read.Assert(c, ticket) })
	result, err = s.Memes.ObserveBytes(ctx, c, b, source, models, s.Codec, memeservice.ObserveOptions{Admit: &sticker, Selection: &memeservice.SelectionContext{Persona: persona, Preferences: fields("expression", expression), SourceContextKnown: true, SourceContext: messages}})
	if err == nil {
		if ok, e := valid(); e != nil {
			err = e
		} else if !ok {
			err = errors.New("image_source_unavailable")
		}
	}
	return
}
func MemeObservation(asset *Object) string {
	tags := obj(get(asset, "tags"))
	visible, uses := fields(), fields()
	for _, k := range []string{"appearance", "style", "subject", "ocr"} {
		_ = visible.Set(k, get(tags, k))
	}
	for _, k := range []string{"emotion", "intent", "tone", "intensity", "memeMeaning"} {
		_ = uses.Set(k, get(tags, k))
	}
	b, e := jsjson.Stringify(fields("source", "scene_visible_meme_library", "visibleEvidence", visible, "possibleUses", uses, "uncertainty", "Stored image labels are fallible observations and possible conventional uses, not proof of sender feelings or intent. Interpret using current conversation. Animation analysis samples frames rather than all motion."))
	if e != nil {
		return fmt.Sprint(e)
	}
	return prefix(b, 4000)
}
