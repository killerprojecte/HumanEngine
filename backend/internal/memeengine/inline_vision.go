package memeengine

import (
	"context"
	"errors"
	"strconv"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"github.com/killerprojecte/HumanEngine/backend/internal/visioncontext"
)

func (s *Service) inlineVision(modelPort *RuntimeModels) func(context.Context, *botruntime.TurnContext, []*Object, func(any, error)) {
	return func(ctx context.Context, turn *botruntime.TurnContext, messages []*Object, settle func(any, error)) {
		value, err := s.describeInlineVision(ctx, modelPort, turn, messages)
		settle(value, err)
	}
}

func (s *Service) selectInlineVision(turn *botruntime.TurnContext, messages []*Object) (*botruntime.DisplayImageSource, *Object, int) {
	for _, id := range turn.ImagePriorityIDs {
		for _, message := range messages {
			if get(message, "id") != id {
				continue
			}
			if source, index := turn.NativeVisionSource(message); source != nil {
				return source, message, index
			}
		}
	}
	return nil, nil, 0
}

func (s *Service) describeInlineVision(ctx context.Context, modelPort *RuntimeModels, turn *botruntime.TurnContext, messages []*Object) (any, error) {
	if turn.Mode != "real" {
		return nil, nil
	}
	// RuntimeModels.BeginTurn created this state before any prompt work. Once
	// the first eligible source is claimed, planner history tools do not scan.
	state := turn.OwnedExtensionState(modelPort, func() any { return &continuationState{} }).(*continuationState)
	state.mu.Lock()
	router := state.router
	started := state.visionStarted || state.closed
	state.mu.Unlock()
	if started {
		return nil, nil
	}
	source, message, partIndex := s.selectInlineVision(turn, messages)
	if source == nil {
		return nil, nil
	}
	id := str(get(message, "id"))
	state.mu.Lock()
	if state.visionStarted || state.closed {
		state.mu.Unlock()
		return nil, nil
	}
	state.visionStarted = true
	state.mu.Unlock()

	validate := func(reads botruntime.ReadAuthority) (bool, error) {
		return s.currentInlineVision(reads, turn.Scene.ID, source)
	}
	require := func(call context.Context) error {
		if call.Err() != nil {
			return errors.New("request_cancelled")
		}
		if !s.TurnRead.ValidContext(call, turn) {
			return errors.New("memory_read_revoked")
		}
		return s.Sources.WithRuntimeReads(call, func(reads botruntime.ReadAuthority) error {
			valid, err := validate(reads)
			if err != nil {
				return err
			}
			if !valid {
				return errors.New("memory_read_revoked")
			}
			return nil
		})
	}
	candidate := &visioncontext.Message{
		ID: id, SceneID: turn.Scene.ID, Status: "available", SentAt: str(get(message, "sentAt")),
		Parts: []visioncontext.Part{{Type: "media", MediaType: "image", AssetID: strconv.Itoa(partIndex), Index: partIndex}},
	}
	withRoute := router.Policy.HasConfiguredRoute("vision")
	result, err := visioncontext.Describe(ctx, new(visioncontext.Turn), visioncontext.Config{
		SceneID: turn.Scene.ID, Mode: turn.Mode, Timezone: turn.Timezone(), VisionRoute: withRoute, Uncached: true,
	}, []*visioncontext.Message{candidate}, nil, visioncontext.Ports{
		Recalled:            func(string, string) (bool, error) { return !source.Current(), nil },
		SourceAvailable:     func(visioncontext.Candidate) (bool, error) { return source.Current(), nil },
		AddDependency:       turn.AddDependency,
		AddSourceValidation: func(visioncontext.Candidate) { turn.AddScopedValidation(validate) },
		ReadImage: func(call context.Context, _ visioncontext.Candidate) ([]byte, error) {
			if !source.IsInline() {
				return source.BrowserImageBytes(call, require)
			}
			if err := require(call); err != nil {
				return nil, err
			}
			return source.InlineBytes(call)
		},
		ImageDataURL: func(call context.Context, image []byte) (string, error) {
			return s.Codec.Compress(call, image, providertransport.ImageUploadLimits{}, true)
		},
		Complete: func(call context.Context, messages []visioncontext.ChatMessage) (string, error) {
			wire := make([]*providertransport.Message, len(messages))
			for i, msg := range messages {
				body := map[string]any{"role": msg.Role, "content": msg.Content}
				if len(msg.Images) != 0 {
					images := make([]any, len(msg.Images))
					for j, image := range msg.Images {
						images[j] = image
					}
					body["images"] = images
				}
				wire[i] = &providertransport.Message{Visible: body}
			}
			response, err := router.Complete(providertransport.WithFinalModelReadGuard(call, require), providertransport.CompletionRequest{
				Role: "vision", NativeOnly: true, Messages: wire,
			})
			if err != nil {
				return "", err
			}
			defer response.Release()
			return response.Text, nil
		},
	}, nil)
	if err != nil {
		return nil, err
	}
	if withRoute {
		if err := require(ctx); err != nil {
			return nil, err
		}
	}
	return result.Project()
}

func (s *Service) currentInlineVision(reads botruntime.ReadAuthority, sceneID string, source *botruntime.DisplayImageSource) (bool, error) {
	if !source.Current() {
		return false, nil
	}
	valid, err := source.Validate(reads)
	if err != nil || !valid {
		return valid, err
	}
	var exact bool
	err = s.Sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
		kind, adapter, account, platform := source.SceneIdentity()
		rows, err := scope.Query(displayImageSceneQuery, sceneID, kind, adapter, account, platform)
		if err != nil {
			return err
		}
		exact = rows.Next()
		return errors.Join(rows.Err(), rows.Close())
	})
	return exact && source.Current(), err
}
