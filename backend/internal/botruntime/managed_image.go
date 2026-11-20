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
	native                           *nativeImageBinding
}

type inlineImageParts [4]uint64

type nativeImageBinding struct {
	generation *InlineImageAdapter
	parts      inlineImageParts
}

// DisplayImageLinks is the private link proof for one selected browser row.
type DisplayImageLinks struct {
	Managed bool
	inline  *inlineImageParts
}

func (l DisplayImageLinks) InlineAt(index int) bool {
	return l.inline != nil && uint(index) <= 255 && l.inline[index/64]&(1<<(index%64)) != 0
}

type browserImageLinker struct {
	runtime *Runtime
	scene   Scene
	current *InlineImageAdapter
	checked bool
}

func (l *browserImageLinker) links(proof *displayMediaScene) DisplayImageLinks {
	if proof == nil || !proof.matches(l.scene) {
		return DisplayImageLinks{}
	}
	if proof.native == nil {
		return DisplayImageLinks{Managed: true}
	}
	if proof.native.parts == (inlineImageParts{}) {
		return DisplayImageLinks{}
	}
	if !l.checked {
		l.checked = true
		if current, ok := l.runtime.adapters.Load(l.scene.AdapterID); ok {
			generation := current.(*Adapter).InlineImage
			if generation != nil && generation.Current() {
				l.current = generation
			}
		}
	}
	if l.current == proof.native.generation {
		return DisplayImageLinks{inline: &proof.native.parts}
	}
	return DisplayImageLinks{}
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
	var native *nativeImageBinding
	for i, part := range array(get(message, "parts")) {
		asset := imageAsset(part)
		if !strings.HasPrefix(asset, "ob:media:") {
			continue
		}
		if native == nil {
			native = &nativeImageBinding{generation: owner.InlineImage}
		}
		if i <= 255 && owner.InlineImage.Links(scene, asset) {
			native.parts[i/64] |= 1 << (i % 64)
		}
	}
	if native == nil {
		return nil
	}
	return &displayMediaScene{scene.Kind, scene.AdapterID, scene.AccountID, scene.PlatformSceneID, native}
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

func (r *Runtime) retainedDisplayImageRow(sceneID, messageID string) DisplayImageSource {
	s := r.state(sceneID)
	if s == nil {
		return DisplayImageSource{}
	}
	v := r.trimPublished(s)
	if v.scene.Kind == "simulation" || v.recalled[messageID] {
		return DisplayImageSource{}
	}
	value, ok := r.adapters.Load(v.scene.AdapterID)
	if !ok {
		return DisplayImageSource{}
	}
	adapter := value.(*Adapter)
	record := retainedRecord(v.history, messageID, true, 0)
	if record == nil || record.displayMedia == nil || !record.displayMedia.matches(v.scene) ||
		get(record.body, "sceneId") != sceneID || get(record.body, "status") != "available" {
		return DisplayImageSource{}
	}
	if origin := record.displayMedia.native; origin != nil &&
		(origin.generation != adapter.InlineImage || !origin.generation.Current()) {
		return DisplayImageSource{}
	}
	return DisplayImageSource{runtime: r, state: s, scene: v.scene, adapter: adapter, record: record}
}

func (r *Runtime) DisplayImage(sceneID, messageID string, partIndex int) *DisplayImageSource {
	if uint(partIndex) > 255 {
		return nil
	}
	source := r.retainedDisplayImageRow(sceneID, messageID)
	if source.record == nil {
		return nil
	}
	parts := array(get(source.record.body, "parts"))
	if partIndex >= len(parts) {
		return nil
	}
	asset := imageAsset(parts[partIndex])
	if source.IsManaged() {
		if !IsManagedImageAssetID(asset) {
			return nil
		}
	} else if !strings.HasPrefix(asset, "ob:media:") {
		return nil
	}
	source.assetID = asset
	return &source
}

// InlineVisionSource uses one retained lookup only for a live row with a native
// image locator. Its private bitset chooses the exact indices, not raw URLs.
func (c *TurnContext) InlineVisionSource(message *Object) (*DisplayImageSource, int) {
	sceneID := c.Scene.ID
	if get(message, "sceneId") != sceneID || get(message, "origin") != "live" ||
		get(message, "senderId") == "bot" || get(message, "status") != "available" {
		return nil, 0
	}
	parts := array(get(message, "parts"))
	for first := 0; first < min(len(parts), 256); first++ {
		if !strings.HasPrefix(imageAsset(parts[first]), "ob:media:") {
			continue
		}
		id := idOf(message)
		if !c.hasRecentSource(id) {
			return nil, 0
		}
		source := c.owner.runtime.retainedDisplayImageRow(sceneID, id)
		if source.record == nil || source.IsManaged() || get(source.record.body, "origin") != "live" ||
			get(source.record.body, "senderId") == "bot" {
			return nil, 0
		}
		mask := DisplayImageLinks{inline: &source.record.displayMedia.native.parts}
		retained := array(get(source.record.body, "parts"))
		for i := first; i < min(len(parts), len(retained), 256); i++ {
			if mask.InlineAt(i) {
				asset := imageAsset(retained[i])
				if asset == imageAsset(parts[i]) {
					source.assetID = asset
					return &source, i
				}
			}
		}
		break
	}
	return nil, 0
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
