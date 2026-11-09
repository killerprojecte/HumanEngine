// Package v1adapterruntime installs the real legacy plugin host onto the native
// Engine/runtime owners. It is separate from plugins to avoid a provider cycle.
package v1adapterruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/dispatch"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsjson"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeengine"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/plugins"
	"github.com/killerprojecte/HumanEngine/backend/internal/quiescence"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
	"io"
	"os"
	"sync"
)

type registration struct {
	generation uint64
	cancel     context.CancelFunc
	adapter    *botruntime.Adapter
}
type Bridge struct {
	Host      *plugins.V1Host
	engine    *memeengine.Service
	mu        sync.Mutex
	adapters  map[string]*registration
	callbacks quiescence.Group
	closing   bool
}

// New is the production construction entry: the same host supplies native model
// protocols and runtime adapter callbacks. It never replaces an existing host.
// Enable/disable/configuration remain owned by the caller's plugin manager.
func New(engine *memeengine.Service, node string, diagnostics io.Writer, options plugins.V1HostOptions) (*Bridge, error) {
	if engine == nil || engine.Runtime == nil || engine.Sources == nil || engine.Memes == nil || engine.Models == nil {
		return nil, errors.New("adapter_runtime_dependencies_required")
	}
	if engine.Models.Host != nil {
		return nil, errors.New("adapter_runtime_host_already_bound")
	}
	if options.AdapterServices.Installed != nil || options.AdapterServices.Stopped != nil || options.AdapterServices.Receive != nil || options.AdapterServices.Edit != nil || options.AdapterServices.Recall != nil || options.AdapterServices.IsRecalled != nil || options.AdapterServices.Media != nil {
		return nil, errors.New("adapter_runtime_services_already_bound")
	}
	b := &Bridge{engine: engine, adapters: map[string]*registration{}}
	options.AdapterServices = b.adapterServices()
	host, e := plugins.NewV1HostWithOptions(node, diagnostics, options)
	if e != nil {
		return nil, e
	}
	b.Host = host
	engine.Models.Host = host
	return b, nil
}
func (b *Bridge) Close(ctx context.Context) error { return b.CloseAndWait(ctx) }
func linked(ctx, life context.Context) (context.Context, func()) {
	next, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(life, cancel)
	if life.Err() != nil {
		cancel()
	}
	return next, func() { stop(); cancel() }
}
func (b *Bridge) install(ctx context.Context, meta plugins.AdapterMetadata) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	life, cancel := context.WithCancel(ctx)
	entry := &registration{generation: meta.Generation, cancel: cancel}
	status := &adapterStatusOwner{cancel: cancel, read: func(ctx context.Context) (string, error) {
		return b.Host.AdapterStatus(ctx, meta.ID, meta.Generation)
	}}
	adapter := &botruntime.Adapter{Dispose: status.dispose, Status: status.status}
	adapter.Transport = dispatch.AbortContainedAdapter(&dispatch.Adapter{Send: func(scene dispatch.SceneRef, segment dispatch.Segment, signal *dispatch.Signal, before func() error) *dispatch.Future[dispatch.Receipt] {
		result := dispatch.NewFuture[dispatch.Receipt]()
		go func() {
			call, done := linked(signal, life)
			defer done()
			var guardedBefore func() error
			if before != nil {
				guardedBefore = func() error {
					done, ok := b.beginCallback()
					if !ok {
						return errBridgeClosed
					}
					defer done()
					return before()
				}
			}
			receipt, e := b.Host.AdapterSend(call, meta.ID, meta.Generation, scene, segment, guardedBefore)
			if e != nil {
				result.Reject(e)
			} else {
				result.Resolve(receipt)
			}
		}()
		return result
	}})
	adapter.History = func(ctx context.Context, scene botruntime.Scene, limit int, settle func(*jsjson.Object, error)) {
		go func() {
			call, done := linked(ctx, life)
			defer done()
			raw, e := b.Host.AdapterHistory(call, meta.ID, meta.Generation, scene.SceneRef, map[string]any{"limit": limit})
			if e != nil {
				settle(nil, e)
				return
			}
			value, e := object(raw)
			settle(value, e)
		}()
	}
	adapter.GetMessage = func(ctx context.Context, scene botruntime.Scene, id string, settle func(*jsjson.Object, error)) {
		go func() {
			call, done := linked(ctx, life)
			defer done()
			raw, e := b.Host.AdapterGetMessage(call, meta.ID, meta.Generation, scene.SceneRef, id)
			if e != nil {
				settle(nil, e)
				return
			}
			value, e := object(raw)
			settle(value, e)
		}()
	}
	// Deliberately no ReadImage: actual legacy installAdapter omits that optional
	// factory method. The explicit legacy public-media fallback belongs to Engine.
	entry.adapter = adapter
	b.mu.Lock()
	defer b.mu.Unlock()
	if e := ctx.Err(); e != nil {
		cancel()
		return e
	}
	if b.closing {
		cancel()
		return errBridgeClosed
	}
	b.adapters[meta.ID] = entry
	b.engine.Runtime.AddAdapter(meta.ID, adapter)
	return nil
}
func (b *Bridge) stop(meta plugins.AdapterMetadata) {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.adapters[meta.ID]
	if entry == nil || entry.generation != meta.Generation {
		return
	}
	delete(b.adapters, meta.ID)
	entry.cancel()
	b.engine.Runtime.RemoveAdapter(meta.ID)
}
func object(raw []byte) (*jsjson.Object, error) {
	v, e := jsjson.ParseBytes(raw)
	if e != nil {
		return nil, e
	}
	o, ok := v.(*jsjson.Object)
	if !ok {
		return nil, errors.New("adapter_runtime_object_required")
	}
	return o, nil
}
func get(o *jsjson.Object, key string) any {
	if o == nil {
		return nil
	}
	v, _ := o.Get(key)
	return v
}
func (b *Bridge) scene(ctx context.Context, ref dispatch.SceneRef) (scene botruntime.Scene, err error) {
	scene.SceneRef = ref
	err = b.engine.Sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		return b.engine.Sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
			rows, e := scope.Query("SELECT value FROM scenes WHERE id=?", ref.ID)
			if e != nil {
				return e
			}
			defer rows.Close()
			if rows.Next() {
				var raw string
				if e = rows.Scan(&raw); e != nil {
					return e
				}
				value, e := object([]byte(raw))
				if e != nil {
					return e
				}
				scene.ParticipationEnabled = get(value, "participationEnabled") == true
				scene.LearningEnabled = get(value, "learningEnabled") == true
				scene.ProactiveEnabled = get(value, "proactiveEnabled") == true
			}
			return rows.Err()
		})
	})
	return
}
func (b *Bridge) receive(ctx context.Context, _ plugins.AdapterMetadata, ref dispatch.SceneRef, raw json.RawMessage) error {
	root, release, e := b.engine.RootContext(ctx)
	if e != nil {
		return e
	}
	defer release()
	scene, e := b.scene(root, ref)
	if e != nil {
		return e
	}
	message, e := object(raw)
	if e != nil {
		return e
	}
	_, e = b.engine.Runtime.IngestScene(root, scene, message, true)
	return e
}
func (b *Bridge) edit(ctx context.Context, _ plugins.AdapterMetadata, ref dispatch.SceneRef, raw, change json.RawMessage) (json.RawMessage, error) {
	root, release, e := b.engine.RootContext(ctx)
	if e != nil {
		return nil, e
	}
	defer release()
	message, e := object(raw)
	if e != nil {
		return nil, e
	}
	edit, e := object(change)
	if e != nil {
		return nil, e
	}
	scene, e := b.scene(root, ref)
	if e != nil {
		return nil, e
	}
	result, e := b.engine.Runtime.EditSceneResult(root, scene, message, edit, true)
	if e != nil {
		return nil, e
	}
	return jsjson.Marshal(result)
}
func (b *Bridge) recall(ctx context.Context, _ plugins.AdapterMetadata, scene, id string) error {
	root, release, e := b.engine.RootContext(ctx)
	if e != nil {
		return e
	}
	defer release()
	return b.engine.Runtime.Recall(root, scene, id)
}
func (b *Bridge) isRecalled(ctx context.Context, _ plugins.AdapterMetadata, scene, id string) (out bool, err error) {
	err = b.engine.Sources.WithRuntimeReads(ctx, func(reads botruntime.ReadAuthority) error {
		return b.engine.Sources.UseRuntimeReadScope(reads, func(scope sourceevents.RuntimeReadScope) error {
			rows, e := scope.Query("SELECT recalled_at FROM recall_tombstones WHERE scene_id=? AND message_id=?", scene, id)
			if e != nil {
				return e
			}
			defer rows.Close()
			out = rows.Next()
			return rows.Err()
		})
	})
	return
}
func (b *Bridge) media(ctx context.Context, _ plugins.AdapterMetadata, id, scene string) (*plugins.AdapterMedia, error) {
	root, release, e := b.engine.RootContext(ctx)
	if e != nil {
		return nil, e
	}
	defer release()
	scope := memeservice.Context{SceneID: scene}
	original, e := b.engine.Memes.Original(root, scope, id)
	if e != nil {
		return nil, e
	}
	data, e := os.ReadFile(original.Path)
	if e != nil {
		return nil, e
	}
	current, e := b.engine.Memes.Original(root, scope, id)
	if e != nil {
		return nil, e
	}
	if current != original {
		return nil, errors.New("image_source_unavailable")
	}
	return &plugins.AdapterMedia{File: "base64://" + base64.StdEncoding.EncodeToString(data), Type: "image"}, nil
}
