package learningowner

import (
	"context"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
)

type IndexAction struct {
	Action                  string
	ExpectedJobID           *string
	ExpectedEncoderRevision string
	ConfirmUnknownRequest   bool
}

type IndexAdminAction func(context.Context, IndexAction) error

func (o *Owner) BindIndexAdminAction(builder func(context.Context, *Object, map[string]any) (*providertransport.NativeModels, error)) (IndexAdminAction, error) {
	if o == nil || builder == nil {
		return nil, errors.New("embedding_index_action_owner_invalid")
	}
	pinnedRouter := func(ctx context.Context, pinned *Object) (*providertransport.NativeModels, error) {
		settings, err := o.settings(ctx)
		if err != nil {
			return nil, err
		}
		return builder(ctx, pinned, settings)
	}
	return func(ctx context.Context, input IndexAction) error {
		switch input.Action {
		case "start", "resume":
			job, err := o.dispatchIndexWork(ctx, input.ExpectedJobID, input.ExpectedEncoderRevision, input.Action == "resume", input.ConfirmUnknownRequest, pinnedRouter)
			if err != nil {
				return err
			}
			return o.awaitInitialIndexWork(ctx, job)
		case "pause", "cancel":
			_, err := o.stopIndexWork(ctx, input.ExpectedJobID, input.Action == "cancel")
			return err
		default:
			return errors.New("embedding_index_action_invalid")
		}
	}, nil
}
