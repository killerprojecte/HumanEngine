package app

import (
	"context"
	"database/sql"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/externaladapter"
	"github.com/killerprojecte/HumanEngine/backend/internal/ingressrouting"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/pluginmanager"
	"github.com/killerprojecte/HumanEngine/backend/internal/scenestore"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

func (a *Application) externalAdapterResolver(owners Owners) pluginmanager.V2AdapterResolver {
	return externaladapter.Resolve(externaladapter.BindingPorts{
		Runtime: a.Engine.Runtime, HasSecret: owners.Credentials.Has, Stopped: a.cancelAdapterScenes,
		Ingest: a.ingestExternalAdapter,
		SceneName: func(ctx context.Context, scene dispatch.SceneRef, lookup func(context.Context) (string, error)) (bool, error) {
			root, release, err := a.Engine.RootContext(ctx)
			if err != nil {
				return false, err
			}
			defer release()
			scenes := scenestore.Store{Main: owners.Main, Authority: owners.Authority}
			return scenes.RefreshSceneName(root, a.readPublisher, scene, lookup)
		},
		SenderName: func(ctx context.Context, scene dispatch.SceneRef, sender, platform string, lookup func(context.Context) (string, error)) (bool, error) {
			root, release, err := a.Engine.RootContext(ctx)
			if err != nil {
				return false, err
			}
			defer release()
			return ingressrouting.RefreshSenderName(root, owners.Main, owners.Authority, a.readPublisher, scene, sender, platform, lookup)
		},
		Original: func(ctx context.Context, scene, asset string) (memeservice.Original, error) {
			root, release, err := a.Engine.RootContext(ctx)
			if err != nil {
				return memeservice.Original{}, err
			}
			defer release()
			return a.Engine.Memes.Original(root, memeservice.Context{SceneID: scene}, asset)
		},
		Recall: func(ctx context.Context, ref dispatch.SceneRef, id string) error {
			root, release, err := a.Engine.RootContext(ctx)
			if err != nil {
				return err
			}
			defer release()
			return a.Engine.Runtime.RecallScene(root, botruntime.Scene{SceneRef: ref}, id)
		},
	})
}

func (a *Application) ingestExternalAdapter(ctx context.Context, owner *botruntime.Adapter, ref dispatch.SceneRef, message *jsjson.Object) error {
	root, release, err := a.Engine.RootContext(ctx)
	if err != nil {
		return err
	}
	defer release()
	scene := botruntime.Scene{SceneRef: ref}
	if current, ok := a.Engine.Runtime.RegisteredScene(ref.ID); ok {
		scene.ParticipationEnabled, scene.LearningEnabled, scene.ProactiveEnabled = current.ParticipationEnabled, current.LearningEnabled, current.ProactiveEnabled
	} else if err := a.Engine.Sources.WithRuntimeReads(root, func(reads botruntime.ReadAuthority) error {
		return a.Engine.Sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
			rows, err := scope.Query(`SELECT json_extract(value,'$.participationEnabled'),json_extract(value,'$.learningEnabled'),json_extract(value,'$.proactiveEnabled') FROM scenes WHERE id=?`, ref.ID)
			if err != nil {
				return err
			}
			defer rows.Close()
			if !rows.Next() {
				return rows.Err()
			}
			var participation, learning, proactive sql.NullBool
			if err := rows.Scan(&participation, &learning, &proactive); err != nil {
				return err
			}
			scene.ParticipationEnabled, scene.LearningEnabled, scene.ProactiveEnabled = participation.Bool, learning.Bool, proactive.Bool
			return nil
		})
	}); err != nil {
		return err
	}
	_, err = a.Engine.Runtime.IngestNativeMedia(root, owner, scene, message, true)
	return err
}
