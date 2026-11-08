package botruntime

import "errors"

// emitPluginEvent snapshots listeners and data without retaining registry locks.
// Listener owners schedule their asynchronous work and own its physical drain.
func (r *Runtime) emitPluginEvent(event *Object) {
	for _, extension := range r.exts() {
		if extension.RuntimeEvent != nil {
			extension.RuntimeEvent(cloneJSON(event).(*Object))
		}
	}
}

// SceneValue preserves WTF-8 strings and the source optional field presence.
func SceneValue(scene Scene) *Object {
	o := obj("id", scene.ID, "kind", scene.Kind, "adapterId", scene.AdapterID, "accountId", scene.AccountID, "platformSceneId", scene.PlatformSceneID)
	if scene.DisplayName != nil {
		_ = o.Set("displayName", *scene.DisplayName)
	}
	if scene.AvatarURL != nil {
		_ = o.Set("avatarUrl", *scene.AvatarURL)
	}
	_ = o.Set("participationEnabled", scene.ParticipationEnabled)
	_ = o.Set("learningEnabled", scene.LearningEnabled)
	_ = o.Set("proactiveEnabled", scene.ProactiveEnabled)
	return o
}
func sceneEventValue(scene Scene) *Object { return SceneValue(scene) }

// PreparePluginEvent runs at listener delivery, not at emission. Each listener
// gets its own clone and a fresh authoritative recall check.
func (r *Runtime) PreparePluginEvent(event *Object) (*Object, error) {
	value := cloneJSON(event).(*Object)
	kind, _ := get(value, "type").(string)
	if kind != "message.received" && kind != "message.sent" {
		return value, nil
	}
	sceneValue, _ := get(value, "scene").(*Object)
	sceneID, _ := get(sceneValue, "id").(string)
	target, _ := get(value, "message").(*Object)
	id := ""
	if kind == "message.received" {
		id, _ = get(target, "id").(string)
	} else {
		result, _ := get(value, "result").(*Object)
		id, _ = get(result, "messageId").(string)
		target, _ = get(value, "segment").(*Object)
	}
	if id == "" {
		return value, nil
	}
	var recalled bool
	var err error
	if state := r.state(sceneID); state != nil {
		recalled, err = r.recalled(state, id)
	} else {
		if r.services.Authority == nil {
			return nil, errors.New("runtime_authority_unavailable")
		}
		scene := Scene{}
		scene.ID = sceneID
		scene.Kind, _ = get(sceneValue, "kind").(string)
		scene.AdapterID, _ = get(sceneValue, "adapterId").(string)
		scene.AccountID, _ = get(sceneValue, "accountId").(string)
		scene.PlatformSceneID, _ = get(sceneValue, "platformSceneId").(string)
		recalled, err = r.services.Authority.IsRecalled(scene, id)
	}
	if err != nil {
		return nil, err
	}
	if recalled && target != nil {
		_ = target.Set("parts", []any{})
		if kind == "message.received" {
			_ = target.Set("status", "recalled")
		}
	}
	return value, nil
}
