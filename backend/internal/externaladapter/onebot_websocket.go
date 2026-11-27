package externaladapter

import (
	"context"
	"errors"
	"net/http"

	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/onebotwire"
)

// The socket qualifies echoes and connections; the guest interprets the
// response envelope for the selected OneBot action.
func (s *httpSession) oneBotSocket(token string, published <-chan struct{}) *onebotwire.WS {
	return onebotwire.NewWS(onebotwire.WSConfig{URL: s.profile.action, Token: token}, onebotwire.WSCallbacks{
		Login: func(ctx context.Context, response any) error {
			raw, err := jsjson.Marshal(response)
			if err != nil {
				return err
			}
			return s.validateOneBotLogin(ctx, raw)
		},
		Reply: func(_ context.Context, response *jsjson.Object, _, _ string) (any, error) {
			return response, nil
		},
		Receive: func(_ context.Context, raw string) error {
			select {
			case <-published:
			case <-s.life.Done():
				return s.life.Err()
			}
			if s.life.Err() != nil {
				return s.life.Err()
			}
			if s.receiveEvent(s.life, []byte(raw)) != http.StatusNoContent {
				return errors.New("external_adapter_event_failed")
			}
			return nil
		},
	})
}

func (s *httpSession) transmitSocket(ctx context.Context, scene dispatch.SceneRef, segment dispatch.Segment, action oneBotAction, before func() error) (dispatch.Receipt, error) {
	var response any
	var err error
	if len(action.Originals) == 0 {
		params, decode := guestObject(action.Params)
		if decode != nil {
			return dispatch.Receipt{}, decode
		}
		response, err = s.ws.ActionWithGuard(ctx, action.Action, params, before)
	} else {
		params, sources, originals, resolve := s.resolveOriginals(ctx, scene, segment, action)
		if resolve != nil {
			return dispatch.Receipt{}, resolve
		}
		response, err = s.ws.ActionPrepared(ctx, action.Action, params, onebotwire.Preparation{Encoder: s.encoder, Sources: sources}, func() error {
			if before != nil {
				if err := before(); err != nil {
					return err
				}
			}
			return s.qualifyOriginals(ctx, scene.ID, originals)
		})
	}
	witness := dispatch.Receipt{Status: dispatch.Unknown}
	if err != nil {
		var failure *onebotwire.Error
		if errors.As(err, &failure) && failure.Transmitted {
			return witness, nil
		}
		return dispatch.Receipt{}, err
	}
	raw, err := jsjson.Marshal(response)
	if err != nil {
		return witness, nil
	}
	return s.actionReceipt(ctx, scene, raw), nil
}
