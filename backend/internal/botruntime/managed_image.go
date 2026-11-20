package botruntime

import (
	"regexp"
	"strings"
)

var managedImageID = regexp.MustCompile("^[0-9a-f]{64}$")

type managedMediaScene struct {
	kind, adapter, account, platform string
}

func (m *managedMediaScene) matches(scene Scene) bool {
	return m.kind == scene.Kind && m.adapter == scene.AdapterID &&
		m.account == scene.AccountID && m.platform == scene.PlatformSceneID
}

func managedAsset(part any) string {
	if get(part, "type") != "media" {
		return ""
	}
	mime, _ := get(part, "mediaType").(string)
	if mime != "image" && !(len(mime) >= len("image/") && strings.EqualFold(mime[:len("image/")], "image/")) {
		return ""
	}
	asset, _ := get(part, "assetId").(string)
	if !managedImageID.MatchString(asset) {
		return ""
	}
	return asset
}

func bindManagedReceipt(scene Scene, message *Object) *managedMediaScene {
	for _, part := range array(get(message, "parts")) {
		if managedAsset(part) != "" {
			return &managedMediaScene{scene.Kind, scene.AdapterID, scene.AccountID, scene.PlatformSceneID}
		}
	}
	return nil
}

// ManagedImageSource captures a private retained bot message and its adapter
// registration. Its raw parts are never returned to an HTTP response.
type ManagedImageSource struct {
	runtime *Runtime
	state   *sceneState
	scene   Scene
	adapter *Adapter
	record  *messageRecord
	assetID string
}

func (r *Runtime) ManagedImage(sceneID, messageID string, partIndex int) *ManagedImageSource {
	s := r.state(sceneID)
	if s == nil || partIndex > 255 {
		return nil
	}
	v := r.trimPublished(s)
	if v.scene.Kind == "simulation" || v.recalled[messageID] {
		return nil
	}
	adapter, ok := r.adapters.Load(v.scene.AdapterID)
	if !ok {
		return nil
	}
	record := retainedRecord(v.history, messageID, true, 0)
	if record == nil || record.managedMedia == nil || !record.managedMedia.matches(v.scene) {
		return nil
	}
	row := record.body
	if get(row, "sceneId") != sceneID || get(row, "senderId") != "bot" ||
		get(row, "status") != "available" || get(row, "origin") == "simulation" {
		return nil
	}
	parts := array(get(row, "parts"))
	if partIndex < 0 || partIndex >= len(parts) {
		return nil
	}
	asset := managedAsset(parts[partIndex])
	if asset == "" {
		return nil
	}
	return &ManagedImageSource{r, s, v.scene, adapter.(*Adapter), record, asset}
}

func (m *ManagedImageSource) AssetID() string { return m.assetID }

// SceneIdentity is the transport identity captured with the retained body,
// without mutable display fields.
func (m *ManagedImageSource) SceneIdentity() (kind, adapter, account, platform string) {
	return m.scene.Kind, m.scene.AdapterID, m.scene.AccountID, m.scene.PlatformSceneID
}

func (m *ManagedImageSource) Validate(reads ReadAuthority) (bool, error) {
	return reads.ValidateSources(m.scene, []*Object{m.record.body}, nil)
}

// Current checks private runtime facts at final publication, without acquiring
// a scene lock or opening a second authority transaction.
func (m *ManagedImageSource) Current() bool {
	r := m.runtime
	v := m.state.view.Load()
	if r.state(m.scene.ID) != m.state ||
		!m.record.managedMedia.matches(v.scene) ||
		v.recalled[idOf(m.record.body)] {
		return false
	}
	adapter, _ := r.adapters.Load(m.scene.AdapterID)
	if adapter != m.adapter {
		return false
	}
	cfg := r.config()
	if !cfg.CacheEnabled && v.active == 0 && v.timers == 0 {
		return false
	}
	return retainedRecord(v.history, idOf(m.record.body), false, r.services.Clock.Now()-r.lifetime(cfg)) == m.record
}
