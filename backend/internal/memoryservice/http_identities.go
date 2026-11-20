package memoryservice

import (
	"context"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func (a *AdminRead) PublishIdentities(ctx context.Context, emit func([]byte) error) (bool, error) {
	return a.publishBody(ctx, func(r *assertions.Reader) ([]byte, error) {
		view, err := r.IdentityAdministration()
		if err != nil {
			return nil, err
		}
		return jsjson.Marshal(view)
	}, emit)
}
