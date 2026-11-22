package botruntime

import (
	"context"
	"math/bits"
	"strings"
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

// InlineObservationLearningEnabled reports the scene setting at acceptance.
func (m *DisplayImageSource) InlineObservationLearningEnabled() bool { return m.scene.LearningEnabled }

// Select visits only proven inline parts with adjacent platform metadata.
func (o NativeInlineObservation) Select(accept func(string) bool) []*DisplayImageSource {
	parts := array(get(o.record.body, "parts"))
	var selected []*DisplayImageSource
	for word, available := range o.record.displayMedia.native.parts {
		for available != 0 {
			index := word*64 + bits.TrailingZeros64(available)
			available &= available - 1
			if metadata := inlinePartMetadata(parts, index); metadata == "" || !accept(metadata) {
				continue
			}
			if source := o.runtime.captureInlineObservation(o.state, o.scene, o.record, index); source != nil {
				selected = append(selected, source)
			}
		}
	}
	return selected
}

// InlinePartMetadata reads only the exact retained part and its adjacent
// platform metadata. Projection or later rows are not consulted.
func (m *DisplayImageSource) InlinePartMetadata(index int) string {
	return inlinePartMetadata(array(get(m.record.body, "parts")), index)
}

func inlinePartMetadata(parts []any, index int) string {
	if index+1 >= len(parts) {
		return ""
	}
	meta := parts[index+1]
	if get(meta, "type") != "unsupported" {
		return ""
	}
	return str(get(meta, "description"))
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

type InlineStickerText struct {
	IsBot bool
	Text  string
}

// InlineStickerContext projects the bounded retained context ending at the
// exact sticker occurrence. New messages cannot replace the donor's context.
// The returned validator keeps the same projected sources tied to the prompt.
func (m *DisplayImageSource) InlineStickerContext(reads ReadAuthority) ([]InlineStickerText, func(ReadAuthority) (bool, error), error) {
	if get(m.record.body, "sourceEventRegistrationLimited") == true {
		return nil, nil, ErrDisplayImageUnavailable
	}
	v := m.runtime.trimPublished(m.state)
	var selected [8]*messageRecord
	at := len(selected)
	found := false
	for b := v.history; b != nil && at != 0; b = b.previous {
		for i := len(b.records) - 1; i >= 0 && at != 0; i-- {
			record := b.records[i]
			found = found || record == m.record
			if !found || v.recalled[idOf(record.body)] || get(record.body, "sceneId") != m.scene.ID ||
				get(record.body, "origin") != "live" || get(record.body, "status") != "available" {
				continue
			}
			at--
			selected[at] = record
		}
	}
	if selected[len(selected)-1] != m.record {
		return nil, nil, ErrDisplayImageUnavailable
	}
	records := selected[at:]
	rows := make([]*Object, len(records))
	for i, record := range records {
		rows[i] = record.body
	}
	valid := func(reads ReadAuthority) (bool, error) {
		v := m.state.view.Load()
		r := m.runtime
		cutoff := r.services.Clock.Now() - r.lifetime(r.config())
		for _, record := range records {
			if record.retention.discarded.Load() || record.receivedAt < cutoff || v.recalled[idOf(record.body)] {
				return false, nil
			}
		}
		return reads.ValidateSources(m.scene, rows, nil)
	}
	if ok, err := valid(reads); err != nil || !ok {
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, ErrDisplayImageUnavailable
	}
	text := make([]InlineStickerText, 0, len(rows))
	for _, row := range rows {
		var parts []string
		for _, part := range array(get(row, "parts")) {
			if get(part, "type") == "text" {
				parts = append(parts, str(get(part, "text")))
			}
		}
		text = append(text, InlineStickerText{get(row, "senderId") == "bot", strings.Join(parts, " ")})
	}
	return text, valid, nil
}
