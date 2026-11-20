package memeengine

import (
	"context"
	"database/sql"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

const remoteImageSceneQuery = `SELECT json_extract(value,'$.id'),json_extract(value,'$.kind'),
	json_extract(value,'$.adapterId'),json_extract(value,'$.accountId'),json_extract(value,'$.platformSceneId') FROM scenes WHERE id=?`

func (s *Service) readRemoteDisplayImage(ctx context.Context, sceneID, messageID string, part int) (*DisplayImage, error) {
	out := &DisplayImage{}
	err := s.authority.WithWriteSession(ctx, s.main, func(ws *recovery.WriteSession) error {
		var err error
		out.witness, err = ws.CaptureReadWitness(func() error {
			scene, err := remoteImageScene(ws, sceneID)
			if err != nil {
				return err
			}
			out.source = s.Runtime.RemoteDisplayImage(scene, messageID, part)
			if out.source == nil {
				return ErrDisplayImageUnavailable
			}
			return s.Sources.WithSession(ws).Read(ctx, func(v *sourceevents.View) error {
				eligible, err := v.UnversionedBrowserImage(sceneID, messageID)
				if err != nil {
					return err
				}
				if !eligible {
					return ErrDisplayImageUnavailable
				}
				return nil
			})
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	before := func(call context.Context) error {
		return s.authority.Publish(call, s.main, func(conn *sql.Conn) ([]byte, error) {
			return nil, out.VerifyPublication(call, conn)
		}, func([]byte) error { return nil })
	}
	if err := out.source.FetchRemote(ctx, before); err != nil {
		return nil, err
	}
	// The request proof itself never enters the runtime or durable store.
	if out.source.IsInline() {
		if err := before(ctx); err != nil {
			return nil, err
		}
		out.Bytes, err = out.source.InlineBytes(ctx)
	} else {
		out.Bytes, err = out.source.BrowserImageBytes(ctx, before)
	}
	if err != nil {
		return nil, err
	}
	out.MIME, err = s.Codec.ValidateDisplayImage(ctx, out.Bytes)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func remoteImageScene(ws *recovery.WriteSession, id string) (scene botruntime.Scene, err error) {
	row, err := ws.Query(remoteImageSceneQuery, id)
	if err != nil {
		return scene, err
	}
	if row.Next() {
		err = row.Scan(&scene.ID, &scene.Kind, &scene.AdapterID, &scene.AccountID, &scene.PlatformSceneID)
	} else {
		err = ErrDisplayImageUnavailable
	}
	return scene, errors.Join(err, row.Err(), row.Close())
}
