package memoryservice

import (
	"context"
	"errors"
	"net/http"

	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/workingset"
)

type WorkingSetPinQuery struct {
	SceneID          string
	Selection        workingset.Selection
	ExpectedRevision float64
	Pinned           bool
}

type WorkingSetPinAdmin func(context.Context, *WorkingSetPinQuery, func(int, []byte) error) error

var errWorkingSetPinScene = errors.New("working_set_pin_scene_unavailable")

func (s *Service) BindWorkingSetPinAdmin(publisher *recovery.ReadPublisher) (WorkingSetPinAdmin, error) {
	publish, err := s.d.WorkingSet.BindPublishedRead(publisher)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, q *WorkingSetPinQuery, emit func(int, []byte) error) error {
		status, body := http.StatusOK, []byte(nil)
		set := func(code int, payload string) { status, body = code, []byte(payload) }
		sceneUnavailable := func() { set(http.StatusNotFound, `{"error":"Scene unavailable"}`) }
		pinUnavailable := func() { set(http.StatusNotFound, `{"error":"Pin unavailable"}`) }
		conflict := func() {
			set(http.StatusConflict, `{"error":"Pin state changed or is unavailable; refresh before retrying"}`)
		}
		var policy workingset.PinPolicy
		var err error
		if q == nil {
			set(http.StatusBadRequest, `{"error":"Invalid request"}`)
		} else if q.SceneID == "" {
			sceneUnavailable()
		} else {
			policy, err = s.setAdminPin(ctx, *q)
			switch {
			case errors.Is(err, errWorkingSetPinScene):
				sceneUnavailable()
			case errors.Is(err, workingset.ErrPinNotFound):
				pinUnavailable()
			case errors.Is(err, workingset.ErrPinConflict), errors.Is(err, workingset.ErrPinTerminal),
				errors.Is(err, workingset.ErrPinDisabled), errors.Is(err, workingset.ErrPinStale):
				conflict()
			case err != nil:
				return err
			}
		}
		return publish(ctx, func(ws *workingset.Service) ([]byte, error) {
			if body != nil {
				return body, nil
			}
			err := ws.ReadScope(ctx, func(scope *workingset.Scope) error {
				ar, err := s.workspaceAssertionReader(scope)
				if err != nil {
					return err
				}
				real, err := realPeopleScene(ar, q.SceneID)
				if err != nil {
					return err
				}
				if !real {
					sceneUnavailable()
					return nil
				}
				ref := policy.Selection.Ref
				current, err := ws.Storage().Pin(q.SceneID, workingset.Entity{Kind: ref.Kind, ID: ref.ID})
				if err != nil {
					return err
				}
				if current == nil {
					pinUnavailable()
					return nil
				}
				if current.PolicyRevision != policy.PolicyRevision || current.Pinned != policy.Pinned || current.Terminal != policy.Terminal ||
					!workingset.SameSelection(current.Selection, policy.Selection) {
					conflict()
					return nil
				}
				raw, err := workingset.JSON(*current)
				body = []byte(raw)
				return err
			})
			return body, err
		}, func(body []byte) error { return emit(status, body) })
	}, nil
}

// A scene check and its exact pin CAS share one write owner. SetPinned's savepoint
// uses that owner's existing typed recovery event, never a second writer.
func (s *Service) setAdminPin(ctx context.Context, q WorkingSetPinQuery) (policy workingset.PinPolicy, err error) {
	err = s.d.Assertions.Transaction(ctx, func(active *assertions.Store) error {
		if err := active.Read(ctx, func(ar *assertions.Reader) error {
			real, err := realPeopleScene(ar, q.SceneID)
			if err == nil && !real {
				return errWorkingSetPinScene
			}
			return err
		}); err != nil {
			return err
		}
		return active.WithWriteSession(func(session *recovery.WriteSession) (e error) {
			policy, e = s.d.WorkingSet.WithSession(session).SetPinned(ctx, workingset.Context{SceneID: q.SceneID}, q.Selection, q.ExpectedRevision, q.Pinned)
			return e
		})
	})
	return
}
