package learningruntime

import (
	"context"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
)

// Extension attaches learning to the existing trusted ingress boundary. Runtime
// edits call Edited before re-entering Received, so a replacement source never
// inherits the old revision's pending admission. No startup/history scan occurs.
func (c *Coordinator) Extension() *botruntime.Extension {
	return &botruntime.Extension{
		Received: func(message *botruntime.Object, _ botruntime.Scene) error {
			if limited, _ := message.Get("sourceEventRegistrationLimited"); limited == true {
				return nil
			}
			_, err := c.Enqueue(context.Background(), message)
			return err
		},
		Edited: func(_ *botruntime.Object, message *botruntime.Object, _ botruntime.Scene) error {
			c.Recall(str(message, "sceneId"), str(message, "id"))
			return nil
		},
		PrepareRecall: func(scene, id string) (func() error, error) {
			return func() error { c.Recall(scene, id); return nil }, nil
		},
		SceneChanged: func(scene botruntime.Scene) {
			if !scene.LearningEnabled {
				c.CancelScene(scene.ID)
			}
		},
		Closed: c.Dispose,
	}
}
