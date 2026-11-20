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

var ErrDisplayImageUnavailable = errors.New("image_unavailable")

type ManagedDisplayImage struct {
	Bytes    []byte
	MIME     string
	source   *botruntime.ManagedImageSource
	witness  *recovery.ReadWitness
	original memeservice.Original
	owner    *memeservice.Service
}

// ReadManagedDisplayImage reads the one verified original for a retained bot
// message. Only the brief qualification snapshot owns a recovery session.
func (s *Service) ReadManagedDisplayImage(ctx context.Context, sceneID, messageID string, part int) (*ManagedDisplayImage, error) {
	source := s.Runtime.ManagedImage(sceneID, messageID, part)
	if source == nil {
		return nil, ErrDisplayImageUnavailable
	}
	out := &ManagedDisplayImage{source: source, owner: s.Memes}
	err := s.authority.WithWriteSession(ctx, s.main, func(ws *recovery.WriteSession) error {
		var err error
		out.witness, err = ws.CaptureReadWitness(func() error {
			// A scene configuration commit and its runtime effect are separate.
			// Never pair a retained row from one transport with a different owner.
			kind, adapter, account, platform := source.SceneIdentity()
			rows, err := ws.Query(`SELECT 1 FROM scenes WHERE id=? AND
				json_extract(value,'$.kind')=? AND json_extract(value,'$.adapterId')=? AND
				json_extract(value,'$.accountId')=? AND json_extract(value,'$.platformSceneId')=?`,
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
			if err != nil {
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

	file, err := os.Open(out.original.Path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	const MaxImageBytes = 64 * 1024 * 1024
	out.Bytes, err = io.ReadAll(io.LimitReader(file, MaxImageBytes+1))
	if err != nil {
		return nil, err
	}
	out.MIME, err = s.Codec.ValidateDisplayImage(ctx, out.Bytes)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(out.Bytes)
	if float64(len(out.Bytes)) != out.original.Bytes || hex.EncodeToString(hash[:]) != out.original.Hash {
		return nil, errors.New("media_content_hash_mismatch")
	}
	return out, nil
}

// VerifyPublication runs before the owned byte response is accepted. All file
// access and image validation have already finished outside the authority fence.
func (m *ManagedDisplayImage) VerifyPublication(ctx context.Context, conn *sql.Conn) error {
	if err := m.witness.Verify(ctx, conn); err != nil {
		if errors.Is(err, recovery.ErrReadWitnessChanged) {
			return ErrDisplayImageUnavailable
		}
		return err
	}
	if !m.source.Current() || !m.owner.OriginalCurrent(m.source.AssetID(), m.original) {
		return ErrDisplayImageUnavailable
	}
	return nil
}
