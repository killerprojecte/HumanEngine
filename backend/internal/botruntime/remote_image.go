package botruntime

import (
	"context"
	"strings"
)

type remoteImage struct {
	messageID string
	part      int
}

func sameMediaScene(a, b Scene) bool {
	return a.ID == b.ID && a.Kind == b.Kind && a.AdapterID == b.AdapterID &&
		a.AccountID == b.AccountID && a.PlatformSceneID == b.PlatformSceneID
}

func (r *Runtime) currentRemoteImage(scene Scene, adapter *Adapter, id string) bool {
	current, _ := r.adapters.Load(scene.AdapterID)
	if current != adapter || !adapter.InlineImage.Current() {
		return false
	}
	if bound, ok := r.RegisteredScene(scene.ID); ok && !sameMediaScene(bound, scene) {
		return false
	}
	if state := r.state(scene.ID); state != nil {
		view := state.view.Load()
		cfg := r.config()
		if view.recalled[id] || (cfg.CacheEnabled || view.active != 0 || view.timers != 0) &&
			retainedRecord(view.history, id, false, r.services.Clock.Now()-r.lifetime(cfg)) != nil {
			return false
		}
	}
	return true
}

// RemoteDisplayImage prepares a request-local fallback. An existing retained
// row, even one without a readable image, always wins over platform history.
func (r *Runtime) RemoteDisplayImage(scene Scene, messageID string, part int) *DisplayImageSource {
	if uint(part) > 255 || (scene.Kind != "group" && scene.Kind != "private") {
		return nil
	}
	value, ok := r.adapters.Load(scene.AdapterID)
	if !ok {
		return nil
	}
	adapter := value.(*Adapter)
	if adapter.BrowserMessage == nil || adapter.BrowserImage == nil || adapter.InlineImage == nil || !r.currentRemoteImage(scene, adapter, messageID) {
		return nil
	}
	return &DisplayImageSource{runtime: r, scene: scene, adapter: adapter, remote: &remoteImage{messageID, part}}
}

func (m *DisplayImageSource) FetchRemote(ctx context.Context, before func(context.Context) error) (bool, error) {
	id, part := m.remote.messageID, m.remote.part
	out, err := trackedWithin(m.runtime, ctx, func(settle func(*Object, error)) {
		m.adapter.BrowserMessage(ctx, m.scene, id, before, settle)
	})
	if err != nil {
		return false, err
	}
	if !m.Current() {
		return false, ErrDisplayImageUnavailable
	}
	for _, raw := range array(get(out, "items")) {
		row, ok := raw.(*Object)
		if !ok || idOf(row) != id || get(row, "sceneId") != m.scene.ID || !remoteImageRow(row) {
			continue
		}
		parts := array(get(row, "parts"))
		if part < len(parts) {
			asset, class := remoteImagePart(m.adapter, m.scene, parts[part])
			if class != 0 && asset != m.assetID {
				m.assetID, m.inline = asset, class == NativeImageInline
				return true, nil
			}
		}
		break
	}
	return false, nil
}

func remoteImageRow(row *Object) bool {
	return get(row, "status") == "available" && get(row, "origin") == "history" && get(row, "sourceEventRef") == nil
}

func remoteImagePart(adapter *Adapter, scene Scene, part any) (string, NativeImageLink) {
	asset := imageAsset(part)
	if !strings.HasPrefix(asset, "ob:media:") {
		return "", 0
	}
	return asset, adapter.InlineImage.Classify(scene, asset)
}

func remoteImageLinks(adapter *Adapter, scene Scene, row *Object) DisplayImageLinks {
	if !remoteImageRow(row) {
		return DisplayImageLinks{}
	}
	var links DisplayImageLinks
	for index, part := range array(get(row, "parts")) {
		if index > 255 {
			break
		}
		_, class := remoteImagePart(adapter, scene, part)
		if class != 0 {
			if links.browser == nil {
				links.browser = new(inlineImageParts)
			}
			links.browser[index/64] |= 1 << (index % 64)
		}
	}
	return links
}
