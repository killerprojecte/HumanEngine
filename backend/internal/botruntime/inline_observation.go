package botruntime

import (
	"context"
	"math/bits"
)

type NativeInlineObservation struct {
	runtime *Runtime
	state   *sceneState
	scene   Scene
	record  *messageRecord
}

func (o NativeInlineObservation) Identity() (sceneID, messageID string) {
	return o.scene.ID, idOf(o.record.body)
}

func (m *DisplayImageSource) InlineObservationIdentity() (sceneID, messageID, senderID string) {
	return m.scene.ID, idOf(m.record.body), str(get(m.record.body, "senderId"))
}

// Select visits only proven inline parts with adjacent platform metadata.
func (o NativeInlineObservation) Select(accept func(string) bool) []*DisplayImageSource {
	parts := array(get(o.record.body, "parts"))
	var selected []*DisplayImageSource
	for word, available := range o.record.displayMedia.native.parts {
		for available != 0 {
			index := word*64 + bits.TrailingZeros64(available)
			available &= available - 1
			if index+1 == len(parts) {
				continue
			}
			meta := parts[index+1]
			if get(meta, "type") != "unsupported" || !accept(str(get(meta, "description"))) {
				continue
			}
			if source := o.runtime.captureInlineObservation(o.state, o.scene, o.record, index); source != nil {
				selected = append(selected, source)
			}
		}
	}
	return selected
}

// captureInlineObservation binds an exact already-accepted record and its
// original native adapter generation. The caller supplies the record from
// ingress, not a subsequent lookup by message ID.
func (r *Runtime) captureInlineObservation(s *sceneState, scene Scene, record *messageRecord, index int) *DisplayImageSource {
	proof := record.displayMedia
	if proof == nil || proof.native == nil || !proof.native.parts.at(index) ||
		get(record.body, "origin") != "live" || get(record.body, "senderId") == "bot" ||
		get(record.body, "status") != "available" || !proof.matches(scene) {
		return nil
	}
	value, ok := r.adapters.Load(scene.AdapterID)
	if !ok {
		return nil
	}
	adapter := value.(*Adapter)
	if adapter.InlineImage != proof.native.generation {
		return nil
	}
	source := &DisplayImageSource{
		runtime: r, state: s, scene: scene, adapter: adapter, record: record,
		inline: true, assetID: imageAsset(array(get(record.body, "parts"))[index]),
	}
	if !source.Current() {
		return nil
	}
	return source
}

// ReadInlineObservation qualifies the exact source before and after byte
// acquisition. No scene mutex or authority read is retained during the read.
func (m *DisplayImageSource) ReadInlineObservation(ctx context.Context) ([]byte, error) {
	if !m.Current() {
		return nil, ErrDisplayImageUnavailable
	}
	bytes, err := m.InlineBytes(ctx)
	if err != nil {
		return nil, err
	}
	if !m.Current() {
		return nil, ErrDisplayImageUnavailable
	}
	return bytes, nil
}
