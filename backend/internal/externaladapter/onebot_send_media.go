package externaladapter

import (
	"context"
	"errors"
	"strings"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/outboundmedia"
)

type resolvedOriginal struct {
	id   string
	file memeservice.Original
}

var errSendOriginal = errors.New("external_adapter_original_invalid")

// The guest supplies only the segment indexes. The dispatcher's approved parts
// supply asset IDs, and the host alone resolves their scoped original bytes.
func (s *httpSession) prepareOriginals(ctx context.Context, scene dispatch.SceneRef, segment dispatch.Segment, action oneBotAction) (*outboundmedia.Lease, []resolvedOriginal, error) {
	expected, peerField := "send_group_msg", "group_id"
	if scene.Kind == "private" {
		expected, peerField = "send_private_msg", "user_id"
	}
	if action.Action != expected {
		return nil, nil, errSendOriginal
	}
	params, err := guestObject(action.Params)
	if err != nil || text(params, peerField) != scene.PlatformSceneID {
		return nil, nil, errSendOriginal
	}
	message, _ := params.Get("message")
	rows, _ := message.([]any)
	originals := make([]resolvedOriginal, 0, len(action.Originals))
	sources := make([]outboundmedia.Source, 0, len(action.Originals))
	for _, mapping := range action.Originals {
		if uint(mapping.Part) >= uint(len(segment.Parts)) || uint(mapping.Index) >= uint(len(rows)) {
			return nil, nil, errSendOriginal
		}
		destination, _ := rows[mapping.Index].(*jsjson.Object)
		if destination == nil || text(destination, "type") != "image" {
			return nil, nil, errSendOriginal
		}
		part, err := guestObject(segment.Parts[mapping.Part])
		if err != nil || text(part, "type") != "media" {
			return nil, nil, errSendOriginal
		}
		if mime := text(part, "mediaType"); mime != "image" && !strings.HasPrefix(mime, "image/") {
			return nil, nil, errSendOriginal
		}
		id := text(part, "assetId")
		if !botruntime.IsManagedImageAssetID(id) {
			return nil, nil, errSendOriginal
		}
		file, err := s.source.original(ctx, scene.ID, id)
		if err != nil {
			return nil, nil, errSendOriginal
		}
		originals = append(originals, resolvedOriginal{id, file})
		sources = append(sources, outboundmedia.Source{Index: mapping.Index, Path: file.Path, Bytes: int64(file.Bytes), Hash: file.Hash})
	}
	wire, err := s.encoder.Encode(ctx, params, sources)
	return wire, originals, err
}

func (s *httpSession) qualifyOriginals(ctx context.Context, scene string, originals []resolvedOriginal) error {
	for _, original := range originals {
		now, err := s.source.original(ctx, scene, original.id)
		if err != nil || now != original.file {
			return errSendOriginal
		}
	}
	return nil
}
