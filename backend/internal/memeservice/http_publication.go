package memeservice

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"os"
	"reflect"
)

func (s *Service) OwnedBy(main *sql.DB, a *recovery.Authority) bool {
	return s != nil && s.main == main && s.authority == a
}

// PublishAsset rechecks the exact final asset before accepting immutable bytes.
// emit only enqueues; no socket/file/image/model work is allowed in this callback.
func (s *Service) PublishAsset(ctx context.Context, id string, expected *Object, body []byte, emit func([]byte) error) error {
	return s.publishAsset(ctx, id, expected, body, emit)
}
func (s *Service) publishAsset(ctx context.Context, id string, expected *Object, body []byte, emit func([]byte) error) error {
	return s.authority.Publish(ctx, s.main, func(c *sql.Conn) ([]byte, error) {
		var raw string
		e := c.QueryRowContext(ctx, "SELECT value FROM meme_assets WHERE id=?", id).Scan(&raw)
		if errors.Is(e, sql.ErrNoRows) {
			if expected == nil {
				return body, nil
			}
			return nil, errors.New("meme_publication_changed")
		}
		if e != nil {
			return nil, e
		}
		value, e := jsjson.Parse(raw)
		if e != nil {
			return nil, e
		}
		if !reflect.DeepEqual(value, expected) {
			return nil, errors.New("meme_publication_changed")
		}
		return body, nil
	}, emit)
}

type pageCaptureKey struct{}
type pageCapture struct{ witness *recovery.ReadWitness }

func (s *Service) PublishPage(ctx context.Context, o PageOptions, emit func([]byte) error) error {
	capture := &pageCapture{}
	page, e := s.AdminPage(context.WithValue(ctx, pageCaptureKey{}, capture), o)
	if e != nil {
		return e
	}
	items := make([]any, len(page.Items))
	for i, v := range page.Items {
		items[i] = v
	}
	body, e := jsjson.Stringify(fields("items", items, "total", float64(page.Total), "page", page.Page, "pageSize", page.PageSize))
	if e != nil {
		return e
	}
	return s.authority.Publish(ctx, s.main, func(c *sql.Conn) ([]byte, error) {
		if capture.witness == nil {
			return nil, errors.New("meme_publication_witness_required")
		}
		if e := capture.witness.Verify(ctx, c); e != nil {
			return nil, e
		}
		return []byte(body), nil
	}, emit)
}
func (s *Service) AdminFile(ctx context.Context, id string) (*Object, []byte, error) {
	asset, e := s.AdminGet(ctx, id)
	if e != nil || asset == nil {
		return asset, nil, e
	}
	path, e := s.file(id, str(get(asset, "format")))
	if e != nil {
		return nil, nil, e
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, nil, e
	}
	return asset, b, nil
}

// AssetProjection retains the complete committed source identity behind a
// sanitized response. It is captured inside Intake's final owning transaction.
type AssetProjection struct {
	scene     string
	available bool
	owner     *Service
	asset     *Object
	witness   *recovery.ReadWitness
}
type projectionCaptureKey struct{}

func (s *Service) CaptureProjection(ctx context.Context, scene string, run func(context.Context) (*Object, error)) (*Object, *AssetProjection, error) {
	projection := &AssetProjection{owner: s, scene: scene}
	value, err := run(context.WithValue(ctx, projectionCaptureKey{}, projection))
	if err != nil {
		return nil, nil, err
	}
	if value != nil && !projection.available {
		return nil, nil, errors.New("meme_not_available")
	}
	if value != nil && projection.asset == nil {
		return nil, nil, errors.New("meme_projection_not_captured")
	}
	return value, projection, nil
}
func (s *Service) captureProjection(ctx context.Context, scope *scope, asset *Object) error {
	projection, _ := ctx.Value(projectionCaptureKey{}).(*AssetProjection)
	if projection == nil || projection.owner != s {
		return nil
	}
	var available bool
	witness, err := scope.ws.CaptureReadWitness(func() error {
		var e error
		available, e = scope.visible(Context{SceneID: projection.scene}, asset, false)
		return e
	})
	if err != nil {
		return err
	}
	projection.asset = copyObject(asset)
	projection.witness = witness
	projection.available = available
	return nil
}
func (s *Service) copyProjection(ctx context.Context, from *AssetProjection) {
	if target, _ := ctx.Value(projectionCaptureKey{}).(*AssetProjection); target != nil && target.owner == s && from != nil && from.owner == s {
		target.asset = copyObject(from.asset)
		target.witness = from.witness
		target.available = from.available && target.scene == from.scene
	}
}
func (s *Service) PublishProjection(ctx context.Context, p *AssetProjection, body []byte, emit func([]byte) error) error {
	if p == nil || p.owner != s || p.asset == nil || !p.available {
		return errors.New("meme_projection_invalid")
	}
	return s.authority.Publish(ctx, s.main, func(c *sql.Conn) ([]byte, error) {
		if p.witness == nil {
			return nil, errors.New("meme_projection_witness_required")
		}
		if err := p.witness.Verify(ctx, c); err != nil {
			return nil, err
		}
		var raw string
		if err := c.QueryRowContext(ctx, "SELECT value FROM main.meme_assets WHERE id=?", str(get(p.asset, "id"))).Scan(&raw); err != nil {
			return nil, err
		}
		value, err := jsjson.Parse(raw)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(value, p.asset) {
			return nil, errors.New("meme_publication_changed")
		}
		return body, nil
	}, emit)
}
