package botruntime

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

var ErrDisplayImageUnavailable = errors.New("image_unavailable")

var managedImageID = regexp.MustCompile("^[0-9a-f]{64}$")

// IsManagedImageAssetID recognizes the local asset namespace, not permission
// to read an asset. A real retained dispatch proof is also required.
func IsManagedImageAssetID(asset string) bool { return managedImageID.MatchString(asset) }

type displayMediaScene struct {
	kind, adapter, account, platform string
	native                           *InlineImageAdapter
}

func (m *displayMediaScene) matches(scene Scene) bool {
	return m.kind == scene.Kind && m.adapter == scene.AdapterID &&
		m.account == scene.AccountID && m.platform == scene.PlatformSceneID
}

func imageAsset(part any) string {
	if get(part, "type") != "media" {
		return ""
	}
	mime, _ := get(part, "mediaType").(string)
	if mime != "image" && !(len(mime) >= len("image/") && strings.EqualFold(mime[:len("image/")], "image/")) {
		return ""
	}
	asset, _ := get(part, "assetId").(string)
	return asset
}

func managedAsset(part any) string {
	asset := imageAsset(part)
	if !IsManagedImageAssetID(asset) {
		return ""
	}
	return asset
}

func bindManagedReceipt(scene Scene, message *Object) *displayMediaScene {
	for _, part := range array(get(message, "parts")) {
		if managedAsset(part) != "" {
			return &displayMediaScene{kind: scene.Kind, adapter: scene.AdapterID, account: scene.AccountID, platform: scene.PlatformSceneID}
		}
	}
	return nil
}

func (r *Runtime) bindNativeImage(scene Scene, message *Object, owner *Adapter) *displayMediaScene {
	if owner == nil || owner.InlineImage == nil || scene.Kind == "simulation" {
		return nil
	}
	current, _ := r.adapters.Load(scene.AdapterID)
	if current != owner {
		return nil
	}
	for _, part := range array(get(message, "parts")) {
		if strings.HasPrefix(imageAsset(part), "ob:media:") {
			return &displayMediaScene{scene.Kind, scene.AdapterID, scene.AccountID, scene.PlatformSceneID, owner.InlineImage}
		}
	}
	return nil
}

// DisplayImageSource captures a private retained record and its adapter.
// Its raw parts are never returned by a browser preview.
type DisplayImageSource struct {
	runtime *Runtime
	state   *sceneState
	scene   Scene
	adapter *Adapter
	record  *messageRecord
	assetID string
}

func (r *Runtime) DisplayImage(sceneID, messageID string, partIndex int) *DisplayImageSource {
	s := r.state(sceneID)
	if s == nil || partIndex < 0 || partIndex > 255 {
		return nil
	}
	v := r.trimPublished(s)
	if v.scene.Kind == "simulation" || v.recalled[messageID] {
		return nil
	}
	value, ok := r.adapters.Load(v.scene.AdapterID)
	if !ok {
		return nil
	}
	adapter := value.(*Adapter)
	record := retainedRecord(v.history, messageID, true, 0)
	if record == nil || record.displayMedia == nil || !record.displayMedia.matches(v.scene) {
		return nil
	}
	row := record.body
	if get(row, "sceneId") != sceneID || get(row, "status") != "available" {
		return nil
	}
	parts := array(get(row, "parts"))
	if partIndex >= len(parts) {
		return nil
	}
	asset := imageAsset(parts[partIndex])
	if owner := record.displayMedia.native; owner != nil {
		if owner != adapter.InlineImage || !owner.Current() || !strings.HasPrefix(asset, "ob:media:") {
			return nil
		}
	} else if !IsManagedImageAssetID(asset) {
		return nil
	}
	return &DisplayImageSource{r, s, v.scene, adapter, record, asset}
}

func (m *DisplayImageSource) IsManaged() bool { return m.record.displayMedia.native == nil }

func (m *DisplayImageSource) InlineBytes(ctx context.Context) ([]byte, error) {
	return m.adapter.InlineImage.Read(ctx, m.scene, m.assetID)
}

func (m *DisplayImageSource) AssetID() string { return m.assetID }

// SceneIdentity is the transport identity captured with the retained body,
// without mutable display fields.
func (m *DisplayImageSource) SceneIdentity() (kind, adapter, account, platform string) {
	return m.scene.Kind, m.scene.AdapterID, m.scene.AccountID, m.scene.PlatformSceneID
}

func (m *DisplayImageSource) Validate(reads ReadAuthority) (bool, error) {
	return reads.ValidateSources(m.scene, []*Object{m.record.body}, nil)
}

// Current checks private runtime facts at final publication, without acquiring
// a scene lock or opening a second authority transaction.
func (m *DisplayImageSource) Current() bool {
	r := m.runtime
	v := m.state.view.Load()
	if r.state(m.scene.ID) != m.state ||
		!m.record.displayMedia.matches(v.scene) ||
		v.recalled[idOf(m.record.body)] {
		return false
	}
	adapter, _ := r.adapters.Load(m.scene.AdapterID)
	if adapter != m.adapter || !m.IsManaged() && !m.adapter.InlineImage.Current() {
		return false
	}
	cfg := r.config()
	if !cfg.CacheEnabled && v.active == 0 && v.timers == 0 {
		return false
	}
	return retainedRecord(v.history, idOf(m.record.body), false, r.services.Clock.Now()-r.lifetime(cfg)) == m.record
}
