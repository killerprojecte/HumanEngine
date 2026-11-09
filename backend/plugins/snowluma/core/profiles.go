package snowluma

import (
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/profileidentity"
)

// ProfileDecision implements the source's existing opportunistic cache policy.
// The 500 entries, four pending lookups and 30/5 minute TTLs are copied from
// OneBotProfiles, not new protection limits. State belongs to one generation.
func ProfileDecision(input *jsjson.Object) any {
	state, _ := object(property(input, "state"))
	if state == nil {
		state = makeObject("cache", []any{}, "pending", []any{}, "next", float64(0))
	}
	operation := textProperty(input, "operation")
	now, _ := property(input, "now").(float64)
	if operation == "profiles_dispose" {
		_ = state.Set("disposed", true)
		_ = state.Set("cache", []any{})
		return httpJSON{"kind": "profiles", "state": state}
	}
	if operation == "profiles_complete" {
		return profileComplete(input, state, now)
	}
	message, err := object(property(input, "message"))
	if err != nil {
		return receiveFailure(err)
	}
	scene, err := object(property(input, "scene"))
	if err != nil {
		return receiveFailure(err)
	}
	jobs := []any{}
	result := httpJSON{"kind": "profiles", "state": state, "message": message, "scene": scene, "jobs": jobs}
	if truthy(property(state, "disposed")) || !sameString(property(message, "status"), "available") {
		return result
	}
	sceneKey := "scene:" + textProperty(scene, "id")
	senderKey := "sender:" + textProperty(scene, "id") + ":" + textProperty(message, "senderId")
	needsScene := !truthy(property(scene, "displayName"))
	needsSender := !truthy(property(message, "senderDisplayName"))
	if !needsScene {
		profileRemember(state, sceneKey, property(scene, "displayName"), now+1800000)
	} else if entry := profileCached(state, sceneKey); entry != nil {
		if v, ok := entry.Get("name"); ok {
			_ = scene.Set("displayName", v)
		}
	}
	if !needsSender {
		profileRemember(state, senderKey, property(message, "senderDisplayName"), now+1800000)
	} else if entry := profileCached(state, senderKey); entry != nil {
		if v, ok := entry.Get("name"); ok {
			_ = message.Set("senderDisplayName", v)
		}
	}
	schedule := func(key, action, field string, id any, kind string) {
		previous := profileCached(state, key)
		if previous != nil {
			until, _ := property(previous, "until").(float64)
			if until > now {
				return
			}
		}
		pending, _ := property(state, "pending").([]any)
		for _, v := range pending {
			if sameString(v, key) {
				return
			}
		}
		if len(pending) >= 4 {
			return
		}
		_ = state.Set("pending", append(pending, key))
		job := makeObject("key", key, "action", action, "params", makeObject(field, id), "kind", kind, "scene", scene.Clone(), "message", message.Clone())
		if previous != nil {
			_ = job.Set("previous", property(previous, "revision"))
		}
		jobs = append(jobs, job)
	}
	if needsScene {
		action, field := "get_stranger_info", "user_id"
		if sameString(property(scene, "kind"), "group") {
			action, field = "get_group_info", "group_id"
		}
		schedule(sceneKey, action, field, property(scene, "platformSceneId"), "scene")
	}
	if sender := property(message, "senderPlatformId"); truthy(sender) && needsSender {
		schedule(senderKey, "get_stranger_info", "user_id", sender, "sender")
	}
	result["jobs"] = jobs
	return result
}
func profileCached(state *jsjson.Object, key string) *jsjson.Object {
	cache, _ := property(state, "cache").([]any)
	for _, v := range cache {
		if entry, ok := v.(*jsjson.Object); ok && sameString(property(entry, "key"), key) {
			return entry
		}
	}
	return nil
}
func profileRemember(state *jsjson.Object, key string, name any, until float64) {
	cache, _ := property(state, "cache").([]any)
	next := []any{}
	for _, v := range cache {
		entry := v.(*jsjson.Object)
		if !sameString(property(entry, "key"), key) {
			next = append(next, entry)
		}
	}
	revision, _ := property(state, "next").(float64)
	revision++
	_ = state.Set("next", revision)
	entry := makeObject("key", key, "until", until, "revision", revision)
	if !isMissing(name) {
		_ = entry.Set("name", name)
	}
	next = append(next, entry)
	for len(next) > 500 {
		next = next[1:]
	}
	_ = state.Set("cache", next)
}
func profileComplete(input, state *jsjson.Object, now float64) any {
	job, err := object(property(input, "job"))
	if err != nil {
		return receiveFailure(err)
	}
	key := textProperty(job, "key")
	pending, _ := property(state, "pending").([]any)
	next := []any{}
	for _, v := range pending {
		if !sameString(v, key) {
			next = append(next, v)
		}
	}
	_ = state.Set("pending", next)
	result := httpJSON{"kind": "profiles", "state": state}
	if truthy(property(state, "disposed")) {
		return result
	}
	cached := profileCached(state, key)
	previous, hadPrevious := job.Get("previous")
	if hadPrevious {
		if cached == nil || property(cached, "revision") != previous {
			return result
		}
	} else if cached != nil {
		return result
	}
	if truthy(property(input, "failed")) {
		profileRemember(state, key, missing{}, now+300000)
		result["failure"] = true
		return result
	}
	scene, _ := object(property(job, "scene"))
	message, _ := object(property(job, "message"))
	data, _ := object(property(input, "value"))
	name := ""
	hasName := false
	if data != nil {
		field, nameField, expected := "user_id", "nickname", property(message, "senderPlatformId")
		if sameString(property(job, "kind"), "scene") {
			expected = property(scene, "platformSceneId")
			if sameString(property(scene, "kind"), "group") {
				field, nameField = "group_id", "group_name"
			}
		}
		value := property(data, field)
		if nullish(value) {
			value = ""
		}
		id, e := jsString(value)
		if e != nil {
			profileRemember(state, key, missing{}, now+300000)
			result["failure"] = true
			return result
		}
		if sameString(expected, id) {
			name, hasName, _ = profileidentity.DisplayName(property(data, nameField))
		}
	}
	if !hasName {
		profileRemember(state, key, missing{}, now+300000)
		return result
	}
	profileRemember(state, key, name, now+1800000)
	if sameString(property(job, "kind"), "scene") {
		updated := scene.Clone()
		_ = updated.Set("displayName", name)
		result["profile"] = makeObject("scene", updated)
	} else {
		sender := makeObject("id", property(message, "senderId"), "platformId", property(message, "senderPlatformId"), "displayName", name)
		if avatar, ok := profileidentity.QQAvatar(textProperty(message, "senderPlatformId"), false); ok {
			_ = sender.Set("avatarUrl", avatar)
		}
		result["profile"] = makeObject("scene", scene, "sender", sender)
	}
	return result
}
