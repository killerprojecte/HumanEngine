package sourceevents

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// RuntimeSourceBodyHash hashes exactly JSON.stringify([sentAt, replyToId ?? null,
// parts]); it never normalizes property order or UTF-16 string identity.
func RuntimeSourceBodyHash(message *jsjson.Object) (string, error) {
	raw, err := jsjson.Marshal([]any{get(message, "sentAt"), get(message, "replyToId"), get(message, "parts")})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func RuntimeSourceIdentity(scene botruntime.Scene, message *jsjson.Object) (Identity, error) {
	if str(message, "sceneId") != scene.ID {
		return Identity{}, errors.New("source_event_scope_mismatch")
	}
	return Identity{scene.ID, scene.AdapterID, scene.AccountID, str(message, "id"), str(message, "senderId")}, nil
}
func RuntimeSourceMediaReference(reference string, ref *Ref) (string, error) {
	if ref == nil {
		return reference, nil
	}
	return jsjson.Stringify([]any{reference, ref.EventID, float64(ref.Revision), float64(ref.DeletionEpoch)})
}
func sceneFromValue(o *jsjson.Object) botruntime.Scene {
	var scene botruntime.Scene
	scene.ID, scene.AdapterID, scene.AccountID = str(o, "id"), str(o, "adapterId"), str(o, "accountId")
	scene.Kind, scene.PlatformSceneID = str(o, "kind"), str(o, "platformSceneId")
	return scene
}
func oneBotScene(o *jsjson.Object) bool {
	raw, err := jsjson.Marshal([]any{get(o, "adapterId"), get(o, "accountId"), get(o, "kind"), get(o, "platformSceneId")})
	return err == nil && str(o, "id") == "ob:s:"+base64.RawURLEncoding.EncodeToString(raw)
}

var qqID = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

func qqAvatar(value any) (any, error) {
	if !runtimeTruthy(value) {
		return nil, nil
	}
	id, err := runtimeString(value)
	if err != nil {
		return nil, err
	}
	if !qqID.MatchString(id) {
		return nil, nil
	}
	return "https://q1.qlogo.cn/g?b=qq&nk=" + id + "&s=100", nil
}
func runtimeString(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case *jsjson.Object:
		if _, ok := v.Get("toString"); ok {
			return "", errors.New("Cannot convert object to primitive value")
		}
		return "[object Object]", nil
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			if item != nil {
				s, err := runtimeString(item)
				if err != nil {
					return "", err
				}
				parts[i] = s
			}
		}
		return strings.Join(parts, ","), nil
	default:
		return jsjson.Stringify(value)
	}
}
func coalescedIdentity(message *jsjson.Object, identity any, key string) (any, bool, error) {
	if value, present := message.Get(key); present && value != nil {
		return value, true, nil
	}
	if identity == nil {
		return nil, false, fmt.Errorf("Cannot read properties of null (reading '%s')", key)
	}
	if object, ok := identity.(*jsjson.Object); ok {
		value, present := object.Get(key)
		return value, present, nil
	}
	return nil, false, nil
}
func optionalProperty(o *jsjson.Object, key string, value any, present bool) {
	if !present {
		o.Delete(key)
	} else {
		_ = o.Set(key, value)
	}
}
func cloneRuntimeJSON(value any) any {
	switch v := value.(type) {
	case *jsjson.Object:
		out := new(jsjson.Object)
		for _, m := range v.Members() {
			_ = out.Set(m.Key, cloneRuntimeJSON(m.Value))
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = cloneRuntimeJSON(x)
		}
		return out
	default:
		return v
	}
}
func runtimeTruthy(value any) bool {
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
