// Package ingressrouting owns scene discovery and presentation identity updates.
// These effects precede duplicate/recall admission, including bot ingress.
package ingressrouting

import (
	"context"
	"errors"
	"math"

	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/profileidentity"
)

type Repository interface {
	Scene(context.Context, string) (any, bool, error)
	SaveScene(context.Context, *jsjson.Object) error
	Identity(context.Context, string, string) (any, bool, error)
	SaveIdentity(context.Context, string, string, *jsjson.Object) error
}
type Router struct{ repository Repository }

func New(repository Repository) (*Router, error) {
	if repository == nil {
		return nil, errors.New("ingress_repository_required")
	}
	return &Router{repository: repository}, nil
}

// Route does not inspect duplicate, tombstone, scheduling or source-event state.
// Its caller owns that subsequent admission decision and runtime registration.
func (r *Router) Route(ctx context.Context, scene dispatch.SceneRef, message *jsjson.Object) error {
	if scene.Kind == "simulation" {
		return nil
	}
	previous, _, err := r.repository.Scene(ctx, scene.ID)
	if err != nil {
		return err
	}
	if !truthy(previous) {
		next := object("id", scene.ID, "adapterId", scene.AdapterID, "accountId", scene.AccountID, "platformSceneId", scene.PlatformSceneID)
		if scene.AvatarURL != nil {
			set(next, "avatarUrl", *scene.AvatarURL)
		}
		set(next, "kind", scene.Kind)
		name := scene.PlatformSceneID
		if scene.DisplayName != nil {
			name = *scene.DisplayName
		}
		set(next, "name", name)
		set(next, "participationEnabled", false)
		set(next, "learningEnabled", false)
		set(next, "proactiveEnabled", false)
		if err = r.repository.SaveScene(ctx, next); err != nil {
			return err
		}
	}
	return r.ObserveIdentity(ctx, scene, message)
}
func (r *Router) ObserveIdentity(ctx context.Context, profile dispatch.SceneRef, message *jsjson.Object) error {
	raw, _, err := r.repository.Scene(ctx, profile.ID)
	if err != nil {
		return err
	}
	scene, ok := raw.(*jsjson.Object)
	if !ok {
		return nil
	}
	id, err := profileidentity.OneBotID(get(scene, "adapterId"), get(scene, "accountId"), get(scene, "kind"), get(scene, "platformSceneId"))
	if err != nil {
		return err
	}
	if get(scene, "id") != id || get(scene, "adapterId") != profile.AdapterID || get(scene, "accountId") != profile.AccountID || get(scene, "platformSceneId") != profile.PlatformSceneID {
		return nil
	}
	var nameValue any
	if profile.DisplayName != nil {
		nameValue = *profile.DisplayName
	}
	name, hasName, err := profileidentity.DisplayName(nameValue)
	if err != nil {
		return err
	}
	avatar, hasAvatar, err := profileidentity.AvatarValue(get(scene, "platformSceneId"), get(scene, "kind") == "group")
	if err != nil {
		return err
	}
	if hasName && get(scene, "name") != name || hasAvatar && get(scene, "avatarUrl") != avatar {
		next := scene.Clone()
		if hasName {
			set(next, "name", name)
		}
		if hasAvatar {
			set(next, "avatarUrl", avatar)
		}
		if err = r.repository.SaveScene(ctx, next); err != nil {
			return err
		}
	}
	platform := get(message, "senderPlatformId")
	if !truthy(platform) {
		return nil
	}
	sender := "undefined"
	if value, present := message.Get("senderId"); present {
		sender, err = profileidentity.StringValue(value)
		if err != nil {
			return err
		}
	}
	previous, exists, err := r.repository.Identity(ctx, profile.ID, sender)
	if err != nil {
		return err
	}
	if !exists {
		previous = object()
	}
	senderName, hasName, err := profileidentity.DisplayName(get(message, "senderDisplayName"))
	if err != nil {
		return err
	}
	next := object()
	if hasName {
		set(next, "senderDisplayName", senderName)
	} else {
		if previous == nil {
			return errors.New("Cannot read properties of null (reading 'senderDisplayName')")
		}
		if old, ok := previous.(*jsjson.Object); ok {
			if v, present := old.Get("senderDisplayName"); present {
				set(next, "senderDisplayName", v)
			}
		}
	}
	set(next, "senderPlatformId", platform)
	avatar, hasAvatar, err = profileidentity.AvatarValue(platform, false)
	if err != nil {
		return err
	}
	if hasAvatar {
		set(next, "senderAvatarUrl", avatar)
	}
	oldText, err := jsjson.Stringify(previous)
	if err != nil {
		return err
	}
	newText, err := jsjson.Stringify(next)
	if err != nil {
		return err
	}
	if oldText == newText {
		return nil
	}
	return r.repository.SaveIdentity(ctx, profile.ID, sender, next)
}
func get(o *jsjson.Object, key string) any        { v, _ := o.Get(key); return v }
func set(o *jsjson.Object, key string, value any) { _ = o.Set(key, value) }
func object(pairs ...any) *jsjson.Object {
	o := new(jsjson.Object)
	for i := 0; i < len(pairs); i += 2 {
		set(o, pairs[i].(string), pairs[i+1])
	}
	return o
}
func truthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case float64:
		return v != 0 && !math.IsNaN(v)
	default:
		return true
	}
}
