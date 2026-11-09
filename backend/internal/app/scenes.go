package app

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
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
		a.Learning.Coordinator.CancelScene(scene.ID)
		a.Semantic.CancelScene(scene.ID)
		a.Contextual.CancelScene(scene.ID)
		a.MediaTasks.CancelScene(scene.ID)
	}
	return nil
}
