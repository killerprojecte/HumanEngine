package app

import "github.com/killerprojecte/HumanEngine/backend/internal/botruntime"

// MediaTasks is the real keyed cancellation/physical-work owner. The safe graph
// has no media ingest producer yet; enabling one requires Accept registration.
func (a *Application) mediaTaskExtension() *botruntime.Extension {
	return &botruntime.Extension{
		Edited: func(_ *botruntime.Object, message *botruntime.Object, scene botruntime.Scene) error {
			id, _ := message.Get("id")
			text, _ := id.(string)
			a.MediaTasks.CancelMessage(scene.ID, text)
			return nil
		},
		PrepareRecall: func(scene, id string) (func() error, error) {
			return func() error { a.MediaTasks.CancelMessage(scene, id); return nil }, nil
		},
		Closed: a.MediaTasks.Close,
	}
}
func (a *Application) contextualExtension() *botruntime.Extension {
	return &botruntime.Extension{
		Edited: func(_ *botruntime.Object, message *botruntime.Object, scene botruntime.Scene) error {
			id, _ := message.Get("id")
			text, _ := id.(string)
			a.Contextual.Recall(scene.ID, text)
			return nil
		},
		PrepareRecall: func(scene, id string) (func() error, error) {
			return func() error { a.Contextual.Recall(scene, id); return nil }, nil
		},
		Closed: a.Contextual.Dispose,
	}
}
