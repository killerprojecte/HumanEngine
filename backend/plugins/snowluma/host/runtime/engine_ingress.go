package snowlumaruntime

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/ingressrouting"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeengine"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"github.com/killerprojecte/HumanEngine/backend/internal/topicroute"
	"time"
)

// EngineIngress borrows the actual application owners. It performs persistence
// and authority IO only; message identities, normalization, bot substitution,
// recalled-set decisions and profile lookup plans are supplied by the guest.
type EngineIngress struct {
	Compare  topicroute.LocaleCompare
	Engine   *memeengine.Service
	Identity *ingressrouting.Router
}

func sceneRef(value *jsjson.Object) (ref dispatch.SceneRef, err error) {
	wire, err := jsjson.Marshal(value)
	if err != nil {
		return ref, err
	}
	err = json.Unmarshal(wire, &ref)
	return
}
func (e EngineIngress) scene(ctx context.Context, value *jsjson.Object) (scene botruntime.Scene, err error) {
	scene.SceneRef, err = sceneRef(value)
	if err != nil {
		return
	}
	err = e.Engine.Sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		return e.Engine.Sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
			rows, err := scope.Query("SELECT value FROM scenes WHERE id=?", scene.ID)
			if err != nil {
				return err
			}
			defer rows.Close()
			if rows.Next() {
				var raw string
				if err = rows.Scan(&raw); err != nil {
					return err
				}
				value, err := jsjson.Parse(raw)
				if err != nil {
					return err
				}
				if o, ok := value.(*jsjson.Object); ok {
					scene.ParticipationEnabled = get(o, "participationEnabled") == true
					scene.LearningEnabled = get(o, "learningEnabled") == true
					scene.ProactiveEnabled = get(o, "proactiveEnabled") == true
				}
			}
			return rows.Err()
		})
	})
	return
}
func (e EngineIngress) Ingest(ctx context.Context, message, ref *jsjson.Object) error {
	root, release, err := e.Engine.RootContext(ctx)
	if err != nil {
		return err
	}
	defer release()
	scene, err := e.scene(root, ref)
	if err != nil {
		return err
	}
	_, err = e.Engine.Runtime.IngestScene(root, scene, message, true)
	return err
}
func (e EngineIngress) Recall(ctx context.Context, scene, id string) error {
	root, release, err := e.Engine.RootContext(ctx)
	if err != nil {
		return err
	}
	defer release()
	return e.Engine.Runtime.Recall(root, scene, id)
}
func (e EngineIngress) Recalled(ctx context.Context, scene, id string) (recalled bool, err error) {
	root, release, err := e.Engine.RootContext(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	err = e.Engine.Sources.WithRuntimeReads(root, func(reads botruntime.ReadAuthority) error {
		var e error
		recalled, e = reads.IsRecalled(botruntime.Scene{SceneRef: dispatch.SceneRef{ID: scene}}, id)
		return e
	})
	return
}
func (e EngineIngress) Profile(ctx context.Context, scene, message *jsjson.Object) error {
	if e.Identity == nil {
		return errors.New("snowluma_identity_owner_missing")
	}
	root, release, err := e.Engine.RootContext(ctx)
	if err != nil {
		return err
	}
	defer release()
	ref, err := sceneRef(scene)
	if err != nil {
		return err
	}
	return e.Identity.ObserveIdentity(root, ref, message)
}

// ReceiverFactory closes over real IO owners; every invocation creates a fresh
// receiver/profile state machine bound to exactly one immutable Sender.
func (e EngineIngress) ReceiverFactory(failure func(), now func() time.Time) func(*Sender) (func(context.Context, string) error, error) {
	return func(sender *Sender) (func(context.Context, string) error, error) {
		if e.Compare == nil {
			return nil, errors.New("snowluma_collation_owner_missing")
		}
		profiles, err := NewProfiles(sender, func(ctx context.Context, profile *jsjson.Object) error {
			scene, ok := get(profile, "scene").(*jsjson.Object)
			if !ok {
				return errors.New("invalid_guest_profile_scene")
			}
			message := obj()
			if person, ok := get(profile, "sender").(*jsjson.Object); ok {
				_ = message.Set("senderId", get(person, "id"))
				_ = message.Set("senderPlatformId", get(person, "platformId"))
				if v, present := person.Get("displayName"); present {
					_ = message.Set("senderDisplayName", v)
				}
			}
			return e.Profile(ctx, scene, message)
		}, failure, now)
		if err != nil {
			return nil, err
		}
		receiver, err := NewReceiver(sender, ReceivePorts{Recalled: e.Recalled, ObserveProfiles: profiles.Observe, Ingest: e.Ingest, Recall: e.Recall, Now: now})
		if err != nil {
			return nil, err
		}
		sender.queries = &Queries{sender: sender, receiver: receiver, profiles: profiles, compare: e.Compare}
		return func(ctx context.Context, raw string) error { defer profiles.Flush(); return receiver.Receive(ctx, raw) }, nil
	}
}
