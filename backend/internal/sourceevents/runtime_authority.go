package sourceevents

import (
	"context"
	"errors"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/contract"
	"github.com/killerprojecte/HumanEngine/backend/internal/ingressrouting"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

// RuntimeAuthority composes the native source store with the existing runtime.
// It owns no connection, startup, reconciliation or plugin business state.
// The source store's owner must outlive every runtime operation.
type RuntimeAuthority struct {
	*runtimeReads
	schemas *contract.Schemas
	routing *ingressrouting.Router
}

func NewRuntimeAuthority(store *Store) (*RuntimeAuthority, error) {
	if store == nil || store.session != nil || store.bindingError != nil {
		return nil, errors.New("runtime_source_store_invalid")
	}
	schemas, err := contract.NewSchemas()
	if err != nil {
		return nil, err
	}
	repository, err := ingressrouting.NewSQLiteRepository(store.main, store.authority)
	if err != nil {
		return nil, err
	}
	routing, err := ingressrouting.New(repository)
	if err != nil {
		return nil, err
	}
	return &RuntimeAuthority{runtimeReads: &runtimeReads{store: store}, schemas: schemas, routing: routing}, nil
}

// RouteIngress persists discovery/identity before duplicate and recall admission.
// It is separate from source revision enrollment, including for bot messages.
func (a *RuntimeAuthority) RouteIngress(ctx context.Context, scene botruntime.Scene, message *jsjson.Object) error {
	return a.routing.Route(ctx, scene.SceneRef, message)
}

// Observe accepts normalized ordinary ingress, but trusted edits retain their
// supplied identity/body until the later ingress normalization step. Ingress never
// accepts a caller's ref, and an enrolled source remains versioned when disabled.
func (a *RuntimeAuthority) Observe(ctx context.Context, scene botruntime.Scene, message, edit *jsjson.Object) (status string, trusted *jsjson.Object, err error) {
	if message == nil {
		return "", nil, ErrInvalid
	}
	m := cloneRuntimeJSON(message).(*jsjson.Object)
	if edit != nil {
		edit = cloneRuntimeJSON(edit).(*jsjson.Object)
	}
	if scene.Kind == "simulation" {
		return "simulation", nil, nil
	}
	if edit != nil && (str(m, "origin") != "live" || str(m, "status") != "available" || str(m, "senderId") == "bot") {
		return "", nil, errors.New("source_event_edit_requires_live_user")
	}
	err = a.store.transaction(ctx, func(s *Store) error {
		if e := s.ready(); e != nil {
			return e
		}
		v := s.view()
		if edit == nil {
			recalled, e := recalledAt(v, scene.ID, str(m, "id"))
			if e != nil {
				return e
			}
			if recalled {
				status = "recalled"
				return nil
			}
		}
		var identity Identity
		var e error
		if edit == nil {
			identity, e = RuntimeSourceIdentity(scene, m)
			if e != nil {
				return e
			}
		}
		enrolled, e := v.Registered(scene.ID, str(m, "id"))
		if e != nil {
			return e
		}
		if edit != nil && enrolled == nil {
			status = "unorderable"
			return nil
		}
		if edit != nil {
			identity, e = RuntimeSourceIdentity(scene, m)
			if e != nil {
				return e
			}
		}
		if edit == nil {
			raw, exists, e := rawRow(v, "SELECT value FROM config WHERE key=?", "settings")
			if e != nil {
				return e
			}
			if !exists {
				raw = string(contract.DefaultSettingsInput())
			}
			settings, e := a.schemas.Parse("Settings", []byte(raw))
			if e != nil {
				return e
			}
			if settings["sourceEventRevisionsEnabled"] != true && enrolled == nil {
				m.Delete("sourceEventRef")
				status, trusted = "new", m
				return nil
			}
		}
		hash, e := RuntimeSourceBodyHash(m)
		if e != nil {
			return e
		}
		change := obj("kind", "ingress", "identity", identity.Value(), "bodyHash", hash, "occurredAt", get(m, "sentAt"))
		if edit != nil {
			_ = change.Set("kind", "edit")
			_ = change.Set("editId", get(edit, "editId"))
			_ = change.Set("order", get(edit, "order"))
		}
		parsed, e := ParseChange(change)
		if e != nil {
			return e
		}
		result, e := s.Observe(ctx, parsed)
		if e != nil {
			return e
		}
		status = result.Status
		if edit != nil {
			if status == "edited" && result.Ref != nil {
				_ = m.Set("sourceEventRef", result.Ref.Value())
				trusted = m
			}
		} else if status == "new" || status == "capacity" {
			if result.Ref == nil {
				m.Delete("sourceEventRef")
			} else {
				_ = m.Set("sourceEventRef", result.Ref.Value())
			}
			_ = m.Set("sourceEventRegistrationLimited", status == "capacity")
			trusted = m
		}
		return nil
	})
	return
}
