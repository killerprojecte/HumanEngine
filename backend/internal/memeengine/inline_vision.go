package memeengine

import (
	"context"
	"errors"
	"slices"

	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
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

type nativeVisionReplay struct {
	id          string
	source      *botruntime.DisplayImageSource
	observation visioncontext.Observation
}

func (s *Service) visionReceipt(modelPort *RuntimeModels) func(context.Context, *botruntime.TurnContext, []*Object, func(any, error)) {
	return func(ctx context.Context, turn *botruntime.TurnContext, rows []*Object, settle func(any, error)) {
		state := turn.OwnedExtensionState(modelPort, func() any { return &continuationState{} }).(*continuationState)
		state.mu.Lock()
		entries := state.visionReplay
		state.mu.Unlock()
		value, err := s.replayNativeVision(ctx, turn, rows, entries)
		settle(value, err)
	}
}

func (s *Service) replayNativeVision(ctx context.Context, turn *botruntime.TurnContext, rows []*Object, entries []nativeVisionReplay) (any, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	var matched []nativeVisionReplay
	for _, row := range rows {
		for _, replay := range entries {
			if replay.source.MatchesImageReceipt(ctx, row) &&
				!slices.ContainsFunc(matched, func(other nativeVisionReplay) bool { return other.source == replay.source }) {
				matched = append(matched, replay)
			}
		}
	}
	if len(matched) == 0 {
		return nil, nil
	}
	if ctx.Err() != nil || !s.TurnRead.ValidContext(ctx, turn) {
		return nil, visioncontext.ErrCancelled
	}
	result := visioncontext.Result{
		Trust: "untrusted_image_descriptions", FramePolicy: "first_frame_only",
		Images: make([]visioncontext.Image, 0, len(matched)),
	}
	err := s.Sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		for _, replay := range matched {
			valid, err := s.currentInlineVision(reads, turn.Scene.ID, replay.source)
			if err != nil {
				return err
			}
			if !valid {
				return visioncontext.ErrCancelled
			}
			result.Images = append(result.Images, visioncontext.Image{MessageID: replay.id, Observation: replay.observation})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result.Project()
}

type nativeVisionCandidate struct {
	source   *botruntime.DisplayImageSource
	sticker  bool
	ticket   *modelread.Ticket
	validate func(botruntime.ReadAuthority) (bool, error)
	require  func(context.Context) error
}

func nativeCandidate(candidate visioncontext.Candidate) *nativeVisionCandidate {
	return candidate.Part.Reference.(*nativeVisionCandidate)
}

func (s *Service) describeInlineVision(ctx context.Context, modelPort *RuntimeModels, turn *botruntime.TurnContext, messages []*Object) (any, error) {
	if turn.Mode != "real" {
		return nil, nil
	}
	// The initial eligible native callback owns the budget. Planner history
	// callbacks cannot add remote or older sources to this batch.
	state := turn.OwnedExtensionState(modelPort, func() any { return &continuationState{} }).(*continuationState)
	state.mu.Lock()
	router := state.router
	started := state.visionStarted || state.closed
	state.mu.Unlock()
	if started {
		return nil, nil
	}
	var candidates []*visioncontext.Message
	var library *visioncontext.Library
	for _, message := range messages {
		id := str(get(message, "id"))
		if !slices.Contains(turn.ImagePriorityIDs, id) {
			continue
		}
		var row *visioncontext.Message
		turn.NativeVisionSources(message, func(source *botruntime.DisplayImageSource, index int) {
			native := &nativeVisionCandidate{
				source:  source,
				sticker: source.IsInline() && IsStickerMetadata(source.InlinePartMetadata(index)),
				validate: func(reads botruntime.ReadAuthority) (bool, error) {
					return s.currentInlineVision(reads, turn.Scene.ID, source)
				},
			}
			native.require = func(call context.Context) error {
				return s.withNativeVisionRead(call, turn, native, nil)
			}
			if native.sticker && library == nil {
				library = &visioncontext.Library{
					CanRecognize: func(c visioncontext.Candidate) (bool, error) {
						return nativeCandidate(c).sticker, nil
					},
					Recognize: func(call context.Context, image []byte, c visioncontext.Candidate) (string, bool, error) {
						native := nativeCandidate(c)
						if native.ticket == nil {
							return "", false, nil
						}
						description, guard, err := s.storedInlineSticker(call, turn, native.source, image, native.ticket)
						if guard != nil {
							turn.AddScopedValidation(guard)
						}
						return description, guard != nil, err
					},
				}
			}
			if row == nil {
				row = &visioncontext.Message{
					ID: id, SceneID: turn.Scene.ID, Status: "available", SentAt: str(get(message, "sentAt")),
				}
				candidates = append(candidates, row)
			}
			row.Parts = append(row.Parts, visioncontext.Part{
				Type: "media", MediaType: "image", AssetID: source.AssetID(), Index: index, Reference: native,
			})
		})
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	state.mu.Lock()
	if state.visionStarted || state.closed {
		state.mu.Unlock()
		return nil, nil
	}
	state.visionStarted = true
	state.mu.Unlock()

	withRoute := router.Policy.HasConfiguredRoute("vision")
	var selected []*nativeVisionCandidate
	var observations []nativeVisionReplay
	result, err := visioncontext.Describe(ctx, new(visioncontext.Turn), visioncontext.Config{
		SceneID: turn.Scene.ID, Mode: turn.Mode, Timezone: turn.Timezone(), VisionRoute: withRoute, PriorityIDs: turn.ImagePriorityIDs,
	}, candidates, nil, visioncontext.Ports{
		// Current includes original-row recall and adapter generation. Loss of
		// that authority is not an ordinary per-image media or VLM failure.
		SourceAvailable: func(c visioncontext.Candidate) (bool, error) {
			if !nativeCandidate(c).source.Current() {
				return false, visioncontext.ErrCancelled
			}
			return true, nil
		},
		AddDependency: turn.AddDependency,
		AddSourceValidation: func(c visioncontext.Candidate) {
			native := nativeCandidate(c)
			selected = append(selected, native)
			turn.AddScopedValidation(native.validate)
		},
		Observe: func(c visioncontext.Candidate, observation visioncontext.Observation) {
			if observation.Status == "described" && observation.FramePolicy == "first_frame_only" {
				observations = append(observations, nativeVisionReplay{c.Message.ID, nativeCandidate(c).source, observation})
			}
		},
		ReadImage: func(call context.Context, c visioncontext.Candidate) ([]byte, error) {
			native := nativeCandidate(c)
			source := native.source
			if !source.IsInline() {
				return source.BrowserImageBytes(call, native.require)
			}
			var capture func(botruntime.ReadAuthority) error
			if native.sticker {
				capture = func(reads botruntime.ReadAuthority) (err error) {
					native.ticket, err = s.TurnRead.CapturePersonalizationScoped(reads, turn.Scene.ID)
					return err
				}
			}
			if err := s.withNativeVisionRead(call, turn, native, capture); err != nil {
				return nil, err
			}
			if native.sticker && native.ticket == nil && !withRoute {
				return nil, errors.New("route_not_configured")
			}
			return source.InlineBytes(call)
		},
		ImageDataURL: func(call context.Context, image []byte) (string, error) {
			return s.Codec.Compress(call, image, providertransport.ImageUploadLimits{}, true)
		},
		CompleteCandidate: func(call context.Context, c visioncontext.Candidate, messages []visioncontext.ChatMessage) (string, error) {
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
			response, err := router.Complete(providertransport.WithFinalModelReadGuard(call, nativeCandidate(c).require), providertransport.CompletionRequest{
				Role: "vision", NativeOnly: true, Messages: wire,
			})
			if err != nil {
				return "", err
			}
			defer response.Release()
			return response.Text, nil
		},
	}, library)
	if err != nil {
		return nil, err
	}
	if len(selected) != 0 {
		if !s.TurnRead.ValidContext(ctx, turn) {
			return nil, visioncontext.ErrCancelled
		}
		if err := s.Sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
			for _, native := range selected {
				if err := native.check(reads); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	value, err := result.Project()
	if err == nil && len(observations) != 0 {
		state.mu.Lock()
		if !state.closed {
			state.visionReplay = observations
		}
		state.mu.Unlock()
	}
	return value, err
}

func (native *nativeVisionCandidate) check(reads botruntime.ReadAuthority) error {
	valid, err := native.validate(reads)
	if err == nil && !valid {
		err = visioncontext.ErrCancelled
	}
	return err
}

func (s *Service) withNativeVisionRead(call context.Context, turn *botruntime.TurnContext, native *nativeVisionCandidate, capture func(botruntime.ReadAuthority) error) error {
	if call.Err() != nil || !s.TurnRead.ValidContext(call, turn) {
		return visioncontext.ErrCancelled
	}
	return s.Sources.WithRuntimeReads(call, func(reads botruntime.ReadAuthority) error {
		if err := native.check(reads); err != nil {
			return err
		}
		if capture != nil {
			return capture(reads)
		}
		return nil
	})
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
