package memoryservice

import (
	"context"
	"errors"
	"net/http"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/identityprivacy"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
)

var errIdentityScene = errors.New("identity_scene_unavailable")

func (a *AdminRead) ChangeIdentity(ctx context.Context, q *identityprivacy.Command, emit func(int, []byte) error) (bool, error) {
	status := http.StatusOK
	var result *Object
	if q == nil {
		status, result = http.StatusBadRequest, personObject("error", "Invalid request")
	} else {
		var ack identityprivacy.Acknowledgement
		err := a.memory.d.Assertions.Transaction(ctx, func(s *assertions.Store) error {
			var adapterID, accountID string
			if sceneID := q.SceneID(); sceneID != "" {
				if err := s.Read(ctx, func(r *assertions.Reader) error {
					scene, err := r.Scene(sceneID)
					if err != nil {
						return err
					}
					switch text(scene, "kind") {
					case "group", "private":
						adapterID, accountID = text(scene, "adapterId"), text(scene, "accountId")
						return nil
					default:
						return errIdentityScene
					}
				}); err != nil {
					return err
				}
			}
			return s.WithWriteSession(func(session *recovery.WriteSession) (err error) {
				ack, err = identityprivacy.RunAdminCommand(session, q, adapterID, accountID)
				return err
			})
		})
		switch {
		case errors.Is(err, errIdentityScene):
			status, result = http.StatusNotFound, personObject("error", errIdentityScene.Error())
		case errors.Is(err, identityprivacy.ErrAliasUnavailable):
			status, result = http.StatusNotFound, personObject("error", identityprivacy.ErrAliasUnavailable.Error())
		case errors.Is(err, identityprivacy.ErrNamespaceMismatch):
			status, result = http.StatusBadRequest, personObject("error", identityprivacy.ErrNamespaceMismatch.Error())
		case errors.Is(err, identityprivacy.ErrRejected):
			status, result = http.StatusConflict, personObject("error", identityprivacy.ErrRejected.Error())
		case err != nil:
			return false, err
		default:
			result = personObject("revision", ack.Revision, "duplicate", ack.Duplicate)
		}
	}
	// A fixed owned acknowledgement never changes to a newer revision if
	// another command wins before publication. A loose intent denies output.
	return a.publishBody(ctx, func(*assertions.Reader) ([]byte, error) {
		return jsjson.Marshal(result)
	}, func(body []byte) error { return emit(status, body) })
}

func (a *AdminRead) RejectIdentityBody(ctx context.Context, err error) (bool, error) {
	return a.publishBody(ctx, func(*assertions.Reader) ([]byte, error) {
		return nil, err
	}, nil)
}
