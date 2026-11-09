package experienceservice

import (
	"context"
	"testing"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
)

func TestLifecycleExtensionDoesNotGrantModelTools(t *testing.T) {
	hints := &RuntimeHints{epochs: map[string]uint64{}}
	tool := runtimeTool("owned.tool", "read")
	changed, closed, received := 0, 0, 0
	base := botruntime.Extension{
		Tools:        []*jsjson.Object{tool},
		SceneChanged: func(botruntime.Scene) { changed++ },
		Closed:       func() { closed++ },
		Received:     func(*jsjson.Object, botruntime.Scene) error { received++; return nil },
	}
	if _, _, err := hints.ComposeLifecycle(base, nil); err == nil {
		t.Fatal("missing diagnostic owner accepted")
	}
	extension, binding, err := hints.ComposeLifecycle(base, func(string, error) { t.Fatal("unexpected domain error") })
	if err != nil {
		t.Fatal(err)
	}
	defer binding.CloseAndWait(context.Background())
	if len(extension.Tools) != 1 || extension.Tools[0] != tool || extension.Execute != nil || extension.PlannerContextData != nil {
		t.Fatal("lifecycle-only registration granted model tools or replaced base")
	}
	scene := botruntime.Scene{}
	scene.ID = "scene"
	extension.SceneChanged(scene)
	if hints.epoch("scene") != 1 || changed != 1 {
		t.Fatal("scene epoch or base callback duplicated")
	}
	// Source-limited ingress must preserve base delivery without manufacturing a
	// feedback read. Actual registered feedback is covered by the application test.
	if err := extension.Received(object("sourceEventRegistrationLimited", true), scene); err != nil {
		t.Fatal(err)
	}
	if received != 1 {
		t.Fatal("base ingress lost")
	}
	extension.Closed()
	if closed != 1 {
		t.Fatal("base close lost")
	}
	if err := binding.CloseAndWait(context.Background()); err != nil {
		t.Fatal(err)
	}
}
