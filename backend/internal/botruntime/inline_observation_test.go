package botruntime

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestExactInlineObservationRetainsOriginalRecordAndGeneration(t *testing.T) {
	for _, stateChange := range []string{"unchanged", "edit", "recall", "retire", "replace"} {
		t.Run(stateChange, func(t *testing.T) {
			r, authority, _ := testRuntime(t, ModelFunc(func(context.Context, ModelRequest, func(Completion, error)) {
				t.Error("observation unexpectedly started a model")
			}))
			authority.enrolled = true
			scene := fixtureScene(false)
			if err := r.RegisterScene(scene); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			const asset = "ob:media:qualified-inline-fixture"
			payload := []byte{1, 2, 3}
			var retired atomic.Bool
			var reads atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			adapter := &Adapter{InlineImage: &InlineImageAdapter{
				Classify: func(Scene, string) NativeImageLink { return NativeImageInline },
				Current:  func() bool { return !retired.Load() },
				Read: func(ctx context.Context, gotScene Scene, gotAsset string) ([]byte, error) {
					if gotScene.ID != scene.ID || gotAsset != asset || reads.Add(1) != 1 {
						t.Error("read did not use the original native source")
					}
					close(entered)
					select {
					case <-release:
						return payload, nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				},
			}}
			r.AddAdapter(scene.AdapterID, adapter)
			msg := fixtureMessage(scene.ID, "fixture-image", "fixture text")
			_ = msg.Set("parts", []any{
				obj("type", "text", "text", "first part"),
				obj("type", "media", "mediaType", "image/png", "assetId", asset),
			})
			if _, err := r.IngestNativeMedia(ctx, adapter, scene, msg, false); err != nil {
				t.Fatal(err)
			}
			s := r.state(scene.ID)
			s.mu.Lock()
			record := s.byID["fixture-image"]
			s.mu.Unlock()
			if record == nil {
				t.Fatal("no accepted native record")
			}
			if r.captureInlineObservation(s, scene, record, 0) != nil {
				t.Fatal("non-image part admitted")
			}
			proof := r.captureInlineObservation(s, scene, record, 1)
			if proof == nil || proof.record != record || proof.AssetID() != asset {
				t.Fatal("exact retained inline part missing")
			}
			if valid, err := proof.Validate(authority); err != nil || !valid {
				t.Fatalf("source authority did not recognize accepted body: %v %v", valid, err)
			}
			type outcome struct {
				bytes []byte
				err   error
			}
			result := make(chan outcome, 1)
			go func() {
				b, err := proof.ReadInlineObservation(ctx)
				result <- outcome{b, err}
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("native read never started")
			}
			// Await a real scene write (edit/recall) or ownership update while
			// read remains physically held; never release it to make progress.
			changed := make(chan error, 1)
			go func() {
				switch stateChange {
				case "edit":
					revised := msg.Clone()
					_ = revised.Set("parts", []any{
						obj("type", "text", "text", "edited but same asset"),
						obj("type", "media", "mediaType", "image/png", "assetId", asset),
					})
					status, err := r.Edit(ctx, scene.ID, revised, obj("editId", "e1", "order", obj("kind", "platform_sequence", "sequence", float64(1))), false)
					if err == nil && status != "edited" {
						err = errors.New("fixture edit did not apply")
					}
					changed <- err
					return
				case "recall":
					changed <- r.Recall(ctx, scene.ID, "fixture-image")
					return
				case "retire":
					retired.Store(true)
				case "replace":
					r.AddAdapter(scene.AdapterID, &Adapter{InlineImage: &InlineImageAdapter{Current: func() bool { return true }}})
				}
				changed <- nil
			}()
			select {
			case err := <-changed:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("mutation blocked behind byte IO")
			}
			close(release)
			select {
			case got := <-result:
				if stateChange == "unchanged" {
					if got.err != nil || !bytes.Equal(got.bytes, payload) {
						t.Fatalf("inline observation bytes: %v %v", got.bytes, got.err)
					}
				} else if !errors.Is(got.err, ErrDisplayImageUnavailable) || got.bytes != nil ||
					r.captureInlineObservation(s, scene, record, 1) != nil {
					t.Fatalf("stale handle survived %s: %v %v", stateChange, got.bytes, got.err)
				}
			case <-ctx.Done():
				t.Fatal("native read never settled")
			}
		})
	}
}
