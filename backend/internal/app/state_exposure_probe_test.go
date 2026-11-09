package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// A real Runtime constructs all private turn state. Its ContextPrompt callback
// is parked before model execution, so this test alone owns the probe's Scene.
func stateExposureProbe(a *Application, ctx context.Context, scene botruntime.Scene) (*botruntime.TurnContext, func() error, error) {
	authority, err := sourceevents.NewRuntimeAuthority(a.Engine.Sources)
	if err != nil {
		return nil, nil, err
	}
	dates, err := jsdate.New("UTC")
	if err != nil {
		return nil, nil, err
	}
	compare := func(a, b string) (int, error) { return strings.Compare(a, b), nil }
	policy, err := replyplan.New(compare, dates)
	if err != nil {
		return nil, nil, err
	}
	settings := botruntime.DefaultSettings()
	settings.TopicAdmissionEnabled = false
	settings.PromptContextWindowEnabled = false
	runtime, err := botruntime.New(settings, botruntime.Services{Authority: authority, Dates: dates, Policy: policy, Compare: compare, Models: botruntime.ModelFunc(func(_ context.Context, _ botruntime.ModelRequest, done func(botruntime.Completion, error)) {
		done(botruntime.Completion{}, errors.New("scope_probe_unexpected_model"))
	})})
	if err != nil {
		return nil, nil, err
	}
	arrived := make(chan *botruntime.TurnContext, 1)
	release := make(chan struct{})
	runtime.AddExtension(&botruntime.Extension{ContextPrompt: func(c *botruntime.TurnContext) (string, error) {
		arrived <- c
		<-release
		return "", errors.New("scope_probe_complete")
	}})
	cleanup := func() error {
		runtime.Cancel(scene.ID)
		close(release)
		wait, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return runtime.CloseAndWait(wait)
	}
	if err = runtime.RegisterScene(scene); err != nil {
		_ = cleanup()
		return nil, nil, err
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	message := memoryToolObject("id", "scope-probe-"+stamp, "sceneId", scene.ID, "senderId", "synthetic-user", "sentAt", stamp, "receivedAt", stamp, "origin", "live", "status", "available", "parts", []any{memoryToolObject("type", "text", "text", "Synthetic scope probe")})
	if _, err = runtime.Ingest(ctx, scene.ID, message, false); err != nil {
		_ = cleanup()
		return nil, nil, err
	}
	snapshot, err := runtime.Snapshot(scene.ID)
	if err != nil {
		_ = cleanup()
		return nil, nil, err
	}
	if len(snapshot.Topics) == 0 {
		_ = cleanup()
		return nil, nil, errors.New("scope_probe_topic_missing")
	}
	handle, err := runtime.Run(scene.ID, snapshot.Topics[0].ID, false)
	if err != nil {
		_ = cleanup()
		return nil, nil, err
	}
	select {
	case c := <-arrived:
		return c, func() error { c.Scene = scene; return cleanup() }, nil
	case <-handle.Done:
		_ = cleanup()
		return nil, nil, errors.New("scope_probe_ended_early")
	case <-ctx.Done():
		_ = cleanup()
		return nil, nil, ctx.Err()
	}
}
