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
	found, err := out.source.FetchRemote(ctx, before)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrDisplayImageUnavailable
	}
	out.Bytes, err = readRemoteImageBytes(ctx, out.source, before)
	if errors.Is(err, botruntime.ErrBrowserImageLocatorExpired) && ctx.Err() == nil {
		// Only this request-local, unversioned exact-message proof can switch
		// to a different locator. Both calls retain the original authority.
		replaced, refreshErr := out.source.FetchRemote(ctx, before)
		if refreshErr != nil {
			return nil, refreshErr
		}
		if replaced {
			out.Bytes, err = readRemoteImageBytes(ctx, out.source, before)
		}
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

func readRemoteImageBytes(ctx context.Context, source *botruntime.DisplayImageSource, before func(context.Context) error) ([]byte, error) {
	if source.IsInline() {
		if err := before(ctx); err != nil {
			return nil, err
		}
		return source.InlineBytes(ctx)
	}
	return source.BrowserImageBytes(ctx, before)
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
