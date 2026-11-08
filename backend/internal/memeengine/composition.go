package memeengine

import (
	"context"
	"database/sql"
	"errors"
	"github.com/killerprojecte/HumanEngine/backend/internal/assertions"
	"github.com/killerprojecte/HumanEngine/backend/internal/botruntime"
	"github.com/killerprojecte/HumanEngine/backend/internal/habitservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/imagecodec"
	"github.com/killerprojecte/HumanEngine/backend/internal/jsdate"
	"github.com/killerprojecte/HumanEngine/backend/internal/memeservice"
	"github.com/killerprojecte/HumanEngine/backend/internal/modelread"
	"github.com/killerprojecte/HumanEngine/backend/internal/providertransport"
	"github.com/killerprojecte/HumanEngine/backend/internal/recovery"
	"github.com/killerprojecte/HumanEngine/backend/internal/replyplan"
	"github.com/killerprojecte/HumanEngine/backend/internal/sourceevents"
)

// NewRuntime installs concrete native provider/model-read, Meme tools and recall
// owners. Caller can register transports and compose additional Engine extensions
// before ingress. Existing authority preparation is retained in order.
func NewRuntime(main *sql.DB, a *recovery.Authority, memes *memeservice.Service, habits *habitservice.Service, store *assertions.Store, sources *sourceevents.Store, codec *imagecodec.Compressor, models *providertransport.NativeModels, authority *sourceevents.RuntimeAuthority, settings botruntime.Settings, dates *jsdate.Parser, compare func(string, string) (int, error), base botruntime.Extension) (*Service, error) {
	if authority == nil || models == nil || models.Policy == nil || models.Client == nil {
		return nil, errors.New("meme_runtime_dependencies_required")
	}
	if e := sources.UseRuntimeReadScope(authority, func(scope sourceevents.RuntimeReadScope) error { return scope.CheckBinding(main, a) }); e != nil {
		return nil, e
	}
	read, e := modelread.NewRuntime(store, sources, main, a)
	if e != nil {
		return nil, e
	}
	policy, e := replyplan.New(compare, dates)
	if e != nil {
		return nil, e
	}
	configured := authority.AppendRecallPreparation(func(ctx context.Context, ws *recovery.WriteSession, _ botruntime.ReadAuthority, scene botruntime.Scene, id string) (func() error, error) {
		return func() error { _, e := memes.RecallWithSession(ctx, ws, scene.ID, id); return e }, nil
	})
	runtime, e := botruntime.New(settings, botruntime.Services{Models: &RuntimeModels{Native: models, Read: read}, Authority: configured, Policy: policy, Dates: dates, Compare: compare})
	if e != nil {
		return nil, e
	}
	engine, e := New(main, a, memes, runtime, habits, store, sources, codec, models)
	if e != nil {
		runtime.Close()
		return nil, e
	}
	engine.Read = read.Policy
	engine.TurnRead = read
	runtime.AddExtension(engine.ComposeExtension(base))
	return engine, nil
}
