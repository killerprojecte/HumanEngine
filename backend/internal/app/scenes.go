package app

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// SceneChanged is invoked only after a definite source scene commit. This
// graph preserves the source ordering, including the default learning=false path.
func (a *Application) SceneChanged(ctx context.Context, value *jsjson.Object) error {
	get := func(key string) any { v, _ := value.Get(key); return v }
	text := func(key string) string { v, _ := get(key).(string); return v }
	scene := botruntime.Scene{}
	scene.ID, scene.AdapterID, scene.AccountID = text("id"), text("adapterId"), text("accountId")
	scene.Kind, scene.PlatformSceneID = text("kind"), text("platformSceneId")
	if v, ok := get("name").(string); ok {
		scene.DisplayName = &v
	}
	if v, ok := get("avatarUrl").(string); ok {
		scene.AvatarURL = &v
	}
	scene.ParticipationEnabled = get("participationEnabled") == true
	scene.LearningEnabled = get("learningEnabled") == true
	scene.ProactiveEnabled = get("proactiveEnabled") == true
	if !scene.LearningEnabled {
		a.ExperienceHints.LearningPaused(scene.ID)
	}
	if e := a.Engine.Runtime.RegisterScene(scene); e != nil {
		return e
	}
	if err := a.Semantic.ConfigureSemantic(ctx); err != nil {
		return err
	}
	if !scene.LearningEnabled {
		a.cancelSceneWork(scene.ID)
	}
	return nil
}

func (a *Application) cancelSceneWork(id string) {
	a.Learning.Coordinator.CancelScene(id)
	a.Semantic.CancelScene(id)
	a.Contextual.CancelScene(id)
	a.MediaTasks.CancelScene(id)
}

// The registration has already withdrawn the adapter; here the application
// revokes background work for its durably enrolled scenes.
func (a *Application) cancelAdapterScenes(adapterID string) error {
	root, release, err := a.Engine.RootContext(context.Background())
	if err != nil {
		return err
	}
	defer release()
	scenes := []string{}
	err = a.Engine.Sources.WithRuntimeReads(root, func(reads botruntime.ReadAuthority) error {
		return a.Engine.Sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
			rows, err := scope.Query("SELECT value FROM scenes")
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var raw string
				if err = rows.Scan(&raw); err != nil {
					return err
				}
				value, e := jsjson.Parse(raw)
				if e != nil {
					return e
				}
				if scene, ok := value.(*jsjson.Object); ok {
					adapter, _ := scene.Get("adapterId")
					if adapter == adapterID {
						id, _ := scene.Get("id")
						if text, ok := id.(string); ok {
							scenes = append(scenes, text)
						}
					}
				}
			}
			return rows.Err()
		})
	})
	if err != nil {
		return err
	}
	for _, id := range scenes {
		a.cancelSceneWork(id)
	}
	return nil
}
