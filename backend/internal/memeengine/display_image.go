package memeengine

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

const displayImageSceneQuery = `SELECT 1 FROM scenes WHERE id=? AND
				json_extract(value,'$.kind')=? AND json_extract(value,'$.adapterId')=? AND
				json_extract(value,'$.accountId')=? AND json_extract(value,'$.platformSceneId')=?`

var ErrDisplayImageUnavailable = botruntime.ErrDisplayImageUnavailable

type DisplayImage struct {
	Bytes    []byte
	MIME     string
	source   *botruntime.DisplayImageSource
	witness  *recovery.ReadWitness
	original memeservice.Original
	owner    *memeservice.Service
}

// ReadDisplayImage reads either a bound managed original or native inline
// ingress bytes. The qualification session does not cover decoding or disk IO.
func (s *Service) ReadDisplayImage(ctx context.Context, sceneID, messageID string, part int) (*DisplayImage, error) {
	source := s.Runtime.DisplayImage(sceneID, messageID, part)
	if source == nil {
		return nil, ErrDisplayImageUnavailable
	}
	out := &DisplayImage{source: source, owner: s.Memes}
	managed := source.IsManaged()
	err := s.authority.WithWriteSession(ctx, s.main, func(ws *recovery.WriteSession) error {
		var err error
		out.witness, err = ws.CaptureReadWitness(func() error {
			// A scene configuration commit and its runtime effect are separate.
			// Never pair a retained row from one transport with a different owner.
			kind, adapter, account, platform := source.SceneIdentity()
			rows, err := ws.Query(displayImageSceneQuery,
				sceneID, kind, adapter, account, platform)
			if err != nil {
				return err
			}
			exact := rows.Next()
			if err := errors.Join(rows.Err(), rows.Close()); err != nil {
				return err
			}
			if !exact {
				return ErrDisplayImageUnavailable
			}
			err = s.Sources.WithSession(ws).WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
				current, e := source.Validate(reads)
				if e != nil {
					return e
				}
				if !current {
					return ErrDisplayImageUnavailable
				}
				return nil
			})
			if err != nil || !managed {
				return err
			}
			out.original, err = s.Memes.OriginalWithSession(ctx, ws, memeservice.Context{SceneID: sceneID}, source.AssetID())
			if errors.Is(err, memeservice.ErrOriginalUnavailable) {
				return ErrDisplayImageUnavailable
			}
			return err
		})
		return err
	})
	if err != nil {
		return nil, err
	}

	if managed {
		out.Bytes, err = readManagedImage(out.original.Path)
	} else if source.IsInline() {
		out.Bytes, err = source.InlineBytes(ctx)
	} else {
		out.Bytes, err = source.BrowserImageBytes(ctx, func(call context.Context) error {
			return s.authority.Publish(call, s.main, func(conn *sql.Conn) ([]byte, error) {
				return nil, out.VerifyPublication(call, conn)
			}, func([]byte) error { return nil })
		})
	}
	if err != nil {
		return nil, err
	}
	// The original pixels determine the response MIME, not a data URL label.
	out.MIME, err = s.Codec.ValidateDisplayImage(ctx, out.Bytes)
	if err != nil {
		return nil, err
	}
	if managed {
		hash := sha256.Sum256(out.Bytes)
		if float64(len(out.Bytes)) != out.original.Bytes || hex.EncodeToString(hash[:]) != out.original.Hash {
			return nil, errors.New("media_content_hash_mismatch")
		}
	}
	return out, nil
}

func readManagedImage(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	const max = 64 * 1024 * 1024
	return io.ReadAll(io.LimitReader(file, max+1))
}

// VerifyPublication runs before the owned byte response is accepted. All file
// access and image validation have already finished outside the authority fence.
func (m *DisplayImage) VerifyPublication(ctx context.Context, conn *sql.Conn) error {
	if err := m.witness.Verify(ctx, conn); err != nil {
		if errors.Is(err, recovery.ErrReadWitnessChanged) {
			return ErrDisplayImageUnavailable
		}
		return err
	}
	if !m.source.Current() || m.source.IsManaged() && !m.owner.OriginalCurrent(m.source.AssetID(), m.original) {
		return ErrDisplayImageUnavailable
	}
	return nil
}
